//go:build linux

package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/facts"
	"github.com/psewar/vordr/internal/policy"
	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/runner"
)

// newAgentScript is a stand-in for a new agent binary: it says its version and accepts any agent.yaml.
func newAgentScript(version string) []byte {
	return []byte("#!/bin/sh\ncase \"$1\" in\n  version) echo " + version + " ;;\n  check-config) exit 0 ;;\nesac\n")
}

type updateFixture struct {
	dir, exe     string
	priv, backup ed25519.PrivateKey // the operator key and its offline backup
	keys         []ed25519.PublicKey
	exits        int
	exitJobs     []string // the job each exit was for
	instances    int      // restarts of the proxy instances
	served       []byte
	guards       []string // each guard started: its job and the binary in place then
	guardErr     error
}

func newUpdateFixture(t *testing.T) *updateFixture {
	t.Helper()
	f := &updateFixture{dir: t.TempDir()}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	bpub, bpriv, _ := ed25519.GenerateKey(rand.Reader)
	f.priv, f.backup, f.keys = priv, bpriv, []ed25519.PublicKey{pub, bpub}
	f.exe = filepath.Join(f.dir, "vordr-agent")
	if err := os.WriteFile(f.exe, []byte("old agent"), 0o755); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *updateFixture) client(t *testing.T, version string, state string) *Client {
	t.Helper()
	cfg := policy.Config{Node: "host-a", StateDir: state, Hub: policy.Hub{URL: "wss://hub.example:8443/v1/tunnel"}}
	c := NewClient(cfg, nil, version, facts.Host{}, &fakeChecker{}, log.New(io.Discard, "", 0))
	c.Updater = &SelfUpdate{Exe: f.exe, Config: filepath.Join(f.dir, "agent.yaml"), Keys: f.keys, Run: runner.Exec{},
		Download: func(_ context.Context, u string, max int64) ([]byte, error) {
			if !strings.HasPrefix(u, "https://hub.example:8443/v1/agent/vordr-agent-") || !strings.Contains(u, "token=") {
				return nil, errors.New("unexpected url " + u)
			}
			return f.served, nil
		},
		Guard: func(_ context.Context, job string) error {
			f.guards = append(f.guards, job+": "+read(t, f.exe))
			return f.guardErr
		},
		Exit:      func(job string) { f.exits++; f.exitJobs = append(f.exitJobs, job) },
		Instances: func(context.Context) error { f.instances++; return nil }}
	return c
}

// job signs a manifest for this platform naming bin's hash; key signs it (the operator's or someone else's).
func (f *updateFixture) job(t *testing.T, version string, bin []byte, key ed25519.PrivateKey) protocol.Job {
	t.Helper()
	sum := sha256.Sum256(bin)
	m := protocol.Manifest{Version: version, Issued: time.Now().UTC(), Binaries: []protocol.ManifestBinary{
		{OS: runtime.GOOS, Arch: runtime.GOARCH, File: "vordr-agent-" + runtime.GOOS + "-" + runtime.GOARCH,
			SHA256: hex.EncodeToString(sum[:]), Size: int64(len(bin))}}}
	raw, _ := json.Marshal(m)
	return protocol.Job{ID: "u1", Node: "host-a", Verb: protocol.VerbAgentUpdate, Manifest: raw,
		ManifestSig: protocol.SignManifest(raw, func(b []byte) []byte { return ed25519.Sign(key, b) }),
		Token:       strings.Repeat("ab", 24)}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return "<" + err.Error() + ">"
	}
	return string(b)
}

// Download, check, swap, restart; the new agent confirms on its first approved session.
func TestSelfUpdateAndConfirm(t *testing.T) {
	defer syscall.Umask(syscall.Umask(0o077)) // as in vordr-agent.service
	f := newUpdateFixture(t)
	state := t.TempDir()
	old := f.client(t, "0.6.0", state)
	f.served = newAgentScript("0.7.0")
	var bringer, session outbox
	old.approvedSession(session.send)
	old.agentUpdate(f.job(t, "0.7.0", f.served, f.priv), bringer.send)
	if f.exits != 1 || read(t, f.exe) != string(f.served) || read(t, f.exe+".old") != "old agent" {
		t.Fatalf("swap: exits %d, exe %q, old %q, answers %+v", f.exits, read(t, f.exe), read(t, f.exe+".old"), bringer.sent)
	}
	// The exit names the job: the Windows service asks that job's guard for its restart (ADR-0103).
	if !slices.Equal(f.exitJobs, []string{"u1"}) {
		t.Fatalf("exit for %q", f.exitJobs)
	}
	if st, err := os.Stat(f.exe); err != nil {
		t.Fatal(err)
	} else if st.Mode().Perm() != 0o755 {
		t.Fatalf("new binary mode %v, want 0755 like install-agent.sh", st.Mode().Perm())
	}
	if fin := session.final(); fin.JobID != "" || len(bringer.sent) != 0 {
		t.Fatalf("the old agent must not report the result itself: %+v %+v", session.sent, bringer.sent)
	}
	// The guard starts once, from the binary still in place before the swap: the old one (ADR-0098).
	if !slices.Equal(f.guards, []string{"u1: old agent"}) {
		t.Fatalf("guards %q", f.guards)
	}
	if f.instances != 0 {
		t.Fatal("proxy instances restarted before the new binary is confirmed")
	}

	// systemd starts the new binary.
	next := f.client(t, "0.7.0", state)
	next.settleUpdate()
	var first outbox
	next.approvedSession(first.send)
	if fin := first.final(); fin.Code != protocol.CodeOK || fin.JobID != "u1" {
		t.Fatalf("confirmation: %+v", first.sent)
	}
	// Confirmed: the proxy instances that share the binary are restarted onto it, once (design/stellvertreter.md 4.3).
	if f.instances != 1 {
		t.Fatalf("proxy instance restarts after the confirmation: %d", f.instances)
	}
	select {
	case <-next.confirmed:
	default:
		t.Fatal("the rollback timer is not stopped")
	}
	if _, err := os.Stat(filepath.Join(state, "update.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("marker left behind")
	}
}

// The operator key is lost or compromised: a release signed with the offline backup key still installs.
func TestSelfUpdateTakesTheBackupKey(t *testing.T) {
	f := newUpdateFixture(t)
	f.served = newAgentScript("0.7.0")
	var out outbox
	f.client(t, "0.6.0", t.TempDir()).agentUpdate(f.job(t, "0.7.0", f.served, f.backup), out.send)
	if f.exits != 1 || read(t, f.exe) != string(f.served) {
		t.Fatalf("backup key: exits %d, exe %q, answers %+v", f.exits, read(t, f.exe), out.sent)
	}
}

// The new agent does not reach the hub: it puts the old binary back; the old agent reports the failed update.
func TestSelfUpdateRollsBack(t *testing.T) {
	f := newUpdateFixture(t)
	state := t.TempDir()
	f.served = newAgentScript("0.7.0")
	f.client(t, "0.6.0", state).agentUpdate(f.job(t, "0.7.0", f.served, f.priv), func(any) error { return nil })
	next := f.client(t, "0.7.0", state)
	next.settleUpdate()
	next.rollBack(*next.update) // what the timer does after confirmWindow
	if read(t, f.exe) != "old agent" || !slices.Equal(f.exitJobs, []string{"u1", "u1"}) {
		t.Fatalf("rollback: exe %q, exits for %q", read(t, f.exe), f.exitJobs)
	}
	back := f.client(t, "0.6.0", state)
	back.settleUpdate()
	var s outbox
	back.approvedSession(s.send)
	if fin := s.final(); fin.Code != protocol.CodeFailedVerification || fin.JobID != "u1" ||
		!strings.Contains(fin.Detail, "(no approved session within 10m0s); back on 0.6.0") {
		t.Fatalf("report after the rollback: %+v", s.sent)
	}
	// A failed update leaves the proxy instances alone: they still run the binary that is back in place.
	if f.instances != 0 {
		t.Fatalf("proxy instances restarted after a failed update: %d", f.instances)
	}
}

// The new agent hangs: it neither confirms nor rolls back, and systemd sees it running. The guard the old agent started
// puts the old binary back; the old agent, started again, reports why.
func TestHungUpdateRolledBackByTheGuard(t *testing.T) {
	f := newUpdateFixture(t)
	state := t.TempDir()
	f.served = newAgentScript("0.7.0")
	f.client(t, "0.6.0", state).agentUpdate(f.job(t, "0.7.0", f.served, f.priv), func(any) error { return nil })
	restarts := 0
	g := NewGuard(state, f.exe, "u1", func(context.Context) error { restarts++; return nil }, log.New(io.Discard, "", 0))
	g.Window = 0
	if out := g.Watch(context.Background()); out != GuardRolledBack || read(t, f.exe) != "old agent" || restarts != 1 {
		t.Fatalf("guard: %s, exe %q, restarts %d", out, read(t, f.exe), restarts)
	}
	back := f.client(t, "0.6.0", state)
	back.settleUpdate()
	var s outbox
	back.approvedSession(s.send)
	if fin := s.final(); fin.Code != protocol.CodeFailedVerification || fin.JobID != "u1" ||
		!strings.Contains(fin.Detail, "hung: not confirmed within 0s, the update guard put 0.6.0 back") {
		t.Fatalf("report after the guard: %+v", s.sent)
	}
	if _, err := os.Stat(filepath.Join(state, "update.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("marker left behind after the report")
	}
}

// Without its guard the update does not happen: no swap, no marker, no .new left behind.
func TestSelfUpdateNeedsTheGuard(t *testing.T) {
	f := newUpdateFixture(t)
	state := t.TempDir()
	f.served = newAgentScript("0.7.0")
	f.guardErr = errors.New("systemd-run exit 1: Unit vordr-agent-update-guard.service already exists")
	var out outbox
	f.client(t, "0.6.0", state).agentUpdate(f.job(t, "0.7.0", f.served, f.priv), out.send)
	if fin := out.final(); fin.Code != protocol.CodeFailedPackageManager || !strings.HasPrefix(fin.Detail, "update guard: ") ||
		f.exits != 0 || read(t, f.exe) != "old agent" {
		t.Fatalf("got %+v, exits %d, exe %q", out.sent, f.exits, read(t, f.exe))
	}
	for _, left := range []string{nextPath(f.exe), f.exe + ".old", filepath.Join(state, "update.json")} {
		if _, err := os.Stat(left); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s left behind", left)
		}
	}
}

type recordingRunner struct{ calls [][]string }

func (r *recordingRunner) Run(_ context.Context, _ []string, name string, args ...string) runner.Result {
	r.calls = append(r.calls, append([]string{name}, args...))
	return runner.Result{}
}

// The guard runs as a transient unit of its own, started (Type=exec) before armGuard returns, from the binary in place.
func TestArmGuardStartsATransientUnit(t *testing.T) {
	var r recordingRunner
	if err := armGuard(context.Background(), &r, "/usr/local/bin/vordr-agent", "/etc/vordr/agent.yaml", "ab12"); err != nil {
		t.Fatal(err)
	}
	want := []string{"systemd-run", "--unit=vordr-agent-update-guard.service",
		"--description=Vördr agent: update guard for job ab12", "--property=Type=exec", "--collect", "--quiet",
		"/usr/local/bin/vordr-agent", "update-guard", "--config", "/etc/vordr/agent.yaml", "--job", "ab12",
		"--exe", "/usr/local/bin/vordr-agent"}
	if len(r.calls) != 1 || !slices.Equal(r.calls[0], want) {
		t.Fatalf("calls %q", r.calls)
	}
}

// The proxy instances are restarted only where they run, without waiting for them (design/stellvertreter.md 4.3).
func TestRestartInstancesTriesTheRunningOnes(t *testing.T) {
	var r recordingRunner
	if err := restartInstances(context.Background(), &r); err != nil {
		t.Fatal(err)
	}
	want := []string{"systemctl", "try-restart", "--no-block", "vordr-agent@*.service"}
	if len(r.calls) != 1 || !slices.Equal(r.calls[0], want) {
		t.Fatalf("calls %q", r.calls)
	}
}

func TestSelfUpdateRefuses(t *testing.T) {
	_, foreign, _ := ed25519.GenerateKey(rand.Reader)
	cases := map[string]struct {
		served  []byte
		job     func(f *updateFixture, t *testing.T) protocol.Job
		code    protocol.Code
		version string
	}{
		"foreign signature": {newAgentScript("0.7.0"), func(f *updateFixture, t *testing.T) protocol.Job {
			return f.job(t, "0.7.0", newAgentScript("0.7.0"), foreign)
		}, protocol.CodeRejectedInvalidJob, "0.6.0"},
		"hash mismatch": {newAgentScript("0.7.1"), func(f *updateFixture, t *testing.T) protocol.Job {
			return f.job(t, "0.7.0", newAgentScript("0.7.0"), f.priv)
		}, protocol.CodeFailedVerification, "0.6.0"},
		"wrong version inside": {newAgentScript("0.6.9"), func(f *updateFixture, t *testing.T) protocol.Job {
			return f.job(t, "0.7.0", newAgentScript("0.6.9"), f.priv)
		}, protocol.CodeFailedVerification, "0.6.0"},
		"already running it": {newAgentScript("0.7.0"), func(f *updateFixture, t *testing.T) protocol.Job {
			return f.job(t, "0.7.0", newAgentScript("0.7.0"), f.priv)
		}, protocol.CodeOK, "0.7.0"},
		// A manifest the operator signed long ago, sent again: it would bring an old agent back.
		"older release": {newAgentScript("0.7.1"), func(f *updateFixture, t *testing.T) protocol.Job {
			return f.job(t, "0.7.1", newAgentScript("0.7.1"), f.priv)
		}, protocol.CodeRejectedInvalidJob, "0.7.2"},
		"older by number, not by text": {newAgentScript("0.7.9"), func(f *updateFixture, t *testing.T) protocol.Job {
			return f.job(t, "0.7.9", newAgentScript("0.7.9"), f.priv)
		}, protocol.CodeRejectedInvalidJob, "0.7.10"},
		"not a release": {newAgentScript("0.8.0-dev"), func(f *updateFixture, t *testing.T) protocol.Job {
			return f.job(t, "0.8.0-dev", newAgentScript("0.8.0-dev"), f.priv)
		}, protocol.CodeRejectedInvalidJob, "0.7.2"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newUpdateFixture(t)
			f.served = c.served
			var out outbox
			f.client(t, c.version, t.TempDir()).agentUpdate(c.job(f, t), out.send)
			if fin := out.final(); fin.Code != c.code || f.exits != 0 || read(t, f.exe) != "old agent" {
				t.Fatalf("got %+v, exits %d, exe %q", out.sent, f.exits, read(t, f.exe))
			}
			if _, err := os.Stat(nextPath(f.exe)); err == nil {
				t.Fatal("left a .new behind")
			}
		})
	}
	var out outbox
	c := NewClient(policy.Config{Node: "host-a"}, nil, "0.6.0", facts.Host{}, &fakeChecker{}, log.New(io.Discard, "", 0))
	c.agentUpdate(protocol.Job{ID: "u2"}, out.send)
	if out.final().Code != protocol.CodeRejectedUnsupported || slices.Contains(c.Verbs(), protocol.VerbAgentUpdate) {
		t.Fatalf("without an updater: %+v %v", out.sent, c.Verbs())
	}
}
