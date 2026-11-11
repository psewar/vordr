package hub

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/psewar/vordr/internal/policy"
	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/wire"
)

// Automatic installs (design 10.3, install.auto: hub). The hub starts install runs itself - inside the host's window,
// with the backup check, in rings: canaries as soon as something is pending, the others only while every canary is
// clean and only when each of their pending updates has met a clean canary install or been pending delay_hours
// (ADR-0107). Both sides must want it: the agent says install.auto: hub (and announces its window and time zone),
// hub.yaml has an auto_install section.

// AutoInstallConfig is the auto_install section of hub.yaml.
type AutoInstallConfig struct {
	Canary     []string `yaml:"canary"`      // install as soon as their window opens
	DelayHours int      `yaml:"delay_hours"` // the others: each update pending this long, unless a canary met it; default 24
}

const (
	defaultAutoDelayH = 24
	autoInterval      = 5 * time.Minute
	// autoRetryAfter: one automatic attempt per host and window; the agent's min_interval_h guards as well.
	autoRetryAfter = 20 * time.Hour
	// ByAuto: the run was started by the hub's automatic installs.
	ByAuto = "auto"
)

func (c *AutoInstallConfig) check() []string {
	var issues []string
	for i, n := range c.Canary {
		if !nodePattern.MatchString(n) {
			issues = append(issues, fmt.Sprintf("auto_install.canary[%d]: %q is not a node name", i, n))
		}
	}
	if len(c.Canary) == 0 {
		issues = append(issues, "auto_install.canary: name at least one host that installs first")
	}
	if c.DelayHours < 1 || c.DelayHours > 24*14 {
		issues = append(issues, "auto_install.delay_hours: must be between 1 and 336")
	}
	return issues
}

// AutoInstall holds the configuration and what the automatic installs tried.
type AutoInstall struct {
	cfg AutoInstallConfig

	mu      sync.Mutex
	tried   map[string]time.Time // last automatic attempt per host
	refused map[string]string    // last reason a host cannot take part, logged once per change
}

func NewAutoInstall(cfg AutoInstallConfig) *AutoInstall {
	return &AutoInstall{cfg: cfg, tried: map[string]time.Time{}, refused: map[string]string{}}
}

// Reasons a host that says install.auto: hub cannot take part - a matter of configuration, logged once per change.
const (
	autoNoWindow = "no_window"   // the agent announces no window (older than 0.7.4)
	autoBadZone  = "bad_tz"      // the agent's time zone is unknown here
	autoScope    = "scope"       // default_scope is neither security nor all
	autoNoVerb   = "no_install"  // the agent does not announce install
	autoNoZone   = "unknown_tz"  // empty time zone
	autoBadWin   = "bad_window"  // the announced window would not pass check-config
	autoReady    = ""            // takes part
	autoNotAuto  = "not_auto"    // not install.auto: hub - not logged
	autoNotReady = "not_due_now" // takes part, but not now (outside window, nothing pending, ring closed, ...)
)

// autoDue says whether the hub starts an automatic install for e now, and with which scope. reason is autoReady when
// due, autoNotReady when the host takes part but not now, otherwise why it cannot take part.
func (s *Server) autoDue(e Entry, now time.Time) (scope, reason string) {
	p := e.Policy
	if e.State != wire.StateApproved || p.Auto != policy.AutoHub {
		return "", autoNotAuto
	}
	// install.auto_scope; agents before 0.8.2 do not announce it, then default_scope as before
	want := p.AutoScope
	if want == "" {
		want = p.DefaultScope
	}
	switch {
	case !slices.Contains(e.Verbs, protocol.VerbInstall):
		return "", autoNoVerb
	case want != "security" && want != "all":
		return "", autoScope
	case p.Window == nil:
		return "", autoNoWindow
	case p.TZ == "":
		return "", autoNoZone
	}
	loc, err := time.LoadLocation(p.TZ)
	if err != nil {
		return "", autoBadZone
	}
	w := policy.Window{Days: p.Window.Days, Start: p.Window.Start, End: p.Window.End}
	if !w.Valid() {
		return "", autoBadWin
	}
	if !w.Contains(now.In(loc)) {
		return "", autoNotReady
	}
	// Busy as the queue sees it (bounded, and a hypervisor backing up a guest), as for the agent rollout.
	if online, _ := s.Online(e.Node); !online || s.running(e.Node) {
		return "", autoNotReady
	}
	if nextInstall(e, now) != nil {
		// The agent would refuse it for its minimum interval (ADR-0110): not now, and the window's one attempt stays.
		return "", autoNotReady
	}
	v := s.HostView(e, now)
	switch {
	case want == "security" && (v.Security == nil || *v.Security == 0):
		return "", autoNotReady
	case want == "all" && (v.Pending == nil || *v.Pending-keptBack(v) == 0): // ADR-0127: what upgrade leaves, it never takes
		return "", autoNotReady
	}
	a := s.AutoInstall
	a.mu.Lock()
	last, tried := a.tried[e.Node]
	a.mu.Unlock()
	if tried && now.Sub(last) < autoRetryAfter {
		return "", autoNotReady
	}
	if slices.Contains(a.cfg.Canary, e.Node) {
		return want, autoReady
	}
	// Later rings: only while every canary is clean (a failed run, offline, a stale host - any problem stops the
	// others), and only when each pending update of the scope has met a canary or the delay first (ADR-0107, ADR-0118).
	var runs []canaryRun // each canary's last clean system install
	for _, c := range a.cfg.Canary {
		ce, ok := s.Reg.Get(c)
		if !ok || s.HostView(ce, now).Problem != nil {
			return "", autoNotReady
		}
		if r, ok := lastCleanRun(ce); ok {
			runs = append(runs, r)
		}
	}
	if !soaked(e.PendingUpdates, want, runs, now, time.Duration(a.cfg.DelayHours)*time.Hour) {
		return "", autoNotReady
	}
	return want, autoReady
}

// keptBack: the pending updates an install of scope all leaves under install.mode upgrade (ADR-0127); 0 without.
func keptBack(v wire.HostView) int {
	if v.KeptBack == nil {
		return 0
	}
	return *v.KeptBack
}

// canaryRun: when a canary's last clean system install started, and which updates it left behind: everything the
// canary still reports pending after the run. The run did not install those - held back (ADR-0116), outside its scope,
// kept back by apt, or one that reached the canary only now - so it met none of them, though it went through
// (ADR-0118).
type canaryRun struct {
	at   time.Time
	left map[updateName]bool
}

// updateName: an update across hosts, by source and package; the versions differ between releases.
type updateName struct{ source, name string }

// lastCleanRun: the canary's last clean system install, once a complete report received after it ended says what is
// still pending there. Before that report the hub cannot tell what the run left behind, so the run counts for nothing
// yet - also when the canary had nothing pending before. Every update in that report is left
// behind, whenever it was first seen: one that first reached the canary's list after the run was not installed by it
// either, nor one whose target version changed.
func lastCleanRun(e Entry) (canaryRun, bool) {
	if e.CleanInstallAt == nil || e.CleanInstallEnd == nil || e.PendingAt == nil || e.PendingAt.Before(*e.CleanInstallEnd) {
		return canaryRun{}, false
	}
	left := map[updateName]bool{}
	for _, u := range e.PendingUpdates {
		left[updateName{u.Source, u.Name}] = true
	}
	return canaryRun{at: *e.CleanInstallAt, left: left}, true
}

// soaked: every pending update of the scope has met a canary - a canary's clean install started no earlier than the
// update was first seen here, and did not leave it behind - or has been pending for the delay (ADR-0107, ADR-0118).
// Without a list of pending updates (a host the hub has not had a complete report from since it keeps one) nothing is
// soaked.
func soaked(pending []PendingUpdate, scope string, runs []canaryRun, now time.Time, delay time.Duration) bool {
	n := 0
	for _, u := range pending {
		if scope == string(protocol.ScopeSecurity) && !u.Security {
			continue
		}
		n++
		if now.Sub(u.Since) < delay && !metACanary(u, runs) {
			return false
		}
	}
	return n > 0
}

func metACanary(u PendingUpdate, runs []canaryRun) bool {
	for _, r := range runs {
		if !r.at.Before(u.Since) && !r.left[updateName{u.Source, u.Name}] {
			return true
		}
	}
	return false
}

// RunAutoInstall looks for due hosts every autoInterval until ctx ends.
func (s *Server) RunAutoInstall(ctx context.Context) {
	for {
		s.autoOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(autoInterval):
		}
	}
}

func (s *Server) autoOnce(ctx context.Context) {
	a := s.AutoInstall
	now := s.Now()
	for _, e := range s.Reg.List() {
		scope, reason := s.autoDue(e, now)
		if reason != autoReady && reason != autoNotReady {
			a.mu.Lock()
			changed := a.refused[e.Node] != reason
			a.refused[e.Node] = reason
			a.mu.Unlock()
			if changed && reason != autoNotAuto {
				s.Events.Log(e.Node, "auto_install_unavailable", map[string]any{"reason": reason})
			}
			continue
		}
		a.mu.Lock()
		delete(a.refused, e.Node)
		if reason == autoReady {
			a.tried[e.Node] = now // one attempt per window, whatever comes of it
		}
		a.mu.Unlock()
		if reason != autoReady {
			continue
		}
		ring := "later"
		if slices.Contains(a.cfg.Canary, e.Node) {
			ring = "canary"
		}
		if s.Backups != nil {
			bctx, cancel := context.WithTimeout(ctx, backupFetchTimeout)
			code, b := s.backupGate(bctx, e.Node)
			cancel()
			if code != AdminOK {
				s.Events.Log(e.Node, "auto_install_skipped", map[string]any{"code": code, "backup": b.State})
				s.changedHA()
				continue
			}
		}
		job, code := s.StartJob(e.Node, protocol.VerbInstall, scope, ByAuto)
		s.Events.Log(e.Node, "auto_install_started", map[string]any{"scope": scope, "ring": ring, "code": code})
		if code != AdminOK {
			continue
		}
		// Once the hub is stopping nobody waits here: the agent's next hello settles the run (ADR-0114, ADR-0142).
		s.spawn(func() {
			wctx, cancel := context.WithTimeout(ctx, installTimeout)
			defer cancel()
			s.WaitJob(wctx, job)
		})
	}
}
