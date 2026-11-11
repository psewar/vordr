package hub

import (
	"testing"
	"time"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/wire"
)

// windowPolicy: install.window 02:00-04:00 every day in UTC, as an agent announces it.
func windowPolicy(outsideWindow bool) wire.PolicySummary {
	return wire.PolicySummary{AllowedScopes: []string{"security", "all"}, DefaultScope: "all", OutsideWindow: outsideWindow,
		Window: &wire.WindowInfo{Days: []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}, Start: "02:00", End: "04:00"},
		TZ:     "UTC"}
}

// The install button installs at once (ADR-0109): outside the window an install without a reason gets the button's
// reason, a person's own reason stays. Inside the window, for a reboot, for an agent that takes no reason, and where
// the hub cannot read the window, nothing is added.
func TestInstallButtonReason(t *testing.T) {
	f := newViewFixture(t)
	closedAt := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	openAt := time.Date(2026, 11, 4, 2, 30, 0, 0, time.UTC)
	f.host(t, "new", []protocol.Verb{protocol.VerbInstall, protocol.VerbReboot}, windowPolicy(true), true)
	f.host(t, "old", []protocol.Verb{protocol.VerbInstall}, windowPolicy(false), true)
	nowin := windowPolicy(true)
	nowin.Window, nowin.TZ = nil, ""
	f.host(t, "nowin", []protocol.Verb{protocol.VerbInstall}, nowin, true)
	badtz := windowPolicy(true)
	badtz.TZ = "Mars/Olympus_Mons"
	f.host(t, "badtz", []protocol.Verb{protocol.VerbInstall}, badtz, true)
	newYork := windowPolicy(true)
	newYork.TZ = "America/New_York" // 02:00 there is 07:00 UTC in winter
	f.host(t, "newyork", []protocol.Verb{protocol.VerbInstall}, newYork, true)

	for _, c := range []struct {
		node  string
		verb  protocol.Verb
		given string
		at    time.Time
		want  string
	}{
		{"new", protocol.VerbInstall, "", closedAt, ButtonReason},
		{"new", protocol.VerbInstall, "ops: now is better", closedAt, "ops: now is better"},
		{"new", protocol.VerbInstall, "", openAt, ""},
		{"new", protocol.VerbReboot, "", closedAt, ""}, // a reboot keeps to its window
		{"new", protocol.VerbReboot, "ops", closedAt, "ops"},
		{"old", protocol.VerbInstall, "", closedAt, ""}, // would refuse a reason as an invalid job
		{"nowin", protocol.VerbInstall, "", closedAt, ""},
		{"badtz", protocol.VerbInstall, "", closedAt, ""},
		{"newyork", protocol.VerbInstall, "", time.Date(2026, 11, 4, 7, 30, 0, 0, time.UTC), ""}, // 02:30 in New York
		{"newyork", protocol.VerbInstall, "", openAt.Add(time.Hour), ButtonReason},               // 22:30 in New York
		{"unknown", protocol.VerbInstall, "", closedAt, ""},
	} {
		f.now = c.at
		if got := f.srv.haReason(c.node, c.verb, c.given); got != c.want {
			t.Errorf("%s %s %q at %s: %q, want %q", c.node, c.verb, c.given, c.at.Format("15:04"), got, c.want)
		}
	}
	if !protocol.ValidReason(ButtonReason) {
		t.Fatal("the button's reason would not pass the agent")
	}
}

// A job the window would stop is refused before a guest is backed up for it: otherwise "update all" outside the window
// would back up every guest, and then every agent would refuse the install. With a reason it waits for its backup as
// before.
func TestNoBackupForAnInstallTheWindowStops(t *testing.T) {
	f := newViewFixture(t)
	f.now = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	f.host(t, "golf", []protocol.Verb{protocol.VerbCheck, protocol.VerbInstall}, windowPolicy(false), true)
	f.host(t, "india", []protocol.Verb{protocol.VerbCheck, protocol.VerbBackup}, wire.PolicySummary{}, true)
	for _, n := range []string{"golf", "india"} {
		f.srv.sessions[n] = &session{node: n, waiters: map[string]chan wire.JobStatus{}}
	}
	pbs := newFakePBS(t, map[string]int64{"vm/301": f.now.Add(-30 * time.Hour).Unix()})
	cfg := backupConfig(t, pbs)
	cfg.BeforeInstall = &BeforeInstall{Hypervisor: "india"}
	b, err := NewBackups(cfg, func() time.Time { return f.now })
	if err != nil {
		t.Fatal(err)
	}
	f.srv.Backups = b

	if _, code := f.srv.startJob("golf", protocol.Job{ID: "j1", Verb: protocol.VerbInstall, Scope: "all"}, ByAdmin); code != AdminOutsideWindow {
		t.Fatalf("install without a reason outside the window: %s", code)
	}
	if e, _ := f.srv.Reg.Get("golf"); e.LastRun != nil {
		t.Fatalf("a refused install started a run: %+v", e.LastRun)
	}
	if f.srv.windowRefuses("golf", protocol.Job{Verb: protocol.VerbCheck}) ||
		f.srv.windowRefuses("golf", protocol.Job{Verb: protocol.VerbAgentUpdate}) {
		t.Fatal("a check or an agent update has no window")
	}

	// Home Assistant, an agent that takes no reason: refused before the backup check (the backup is 30 h old, so
	// without this the answer would be backup_stale), nothing backed up.
	h, fp := haHello(t, "bravo")
	f.srv.Clients.Admit(h, fp, f.now)
	f.srv.Clients.Decide("bravo", wire.StateApproved, fp, f.now)
	sess := &haSession{name: "bravo", fingerprint: fp}
	if res := f.srv.haCommand(sess, wire.Command{T: wire.TCommand, RequestID: "r1", Node: "golf", Command: "install"}); res.Code != string(AdminOutsideWindow) {
		t.Fatalf("install button, older agent, window closed: %+v", res)
	}
	if e, _ := f.srv.Reg.Get("golf"); e.LastRun != nil {
		t.Fatalf("a refused command started a run: %+v", e.LastRun)
	}

	// Inside the window the same install waits for its guest's backup.
	f.now = time.Date(2026, 11, 5, 2, 30, 0, 0, time.UTC)
	j, code := f.srv.startJob("golf", protocol.Job{ID: "j2", Verb: protocol.VerbInstall, Scope: "all"}, ByAdmin)
	if code != AdminOK || j.pending == nil {
		t.Fatalf("install inside the window: %s %+v", code, j)
	}
	if e, _ := f.srv.Reg.Get("golf"); e.LastRun == nil || e.LastRun.Phase != protocol.PhaseBackingUp {
		t.Fatalf("run inside the window: %+v", e.LastRun)
	}
}
