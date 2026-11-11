package hub

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/facts"
	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/wire"
)

// publish writes a signed release with one binary into dir, as the operator's tool does.
func publish(t *testing.T, dir string, priv ed25519.PrivateKey, version string) {
	t.Helper()
	bin := []byte("#!/bin/sh\necho " + version + "\n")
	sum := sha256.Sum256(bin)
	m := protocol.Manifest{Version: version, Issued: time.Now().UTC(), Binaries: []protocol.ManifestBinary{
		{OS: "linux", Arch: "amd64", File: "vordr-agent-linux-amd64", SHA256: hex.EncodeToString(sum[:]),
			Size: int64(len(bin))}}}
	raw, _ := json.Marshal(m)
	for name, data := range map[string][]byte{"vordr-agent-linux-amd64": bin, "manifest.json": raw,
		"manifest.sig": protocol.SignManifest(raw, func(b []byte) []byte { return ed25519.Sign(priv, b) })} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

type releaseFixture struct {
	*viewFixture
	priv ed25519.PrivateKey
	dir  string
}

func newReleaseFixture(t *testing.T) *releaseFixture {
	t.Helper()
	f := &releaseFixture{viewFixture: newViewFixture(t), dir: t.TempDir()}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	f.priv = priv
	cfg := AgentUpdateConfig{PublicKey: base64.StdEncoding.EncodeToString(pub), Canary: []string{"canary"},
		CanaryCleanHours: 24, RetryAfterHours: 6}
	f.srv.Releases = NewReleases(cfg, f.dir, func() time.Time { return f.now })
	for _, n := range []string{"canary", "other"} {
		f.host(t, n, []protocol.Verb{protocol.VerbCheck, protocol.VerbAgentUpdate}, wire.PolicySummary{AgentUpdate: true}, true)
		f.srv.sessions[n] = &session{node: n, waiters: map[string]chan wire.JobStatus{}} // connected
	}
	return f
}

func TestReleaseNeedsTheOperatorsSignature(t *testing.T) {
	f := newReleaseFixture(t)
	if rel, why := f.srv.Releases.Current(); rel != nil || why != "" {
		t.Fatalf("no release yet: %v %q", rel, why)
	}
	_, foreign, _ := ed25519.GenerateKey(rand.Reader)
	publish(t, f.dir, foreign, "0.7.0")
	if rel, why := f.srv.Releases.Current(); rel != nil || !strings.Contains(why, "not signed") {
		t.Fatalf("release signed by someone else: %v %q", rel, why)
	}
	if _, code := f.srv.StartAgentUpdate("canary", ByAdmin); code != AdminNoRelease {
		t.Fatalf("update without a valid release: %s", code)
	}
	publish(t, f.dir, f.priv, "0.7.0")
	if rel, _ := f.srv.Releases.Current(); rel == nil || rel.Manifest.Version != "0.7.0" ||
		f.view(t, "canary", f.now).AgentRelease != "0.7.0" {
		t.Fatalf("operator's release: %+v", rel)
	}
}

// Canaries first; the others once every canary ran the release long enough and without a problem; a host that was
// asked is asked again only after retry_after_hours.
func TestRolloutRings(t *testing.T) {
	f := newReleaseFixture(t)
	publish(t, f.dir, f.priv, "0.7.0")
	rel, _ := f.srv.Releases.Current()
	due := func(node string) bool {
		e, _ := f.srv.Reg.Get(node)
		return f.srv.rolloutDue(e, rel, f.now)
	}
	if !due("canary") || due("other") {
		t.Fatalf("first ring: canary %v, other %v", due("canary"), due("other"))
	}
	f.srv.Releases.attempts["canary"] = attempt{version: "0.7.0", at: f.now}
	if due("canary") {
		t.Fatal("asked again right after an attempt")
	}
	// The canary comes back with the release.
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	e, _ := f.srv.Reg.Get("canary")
	h := wire.Hello{Node: "canary", PublicKey: pub, AgentVersion: "0.7.0", Verbs: e.Verbs, Policy: e.Policy}
	f.srv.Reg.mu.Lock()
	f.srv.Reg.nodes["canary"].PublicKey = pub
	f.srv.Reg.nodes["canary"].Fingerprint = "fp-canary"
	f.srv.Reg.mu.Unlock()
	if _, _, err := f.srv.Reg.Admit(h, "fp-canary", f.now, 5); err != nil {
		t.Fatal(err)
	}
	if due("canary") || due("other") {
		t.Fatal("canary on the release: nothing for it, the others wait")
	}
	f.now = f.now.Add(25 * time.Hour)
	f.srv.Reg.SetReport("canary", aptReport(f.now, 0, 0, nil))
	if !due("other") {
		t.Fatal("canary ran the release 25 h without a problem: the others are due")
	}
	if _, code := f.srv.StartAgentUpdate("canary", ByAdmin); code != AdminAlready {
		t.Fatalf("update to the version it runs: %s", code)
	}
}

// A release goes only to hosts it has a binary for (ADR-0102): neither the rollout nor the install button offers it
// elsewhere, and the view says why. A canary it has no binary for does not hold the others back, but one canary must
// have run it. Hosts that do not report their platform are offered it as before (TestRolloutRings).
func TestReleaseOnlyWhereItHasABinary(t *testing.T) {
	f := newReleaseFixture(t)
	f.srv.Releases.cfg.Canary = []string{"canary", "pi"}
	f.host(t, "pi", []protocol.Verb{protocol.VerbCheck, protocol.VerbAgentUpdate}, wire.PolicySummary{AgentUpdate: true}, true)
	f.srv.sessions["pi"] = &session{node: "pi", waiters: map[string]chan wire.JobStatus{}}
	platform := func(node, goos, arch string) {
		f.srv.Reg.mu.Lock()
		f.srv.Reg.nodes[node].Facts = facts.Host{GOOS: goos, Arch: arch}
		f.srv.Reg.mu.Unlock()
	}
	platform("canary", "linux", "amd64")
	platform("pi", "linux", "arm64")
	platform("other", "windows", "amd64")
	publish(t, f.dir, f.priv, "0.7.0") // linux/amd64 only
	rel, _ := f.srv.Releases.Current()
	due := func(node string) bool {
		e, _ := f.srv.Reg.Get(node)
		return f.srv.rolloutDue(e, rel, f.now)
	}
	if !due("canary") || due("pi") || due("other") {
		t.Fatalf("first ring: canary %v, pi %v, other %v", due("canary"), due("pi"), due("other"))
	}
	if _, code := f.srv.StartAgentUpdate("pi", ByAdmin); code != AdminNoBinary {
		t.Fatalf("install without a binary for the host: %s", code)
	}
	if v := f.view(t, "pi", f.now); v.AgentRelease != "0.7.0" || !v.AgentReleaseNoBinary {
		t.Fatalf("view of a host the release has no binary for: %q %v", v.AgentRelease, v.AgentReleaseNoBinary)
	}
	if v := f.view(t, "canary", f.now); v.AgentReleaseNoBinary {
		t.Fatal("view of a host the release has a binary for")
	}

	// The canary with a binary runs the release long enough; the one without does not hold the others back.
	platform("other", "linux", "amd64")
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	e, _ := f.srv.Reg.Get("canary")
	f.srv.Reg.mu.Lock()
	f.srv.Reg.nodes["canary"].PublicKey = pub
	f.srv.Reg.nodes["canary"].Fingerprint = "fp-canary"
	f.srv.Reg.mu.Unlock()
	h := wire.Hello{Node: "canary", PublicKey: pub, AgentVersion: "0.7.0", Verbs: e.Verbs, Policy: e.Policy,
		Facts: facts.Host{GOOS: "linux", Arch: "amd64"}}
	if _, _, err := f.srv.Reg.Admit(h, "fp-canary", f.now, 5); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(25 * time.Hour)
	f.srv.Reg.SetReport("canary", aptReport(f.now, 0, 0, nil))
	if !due("other") {
		t.Fatal("the canary with a binary ran the release 25 h without a problem: the others are due")
	}
	// No canary could try the release: the others wait.
	platform("canary", "linux", "arm64")
	if due("other") {
		t.Fatal("no canary could try the release, yet the others are due")
	}
}

// A release signed with the offline backup key counts; an agent already on a newer release is not offered an older
// one (it would refuse it as older_release).
func TestReleaseBackupKeyAndNoDowngrade(t *testing.T) {
	f := newReleaseFixture(t)
	bpub, bpriv, _ := ed25519.GenerateKey(rand.Reader)
	cfg := f.srv.Releases.cfg
	cfg.BackupPublicKey = base64.StdEncoding.EncodeToString(bpub)
	f.srv.Releases = NewReleases(cfg, f.dir, func() time.Time { return f.now })
	publish(t, f.dir, bpriv, "0.7.0")
	rel, why := f.srv.Releases.Current()
	if rel == nil {
		t.Fatalf("release signed with the backup key: %q", why)
	}
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	e, _ := f.srv.Reg.Get("canary")
	f.srv.Reg.mu.Lock()
	f.srv.Reg.nodes["canary"].PublicKey = pub
	f.srv.Reg.nodes["canary"].Fingerprint = "fp-canary"
	f.srv.Reg.mu.Unlock()
	h := wire.Hello{Node: "canary", PublicKey: pub, AgentVersion: "0.7.10", Verbs: e.Verbs, Policy: e.Policy}
	if _, _, err := f.srv.Reg.Admit(h, "fp-canary", f.now, 5); err != nil {
		t.Fatal(err)
	}
	if e, _ := f.srv.Reg.Get("canary"); f.srv.rolloutDue(e, rel, f.now) {
		t.Fatal("0.7.0 offered to an agent on 0.7.10")
	}
	if issues := (&AgentUpdateConfig{PublicKey: cfg.PublicKey, BackupPublicKey: cfg.PublicKey, Canary: []string{"a"},
		CanaryCleanHours: 24, RetryAfterHours: 6}).check(); len(issues) != 1 {
		t.Fatalf("backup key equal to the key: %v", issues)
	}
}

func TestReleaseDownloadNeedsAnOpenToken(t *testing.T) {
	f := newReleaseFixture(t)
	publish(t, f.dir, f.priv, "0.7.0")
	srv := httptest.NewServer(f.srv.Handler())
	defer srv.Close()
	get := func(path string) (int, string) {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}
	token := f.srv.Releases.newToken("canary", "j1")
	if code, body := get(AgentReleasePath + "vordr-agent-linux-amd64?token=" + token); code != 200 ||
		body != "#!/bin/sh\necho 0.7.0\n" {
		t.Fatalf("download with the token: %d %q", code, body)
	}
	if code, _ := get(AgentReleasePath + "vordr-agent-linux-amd64?token=" + strings.Repeat("0", 48)); code != 403 {
		t.Fatalf("unknown token: %d", code)
	}
	if code, _ := get(AgentReleasePath + "manifest.json?token=" + token); code != 404 {
		t.Fatalf("a file the manifest does not name: %d", code)
	}
	f.now = f.now.Add(downloadWindow + time.Minute)
	if code, _ := get(AgentReleasePath + "vordr-agent-linux-amd64?token=" + token); code != 403 {
		t.Fatalf("token after its window: %d", code)
	}
}

func TestAgentUpdateConfigIssues(t *testing.T) {
	bad := AgentUpdateConfig{PublicKey: "nope", Canary: []string{"Bad Name"}, CanaryCleanHours: 0, RetryAfterHours: 999}
	issues := strings.Join(bad.check(), "\n")
	for _, want := range []string{"public_key", "canary[0]", "canary_clean_hours", "retry_after_hours"} {
		if !strings.Contains(issues, want) {
			t.Errorf("missing %q in:\n%s", want, issues)
		}
	}
}
