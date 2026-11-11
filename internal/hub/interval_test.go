package hub

import (
	"testing"
	"time"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/wire"
)

// The agent's minimum interval as the hub reads it (ADR-0110): from the start of its last run that changed packages.
// An agent without the announcement, or without such a run, leaves the hub nothing to check.
func TestNextInstallFromTheAgentsInterval(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	last := now.Add(-10 * time.Minute)
	for name, c := range map[string]struct {
		interval int
		last     *time.Time
		want     *time.Time
	}{
		"within":        {6, &last, ptr(last.Add(6 * time.Hour))},
		"after":         {6, ptr(now.Add(-6 * time.Hour)), nil},
		"older agent":   {0, &last, nil},
		"no run so far": {6, nil, nil},
	} {
		e := Entry{Policy: wire.PolicySummary{MinIntervalH: c.interval}, LastInstall: c.last}
		got := nextInstall(e, now)
		if (got == nil) != (c.want == nil) || (got != nil && !got.Equal(*c.want)) {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
	for scope, want := range map[string]bool{"all": true, "security": true, "app:example": true,
		"pkg:npm-global": false, "pkg:npm-global/example-lib": false, "deps:npm-global": false, "nonsense:": false,
		"image:stack/web": false, "images": false} {
		if got := intervalApplies(protocol.Job{Verb: protocol.VerbInstall, Scope: scope}); got != want {
			t.Errorf("interval applies to %q: %v, want %v", scope, got, want)
		}
	}
	if intervalApplies(protocol.Job{Verb: protocol.VerbReboot}) || intervalApplies(protocol.Job{Verb: protocol.VerbCheck}) {
		t.Error("the interval holds only installs")
	}
}

func ptr(t time.Time) *time.Time { return &t }

// An install the agent's minimum interval would stop is refused before its guest is backed up: otherwise a dependency
// refresh followed by an install minutes later would back up the guest, and the agent would refuse the install after
// the backup step. A pip/npm run is not held back. The view says when the next install is possible.
func TestRateLimitedRefusedBeforeBackup(t *testing.T) {
	f := newViewFixture(t)
	f.now = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	pol := wire.PolicySummary{AllowedScopes: []string{"security", "all"}, DefaultScope: "all", OutsideWindow: true,
		MinIntervalH: 6}
	f.host(t, "golf", []protocol.Verb{protocol.VerbCheck, protocol.VerbInstall}, pol, true)
	f.host(t, "india", []protocol.Verb{protocol.VerbCheck, protocol.VerbBackup}, wire.PolicySummary{}, true)
	for _, n := range []string{"golf", "india"} {
		f.srv.sessions[n] = &session{node: n, waiters: map[string]chan wire.JobStatus{}}
	}
	last := f.now.Add(-10 * time.Minute)
	if err := f.srv.Reg.SetLastInstall("golf", last); err != nil {
		t.Fatal(err)
	}
	pbs := newFakePBS(t, map[string]int64{"vm/301": f.now.Add(-30 * time.Hour).Unix()})
	cfg := backupConfig(t, pbs)
	cfg.BeforeInstall = &BeforeInstall{Hypervisor: "india"}
	b, err := NewBackups(cfg, func() time.Time { return f.now })
	if err != nil {
		t.Fatal(err)
	}
	f.srv.Backups = b

	if _, code := f.srv.startJob("golf", protocol.Job{ID: "j1", Verb: protocol.VerbInstall, Scope: "all"}, ByAdmin); code != AdminRateLimited {
		t.Fatalf("install within the interval: %s", code)
	}
	if e, _ := f.srv.Reg.Get("golf"); e.LastRun != nil {
		t.Fatalf("a refused install started a run: %+v", e.LastRun)
	}
	if v := f.view(t, "golf", f.now); v.NextInstall == nil || !v.NextInstall.Equal(last.Add(6*time.Hour)) {
		t.Fatalf("view: next install %v", v.NextInstall)
	}

	// Home Assistant: refused before the backup check (the backup is 30 h old, so the answer would be backup_stale).
	h, fp := haHello(t, "bravo")
	f.srv.Clients.Admit(h, fp, f.now)
	f.srv.Clients.Decide("bravo", wire.StateApproved, fp, f.now)
	sess := &haSession{name: "bravo", fingerprint: fp}
	if res := f.srv.haCommand(sess, wire.Command{T: wire.TCommand, RequestID: "r1", Node: "golf", Command: "install"}); res.Code != string(AdminRateLimited) {
		t.Fatalf("install button within the interval: %+v", res)
	}

	// A pip/npm run is not held back: it goes on to wait for its guest's backup.
	if j, code := f.srv.startJob("golf", protocol.Job{ID: "j2", Verb: protocol.VerbInstall, Scope: "pkg:npm-global"}, ByAdmin); code != AdminOK || j.pending == nil {
		t.Fatalf("pip/npm run within the interval: %s %+v", code, j)
	}

	// Once the interval has passed, the install waits for its backup like any other, and the view says nothing.
	_ = f.srv.Reg.SetRun("golf", Run{JobID: "j2", Verb: protocol.VerbInstall, Scope: "pkg:npm-global", Code: protocol.CodeOK,
		Started: f.now, Finished: &f.now})
	f.now = last.Add(6 * time.Hour)
	if j, code := f.srv.startJob("golf", protocol.Job{ID: "j3", Verb: protocol.VerbInstall, Scope: "all"}, ByAdmin); code != AdminOK || j.pending == nil {
		t.Fatalf("install after the interval: %s %+v", code, j)
	}
	if v := f.view(t, "golf", f.now); v.NextInstall != nil {
		t.Fatalf("view after the interval: %v", v.NextInstall)
	}
}

// An automatic install the interval would stop is not due yet, and does not use up the window's one attempt: later in
// the same window, once the interval has passed, the host is due.
func TestAutoInstallWaitsForTheInterval(t *testing.T) {
	f := newAutoFixture(t, func(p *wire.PolicySummary) { p.MinIntervalH = 6 })
	if err := f.srv.Reg.SetLastInstall("canary", f.now.Add(-6*time.Hour+10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, r := f.due(t, "canary"); r != autoNotReady {
		t.Fatalf("canary within its interval: %q", r)
	}
	f.now = f.now.Add(15 * time.Minute) // 02:45, still inside the window
	if scope, r := f.due(t, "canary"); r != autoReady || scope != "security" {
		t.Fatalf("canary after its interval: %q %q", scope, r)
	}
}

// The register takes the agent's last run from its hello and from final statuses (ADR-0110); a hello without it (no
// journal) clears it, since the agent then holds nothing back.
func TestLastInstallFromHelloAndStatus(t *testing.T) {
	f := newViewFixture(t)
	pol := wire.PolicySummary{AllowedScopes: []string{"all"}, DefaultScope: "all", MinIntervalH: 6}
	fp := f.host(t, "golf", []protocol.Verb{protocol.VerbInstall}, pol, true)
	e, _ := f.srv.Reg.Get("golf")
	at := f.now.Add(-time.Hour)
	h := wire.Hello{Node: "golf", PublicKey: e.PublicKey, AgentVersion: "1.0", Verbs: e.Verbs, Policy: pol, LastInstall: &at}
	if _, _, err := f.srv.Reg.Admit(h, fp, f.now, 5); err != nil {
		t.Fatal(err)
	}
	if e, _ := f.srv.Reg.Get("golf"); e.LastInstall == nil || !e.LastInstall.Equal(at) {
		t.Fatalf("from the hello: %v", e.LastInstall)
	}
	later := f.now
	if err := f.srv.Reg.SetLastInstall("golf", later); err != nil {
		t.Fatal(err)
	}
	if e, _ := f.srv.Reg.Get("golf"); !e.LastInstall.Equal(later) {
		t.Fatalf("from a status: %v", e.LastInstall)
	}
	h.LastInstall = nil
	if _, _, err := f.srv.Reg.Admit(h, fp, f.now, 5); err != nil {
		t.Fatal(err)
	}
	if e, _ := f.srv.Reg.Get("golf"); e.LastInstall != nil {
		t.Fatalf("a hello without a journal kept %v", e.LastInstall)
	}
}
