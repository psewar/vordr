package hub

import (
	"time"

	"github.com/psewar/vordr/internal/policy"
	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/wire"
)

// The install window as the hub sees it (ADR-0109). The agent decides about its window itself; the hub reads the
// window and time zone the agent announces so that it does not ask for what the agent would refuse - and does not
// have a guest backed up first for an install that the window would then stop (ADR-0096).

// ButtonReason is the reason an install asked for in Home Assistant carries when the host's window is closed and the
// person gave none: the install button installs at once (ADR-0109). It stays with the run like any other reason.
const ButtonReason = "install started in Home Assistant outside the window"

// AdminOutsideWindow: the install would run outside the host's window without a reason, and the agent would refuse
// it (rejected_outside_window). The hub says so before it sends the job or has the guest backed up.
const AdminOutsideWindow AdminCode = "outside_window"

// installWindowClosed reports whether the install window the agent announces (install.window, in its time zone) is
// closed at now. Without a window the host has none; with one the hub cannot read (an agent before 0.7.4 announces
// none, an unknown time zone) the hub cannot tell and leaves the decision to the agent: false either way.
func installWindowClosed(p wire.PolicySummary, now time.Time) bool {
	if p.Window == nil || p.TZ == "" {
		return false
	}
	loc, err := time.LoadLocation(p.TZ)
	if err != nil {
		return false // a zone this hub's time zone data does not know: the agent checks its window itself
	}
	w := policy.Window{Days: p.Window.Days, Start: p.Window.Start, End: p.Window.End}
	return w.Valid() && !w.Contains(now.In(loc))
}

// windowRefuses: an install job for node without a reason that the host's window keeps out now.
func (s *Server) windowRefuses(node string, job protocol.Job) bool {
	if job.Verb != protocol.VerbInstall || job.OutsideWindow != "" {
		return false
	}
	e, ok := s.Reg.Get(node)
	return ok && installWindowClosed(e.Policy, s.Now())
}

// haReason is the reason a job asked for in Home Assistant carries. A reason the person gave stays. An install
// without one gets ButtonReason while the host's install window is closed and its agent takes reasons (ADR-0109);
// everything else (a reboot, an install inside the window) runs without one.
func (s *Server) haReason(node string, verb protocol.Verb, given string) string {
	if given != "" || verb != protocol.VerbInstall {
		return given
	}
	e, ok := s.Reg.Get(node)
	if !ok || !e.Policy.OutsideWindow || !installWindowClosed(e.Policy, s.Now()) {
		return ""
	}
	return ButtonReason
}
