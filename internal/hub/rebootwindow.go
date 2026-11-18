package hub

import (
	"time"

	"github.com/psewar/vordr/internal/policy"
	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/wire"
)

// The reboot window as the hub sees it (ADR-0187). The agent decides about its reboot window itself (ADR-0041,
// ADR-0154): a reboot without a reason runs only inside reboot.window, and on a host with reboot.window_only also one
// with a reason. The agent reports the window, its time zone and window_only, so that Home Assistant can say from when
// the reboot button and the reboot repair work, instead of sending a reboot the agent refuses. The hub only passes the
// window on and says when it opens; it refuses no reboot for it.

// Why the hub does not take the reboot window a hello reports (ADR-0187): the fixed codes of the event
// reboot_window_refused. The window is bounded before it is kept, computed with, or shown in Home Assistant; a host
// whose window is not taken counts as one that reports none.
const (
	rebootWindowBadDays   = "days"   // more than protocol.MaxWindowDays entries in days
	rebootWindowBadWindow = "window" // check-config would refuse it: no day, no day name, start or end not HH:MM, equal
	rebootWindowBadZone   = "tz"     // a zone name longer than maxZoneLen, or one this hub's time zone data does not know
)

// maxZoneLen bounds the zone name the hub takes with a reboot window; IANA names are far shorter.
const maxZoneLen = 64

// rebootWindowRefusal is why the hub does not take the reboot window p reports, "" when it does or p reports none. A
// window without a zone is taken: the host does not name its zone, and the window is passed on without a next opening.
func rebootWindowRefusal(p wire.PolicySummary) string {
	if p.RebootWindow == nil {
		return ""
	}
	if why := windowRefusal(*p.RebootWindow); why != "" {
		return why
	}
	if p.TZ == "" {
		return ""
	}
	if len(p.TZ) > maxZoneLen {
		return rebootWindowBadZone
	}
	// Whether this hub knows the zone shows only by loading it; there is no lookup without the load.
	if _, err := time.LoadLocation(p.TZ); err != nil {
		return rebootWindowBadZone
	}
	return ""
}

// windowRefusal is why the hub does not take a window a hello reports, "" when it does: the window check-config takes -
// day names, a day repeated too, start and end HH:MM and not equal (else rebootWindowBadWindow) - with at most
// protocol.MaxWindowDays entries (else rebootWindowBadDays), the bound check-config keeps too (ADR-0195). The same for
// the reboot window (ADR-0187) and the install window (ADR-0191). A day repeated is kept as sent: Contains asks each
// day name once, and the entries are bounded.
func windowRefusal(w wire.WindowInfo) string {
	if len(w.Days) > protocol.MaxWindowDays {
		return rebootWindowBadDays
	}
	if pw := (policy.Window{Days: w.Days, Start: w.Start, End: w.End}); !pw.Valid() {
		return rebootWindowBadWindow
	}
	return ""
}

// takeRebootWindow drops the reboot window of a hello the hub does not take (rebootWindowRefusal) and returns why, ""
// when it takes it. The event reboot_window_refused follows once the register has taken the host (logDrops).
func takeRebootWindow(h *wire.Hello) string {
	why := rebootWindowRefusal(h.Policy)
	if why != "" {
		h.Policy.RebootWindow, h.Policy.RebootWindowOnly = nil, false
	}
	return why
}

// rebootWindowView is the host's reboot window as its agent reports it; nil when the hub offers no reboot for the host
// (c.Reboot), and when its agent reports none (before ADR-0187, or a window the hub did not take). check-config requires
// reboot.window with reboot.allowed, so a host that offers the reboot without a window has an invalid configuration.
func rebootWindowView(p wire.PolicySummary, c wire.Capabilities) *wire.RebootWindowView {
	if !c.Reboot || p.RebootWindow == nil {
		return nil
	}
	w := p.RebootWindow
	return &wire.RebootWindowView{Days: append([]string{}, w.Days...), Start: w.Start, End: w.End, TZ: p.TZ,
		WindowOnly: p.RebootWindowOnly}
}

// nextReboot is when the host's reboot window opens next, while it is closed at now; nil when it is open, when the
// host reports none (rebootWindowView), and when the hub cannot read it - no time zone, one this hub's time zone data
// does not know, a window that is not valid. Then Home Assistant behaves as before and the agent decides alone.
func nextReboot(p wire.PolicySummary, c wire.Capabilities, now time.Time) *time.Time {
	if rebootWindowView(p, c) == nil || p.TZ == "" {
		return nil
	}
	loc, err := time.LoadLocation(p.TZ)
	if err != nil {
		return nil // a zone this hub's time zone data does not know: the agent checks its window itself
	}
	w := policy.Window{Days: p.RebootWindow.Days, Start: p.RebootWindow.Start, End: p.RebootWindow.End}
	local := now.In(loc)
	if !w.Valid() || w.Contains(local) {
		return nil
	}
	at, ok := w.Opens(local)
	if !ok {
		return nil
	}
	at = at.UTC()
	return &at
}
