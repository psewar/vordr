package hub

import (
	"slices"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/wire"
)

// The run handshake (ADR-0114). When the tunnel breaks the hub has to guess what became of the host's run, and it
// records it as interrupted. An agent that knows the handshake says at its next hello which jobs it still runs and
// which results it holds; the hub takes that as the fact. It acknowledges every final status once it is stored, and
// the agent's run journal counts a result as reported only then.

// lostRunDetail: the run the hub had open, but the agent named neither as running nor as a result it holds.
const lostRunDetail = "the agent reported no such run when it connected again"

// reconcileRuns sets the host's last run to what the agent's hello says, before any job can reach the host:
//   - a run the agent names as running stays running, also if the hub recorded it as interrupted at the disconnect;
//   - a run whose result the agent holds stays as it is: the result follows on this session;
//   - an open run the agent does not name is gone (the agent restarted before its journal, or a result went where
//     nobody listened): it ends as interrupted now, and the host is free without waiting for the job's timeout;
//   - an install still waiting for its guest's backup (backing_up) is not the agent's yet: the hub sends it later.
func (s *Server) reconcileRuns(node string, r wire.Runs) {
	e, ok := s.Reg.Get(node)
	if !ok || e.LastRun == nil {
		return
	}
	run := *e.LastRun
	open := run.Code == ""
	if !open && run.Code != protocol.CodeInterrupted {
		return // settled with a result
	}
	if open && run.Phase == protocol.PhaseBackingUp {
		return // not sent yet (prebackup.go); the agent cannot know it
	}
	switch {
	case slices.Contains(r.Running, run.JobID):
		if open {
			return
		}
		run.Code, run.Reason, run.Detail, run.Finished = "", "", "", nil
		s.Events.Log(node, "run_continues", map[string]any{"job_id": run.JobID})
		s.storeRun(node, run)
	case slices.Contains(r.Unacknowledged, run.JobID):
		// The agent sends the result right after the hello.
	case open:
		finished := s.Now()
		run.Phase, run.Code, run.Detail, run.Finished = "", protocol.CodeInterrupted, lostRunDetail, &finished
		run.Progress, run.Item = nil, ""
		s.Events.Log(node, "run_lost", map[string]any{"job_id": run.JobID})
		s.storeRun(node, run)
		s.mu.Lock()
		succ := s.successors[run.JobID]
		s.mu.Unlock()
		if succ == nil {
			s.dispatchNext(node) // nobody waits for the run that held the queue
			return
		}
		select { // a self-update waiting for the agent that starts next: there is none to wait for
		case succ <- wire.JobStatus{JobID: run.JobID, Code: protocol.CodeInterrupted, Detail: lostRunDetail}:
		default:
		}
	case run.Detail == "":
		// The hub's own guess at the disconnect (WaitJob records interrupted without a detail), and the agent confirms
		// it has nothing more: it stays so, and says why. An interrupted run with its own detail - the agent's journal,
		// a backup that ended it - keeps it.
		run.Detail = lostRunDetail
		s.Events.Log(node, "run_lost", map[string]any{"job_id": run.JobID})
		s.storeRun(node, run)
	}
}

// claim records what the session's hello named. A request waiting for one of these jobs on the agent's old connection
// sees it when that connection ends, before or after this hello (claimedOpen, WaitJob); the jobs named as running
// keep the host busy until their results come (claimedBusy).
func (s *Server) claim(sess *session, r wire.Runs) {
	s.mu.Lock()
	sess.claimed = append(append([]string{}, r.Running...), r.Unacknowledged...)
	sess.claimedRunning = append([]string{}, r.Running...)
	s.claims[sess.node] = sess
	s.mu.Unlock()
}

// unclaim forgets the claims of a session that did not come up.
func (s *Server) unclaim(sess *session) {
	s.mu.Lock()
	if s.claims[sess.node] == sess {
		delete(s.claims, sess.node)
	}
	s.mu.Unlock()
}

// claimedOpen: the host's run is still this job and open, and the agent's current session named it.
func (s *Server) claimedOpen(node, jobID string) bool {
	s.mu.Lock()
	c := s.claims[node]
	named := c != nil && slices.Contains(c.claimed, jobID)
	s.mu.Unlock()
	if !named {
		return false
	}
	run, ok := s.currentRun(node, jobID)
	return ok && run.Code == ""
}

// claimedBusy: the agent's current session named jobs as running whose results have not come yet - also jobs that
// are no run of the host's own, like a hypervisor's backup for a guest. A command waits for them.
func (s *Server) claimedBusy(node string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.claims[node]
	return c != nil && len(c.claimedRunning) > 0
}

// claimSettled: a final status for a job has come. true: it was the last job the agent named as running, so what
// waits for the host may start now.
func (s *Server) claimSettled(node, jobID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.claims[node]
	if c == nil || !slices.Contains(c.claimedRunning, jobID) {
		return false
	}
	c.claimedRunning = slices.DeleteFunc(c.claimedRunning, func(id string) bool { return id == jobID })
	return len(c.claimedRunning) == 0
}

// settledAlready: the host's last run is this job and already carries this result - a second copy of it. The detail
// tells an agent's own interrupted result from the hub's guess at a disconnect, which has none.
func (s *Server) settledAlready(node string, st wire.JobStatus) bool {
	e, ok := s.Reg.Get(node)
	return ok && e.LastRun != nil && e.LastRun.JobID == st.JobID && e.LastRun.Code == st.Code &&
		e.LastRun.Detail == st.Detail
}

// settledRun: the host's last run is this job and has its result already, as a final status.
func (s *Server) settledRun(node, jobID string) (wire.JobStatus, bool) {
	e, ok := s.Reg.Get(node)
	if !ok || e.LastRun == nil || e.LastRun.JobID != jobID || e.LastRun.Code == "" {
		return wire.JobStatus{}, false
	}
	r := e.LastRun
	return wire.JobStatus{JobID: jobID, Code: r.Code, Reason: r.Reason, Detail: r.Detail, Summary: r.Summary}, true
}

// acknowledge tells an agent that takes acks that a final status is stored.
func (s *Server) acknowledge(sess *session, jobID string) {
	if !sess.acks {
		return
	}
	if err := sess.conn.Send(wire.Ack{T: wire.TAck, JobID: jobID}); err != nil {
		// The agent sends the result again on its next session and gets the ack then.
		s.Log.Printf("ack %s to %s: %v", jobID, sess.node, err)
	}
}
