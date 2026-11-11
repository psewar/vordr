package hub

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/wire"
)

// handshakeFixture: host h with an install j1 as its last run, as the hub recorded it.
func handshakeFixture(t *testing.T, code protocol.Code) *viewFixture {
	t.Helper()
	f := newViewFixture(t)
	f.host(t, "h", []protocol.Verb{protocol.VerbCheck, protocol.VerbInstall}, wire.PolicySummary{}, true)
	run := Run{JobID: "j1", Verb: protocol.VerbInstall, Scope: "all", Phase: protocol.PhaseInstalling, Started: f.now}
	if code != "" {
		fin := f.now.Add(time.Minute)
		run.Phase, run.Code, run.Finished = "", code, &fin
	}
	if err := f.srv.Reg.SetRun("h", run); err != nil {
		t.Fatal(err)
	}
	return f
}

func lastRun(t *testing.T, f *viewFixture) Run {
	t.Helper()
	e, _ := f.srv.Reg.Get("h")
	return *e.LastRun
}

// events lists the names of the logged events of host h.
func events(t *testing.T, f *viewFixture) []string {
	t.Helper()
	raw, _, err := f.srv.Events.Read(EventFilter{Node: "h", Limit: MaxEventLimit})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range raw {
		var e struct {
			Event string `json:"event"`
		}
		_ = json.Unmarshal(r, &e)
		names = append(names, e.Event)
	}
	return names
}

// The tunnel broke during an install and the hub recorded it as interrupted. The agent's hello names it as running:
// it is running again, and a command for the host waits for it instead of running into it (ADR-0114).
func TestHelloKeepsARunThatGoesOn(t *testing.T) {
	old := queueSettle
	queueSettle = 0
	defer func() { queueSettle = old }()
	f := handshakeFixture(t, protocol.CodeInterrupted)
	if f.srv.running("h") {
		t.Fatal("an interrupted run counts as running before the hello")
	}
	f.srv.reconcileRuns("h", wire.Runs{Running: []string{"j1"}})
	if run := lastRun(t, f); run.Code != "" || run.Finished != nil || run.Phase != "" {
		t.Fatalf("after the hello: %+v", run)
	}
	if !f.srv.running("h") {
		t.Fatal("the run the agent runs does not hold the host")
	}
	var log startedLog
	f.srv.enqueue("h", queuedJob{verb: protocol.VerbCheck, start: log.stub("check", AdminNotConnected)})
	f.srv.dispatchNext("h")
	time.Sleep(50 * time.Millisecond)
	if got := log.wait(t, 0); len(got) != 0 || len(f.srv.Queued("h")) != 1 {
		t.Fatalf("a queued command went to the busy agent: %v, queued %v", got, f.srv.Queued("h"))
	}
	f.srv.deliver(&session{node: "h", waiters: map[string]chan wire.JobStatus{}},
		wire.JobStatus{JobID: "j1", Code: protocol.CodeOK, Summary: &wire.RunSummary{PackageCount: 4}})
	if run := lastRun(t, f); run.Code != protocol.CodeOK || run.Summary == nil || run.Summary.PackageCount != 4 {
		t.Fatalf("the real result: %+v", run)
	}
	if got := log.wait(t, 1); !slices.Equal(got, []string{"check"}) {
		t.Fatalf("queued after the result: %v", got)
	}
	if !slices.Contains(events(t, f), "run_continues") {
		t.Fatalf("events: %v", events(t, f))
	}
}

// The hub restarted during a run, or a check's answer was lost: the run is open, and the agent's hello names nothing.
// The host is free at once, not after the job's timeout.
func TestHelloFreesAHostWhoseRunIsGone(t *testing.T) {
	old := queueSettle
	queueSettle = 0
	defer func() { queueSettle = old }()
	f := handshakeFixture(t, "")
	if !f.srv.running("h") {
		t.Fatal("fixture: the open run holds the host")
	}
	var log startedLog
	f.srv.enqueue("h", queuedJob{verb: protocol.VerbCheck, start: log.stub("check", AdminNotConnected)})
	f.srv.reconcileRuns("h", wire.Runs{Running: []string{"other"}, Unacknowledged: []string{"older"}})
	run := lastRun(t, f)
	if run.Code != protocol.CodeInterrupted || run.Detail != lostRunDetail || run.Finished == nil || f.srv.running("h") {
		t.Fatalf("after the hello: %+v", run)
	}
	if got := log.wait(t, 1); !slices.Equal(got, []string{"check"}) {
		t.Fatalf("the waiting command did not start: %v", got)
	}
	// Every later hello finds it settled: no second event.
	f.srv.reconcileRuns("h", wire.Runs{})
	if n := countOf(events(t, f), "run_lost"); n != 1 {
		t.Fatalf("run_lost %d times", n)
	}
}

func countOf(list []string, s string) int {
	n := 0
	for _, v := range list {
		if v == s {
			n++
		}
	}
	return n
}

// The agent holds the result (the hub restarted after the agent wrote it): the run stays as it is until it arrives.
func TestHelloLeavesARunWhoseResultFollows(t *testing.T) {
	for _, code := range []protocol.Code{"", protocol.CodeInterrupted} {
		f := handshakeFixture(t, code)
		f.srv.reconcileRuns("h", wire.Runs{Unacknowledged: []string{"j1"}})
		if run := lastRun(t, f); run.Code != code || run.Detail != "" {
			t.Fatalf("%q after the hello: %+v", code, run)
		}
	}
	f := handshakeFixture(t, protocol.CodeOK) // settled with a result: nothing to reconcile
	f.srv.reconcileRuns("h", wire.Runs{})
	if run := lastRun(t, f); run.Code != protocol.CodeOK {
		t.Fatalf("a settled run changed: %+v", run)
	}
}

// The agent sends a result again (its ack was lost): stored once, no second job_status.
func TestSecondCopyOfAResultChangesNothing(t *testing.T) {
	f := handshakeFixture(t, "")
	sess := &session{node: "h", waiters: map[string]chan wire.JobStatus{}}
	f.srv.deliver(sess, wire.JobStatus{JobID: "j1", Code: protocol.CodeOK})
	first := lastRun(t, f)
	f.srv.deliver(sess, wire.JobStatus{JobID: "j1", Code: protocol.CodeOK})
	if again := lastRun(t, f); !again.Finished.Equal(*first.Finished) || again.Code != protocol.CodeOK {
		t.Fatalf("second copy changed the run: %+v", again)
	}
	ev := events(t, f)
	if countOf(ev, "job_status") != 1 || countOf(ev, "job_status_again") != 1 {
		t.Fatalf("events: %v", ev)
	}
}

// The agent's new session named the job before its old connection ended at the hub: the request that waits for the
// job keeps waiting, and the result over the new session ends it - no interrupted in between.
func TestOldConnectionEndingAfterTheHelloIsNoInterruption(t *testing.T) {
	f := handshakeFixture(t, "")
	old := &session{node: "h", waiters: map[string]chan wire.JobStatus{}}
	ch := make(chan wire.JobStatus, 1)
	j := &SentJob{ID: "j1", node: "h", sess: old, run: lastRun(t, f), ch: ch}
	done := make(chan wire.JobStatus, 1)
	go func() {
		st, _ := f.srv.WaitJob(context.Background(), j)
		done <- st
	}()
	next := &session{node: "h", waiters: map[string]chan wire.JobStatus{}}
	f.srv.claim(next, wire.Runs{Running: []string{"j1"}})
	f.srv.reconcileRuns("h", wire.Runs{Running: []string{"j1"}})
	close(ch) // only now drop(old) closes its waiters
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		f.srv.mu.Lock()
		listening := f.srv.successors["j1"] != nil
		f.srv.mu.Unlock()
		if listening {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("WaitJob did not wait on: %+v", lastRun(t, f))
		}
	}
	if run := lastRun(t, f); run.Code != "" {
		t.Fatalf("recorded while the agent runs it: %+v", run)
	}
	f.srv.deliver(next, wire.JobStatus{JobID: "j1", Code: protocol.CodeOK})
	select {
	case st := <-done:
		if st.Code != protocol.CodeOK {
			t.Fatalf("WaitJob: %+v", st)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WaitJob did not return")
	}
	if !slices.Contains(events(t, f), "job_continues") {
		t.Fatalf("events: %v", events(t, f))
	}
	// Once that session is gone, its claims go with it.
	f.srv.drop(next)
	if f.srv.claimedOpen("h", "j1") {
		t.Fatal("claim outlived its session")
	}
}

// A self-update waits for the agent that starts next. That agent's hello names the job neither as running nor as a
// result it reports: the wait ends now, as interrupted, instead of after half an hour.
func TestHelloEndsTheWaitForASuccessorThatHasNothing(t *testing.T) {
	f := newRestartFixture(t, context.Background(), protocol.PhaseRebooting)
	f.listening(t)
	f.srv.reconcileRuns("h", wire.Runs{})
	if st := f.result(t); st.Code != protocol.CodeInterrupted || st.Detail != lostRunDetail {
		t.Fatalf("WaitJob: %+v", st)
	}
	if run := f.view(t, "h", f.now).LastRun; run.State != "interrupted" {
		t.Fatalf("run: %+v", run)
	}
}

// An agent before ADR-0114 sends no Runs: the hub keeps its guess, and no ack goes to it.
func TestOldAgentKeepsTodaysBehaviour(t *testing.T) {
	f := handshakeFixture(t, "")
	sess := &session{node: "h", waiters: map[string]chan wire.JobStatus{}} // acks false, conn nil: an ack would panic
	f.srv.deliver(sess, wire.JobStatus{JobID: "j1", Code: protocol.CodeOK})
	if run := lastRun(t, f); run.Code != protocol.CodeOK {
		t.Fatalf("run: %+v", run)
	}
	if !slices.Contains(f.srv.features(), wire.FeatureAck) {
		t.Fatalf("features: %v", f.srv.features())
	}
}
