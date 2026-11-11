package hub

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/psewar/vordr/internal/facts"
	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/wire"
)

// Agent releases (design 7.6 and 10.4): the operator signs a manifest away from the hub and puts it with the binaries
// into <state_dir>/agent. The hub checks the signature itself (to know which version is current), hands manifest and
// a one-time download token out with each agent_update job, serves the binaries the manifest names to the holder of
// such a token, and rolls a release out in rings: the canary hosts first, the others once every canary has run it
// for canary_clean_hours without a problem. A release goes only to hosts it has a binary for (ADR-0102).

// AgentUpdateConfig is the agent_update section of hub.yaml. Without it the hub hands out no releases.
type AgentUpdateConfig struct {
	PublicKey        string   `yaml:"public_key"`         // base64 operator key that signs releases (as in agent.yaml)
	BackupPublicKey  string   `yaml:"backup_public_key"`  // its offline backup (as in agent.yaml); optional
	Canary           []string `yaml:"canary"`             // hosts that get a release first
	CanaryCleanHours int      `yaml:"canary_clean_hours"` // default 24
	RetryAfterHours  int      `yaml:"retry_after_hours"`  // a host whose update failed is asked again after this; default 6
}

const (
	defaultCanaryCleanH = 24
	defaultRetryAfterH  = 6
	rolloutInterval     = 5 * time.Minute
	// downloadWindow: how long a job's token opens its binary - the job itself is valid for minutes only.
	downloadWindow = 15 * time.Minute
	// agentUpdateTimeout: the old agent downloads, checks and swaps; the new one reports from its first session.
	agentUpdateTimeout = 30 * time.Minute
	AgentReleasePath   = "/v1/agent/"
)

// AdminNoRelease: an agent update asked of a hub that has no (valid) release.
const AdminNoRelease AdminCode = "no_release"

// AdminNoBinary: the release has no binary for the host's platform; its agent could only refuse it.
const AdminNoBinary AdminCode = "no_binary_for_platform"

func (c *AgentUpdateConfig) check() []string {
	var issues []string
	if !validKeyB64(c.PublicKey) {
		issues = append(issues, "agent_update.public_key: missing or not a base64 Ed25519 public key")
	}
	if b := c.BackupPublicKey; b != "" && (!validKeyB64(b) || b == c.PublicKey) {
		issues = append(issues, "agent_update.backup_public_key: not a base64 Ed25519 public key, or the same as public_key")
	}
	for i, n := range c.Canary {
		if !nodePattern.MatchString(n) {
			issues = append(issues, fmt.Sprintf("agent_update.canary[%d]: %q is not a node name", i, n))
		}
	}
	if len(c.Canary) == 0 {
		issues = append(issues, "agent_update.canary: name at least one host that gets a release first")
	}
	if c.CanaryCleanHours < 1 || c.CanaryCleanHours > 24*14 {
		issues = append(issues, "agent_update.canary_clean_hours: must be between 1 and 336")
	}
	if c.RetryAfterHours < 1 || c.RetryAfterHours > 24*7 {
		issues = append(issues, "agent_update.retry_after_hours: must be between 1 and 168")
	}
	return issues
}

func validKeyB64(s string) bool {
	raw, err := base64.StdEncoding.DecodeString(s)
	return err == nil && len(raw) == 32
}

// Release is a verified manifest with its exact bytes, as the agent checks them.
type Release struct {
	Manifest protocol.Manifest
	Raw, Sig []byte
}

// Fits: the release has a binary for the host's platform. A host that does not report its platform is offered the
// release as before; its agent checks the manifest itself and refuses with rejected_unsupported.
func (r *Release) Fits(h facts.Host) bool {
	goos, arch, ok := h.Platform()
	if !ok {
		return true
	}
	_, has := r.Manifest.For(goos, arch)
	return has
}

type download struct {
	node, jobID string
	until       time.Time
}

type attempt struct {
	version string
	at      time.Time
}

// Releases holds the current release, the open download tokens and the rollout attempts.
type Releases struct {
	cfg  AgentUpdateConfig
	keys []ed25519.PublicKey
	dir  string
	now  func() time.Time

	mu       sync.Mutex
	tokens   map[string]download
	attempts map[string]attempt
	logged   string   // the last load outcome that was logged (so a broken release is not logged every tick)
	loaded   *Release // the release Current found last; views read this instead of the disk
}

// Loaded is the release the last Current call found, nil when none.
func (r *Releases) Loaded() *Release {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.loaded
}

// NewReleases reads releases from dir. cfg must have passed ParseConfig.
func NewReleases(cfg AgentUpdateConfig, dir string, now func() time.Time) *Releases {
	var keys []ed25519.PublicKey
	for _, k := range []string{cfg.PublicKey, cfg.BackupPublicKey} {
		if raw, err := base64.StdEncoding.DecodeString(k); err == nil && len(raw) == ed25519.PublicKeySize {
			keys = append(keys, raw)
		}
	}
	return &Releases{cfg: cfg, keys: keys, dir: dir, now: now, tokens: map[string]download{}, attempts: map[string]attempt{}}
}

// Current reads and verifies the release in dir. No release (no manifest) is a normal state; a manifest with a bad
// signature counts as none and says why.
func (r *Releases) Current() (*Release, string) {
	rel, why := r.read()
	r.mu.Lock()
	r.loaded = rel
	r.mu.Unlock()
	return rel, why
}

func (r *Releases) read() (*Release, string) {
	path := filepath.Join(r.dir, "manifest.json")
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return nil, ""
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, "manifest.json: " + err.Error()
	}
	sig, err := os.ReadFile(filepath.Join(r.dir, "manifest.sig"))
	if err != nil {
		return nil, "manifest.sig: " + err.Error()
	}
	m, ok := protocol.VerifyManifestWith(raw, sig, r.keys)
	if !ok {
		return nil, "manifest.json is not signed by agent_update.public_key or backup_public_key (or not a valid manifest)"
	}
	return &Release{Manifest: m, Raw: raw, Sig: sig}, ""
}

// newToken opens the release's binaries for one job until downloadWindow passes.
func (r *Releases) newToken(node, jobID string) string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	t := hex.EncodeToString(b)
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	for k, d := range r.tokens {
		if now.After(d.until) {
			delete(r.tokens, k)
		}
	}
	r.tokens[t] = download{node: node, jobID: jobID, until: now.Add(downloadWindow)}
	return t
}

// ServeHTTP serves a binary of the current release to the holder of an open token: GET /v1/agent/<file>?token=<t>.
// The binary's integrity does not rest on this - the agent checks it against the operator's manifest.
func (r *Releases) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	file := strings.TrimPrefix(req.URL.Path, AgentReleasePath)
	token := req.URL.Query().Get("token")
	r.mu.Lock()
	d, ok := r.tokens[token]
	r.mu.Unlock()
	if req.Method != http.MethodGet || !protocol.ValidToken(token) || !ok || r.now().After(d.until) {
		http.Error(w, "no open download for this token", http.StatusForbidden)
		return
	}
	rel, _ := r.Current()
	if rel == nil {
		http.Error(w, "no release", http.StatusNotFound)
		return
	}
	if _, named := rel.Manifest.Has(file); !named {
		http.Error(w, "not part of the release", http.StatusNotFound)
		return
	}
	f, err := os.Open(filepath.Join(r.dir, file))
	if err != nil {
		http.Error(w, "binary missing on the hub", http.StatusNotFound)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		http.Error(w, "binary unreadable", http.StatusInternalServerError)
		return
	}
	http.ServeContent(w, req, file, info.ModTime(), f)
}

// StartAgentUpdate signs an agent_update job with the current release for node. The hub decides only what it can
// know (a release, a different version, the verb announced); the agent checks the manifest against its own key.
func (s *Server) StartAgentUpdate(node, by string) (*SentJob, AdminCode) {
	if s.Releases == nil {
		return nil, AdminNoRelease
	}
	rel, _ := s.Releases.Current()
	if rel == nil {
		return nil, AdminNoRelease
	}
	e, ok := s.Reg.Get(node)
	if !ok {
		return nil, AdminUnknownNode
	}
	if e.AgentVersion == rel.Manifest.Version {
		return nil, AdminAlready
	}
	if !rel.Fits(e.Facts) {
		return nil, AdminNoBinary
	}
	jobID := newJobID()
	token := s.Releases.newToken(node, jobID)
	return s.startJob(node, protocol.Job{ID: jobID, Verb: protocol.VerbAgentUpdate, Manifest: rel.Raw,
		ManifestSig: rel.Sig, Token: token}, by)
}

// RunRollout offers the current release to every host that announced agent_update and runs another version, in
// rings, until ctx ends.
func (s *Server) RunRollout(ctx context.Context) {
	for {
		s.rolloutOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(rolloutInterval):
		}
	}
}

func (s *Server) rolloutOnce(ctx context.Context) {
	rel, why := s.Releases.Current()
	s.Releases.mu.Lock()
	if why != s.Releases.logged {
		s.Releases.logged = why
		if why != "" {
			s.Log.Printf("agent release: %s", why)
		}
	}
	s.Releases.mu.Unlock()
	if rel == nil {
		return
	}
	now := s.Now()
	for _, e := range s.Reg.List() {
		if !s.rolloutDue(e, rel, now) {
			continue
		}
		job, code := s.StartAgentUpdate(e.Node, ByRollout)
		s.Events.Log(e.Node, "agent_update_offered", map[string]any{"version": rel.Manifest.Version, "code": code})
		if code != AdminOK {
			continue
		}
		s.Releases.mu.Lock()
		s.Releases.attempts[e.Node] = attempt{version: rel.Manifest.Version, at: now}
		s.Releases.mu.Unlock()
		go func() {
			wctx, cancel := context.WithTimeout(ctx, agentUpdateTimeout)
			defer cancel()
			s.WaitJob(wctx, job)
		}()
	}
}

// rolloutDue: the host takes updates, runs another version, the release has a binary for it, it is idle and
// connected, was not asked for this version recently, and its ring is open - canaries at once, everyone else once the
// canaries ran the release long enough.
func (s *Server) rolloutDue(e Entry, rel *Release, now time.Time) bool {
	cfg := s.Releases.cfg
	if e.State != wire.StateApproved || e.AgentVersion == rel.Manifest.Version ||
		!slices.Contains(e.Verbs, protocol.VerbAgentUpdate) || !e.Policy.AgentUpdate || !rel.Fits(e.Facts) {
		return false
	}
	// An agent on a newer release would refuse it (older_release); the rollout does not ask.
	if protocol.IsRelease(e.AgentVersion) && !protocol.NewerRelease(rel.Manifest.Version, e.AgentVersion) {
		return false
	}
	// Busy as the queue sees it: a run without a result for at most as long as its job may take (a run whose result
	// was lost does not hold the host back for ever), or a backup on a hypervisor for another host's install.
	if online, _ := s.Online(e.Node); !online || s.running(e.Node) {
		return false
	}
	s.Releases.mu.Lock()
	a, tried := s.Releases.attempts[e.Node]
	s.Releases.mu.Unlock()
	if tried && a.version == rel.Manifest.Version && now.Sub(a.at) < time.Duration(cfg.RetryAfterHours)*time.Hour {
		return false
	}
	if slices.Contains(cfg.Canary, e.Node) {
		return true
	}
	// A canary the release has no binary for cannot try it and does not hold the others back. Every other canary
	// must have run it long enough, and at least one must have.
	clean := 0
	for _, c := range cfg.Canary {
		ce, ok := s.Reg.Get(c)
		if ok && !rel.Fits(ce.Facts) {
			continue
		}
		if !ok || ce.AgentVersion != rel.Manifest.Version || ce.AgentSince == nil ||
			now.Sub(*ce.AgentSince) < time.Duration(cfg.CanaryCleanHours)*time.Hour || s.HostView(ce, now).Problem != nil {
			return false
		}
		clean++
	}
	return clean > 0
}
