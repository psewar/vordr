package hub

import (
	"context"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/wire"
)

// restartFixture: an agent_update job on host h, its agent's session, and WaitJob running for it.
type restartFixture struct {
	*viewFixture
	old  *session
	ch   chan wire.JobStatus
	done chan wire.JobStatus
}

func newRestartFixture(t *testing.T, ctx context.Context, phase protocol.Phase) *restartFixture {
	t.Helper()
	f := &restartFixture{viewFixture: newViewFixture(t), old: &session{node: "h", waiters: map[string]chan wire.JobStatus{}},
		ch: make(chan wire.JobStatus, 1), done: make(chan wire.JobStatus, 1)}
	f.host(t, "h", []protocol.Verb{protocol.VerbCheck, protocol.VerbAgentUpdate}, wire.PolicySummary{AgentUpdate: true}, true)
	run := Run{JobID: "u1", Verb: protocol.VerbAgentUpdate, By: ByRollout, Phase: protocol.PhaseQueued, Started: f.now}
	if err := f.srv.Reg.SetRun("h", run); err != nil {
		t.Fatal(err)
	}
	pct := 90
	f.srv.deliver(f.old, wire.JobStatus{JobID: "u1", Phase: phase, Progress: &pct})
	j := &SentJob{ID: "u1", node: "h", sess: f.old, run: run, ch: f.ch}
	go func() {
		st, _ := f.srv.WaitJob(ctx, j)
		f.done <- st
	}()
	close(f.ch) // the agent's connection ends: drop closes its waiters
	return f
}

// listening waits until WaitJob waits for the agent that starts next.
func (f *restartFixture) listening(t *testing.T) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		f.srv.mu.Lock()
		ok := f.srv.successors["u1"] != nil
		f.srv.mu.Unlock()
		if ok {
			return
		}
	}
	t.Fatal("WaitJob does not wait for the successor")
}

func (f *restartFixture) result(t *testing.T) wire.JobStatus {
	t.Helper()
	select {
	case st := <-f.done:
		return st
	case <-time.After(5 * time.Second):
		t.Fatal("WaitJob did not return")
	}
	return wire.JobStatus{}
}

// The agent said it restarts itself (agent_update after the swap): the end of its connection is no interruption.
// Home Assistant sees the run going on until the new agent confirms - no "failed" in between.
func TestAnnouncedRestartIsNoInterruption(t *testing.T) {
	f := newRestartFixture(t, context.Background(), protocol.PhaseRebooting)
	f.listening(t)
	if run := f.view(t, "h", f.now).LastRun; run.State != "rebooting" || run.Finished != nil || run.By != ByRollout {
		t.Fatalf("while the agent restarts: %+v", run)
	}
	next := &session{node: "h", waiters: map[string]chan wire.JobStatus{}}
	f.srv.deliver(next, wire.JobStatus{JobID: "u1", Code: protocol.CodeOK})
	if st := f.result(t); st.Code != protocol.CodeOK {
		t.Fatalf("WaitJob: %+v", st)
	}
	if run := f.view(t, "h", f.now).LastRun; run.State != "ok" || run.Finished == nil {
		t.Fatalf("after the confirmation: %+v", run)
	}
	f.srv.mu.Lock()
	defer f.srv.mu.Unlock()
	if len(f.srv.successors) != 0 {
		t.Fatal("successor wait left behind")
	}
}

// The old agent, put back by the rollback, reports the failed update: that is the result.
func TestRestartRolledBackIsReported(t *testing.T) {
	f := newRestartFixture(t, context.Background(), protocol.PhaseRebooting)
	f.listening(t)
	back := &session{node: "h", waiters: map[string]chan wire.JobStatus{}}
	f.srv.deliver(back, wire.JobStatus{JobID: "u1", Code: protocol.CodeFailedVerification, Detail: "back on 0.7.1"})
	if st := f.result(t); st.Code != protocol.CodeFailedVerification {
		t.Fatalf("WaitJob: %+v", st)
	}
	if run := f.view(t, "h", f.now).LastRun; run.State != string(protocol.CodeFailedVerification) {
		t.Fatalf("after the rollback: %+v", run)
	}
}

// No agent comes back within the job's wait: then the run has failed, and says why.
func TestRestartWithoutSuccessorTimesOut(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	f := newRestartFixture(t, ctx, protocol.PhaseRebooting)
	f.listening(t)
	cancel()
	if st := f.result(t); st.Code != protocol.CodeTimeout || st.Detail == "" {
		t.Fatalf("WaitJob: %+v", st)
	}
	if run := f.view(t, "h", f.now).LastRun; run.State != string(protocol.CodeTimeout) || run.Finished == nil {
		t.Fatalf("no successor: %+v", run)
	}
}

// Without the announcement a lost connection still counts as interrupted at once.
func TestUnannouncedDisconnectIsInterrupted(t *testing.T) {
	f := newRestartFixture(t, context.Background(), protocol.PhaseDownloading)
	if st := f.result(t); st.Code != protocol.CodeInterrupted {
		t.Fatalf("WaitJob: %+v", st)
	}
	if run := f.view(t, "h", f.now).LastRun; run.State != string(protocol.CodeInterrupted) {
		t.Fatalf("lost connection: %+v", run)
	}
}
