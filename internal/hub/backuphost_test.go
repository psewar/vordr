package hub

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/wire"
)

// backupHostFixture: guest golf (vm/301) gets a fresh backup before its install, from a hypervisor each case names,
// and hypervisors in every state the gate tells apart.
type backupHostFixture struct {
	*viewFixture
	pbs *fakePBS
}

func newBackupHostFixture(t *testing.T) *backupHostFixture {
	t.Helper()
	f := &backupHostFixture{viewFixture: newViewFixture(t)}
	f.now = time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)
	f.pbs = newFakePBS(t, map[string]int64{"vm/301": f.now.Add(-time.Hour).Unix()})
	install := wire.PolicySummary{AllowedScopes: []string{"all"}, DefaultScope: "all"}
	f.host(t, "golf", []protocol.Verb{protocol.VerbCheck, protocol.VerbInstall, protocol.VerbAgentUpdate}, install, true)
	backup := []protocol.Verb{protocol.VerbCheck, protocol.VerbAgentUpdate, protocol.VerbBackup}
	f.host(t, "pending", backup, wire.PolicySummary{}, false)
	f.host(t, "old", []protocol.Verb{protocol.VerbCheck, protocol.VerbAgentUpdate}, wire.PolicySummary{}, true)
	for _, hv := range []string{"here", "away", "updating", "updated", "installing"} {
		f.host(t, hv, backup, wire.PolicySummary{}, true)
	}
	for _, n := range []string{"golf", "old", "here"} {
		f.srv.sessions[n] = &session{node: n, waiters: map[string]chan wire.JobStatus{}}
	}
	// updating: the agent said it restarts for its update, the run has no result yet - "update all" in Home Assistant
	// updates the hypervisor's agent while it installs its guests.
	f.srv.storeRun("updating", Run{JobID: "u1", Verb: protocol.VerbAgentUpdate, By: "ha:bravo",
		Phase: protocol.PhaseRebooting, Started: f.now})
	// updated: the same run, over; the agent did not come back.
	done := f.now
	f.srv.storeRun("updated", Run{JobID: "u2", Verb: protocol.VerbAgentUpdate, By: "ha:bravo",
		Started: f.now.Add(-time.Minute), Code: protocol.CodeOK, Finished: &done})
	// installing: a run goes, but the agent never said it restarts: its connection ended for another reason.
	f.srv.storeRun("installing", Run{JobID: "i1", Verb: protocol.VerbInstall, Scope: "all", By: ByAdmin,
		Phase: protocol.PhaseInstalling, Started: f.now})
	return f
}

// hypervisor makes hv the one that backs golf up before its install.
func (f *backupHostFixture) hypervisor(t *testing.T, hv string) {
	t.Helper()
	cfg := backupConfig(t, f.pbs)
	cfg.BeforeInstall = &BeforeInstall{Hypervisor: hv}
	b, err := NewBackups(cfg, func() time.Time { return f.now })
	if err != nil {
		t.Fatal(err)
	}
	f.srv.Backups = b
}

// Each state of the hypervisor's agent that keeps it from the backup has its own code (ADR-0199). Away only because a
// run of the hub's restarts it, it counts as there: the install's run waits for it.
func TestBackupHostStates(t *testing.T) {
	f := newBackupHostFixture(t)
	for hv, want := range map[string]AdminCode{
		"nobody":     AdminBackupHostUnknown,
		"pending":    AdminBackupHostNotApproved,
		"old":        AdminBackupHostNoGuestBackup,
		"away":       AdminBackupHostOffline,
		"updated":    AdminBackupHostOffline,
		"installing": AdminBackupHostOffline,
		"updating":   AdminOK,
		"here":       AdminOK,
	} {
		f.hypervisor(t, hv)
		if code, v := f.srv.backupGate(context.Background(), "golf"); code != want || v.BeforeInstall != hv {
			t.Errorf("hypervisor %s: %s, %+v; want %s", hv, code, v, want)
		}
	}
}

// Home Assistant's install button on a guest while the hub updates its hypervisor's agent and the guest's own: the
// install waits for the guest's run, as any command does, and its backup for the hypervisor's. A hypervisor that is
// simply offline still stops it before anything starts, and the refusal names that state.
func TestInstallWhileTheHypervisorRestarts(t *testing.T) {
	f := newBackupHostFixture(t)
	f.srv.storeRun("golf", Run{JobID: "g1", Verb: protocol.VerbAgentUpdate, By: "ha:bravo",
		Phase: protocol.PhaseRebooting, Started: f.now})
	h, fp := haHello(t, "bravo")
	f.srv.Clients.Admit(h, fp, f.now)
	f.srv.Clients.Decide("bravo", wire.StateApproved, fp, f.now)
	sess := &haSession{name: "bravo", fingerprint: fp}
	install := wire.Command{T: wire.TCommand, RequestID: "r1", Node: "golf", Command: "install"}

	f.hypervisor(t, "away")
	if res := f.srv.haCommand(sess, install); res.Code != string(AdminBackupHostOffline) {
		t.Fatalf("hypervisor offline: %+v", res)
	}
	if q := f.srv.Queued("golf"); len(q) != 0 {
		t.Fatalf("a refused install waits: %v", q)
	}

	f.hypervisor(t, "updating")
	if res := f.srv.haCommand(sess, install); res.Code != string(AdminOK) {
		t.Fatalf("hypervisor restarting for its agent update: %+v", res)
	}
	if q := f.srv.Queued("golf"); len(q) != 1 || q[0] != "install:all" {
		t.Fatalf("the install does not wait for the guest's run: %v", q)
	}

	lines, _, err := f.srv.Events.Read(EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	var refused, queued int
	for _, l := range lines {
		s := string(l)
		switch {
		case strings.Contains(s, `"event":"command_refused"`) && strings.Contains(s, `"code":"backup_host_offline"`):
			refused++
		case strings.Contains(s, `"event":"command_queued"`):
			queued++
		}
	}
	if refused != 1 || queued != 1 {
		t.Fatalf("events: %d refused with backup_host_offline, %d queued; want 1 and 1", refused, queued)
	}
}
