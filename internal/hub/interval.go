package hub

import (
	"time"

	"github.com/psewar/vordr/internal/protocol"
)

// The agent's minimum interval as the hub sees it (ADR-0110). The agent refuses an install of the system packages or
// an app while its last run that changed packages started less than install.min_interval_h ago. It announces the
// interval and that start, so the hub refuses such an install itself - before it sends the job and before it has the
// guest backed up for it (ADR-0096) - and does not spend an automatic attempt on it.

// AdminRateLimited: the agent's minimum interval since its last run that changed packages has not passed yet; it would
// refuse the install (rejected_rate_limited). HostView.NextInstall says when it takes one again.
const AdminRateLimited AdminCode = "rate_limited"

// nextInstall is when the agent takes an install of the system packages or an app again, while that lies ahead of
// now; nil when it would take one now, or when the hub cannot tell (an agent before ADR-0110 announces no interval).
func nextInstall(e Entry, now time.Time) *time.Time {
	if e.Policy.MinIntervalH <= 0 || e.LastInstall == nil {
		return nil
	}
	next := e.LastInstall.Add(time.Duration(e.Policy.MinIntervalH) * time.Hour)
	if !now.Before(next) {
		return nil
	}
	return &next
}

// intervalApplies: the agent applies install.min_interval_h to install runs of the system packages and of apps, not
// to pip/npm runs, dependency refreshes or image updates (those count for it, but are not held back by it).
func intervalApplies(job protocol.Job) bool {
	scope, ok := protocol.ParseScope(job.Scope)
	if job.Verb != protocol.VerbInstall || !ok {
		return false // a scope that does not parse: the agent refuses the job as invalid anyway
	}
	return scope.Kind != protocol.ScopePkg && scope.Kind != protocol.ScopeDeps && !scope.Images()
}

// intervalRefuses: an install for node that the agent's minimum interval keeps out now.
func (s *Server) intervalRefuses(node string, job protocol.Job) bool {
	if !intervalApplies(job) {
		return false
	}
	e, ok := s.Reg.Get(node)
	return ok && nextInstall(e, s.Now()) != nil
}
