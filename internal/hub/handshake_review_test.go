package hub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/wire"
)

// Tests for what the review of ADR-0114 found.

// An install that waits for its guest's backup has not been sent: the guest's hello cannot name it, and it stays.
func TestHelloLeavesAnInstallWaitingForItsBackup(t *testing.T) {
	f := handshakeFixture(t, "")
	run := lastRun(t, f)
	run.Phase = protocol.PhaseBackingUp
	if err := f.srv.Reg.SetRun("h", run); err != nil {
		t.Fatal(err)
	}
	f.srv.reconcileRuns("h", wire.Runs{})
	if got := lastRun(t, f); got.Code != "" || got.Phase != protocol.PhaseBackingUp || !f.srv.running("h") {
		t.Fatalf("after the guest's hello: %+v", got)
	}
}

// The agent named a job as running that is no run of the host's own (a hypervisor's backup for a guest): the host is
// busy until its result comes, and then what waits starts.
func TestJobsNamedAsRunningKeepTheHostBusy(t *testing.T) {
	old := queueSettle
	queueSettle = 0
	defer func() { queueSettle = old }()
	f := handshakeFixture(t, protocol.CodeOK) // the host's own last run is settled
	sess := &session{node: "h", waiters: map[string]chan wire.JobStatus{}}
	f.srv.claim(sess, wire.Runs{Running: []string{"backup-1"}})
	if !f.srv.running("h") {
		t.Fatal("a job the agent runs does not hold the host")
	}
	var log startedLog
	f.srv.enqueue("h", queuedJob{verb: protocol.VerbCheck, start: log.stub("check", AdminNotConnected)})
	f.srv.dispatchNext("h")
	time.Sleep(50 * time.Millisecond)
	if got := log.wait(t, 0); len(got) != 0 {
		t.Fatalf("started while the backup runs: %v", got)
	}
	f.srv.deliver(sess, wire.JobStatus{JobID: "backup-1", Code: protocol.CodeOK})
	if f.srv.running("h") {
		t.Fatal("still busy after the result")
	}
	if got := log.wait(t, 1); !slices.Equal(got, []string{"check"}) {
		t.Fatalf("after the result: %v", got)
	}
}

// An interrupted run with a detail of its own (the agent's journal, a backup that ended it) keeps it: only the hub's
// own guess at the disconnect, which has none, learns that the agent has nothing more.
func TestHelloKeepsTheDetailOfAnInterruptedRun(t *testing.T) {
	f := handshakeFixture(t, protocol.CodeInterrupted)
	run := lastRun(t, f)
	run.Detail = "the agent or the host stopped during the run; check the package database"
	if err := f.srv.Reg.SetRun("h", run); err != nil {
		t.Fatal(err)
	}
	f.srv.reconcileRuns("h", wire.Runs{})
	if got := lastRun(t, f); got.Detail != run.Detail || slices.Contains(events(t, f), "run_lost") {
		t.Fatalf("after the hello: %+v, events %v", got, events(t, f))
	}
}

// The result came over the new session before the old connection ended: WaitJob returns it and records nothing over it.
func TestResultBeforeTheOldConnectionEnds(t *testing.T) {
	f := handshakeFixture(t, "")
	old := &session{node: "h", waiters: map[string]chan wire.JobStatus{}}
	ch := make(chan wire.JobStatus, 1)
	j := &SentJob{ID: "j1", node: "h", sess: old, run: lastRun(t, f), ch: ch}
	next := &session{node: "h", waiters: map[string]chan wire.JobStatus{}}
	f.srv.deliver(next, wire.JobStatus{JobID: "j1", Code: protocol.CodeOK, Detail: "done"})
	close(ch)
	st, _ := f.srv.WaitJob(context.Background(), j)
	if st.Code != protocol.CodeOK || lastRun(t, f).Code != protocol.CodeOK || lastRun(t, f).Detail != "done" {
		t.Fatalf("WaitJob %+v, run %+v", st, lastRun(t, f))
	}
}

// A second copy is known by code and detail: the agent's own interrupted result replaces the hub's guess, and a copy
// of it changes nothing.
func TestSecondCopyOfAnInterruptedResult(t *testing.T) {
	f := handshakeFixture(t, protocol.CodeInterrupted) // the hub's guess, no detail
	sess := &session{node: "h", waiters: map[string]chan wire.JobStatus{}}
	agent := wire.JobStatus{JobID: "j1", Code: protocol.CodeInterrupted, Detail: "the agent or the host stopped"}
	f.srv.deliver(sess, agent)
	first := lastRun(t, f)
	if first.Detail != agent.Detail {
		t.Fatalf("the agent's result did not replace the guess: %+v", first)
	}
	f.srv.deliver(sess, agent)
	if again := lastRun(t, f); !again.Finished.Equal(*first.Finished) {
		t.Fatalf("stored twice: %+v", again)
	}
	if ev := events(t, f); countOf(ev, "job_status") != 1 || countOf(ev, "job_status_again") != 1 {
		t.Fatalf("events: %v", ev)
	}
}

// ackSession: a session whose agent takes acks, over a real WebSocket; the returned connection is the agent's end.
func ackSession(t *testing.T, node string) (*session, *websocket.Conn) {
	t.Helper()
	server := make(chan *websocket.Conn, 1)
	up := websocket.Upgrader{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err == nil {
			server <- c
		}
	}))
	t.Cleanup(ts.Close)
	agent, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { agent.Close() })
	return &session{node: node, conn: wire.NewConn(<-server), waiters: map[string]chan wire.JobStatus{}, acks: true}, agent
}

// acks reads the agent's end until it closes and hands on the job ID of every ack (a read deadline would leave the
// connection unusable after the first timeout).
func acks(c *websocket.Conn) <-chan string {
	out := make(chan string, 8)
	go func() {
		for {
			var a wire.Ack
			if c.ReadJSON(&a) != nil {
				return
			}
			out <- a.JobID
		}
	}()
	return out
}

// readAck: the job ID of the next ack within d, "" when none comes.
func readAck(t *testing.T, in <-chan string, d time.Duration) string {
	t.Helper()
	select {
	case id := <-in:
		return id
	case <-time.After(d):
		return ""
	}
}

// The register could not be written: no ack, so the agent keeps the result and sends it again.
func TestNoAckWhenTheResultWasNotStored(t *testing.T) {
	f := handshakeFixture(t, "")
	sess, conn := ackSession(t, "h")
	agent := acks(conn)
	good := f.srv.Reg.path
	blocker := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	f.srv.Reg.path = filepath.Join(blocker, "r.json") // its directory is a file: the save fails
	f.srv.deliver(sess, wire.JobStatus{JobID: "j1", Code: protocol.CodeOK})
	if id := readAck(t, agent, 300*time.Millisecond); id != "" {
		t.Fatalf("acknowledged although not stored: %s", id)
	}
	f.srv.Reg.path = good
	f.srv.deliver(sess, wire.JobStatus{JobID: "j1", Code: protocol.CodeOK}) // sent again on the next session
	if id := readAck(t, agent, 2*time.Second); id != "j1" {
		t.Fatalf("no ack once stored: %q", id)
	}
}
