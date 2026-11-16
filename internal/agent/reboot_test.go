package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/policy"
	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/runner"
)

// fakeHost stands in for the machine: its boot ID, whether it needs a reboot, the reboots asked of it, and what its
// idle check says (busy: why it is not idle; the check is there only with hasIdle).
type fakeHost struct {
	boot     string
	required bool
	reboots  int
	fail     error
	hasIdle  bool
	busy     string
}

func (h *fakeHost) rebooter() *Rebooter {
	r := &Rebooter{BootID: func() string { return h.boot },
		Reboot:   func(context.Context) error { h.reboots++; return h.fail },
		Required: func(context.Context) bool { return h.required }}
	if h.hasIdle {
		r.Idle = func(context.Context) (bool, string) { return h.busy == "", h.busy }
	}
	return r
}

var allDay = policy.Window{Days: []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}, Start: "00:00", End: "23:59"}

func rebootClient(t *testing.T, h *fakeHost, state string, w *policy.Window) *Client {
	t.Helper()
	c := installClient(t, &fakeInstaller{}, allowAll)
	c.Cfg.StateDir = state
	c.Cfg.Reboot = policy.Reboot{Allowed: true, Window: w}
	c.Rebooter = h.rebooter()
	return c
}

func rebootJob(id string) protocol.Job {
	return protocol.Job{ID: id, Node: "host-a", Verb: protocol.VerbReboot}
}

// The host needs a reboot: the agent says it restarts, reboots, and the agent after the boot proves it.
func TestRebootAndProof(t *testing.T) {
	h := &fakeHost{boot: "boot-1", required: true}
	state := t.TempDir()
	c := rebootClient(t, h, state, &allDay)
	if !slices.Contains(c.Verbs(), protocol.VerbReboot) {
		t.Fatalf("reboot not announced: %v", c.Verbs())
	}
	var session outbox
	c.approvedSession(session.send)
	c.rebootHost(rebootJob("r1"), session.send)
	if h.reboots != 1 {
		t.Fatalf("reboots: %d", h.reboots)
	}
	st := session.statuses()
	if len(st) == 0 || st[len(st)-1].Phase != protocol.PhaseRebooting || session.final().JobID != "" {
		t.Fatalf("before the reboot only the announcement: %+v", st)
	}

	// The host comes back on a new boot, the reboot is no longer needed.
	h.boot, h.required = "boot-2", false
	next := rebootClient(t, h, state, &allDay)
	var after outbox
	next.approvedSession(after.send)
	if fin := after.final(); fin.JobID != "r1" || fin.Code != protocol.CodeOK {
		t.Fatalf("after the boot: %+v", after.sent)
	}
	var again outbox
	next.approvedSession(again.send)
	if len(again.statuses()) != 0 {
		t.Fatalf("reported twice: %+v", again.sent)
	}
	// No second reboot within the hour.
	var soon outbox
	h.required = true
	next.rebootHost(rebootJob("r2"), soon.send)
	if soon.final().Code != protocol.CodeRejectedRateLimited || h.reboots != 1 {
		t.Fatalf("second reboot within the hour: %+v", soon.sent)
	}
}

// The host did not reboot, or still needs one: the result says so.
func TestRebootProofFails(t *testing.T) {
	for name, tc := range map[string]struct {
		boot     string
		required bool
		want     string
	}{
		"same boot":    {"boot-1", false, "did not reboot"},
		"still needed": {"boot-2", true, "still needs a reboot"},
	} {
		t.Run(name, func(t *testing.T) {
			h := &fakeHost{boot: "boot-1", required: true}
			state := t.TempDir()
			rebootClient(t, h, state, &allDay).rebootHost(rebootJob("r1"), (&outbox{}).send)
			h.boot, h.required = tc.boot, tc.required
			var after outbox
			rebootClient(t, h, state, &allDay).approvedSession(after.send)
			if fin := after.final(); fin.Code != protocol.CodeFailedVerification || !strings.Contains(fin.Detail, tc.want) {
				t.Fatalf("result: %+v", fin)
			}
		})
	}
}

// Outside the reboot window with a reason (ADR-0087): it reboots.
func TestRebootOutsideWindowWithReason(t *testing.T) {
	h := &fakeHost{boot: "boot-1", required: true}
	closed := closedWindow()
	c := rebootClient(t, h, t.TempDir(), &closed)
	var session outbox
	c.approvedSession(session.send)
	j := rebootJob("r1")
	j.OutsideWindow = "kernel update, approved for tonight"
	c.rebootHost(j, session.send)
	if h.reboots != 1 {
		t.Fatalf("reboots %d, sent %+v", h.reboots, session.sent)
	}
}

// On a host with reboot.window_only (ADR-0154) a reason does not open the window; inside it the reboot goes through.
func TestRebootWindowOnly(t *testing.T) {
	h := &fakeHost{boot: "boot-1", required: true}
	closed := closedWindow()
	c := rebootClient(t, h, t.TempDir(), &closed)
	c.Cfg.Reboot.WindowOnly = true
	var out outbox
	j := rebootJob("r1")
	j.OutsideWindow = "started in Home Assistant"
	c.rebootHost(j, out.send)
	if fin := out.final(); fin.Code != protocol.CodeRejectedOutsideWindow || !strings.Contains(fin.Detail, "window_only") ||
		h.reboots != 0 {
		t.Fatalf("outside the window with a reason: reboots %d, sent %+v", h.reboots, out.sent)
	}
	c.Cfg.Reboot.Window = &allDay
	c.rebootHost(j, (&outbox{}).send)
	if h.reboots != 1 {
		t.Fatalf("inside the window: reboots %d", h.reboots)
	}
}

// The host's idle check (ADR-0155) holds a needed reboot while it says the host is busy; once it is idle, it reboots.
func TestRebootWaitsForIdle(t *testing.T) {
	h := &fakeHost{boot: "boot-1", required: true, hasIdle: true, busy: "2 tasks running (garbage collection)"}
	c := rebootClient(t, h, t.TempDir(), &allDay)
	var out outbox
	c.rebootHost(rebootJob("r1"), out.send)
	if fin := out.final(); fin.Code != protocol.CodeRejectedNotIdle || fin.Detail != h.busy || h.reboots != 0 {
		t.Fatalf("busy: reboots %d, sent %+v", h.reboots, out.sent)
	}
	if _, marked := c.readRebootMarker(); marked {
		t.Fatal("marker left behind")
	}
	h.busy = ""
	c.rebootHost(rebootJob("r2"), (&outbox{}).send)
	if h.reboots != 1 {
		t.Fatalf("idle: reboots %d", h.reboots)
	}
}

// A reboot that did not take (ADR-0153) holds every further reboot through Vördr while the host runs the boot in which
// that was found, also after the hour; once the host has booted another way, a needed reboot goes through again.
func TestRebootAfterFailedProofIsHeld(t *testing.T) {
	for name, bootAfter := range map[string]string{"still needed": "boot-2", "same boot": "boot-1"} {
		t.Run(name, func(t *testing.T) {
			h := &fakeHost{boot: "boot-1", required: true}
			state := t.TempDir()
			rebootClient(t, h, state, &allDay).rebootHost(rebootJob("r1"), (&outbox{}).send)
			h.boot = bootAfter
			next := rebootClient(t, h, state, &allDay)
			var after outbox
			next.approvedSession(after.send)
			if fin := after.final(); fin.Code != protocol.CodeFailedVerification {
				t.Fatalf("proof: %+v", after.sent)
			}
			next.Now = func() time.Time { return time.Now().Add(2 * time.Hour) }
			var held outbox
			next.rebootHost(rebootJob("r2"), held.send)
			if fin := held.final(); fin.Code != protocol.CodeRejectedLastRebootFailed || h.reboots != 1 {
				t.Fatalf("after the failed proof: reboots %d, sent %+v", h.reboots, held.sent)
			}
			h.boot = "boot-3" // someone rebooted the host another way; it still needs a reboot
			next.rebootHost(rebootJob("r3"), (&outbox{}).send)
			if h.reboots != 2 {
				t.Fatalf("after a boot another way: reboots %d", h.reboots)
			}
		})
	}
}

// The real idle check (ADR-0155): its exit status is its answer; a check that is gone or hangs holds the reboot too.
func TestIdleCheckCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the reboot verb is Linux only")
	}
	dir := t.TempDir()
	script := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	defer func(was time.Duration) { idleCheckTimeout = was }(idleCheckTimeout)
	idleCheckTimeout = 500 * time.Millisecond
	for name, tc := range map[string]struct {
		argv []string
		idle bool
		why  string
	}{
		"idle":            {[]string{script("idle", "exit 0")}, true, ""},
		"busy":            {[]string{script("busy", "echo; echo '  backup of ct/302 running  '; exit 1")}, false, "backup of ct/302 running"},
		"busy, no output": {[]string{script("quiet", "exit 3")}, false, "idle check exited with status 3"},
		"arguments":       {[]string{script("args", `[ "$1" = --strict ] || { echo "got $1" >&2; exit 2; }`), "--strict"}, true, ""},
		"missing":         {[]string{filepath.Join(dir, "gone")}, false, "is missing"},
		"hangs":           {[]string{script("hangs", "exec sleep 5")}, false, "did not finish"},
	} {
		t.Run(name, func(t *testing.T) {
			idle, why := idleCheck(tc.argv, runner.Exec{})(context.Background())
			if idle != tc.idle || !strings.Contains(why, tc.why) || (tc.why == "" && why != "") {
				t.Fatalf("idle %v, why %q; want %v, %q", idle, why, tc.idle, tc.why)
			}
		})
	}
}

// Refusals: nothing reboots.
func TestRebootRefusals(t *testing.T) {
	now := time.Now().In(time.Local)
	closed := policy.Window{Days: []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"},
		Start: now.Add(time.Hour).Format("15:04"), End: now.Add(2 * time.Hour).Format("15:04")}
	cases := map[string]struct {
		setup func(c *Client, h *fakeHost)
		code  protocol.Code
	}{
		"not allowed":   {func(c *Client, _ *fakeHost) { c.Cfg.Reboot.Allowed = false }, protocol.CodeRejectedNotAllowed},
		"window closed": {func(c *Client, _ *fakeHost) { c.Cfg.Reboot.Window = &closed }, protocol.CodeRejectedOutsideWindow},
		"not needed":    {func(_ *Client, h *fakeHost) { h.required = false }, protocol.CodeOK},
		"busy":          {func(c *Client, _ *fakeHost) { c.checkMu.Lock() }, protocol.CodeRejectedBusy},
		"not idle":      {func(_ *Client, h *fakeHost) { h.hasIdle, h.busy = true, "verify job running" }, protocol.CodeRejectedNotIdle},
		"reboot fails":  {func(_ *Client, h *fakeHost) { h.fail = errors.New("Access denied") }, protocol.CodeFailedVerification},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h := &fakeHost{boot: "boot-1", required: true}
			c := rebootClient(t, h, t.TempDir(), &allDay)
			tc.setup(c, h)
			c.Rebooter = h.rebooter() // after the setup: the idle check is there only if the case put it there
			var out outbox
			c.rebootHost(rebootJob("r1"), out.send)
			if fin := out.final(); fin.Code != tc.code {
				t.Fatalf("got %+v", out.sent)
			}
			if name != "reboot fails" && h.reboots != 0 {
				t.Fatal("rebooted despite the refusal")
			}
			if _, marked := c.readRebootMarker(); marked {
				t.Fatal("marker left behind")
			}
		})
	}
	if v := installClient(t, &fakeInstaller{}, allowAll).Verbs(); slices.Contains(v, protocol.VerbReboot) {
		t.Fatalf("without reboot.allowed the agent announces %v", v)
	}
}
