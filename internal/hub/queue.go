package hub

import (
	"context"
	"time"

	"github.com/psewar/vordr/internal/protocol"
)

// Jobs waiting per host. Home Assistant's "update all" presses several buttons of one host in the same second; without
// a queue the agent update would run and the install be refused (rejected_busy) - an agent runs one job at a time. A
// command for a host whose run is still going waits and starts when that run has ended.

type queuedJob struct {
	verb          protocol.Verb
	scope         string
	by            string
	outsideWindow string                       // the reason, if asked for outside the window (ADR-0087)
	start         func() (*SentJob, AdminCode) // signs and sends the job when its turn comes
}

const (
	maxQueued = 3
	// AdminQueueFull: the host already has maxQueued commands waiting.
	AdminQueueFull AdminCode = "queue_full"
)

// queueSettle: pause between the end of a run and the next waiting one. An agent that just came back (self-update,
// reboot) checks first and holds its lock for a moment.
var queueSettle = 30 * time.Second

// runningRun: the host's last run while it has no result yet and is younger than its job may take (a run the hub
// lost track of, e.g. over a hub restart, does not block the queue for ever).
func (s *Server) runningRun(node string) (Run, bool) {
	e, ok := s.Reg.Get(node)
	if !ok || e.LastRun == nil || e.LastRun.Code != "" || s.Now().Sub(e.LastRun.Started) >= waitTimeout(e.LastRun.Verb) {
		return Run{}, false
	}
	return *e.LastRun, true
}

// running: the host's own run goes, or - on the hypervisor - a backup for another host's install (prebackup.go), or
// the agent's hello named jobs as running whose results have not come (ADR-0114). Either way its agent is busy, and a
// command for it waits.
func (s *Server) running(node string) bool {
	_, ok := s.runningRun(node)
	return ok || s.backingUpOn(node) || s.claimedBusy(node)
}

// enqueue lets a command wait for the host's running run. The same command twice waits once; asked for again with a
// reason to run outside the window, the waiting one takes it (else it would be refused outside the window).
func (s *Server) enqueue(node string, q queuedJob) AdminCode {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, w := range s.queues[node] {
		if w.verb == q.verb && w.scope == q.scope {
			if q.outsideWindow != "" && w.outsideWindow == "" {
				s.queues[node][i] = q
			}
			return AdminOK
		}
	}
	if len(s.queues[node]) >= maxQueued {
		return AdminQueueFull
	}
	s.queues[node] = append(s.queues[node], q)
	return AdminOK
}

// Queued lists what waits for a host, as verb or verb:scope.
func (s *Server) Queued(node string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, q := range s.queues[node] {
		if q.scope != "" {
			out = append(out, string(q.verb)+":"+q.scope)
		} else {
			out = append(out, string(q.verb))
		}
	}
	return out
}

// dispatchNext starts the host's next waiting job once its run has ended; WaitJob calls it when it returns. One
// dispatch per host at a time, so two ends cannot start two jobs. Once the hub is stopping nothing more starts (the
// queue lives in memory and ends with the hub); otherwise Close waits for the dispatch (ADR-0142).
func (s *Server) dispatchNext(node string) {
	s.mu.Lock()
	if len(s.queues[node]) == 0 || s.dispatching[node] || s.closing {
		s.mu.Unlock()
		return
	}
	s.dispatching[node] = true
	s.work.Add(1) // as spawn does, under the same lock as the check
	s.mu.Unlock()
	go func() {
		defer s.work.Done()
		select {
		case <-time.After(queueSettle):
		case <-s.closed.Done():
			s.mu.Lock()
			delete(s.dispatching, node)
			s.mu.Unlock()
			return // the hub is stopping; the queue ends with it
		}
		s.mu.Lock()
		q := s.queues[node]
		if len(q) == 0 {
			delete(s.dispatching, node)
			s.mu.Unlock()
			return
		}
		next := q[0]
		s.queues[node] = q[1:]
		s.mu.Unlock()
		var job *SentJob
		code := AdminOK
		// Another run started meanwhile: this one waits for that run's end. Its own flag, not a code: start() can
		// answer AdminAlready itself (the agent runs the release already), and that must hand on to the next one.
		requeued := s.running(node)
		if requeued {
			s.mu.Lock()
			s.queues[node] = append([]queuedJob{next}, s.queues[node]...)
			s.mu.Unlock()
		} else {
			job, code = next.start()
		}
		s.mu.Lock()
		delete(s.dispatching, node)
		s.mu.Unlock()
		if requeued {
			return
		}
		fields := map[string]any{"verb": next.verb, "scope": next.scope, "by": next.by, "code": code}
		if next.outsideWindow != "" {
			fields["outside_window"] = next.outsideWindow
		}
		s.Events.Log(node, "queued_job_started", fields)
		s.changedHA()
		if code != AdminOK {
			s.dispatchNext(node) // could not start (offline, no release): the next one tries
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), waitTimeout(next.verb))
		defer cancel()
		s.WaitJob(ctx, job)
	}()
}
