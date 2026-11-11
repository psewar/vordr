package agent

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/policy"
	"github.com/psewar/vordr/internal/protocol"
)

// fakeHost stands in for the machine: its boot ID, whether it needs a reboot, and the reboots asked of it.
type fakeHost struct {
	boot     string
	required bool
	reboots  int
	fail     error
}

func (h *fakeHost) rebooter() *Rebooter {
	return &Rebooter{BootID: func() string { return h.boot },
		Reboot:   func(context.Context) error { h.reboots++; return h.fail },
		Required: func(context.Context) bool { return h.required }}
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
		"reboot fails":  {func(_ *Client, h *fakeHost) { h.fail = errors.New("Access denied") }, protocol.CodeFailedVerification},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h := &fakeHost{boot: "boot-1", required: true}
			c := rebootClient(t, h, t.TempDir(), &allDay)
			tc.setup(c, h)
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
