package hub

import (
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/wire"
)

func queueFixture(t *testing.T) (*viewFixture, *haSession) {
	t.Helper()
	f := newViewFixture(t)
	f.host(t, "h", []protocol.Verb{protocol.VerbCheck, protocol.VerbInstall},
		wire.PolicySummary{AllowedScopes: []string{"security", "all"}, DefaultScope: "all"}, true)
	h, fp := haHello(t, "bravo")
	f.srv.Clients.Admit(h, fp, f.now)
	f.srv.Clients.Decide("bravo", wire.StateApproved, fp, f.now)
	if err := f.srv.Reg.SetRun("h", Run{JobID: "j1", Verb: protocol.VerbAgentUpdate, Phase: protocol.PhaseDownloading,
		Started: f.now}); err != nil {
		t.Fatal(err)
	}
	return f, &haSession{name: "bravo", fingerprint: fp}
}

// "Update all" presses several buttons of one host at once: what comes while a run goes waits instead of being
// refused by the agent (rejected_busy); the same command twice waits once; a full queue says so.
func TestCommandWaitsForTheRunningRun(t *testing.T) {
	f, sess := queueFixture(t)
	cmd := func(c string) wire.CommandResult {
		return f.srv.haCommand(sess, wire.Command{T: wire.TCommand, RequestID: "r-" + c, Node: "h", Command: c})
	}
	for _, c := range []string{"install", "install", "install_security", "check"} {
		if res := cmd(c); res.Code != string(AdminOK) || res.JobID != "" {
			t.Fatalf("%s while a run goes: %+v", c, res)
		}
	}
	if got := f.srv.Queued("h"); !slices.Equal(got, []string{"install:all", "install:security", "check"}) {
		t.Fatalf("queued: %v", got)
	}
	if v := f.view(t, "h", f.now); !slices.Equal(v.Queued, []string{"install:all", "install:security", "check"}) {
		t.Fatalf("view: %v", v.Queued)
	}
	if code := f.srv.enqueue("h", queuedJob{verb: protocol.VerbReboot}); code != AdminQueueFull {
		t.Fatalf("fourth distinct command: %s", code)
	}
	if e, _ := f.srv.Reg.Get("h"); e.LastRun.JobID != "j1" {
		t.Fatalf("a waiting command started a run: %+v", e.LastRun)
	}
}

// The same command as the running run is that run: two taps on "reboot now" must not queue a second reboot that the
// agent would refuse (rejected_rate_limited). Another scope of the same verb still waits.
func TestSameCommandAsTheRunningRunJoinsIt(t *testing.T) {
	f, sess := queueFixture(t)
	if err := f.srv.Reg.SetRun("h", Run{JobID: "j2", Verb: protocol.VerbInstall, Scope: "all",
		Phase: protocol.PhaseInstalling, Started: f.now}); err != nil {
		t.Fatal(err)
	}
	res := f.srv.haCommand(sess, wire.Command{T: wire.TCommand, RequestID: "r-1", Node: "h", Command: "install"})
	if res.Code != string(AdminOK) || res.JobID != "j2" {
		t.Fatalf("same command as the running run: %+v", res)
	}
	if got := f.srv.Queued("h"); len(got) != 0 {
		t.Fatalf("queued behind itself: %v", got)
	}
	res = f.srv.haCommand(sess, wire.Command{T: wire.TCommand, RequestID: "r-2", Node: "h", Command: "install_security"})
	if res.Code != string(AdminOK) || res.JobID != "" || !slices.Equal(f.srv.Queued("h"), []string{"install:security"}) {
		t.Fatalf("other scope: %+v, queued %v", res, f.srv.Queued("h"))
	}
}

// When the run has ended the next waiting job starts; one that cannot start hands over to the one after it. While a
// run goes, nothing starts.
func TestQueuedJobStartsWhenTheRunEnds(t *testing.T) {
	old := queueSettle
	queueSettle = 0
	defer func() { queueSettle = old }()
	f, _ := queueFixture(t)
	var mu sync.Mutex
	var started []string
	stub := func(name string, code AdminCode) func() (*SentJob, AdminCode) {
		return func() (*SentJob, AdminCode) {
			mu.Lock()
			started = append(started, name)
			mu.Unlock()
			return nil, code
		}
	}
	f.srv.enqueue("h", queuedJob{verb: protocol.VerbInstall, scope: "all", start: stub("install", AdminNotConnected)})
	f.srv.enqueue("h", queuedJob{verb: protocol.VerbCheck, start: stub("check", AdminNotConnected)})

	f.srv.dispatchNext("h") // the run still goes
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	if len(started) != 0 {
		t.Fatalf("started while a run goes: %v", started)
	}
	mu.Unlock()
	if got := f.srv.Queued("h"); len(got) != 2 {
		t.Fatalf("queue after a dispatch during a run: %v", got)
	}

	fin := f.now
	_ = f.srv.Reg.SetRun("h", Run{JobID: "j1", Verb: protocol.VerbAgentUpdate, Code: protocol.CodeOK, Started: f.now,
		Finished: &fin})
	f.srv.dispatchNext("h")
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		mu.Lock()
		n := len(started)
		mu.Unlock()
		if n == 2 {
			break
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(started, []string{"install", "check"}) || len(f.srv.Queued("h")) != 0 {
		t.Fatalf("started %v, left %v", started, f.srv.Queued("h"))
	}
}

// A reason to run outside the window (ADR-0087) goes only to an agent that announces it; an older one would refuse
// the job as invalid. A command waiting for the running run takes the reason when it is asked for again with one.
func TestOutsideWindowCommands(t *testing.T) {
	f, sess := queueFixture(t) // host h: an agent from before ADR-0087, a run going
	cmd := func(node, c, reason string) wire.CommandResult {
		return f.srv.haCommand(sess, wire.Command{T: wire.TCommand, RequestID: "r", Node: node, Command: c,
			Reason: reason})
	}
	if res := cmd("h", "install", "ops"); res.Code != string(AdminOutsideWindowUnsupported) {
		t.Fatalf("older agent: %+v", res)
	}
	if v := f.view(t, "h", f.now); v.Capabilities.OutsideWindow {
		t.Fatal("offered for an older agent")
	}
	f.host(t, "w", []protocol.Verb{protocol.VerbCheck, protocol.VerbInstall},
		wire.PolicySummary{AllowedScopes: []string{"all"}, DefaultScope: "all", OutsideWindow: true}, true)
	if v := f.view(t, "w", f.now); !v.Capabilities.OutsideWindow {
		t.Fatal("not offered for an agent that announces it")
	}
	if err := f.srv.Reg.SetRun("w", Run{JobID: "j1", Verb: protocol.VerbCheck, Phase: protocol.PhasePrechecking,
		Started: f.now}); err != nil {
		t.Fatal(err)
	}
	for _, reason := range []string{"", "ops: good time now", "someone else"} {
		if res := cmd("w", "install", reason); res.Code != string(AdminOK) {
			t.Fatalf("install with %q: %+v", reason, res)
		}
	}
	f.srv.mu.Lock()
	q := f.srv.queues["w"]
	f.srv.mu.Unlock()
	if len(q) != 1 || q[0].outsideWindow != "ops: good time now" {
		t.Fatalf("queue: %+v", q)
	}
	for c, want := range map[string]AdminCode{"check": AdminBadRequest, "agent_update": AdminVerbUnsupported} {
		if res := cmd("w", c, "ops"); res.Code != string(want) {
			t.Errorf("%s with a reason: %+v, want %s", c, res, want)
		}
	}
	if res := cmd("w", "install", "ops\x07"); res.Code != string(AdminBadReason) {
		t.Errorf("unreadable reason: %+v", res)
	}
}

// A run the hub lost track of (older than its job may take) does not hold the queue for ever.
func TestStaleRunDoesNotBlock(t *testing.T) {
	f, sess := queueFixture(t)
	f.now = f.now.Add(2 * time.Hour) // the agent update (30 min) is long over
	res := f.srv.haCommand(sess, wire.Command{T: wire.TCommand, RequestID: "r", Node: "h", Command: "check"})
	if res.Code == string(AdminOK) && res.JobID == "" {
		t.Fatalf("queued behind a stale run: %+v", res)
	}
	if !strings.Contains(res.Code, "not_connected") {
		t.Fatalf("expected a direct start (host not connected in the fixture): %+v", res)
	}
}
