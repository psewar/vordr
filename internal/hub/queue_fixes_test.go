package hub

import (
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/wire"
)

// startedLog records which queued jobs started.
type startedLog struct {
	mu   sync.Mutex
	list []string
}

func (l *startedLog) stub(name string, code AdminCode) func() (*SentJob, AdminCode) {
	return func() (*SentJob, AdminCode) {
		l.mu.Lock()
		l.list = append(l.list, name)
		l.mu.Unlock()
		return nil, code
	}
}

func (l *startedLog) wait(t *testing.T, n int) []string {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		l.mu.Lock()
		got := len(l.list)
		l.mu.Unlock()
		if got >= n {
			break
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string{}, l.list...)
}

// A final status nobody waits for (the hub restarted during the run, or the tunnel broke and the run went on) settles
// the run and starts what waits behind it; before, the queue stayed until the next command.
func TestFinalStatusWithoutWaiterStartsTheQueue(t *testing.T) {
	old := queueSettle
	queueSettle = 0
	defer func() { queueSettle = old }()
	f, _ := queueFixture(t) // host h runs j1 (agent update), nobody waits for it
	var log startedLog
	f.srv.enqueue("h", queuedJob{verb: protocol.VerbInstall, scope: "all", start: log.stub("install", AdminNotConnected)})
	sess := &session{node: "h", waiters: map[string]chan wire.JobStatus{}}
	f.srv.deliver(sess, wire.JobStatus{T: wire.TJobStatus, JobID: "j1", Code: protocol.CodeOK})
	if got := log.wait(t, 1); !slices.Equal(got, []string{"install"}) {
		t.Fatalf("started after the late result: %v", got)
	}
	if e, _ := f.srv.Reg.Get("h"); e.LastRun.Code != protocol.CodeOK {
		t.Fatalf("run not settled: %+v", e.LastRun)
	}
}

// A queued agent update that finds the release already running answers AdminAlready itself; that is no "put back",
// and the job after it starts.
func TestQueuedJobThatIsAlreadyDoneHandsOn(t *testing.T) {
	old := queueSettle
	queueSettle = 0
	defer func() { queueSettle = old }()
	f, _ := queueFixture(t)
	fin := f.now
	_ = f.srv.Reg.SetRun("h", Run{JobID: "j1", Verb: protocol.VerbAgentUpdate, Code: protocol.CodeOK, Started: f.now,
		Finished: &fin})
	var log startedLog
	f.srv.enqueue("h", queuedJob{verb: protocol.VerbAgentUpdate, start: log.stub("agent_update", AdminAlready)})
	f.srv.enqueue("h", queuedJob{verb: protocol.VerbInstall, scope: "all", start: log.stub("install", AdminNotConnected)})
	f.srv.dispatchNext("h")
	if got := log.wait(t, 2); !slices.Equal(got, []string{"agent_update", "install"}) || len(f.srv.Queued("h")) != 0 {
		t.Fatalf("started %v, left %v", got, f.srv.Queued("h"))
	}
}

// Automatic installs and the agent rollout see a host as busy the way the queue does: a run whose
// result was lost holds the host only as long as its job may take, and a hypervisor that backs up a guest is busy.
func TestAutoAndRolloutUseTheBoundedBusyCheck(t *testing.T) {
	f := newAutoFixture(t)
	lost := Run{JobID: "lost", Verb: protocol.VerbCheck, Started: f.now.Add(-waitTimeout(protocol.VerbCheck) - time.Minute)}
	_ = f.srv.Reg.SetRun("canary", lost)
	if _, r := f.due(t, "canary"); r != autoReady {
		t.Fatalf("a run whose result was lost long ago still holds the canary: %q", r)
	}
	_ = f.srv.Reg.SetRun("canary", Run{JobID: "now", Verb: protocol.VerbCheck, Started: f.now})
	if _, r := f.due(t, "canary"); r != autoNotReady {
		t.Fatalf("a running check: %q", r)
	}
	_ = f.srv.Reg.SetRun("canary", Run{JobID: "done", Verb: protocol.VerbCheck, Code: protocol.CodeOK, Started: f.now})
	f.srv.mu.Lock()
	f.srv.backingUp["canary"] = true
	f.srv.mu.Unlock()
	if _, r := f.due(t, "canary"); r != autoNotReady {
		t.Fatalf("backing up a guest: %q", r)
	}

	rf := newReleaseFixture(t)
	publish(t, rf.dir, rf.priv, "0.7.0")
	rel, _ := rf.srv.Releases.Current()
	_ = rf.srv.Reg.SetRun("canary", Run{JobID: "lost", Verb: protocol.VerbCheck,
		Started: rf.now.Add(-waitTimeout(protocol.VerbCheck) - time.Minute)})
	if e, _ := rf.srv.Reg.Get("canary"); !rf.srv.rolloutDue(e, rel, rf.now) {
		t.Fatal("rollout: a lost run holds the canary for ever")
	}
	rf.srv.mu.Lock()
	rf.srv.backingUp["canary"] = true
	rf.srv.mu.Unlock()
	if e, _ := rf.srv.Reg.Get("canary"); rf.srv.rolloutDue(e, rel, rf.now) {
		t.Fatal("rollout: an agent update to a hypervisor while it backs up a guest")
	}
}

// The register is written whole and leaves no temporary file behind.
func TestSaveListIsWholeAndLeavesNoTemporaryFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "list.json")
	if err := saveList(path, []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	got, err := loadList[string](path)
	if err != nil || !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("read back %v %v", got, err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temporary file left: %v", err)
	}
}
