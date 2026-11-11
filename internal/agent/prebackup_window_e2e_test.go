package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/hub"
	"github.com/psewar/vordr/internal/policy"
	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/runner"
	"github.com/psewar/vordr/internal/wire"
)

// fakeClock is one clock for the hub and its agents, moved on by the test.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// windowFixture: a guest with install.window 02:00-04:00 and a backup before each install; the clock stands at 03:30,
// inside the window. The hub reads the window in the zone the agent announces, in one process this one's own.
func windowFixture(t *testing.T) (*backupFixture, *fakeClock) {
	t.Helper()
	oldZone := zoneName
	zoneName = func() string { return "Local" }
	t.Cleanup(func() { zoneName = oldZone })
	y, m, d := time.Now().Date()
	clk := &fakeClock{t: time.Date(y, m, d, 3, 30, 0, 0, time.Local)}
	win := policy.Window{Days: []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}, Start: "02:00", End: "04:00"}
	f := newBackupFixtureAt(t, map[string]time.Time{"ct/302": clk.now().Add(-30 * time.Hour)}, true, clk.now, &win)
	return f, clk
}

// The window closes while the guest is backed up for an install asked for in Home Assistant (review of ADR-0109): the
// install still goes out right after the backup, with the button's reason, instead of being refused by the agent with
// the guest backed up for nothing.
func TestWindowClosesDuringTheBackupFromHomeAssistant(t *testing.T) {
	f, clk := windowFixture(t)
	hold := make(chan struct{})
	f.setVzdump(func() runner.Result {
		<-hold
		clk.add(time.Hour) // 04:30: the window closed while the backup ran
		f.pbs.set("ct/302", clk.now())
		return runner.Result{Stderr: []byte("INFO: Finished Backup of VM 302\n")}
	})
	ha, _ := dialHA(t, f.hub, wire.HAPath, "bravo", newHAKey(t), nil)
	f.hub.srv.Admin(hub.AdminRequest{Cmd: "approve-client", Client: "bravo", Fingerprint: ha.fp})
	var snap wire.Snapshot
	ha.until(wire.TSnapshot, &snap, nil)
	res := ha.command("r1", "guest", "install")
	if res.Code != string(hub.AdminOK) || res.JobID == "" {
		t.Fatalf("install button inside the window: %+v", res)
	}
	waitFor(t, "vzdump", func() bool { return f.vzdumps.Load() == 1 })
	if r := f.run("guest"); r == nil || r.Phase != protocol.PhaseBackingUp || r.OutsideWindow != "" {
		t.Fatalf("inside the window, before the backup: %+v", r)
	}
	close(hold)
	waitFor(t, "install", func() bool { r := f.run("guest"); return r != nil && r.Finished != nil })
	if r := f.run("guest"); r.Code != protocol.CodeOK || r.OutsideWindow != hub.ButtonReason || f.inst.calls.Load() != 1 {
		t.Fatalf("after the backup, window closed: run %+v, installs %d", r, f.inst.calls.Load())
	}
}

// The same for an install without a reason from elsewhere (the admin socket, automatic installs): the hub ends the run
// with the agent's code itself, after the backup, and sends nothing.
func TestWindowClosesDuringTheBackupWithoutReason(t *testing.T) {
	f, clk := windowFixture(t)
	f.setVzdump(func() runner.Result {
		clk.add(time.Hour)
		f.pbs.set("ct/302", clk.now())
		return runner.Result{Stderr: []byte("INFO: Finished Backup of VM 302\n")}
	})
	job, code := f.hub.srv.StartJob("guest", protocol.VerbInstall, "all", hub.ByAdmin)
	if code != hub.AdminOK {
		t.Fatalf("start inside the window: %s", code)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, _ := f.hub.srv.WaitJob(ctx, job)
	if st.Code != protocol.CodeRejectedOutsideWindow || !strings.Contains(st.Detail, "after the backup") ||
		f.vzdumps.Load() != 1 || f.inst.calls.Load() != 0 {
		t.Fatalf("result %+v, vzdump %d, installs %d", st, f.vzdumps.Load(), f.inst.calls.Load())
	}
}

// When the window closes while the install waits for its turn, the hub ends the run before anything is backed up.
func TestWindowClosesBeforeTheBackup(t *testing.T) {
	f, clk := windowFixture(t)
	f.setVzdump(func() runner.Result {
		f.pbs.set("ct/302", clk.now())
		return runner.Result{Stderr: []byte("INFO: Finished Backup of VM 302\n")}
	})
	job, code := f.hub.srv.StartJob("guest", protocol.VerbInstall, "all", hub.ByAdmin)
	if code != hub.AdminOK {
		t.Fatalf("start inside the window: %s", code)
	}
	clk.add(time.Hour) // the turn comes after 04:00
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, _ := f.hub.srv.WaitJob(ctx, job)
	if st.Code != protocol.CodeRejectedOutsideWindow || !strings.Contains(st.Detail, "before the backup") ||
		f.vzdumps.Load() != 0 || f.inst.calls.Load() != 0 {
		t.Fatalf("result %+v, vzdump %d, installs %d", st, f.vzdumps.Load(), f.inst.calls.Load())
	}
}
