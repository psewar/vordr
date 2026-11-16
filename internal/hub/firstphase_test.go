package hub

import (
	"encoding/json"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/wire"
)

// receiveJobID reads the agent's end of the tunnel until a job comes and returns its ID, checked as the agent would.
func receiveJobID(t *testing.T, f *viewFixture, agent *wire.Conn, node string) string {
	t.Helper()
	for {
		typ, raw, err := agent.Receive(5 * time.Second)
		if err != nil {
			t.Fatalf("no job reached the agent: %v", err)
		}
		var m wire.JobMsg
		if typ != wire.TJob || json.Unmarshal(raw, &m) != nil {
			continue
		}
		j, why := protocol.VerifyJob(m.Job, f.srv.Key.Public(), node, f.srv.Now(), protocol.NewReplayGuard())
		if why != protocol.InvalidNone {
			t.Fatalf("job: %s", why)
		}
		return j.ID
	}
}

// The agent may report the first phase of a job before the hub has recorded the run it belongs to: the hub sends the
// job, writes its job_sent event and only then stores the run. Here that event waits (its lock is held, as on a slow
// disk) while the agent reports. The phase must land in the run once it is recorded, not be dropped for want of it.
func TestFirstPhaseRightAfterTheJobIsKept(t *testing.T) {
	f := newViewFixture(t)
	ts := httptest.NewServer(f.srv.Handler())
	t.Cleanup(ts.Close)
	agent := tunnelAgent(t, f, ts, "h", protocol.VerbCheck)

	f.srv.Events.mu.Lock() // sendJob stops after the job went out, before its run is recorded
	unlock := sync.OnceFunc(f.srv.Events.mu.Unlock)
	defer unlock()
	started := make(chan AdminCode, 1)
	go func() {
		_, code := f.srv.StartJob("h", protocol.VerbCheck, "", ByAdmin)
		started <- code
	}()
	id := receiveJobID(t, f, agent, "h")
	if err := agent.Send(wire.JobStatus{T: wire.TJobStatus, JobID: id, Phase: protocol.PhasePrechecking}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond) // the hub's session reads the phase while the run is not recorded yet
	unlock()
	if code := <-started; code != AdminOK {
		t.Fatalf("start: %s", code)
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		e, _ := f.srv.Reg.Get("h")
		if r := e.LastRun; r != nil && r.JobID == id && r.Phase == protocol.PhasePrechecking {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the phase the agent reported first is lost: %+v", e.LastRun)
		}
	}
}
