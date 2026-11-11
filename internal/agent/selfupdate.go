package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/psewar/vordr/internal/policy"
	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/runner"
	"github.com/psewar/vordr/internal/source"
	"github.com/psewar/vordr/internal/wire"
)

// Self-update (design 7.6 and 10.4). The agent replaces its binary only with one whose hash a manifest names that the
// operator key from its own agent.yaml signed - the hub passes the manifest on and serves the file, nothing more.
// Before the swap the new binary must say its version and accept agent.yaml (check-config). After the swap the new
// agent has confirmWindow to reach the hub; if it does not, it puts the old binary back. If it cannot even start, the
// unit's OnFailure (rollback.sh) does that, on Windows the service's third recovery action (rollback.cmd). A new agent
// that hangs does neither: for it the update guard puts the old binary back (ADR-0098, updateguard.go). The old
// agent, back again, reports the failed update. What differs between Linux and Windows is in selfupdate_<os>.go. As
// a Windows service the agent stops cleanly for the restart and the update guard starts it again, so that the service
// manager counts no failure (ADR-0103).

// confirmWindow: how long a freshly installed agent has to reach an approved session before it rolls back.
const confirmWindow = 10 * time.Minute

// SelfUpdate is what an agent needs to replace itself; nil where it cannot (see NewSelfUpdate).
type SelfUpdate struct {
	Exe      string                                                           // the running binary, symlinks resolved
	Config   string                                                           // agent.yaml, for the new binary's check-config
	Keys     []ed25519.PublicKey                                              // the operator key, then its backup if set
	Run      runner.Runner                                                    // smoke test of the new binary
	Download func(ctx context.Context, url string, max int64) ([]byte, error) // from the hub, with the tunnel's TLS
	Guard    func(ctx context.Context, job string) error                      // starts the update guard from Exe, before the swap
	Exit     func(job string)                                                 // ends the process for job's restart with the binary now in place
	// Instances restarts the proxy instances that run the same binary, once an update is confirmed; nil: none here.
	Instances func(ctx context.Context) error
}

// NewSelfUpdate returns the self-update for this agent, or nil and why not: on Linux as root, on Windows as the
// service (selfUpdatePlatform), with agent_update.enabled and a valid operator key, and a binary in a directory the
// agent can write.
func NewSelfUpdate(cfg policy.Config, cfgPath string) (*SelfUpdate, string) {
	if !cfg.AgentUpdate.Enabled {
		return nil, "agent_update.enabled is false"
	}
	if why := selfUpdatePlatform(); why != "" {
		return nil, why
	}
	keys := cfg.AgentUpdate.Keys()
	if len(keys) == 0 {
		return nil, "agent_update.public_key is not an Ed25519 key"
	}
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		return nil, "own binary not found: " + err.Error()
	}
	// The update guard runs elsewhere (a transient unit's working directory is /): it gets agent.yaml by full path.
	if cfgPath, err = filepath.Abs(cfgPath); err != nil {
		return nil, "agent.yaml path: " + err.Error()
	}
	// Writable is what the agent's own attempt says (ACLs, read-only mounts), not the mode bits.
	probe, err := os.CreateTemp(filepath.Dir(exe), ".vordr-write-check-*")
	if err != nil {
		return nil, filepath.Dir(exe) + " is not writable"
	}
	probe.Close()
	_ = os.Remove(probe.Name())
	tlsCfg, err := TLSConfig(cfg.Hub.CAFile)
	if err != nil {
		return nil, "hub.ca_file: " + err.Error()
	}
	client := &http.Client{Timeout: 5 * time.Minute, Transport: &http.Transport{TLSClientConfig: tlsCfg}}
	run := runner.Exec{}
	return &SelfUpdate{Exe: exe, Config: cfgPath, Keys: keys, Run: run,
		Download:  func(ctx context.Context, u string, max int64) ([]byte, error) { return download(ctx, client, u, max) },
		Guard:     func(ctx context.Context, job string) error { return armGuard(ctx, run, exe, cfgPath, job) },
		Exit:      restartExit,
		Instances: func(ctx context.Context) error { return restartInstances(ctx, run) }}, ""
}

func download(ctx context.Context, client *http.Client, u string, max int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("hub answered %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, max+1))
}

// releaseURL: the hub serves releases on the tunnel's host and port.
func releaseURL(tunnel, file, token string) (string, error) {
	u, err := url.Parse(tunnel)
	if err != nil {
		return "", err
	}
	u.Scheme, u.Path = "https", "/v1/agent/"+file
	u.RawQuery = url.Values{"token": {token}}.Encode()
	return u.String(), nil
}

// updateMarker is written right before the swap and settled by the agent that starts next.
type updateMarker struct {
	JobID string    `json:"job_id"`
	From  string    `json:"from"`
	To    string    `json:"to"`
	At    time.Time `json:"at"`
	// RolledBack: who put the previous binary back and why, for the report of the agent that comes back. Empty when
	// rollback.sh or rollback.cmd did, or an agent from before ADR-0098.
	RolledBack string `json:"rolled_back,omitempty"`
}

func markerFile(stateDir string) string { return filepath.Join(stateDir, "update.json") }

func (c *Client) markerPath() string { return markerFile(c.Cfg.StateDir) }

// agentUpdate carries out an agent_update job: check, download, test, swap, restart. Answers before the swap go to the
// session that brought the job; after it the final result comes from the agent that runs next.
//
// The agent that is already installed runs this code, not the one it installs: a change here takes effect one release
// later. 0.7.2 brought the 0755 fix below, and it was still installed as 0700 by 0.7.1.
func (c *Client) agentUpdate(j protocol.Job, send func(any) error) {
	answer := func(st wire.JobStatus) {
		st.T, st.JobID = wire.TJobStatus, j.ID
		c.Log.Printf("job %s agent_update: %s %s", j.ID, st.Code, st.Detail)
		c.answer(send, st) // over a newer session, or kept for the next one
	}
	u := c.Updater
	if u == nil {
		answer(wire.JobStatus{Code: protocol.CodeRejectedUnsupported})
		return
	}
	m, ok := protocol.VerifyManifestWith(j.Manifest, j.ManifestSig, u.Keys)
	if !ok {
		// Signed by the hub, but not by the operator: worth seeing (design 7.3, rejected_invalid_job).
		answer(wire.JobStatus{Code: protocol.CodeRejectedInvalidJob, Reason: protocol.InvalidBadManifest})
		return
	}
	if m.Version == c.Version {
		answer(wire.JobStatus{Code: protocol.CodeOK, Detail: "already running " + m.Version})
		return
	}
	// Every release the operator ever signed stays valid: an agent on a release takes only newer ones, so whoever
	// holds the hub cannot send an old manifest again to bring back an old agent. A hand build takes any release.
	if protocol.IsRelease(c.Version) && !protocol.NewerRelease(m.Version, c.Version) {
		answer(wire.JobStatus{Code: protocol.CodeRejectedInvalidJob, Reason: protocol.InvalidOlderRelease,
			Detail: fmt.Sprintf("release %s is not newer than the running %s", m.Version, c.Version)})
		return
	}
	bin, ok := m.For(runtime.GOOS, runtime.GOARCH)
	if !ok {
		answer(wire.JobStatus{Code: protocol.CodeRejectedUnsupported,
			Detail: fmt.Sprintf("release %s has no build for %s/%s", m.Version, runtime.GOOS, runtime.GOARCH)})
		return
	}
	if !c.checkMu.TryLock() {
		answer(wire.JobStatus{Code: protocol.CodeRejectedBusy})
		return
	}
	defer c.checkMu.Unlock()
	progress := c.progressSender(j.ID)
	progress(phaseAt(protocol.PhaseDownloading, 10))
	src, err := releaseURL(c.Cfg.Hub.URL, bin.File, j.Token)
	var data []byte
	if err == nil {
		ctx, cancel := context.WithTimeout(c.root(), 10*time.Minute)
		data, err = u.Download(ctx, src, bin.Size)
		cancel()
	}
	if err != nil { // network or hub: a real failure to report, nothing to decide on
		answer(wire.JobStatus{Code: protocol.CodePrecheckRepoUnreachable, Detail: "download: " + err.Error()})
		return
	}
	sum := sha256.Sum256(data)
	if int64(len(data)) != bin.Size || hex.EncodeToString(sum[:]) != strings.ToLower(bin.SHA256) {
		answer(wire.JobStatus{Code: protocol.CodeFailedVerification, Detail: "download does not match the manifest"})
		return
	}
	progress(phaseAt(protocol.PhaseVerifying, 60))
	next := nextPath(u.Exe)
	err = os.WriteFile(next, data, 0o755)
	if err == nil {
		// The unit's UMask=0077 would leave 0700; the binary keeps the mode install-agent.sh gives it.
		err = os.Chmod(next, 0o755)
	}
	if err != nil {
		answer(wire.JobStatus{Code: protocol.CodeFailedPackageManager, Detail: "write " + next + ": " + err.Error()})
		return
	}
	if detail := c.smokeTest(u, next, m.Version); detail != "" {
		_ = os.Remove(next)
		answer(wire.JobStatus{Code: protocol.CodeFailedVerification, Detail: detail})
		return
	}
	marker := updateMarker{JobID: j.ID, From: c.Version, To: m.Version, At: c.Now().UTC()}
	if err := writeJSON(c.markerPath(), marker); err != nil {
		_ = os.Remove(next)
		answer(wire.JobStatus{Code: protocol.CodeFailedPackageManager, Detail: "update marker: " + err.Error()})
		return
	}
	// No swap without the guard: it is the way back from a new agent that hangs (ADR-0098). It starts from the binary
	// still in place, this one.
	ctx, cancel := context.WithTimeout(c.root(), time.Minute)
	err = u.Guard(ctx, j.ID)
	cancel()
	if err != nil {
		_ = os.Remove(next)
		_ = os.Remove(c.markerPath())
		answer(wire.JobStatus{Code: protocol.CodeFailedPackageManager, Detail: "update guard: " + err.Error()})
		return
	}
	if err := swap(u.Exe, next); err != nil {
		_ = os.Remove(c.markerPath()) // the guard sees the marker gone and ends
		answer(wire.JobStatus{Code: protocol.CodeFailedPackageManager, Detail: "swap: " + err.Error()})
		return
	}
	progress(phaseAt(protocol.PhaseRebooting, 90))
	c.Log.Printf("job %s agent_update: %s -> %s installed, restarting", j.ID, c.Version, m.Version)
	u.Exit(j.ID)
}

func phaseAt(ph protocol.Phase, pct int) source.Progress {
	return source.Progress{Phase: ph, Percent: pct}
}

// smokeTest: the new binary says the manifest's version and accepts this host's agent.yaml; "" when it does.
func (c *Client) smokeTest(u *SelfUpdate, bin, version string) string {
	ctx, cancel := context.WithTimeout(c.root(), time.Minute)
	defer cancel()
	res := u.Run.Run(ctx, nil, bin, "version")
	if !res.Ran() || res.ExitCode != 0 || strings.TrimSpace(string(res.Stdout)) != version {
		return fmt.Sprintf("new agent says %q instead of %s", strings.TrimSpace(string(res.Stdout)), version)
	}
	res = u.Run.Run(ctx, nil, bin, "check-config", "--config", u.Config)
	if !res.Ran() || res.ExitCode != 0 {
		return "new agent does not accept agent.yaml: " + strings.TrimSpace(string(res.Stdout)+string(res.Stderr))
	}
	return ""
}

// swap puts next in place of exe and keeps exe as exe.old (the way back).
func swap(exe, next string) error {
	if err := os.Rename(exe, exe+".old"); err != nil {
		return err
	}
	if err := os.Rename(next, exe); err != nil {
		_ = os.Rename(exe+".old", exe)
		return err
	}
	return nil
}

// settleUpdate runs once at start. The agent an update installed confirms on its first approved session or rolls
// back after confirmWindow. Any other agent finding the marker is the one the update replaced, back again: the update
// failed, and it says so.
func (c *Client) settleUpdate() {
	if c.Updater != nil {
		removeLeftovers(c.Updater.Exe)
	}
	path := c.markerPath()
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return
	}
	var m updateMarker
	raw, err := os.ReadFile(path)
	if err == nil {
		err = json.Unmarshal(raw, &m)
	}
	if err != nil {
		c.Log.Printf("update marker %s unreadable, removed: %v", path, err)
		_ = os.Remove(path)
		return
	}
	c.stateMu.Lock()
	c.update = &m
	c.stateMu.Unlock()
	if m.To != c.Version || c.Updater == nil {
		return // reported on the next approved session
	}
	c.Log.Printf("updated %s -> %s (job %s): confirming on the first approved session", m.From, m.To, m.JobID)
	go func() {
		select {
		case <-c.confirmed:
		case <-time.After(confirmWindow):
			c.rollBack(m)
		}
	}()
}

// rollBack puts the previous binary back and ends the process; systemd or the service manager starts the old agent,
// which reports.
func (c *Client) rollBack(m updateMarker) {
	exe := c.Updater.Exe
	if _, err := os.Stat(exe + ".old"); err != nil {
		c.Log.Printf("no %s.old to go back to (%v) - staying on %s", exe, err, c.Version)
		return
	}
	c.Log.Printf("no approved session within %s after the update to %s - going back to %s", confirmWindow, m.To, m.From)
	if err := putBack(exe); err != nil {
		c.Log.Printf("rollback: %v", err)
		return
	}
	// Best effort: on Windows replacing the marker fails at the moment the update guard looks at it.
	m.RolledBack = fmt.Sprintf("no approved session within %s", confirmWindow)
	if err := writeJSON(c.markerPath(), m); err != nil {
		c.Log.Printf("update marker: %v - the report will not say why", err)
	}
	c.Updater.Exit(m.JobID)
}

// reportUpdate, on an approved session: the result of the last self-update, once.
func (c *Client) reportUpdate(send func(any) error) {
	c.stateMu.Lock()
	m := c.update
	c.stateMu.Unlock()
	if m == nil {
		return
	}
	st := wire.JobStatus{T: wire.TJobStatus, JobID: m.JobID, Code: protocol.CodeOK,
		Detail: fmt.Sprintf("agent %s -> %s", m.From, m.To)}
	if m.To != c.Version {
		why := m.RolledBack
		if why == "" {
			why = "no hub session or no start"
		}
		st.Code = protocol.CodeFailedVerification
		st.Detail = fmt.Sprintf("agent %s did not stay up (%s); back on %s", m.To, why, c.Version)
	}
	if send(st) != nil {
		return // the next session reports it
	}
	c.Log.Printf("update %s -> %s reported: %s", m.From, m.To, st.Code)
	// The marker gone is what tells the update guard the update is settled (ADR-0098).
	if err := os.Remove(c.markerPath()); err != nil {
		c.Log.Printf("update marker: %v - the update guard will take the update for pending", err)
	}
	c.stateMu.Lock()
	c.update = nil
	c.stateMu.Unlock()
	if st.Code == protocol.CodeOK {
		close(c.confirmed)
		c.restartInstances()
	}
}

// restartInstances: the proxy instances share this agent's binary and do not update themselves; once this agent's
// update is confirmed, they are restarted onto it (design/proxy-agent.md 4.3, ADR-0089). Not before: a new binary
// that fails would take them down with it, and they would not come back with the rollback.
func (c *Client) restartInstances() {
	if c.Updater == nil || c.Updater.Instances == nil {
		return
	}
	ctx, cancel := context.WithTimeout(c.root(), time.Minute)
	defer cancel()
	if err := c.Updater.Instances(ctx); err != nil {
		c.Log.Printf("proxy instances not restarted: %v - they run the previous binary until their next start", err)
	}
}

func writeJSON(path string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
