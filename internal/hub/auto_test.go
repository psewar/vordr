package hub

import (
	"testing"
	"time"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/wire"
)

// autoFixture: a canary and another host, both install.auto: hub with a window around the fixture's "now" (UTC),
// connected, with security updates pending since that moment.
func newAutoFixture(t *testing.T, tweak ...func(*wire.PolicySummary)) *viewFixture {
	t.Helper()
	f := newViewFixture(t)
	f.now = time.Date(2026, 10, 30, 2, 30, 0, 0, time.UTC) // a Friday
	f.srv.AutoInstall = NewAutoInstall(AutoInstallConfig{Canary: []string{"canary"}, DelayHours: 24})
	pol := wire.PolicySummary{AllowedScopes: []string{"security"}, DefaultScope: "security", Auto: "hub",
		Window: &wire.WindowInfo{Days: []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}, Start: "02:00", End: "04:00"},
		TZ:     "UTC"}
	for _, tw := range tweak {
		tw(&pol)
	}
	for _, n := range []string{"canary", "other"} {
		f.host(t, n, []protocol.Verb{protocol.VerbCheck, protocol.VerbInstall}, pol, true)
		f.srv.sessions[n] = &session{node: n, waiters: map[string]chan wire.JobStatus{}}
		if err := f.srv.Reg.SetReport(n, aptReport(f.now, 5, 2, nil)); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f *viewFixture) due(t *testing.T, node string) (string, string) {
	t.Helper()
	e, _ := f.srv.Reg.Get(node)
	return f.srv.autoDue(e, f.now)
}

// The canary installs as soon as its window is open and security updates are pending; the others wait until the
// updates have been pending delay_hours, and only while the canary is clean.
func TestAutoInstallRings(t *testing.T) {
	f := newAutoFixture(t)
	if scope, r := f.due(t, "canary"); r != autoReady || scope != "security" {
		t.Fatalf("canary: %q %q", scope, r)
	}
	if _, r := f.due(t, "other"); r != autoNotReady {
		t.Fatalf("other right away: %q", r)
	}
	f.now = f.now.Add(24 * time.Hour) // the next night, same window
	for _, n := range []string{"canary", "other"} {
		_ = f.srv.Reg.SetReport(n, aptReport(f.now, 5, 2, nil))
	}
	if _, r := f.due(t, "other"); r != autoReady {
		t.Fatalf("other after 24 h: %q", r)
	}
	// A failed run on the canary stops the later rings.
	fin := f.now
	_ = f.srv.Reg.SetRun("canary", Run{JobID: "j", Verb: protocol.VerbInstall, Code: protocol.CodeFailedPackageManager,
		Started: f.now, Finished: &fin})
	if _, r := f.due(t, "other"); r != autoNotReady {
		t.Fatalf("other while the canary has a problem: %q", r)
	}
}

func TestAutoInstallOnlyWhenItMakesSense(t *testing.T) {
	f := newAutoFixture(t)
	f.srv.AutoInstall.tried["canary"] = f.now.Add(-time.Hour)
	if _, r := f.due(t, "canary"); r != autoNotReady {
		t.Fatal("asked again in the same window")
	}
	delete(f.srv.AutoInstall.tried, "canary")
	f.now = f.now.Add(3 * time.Hour) // 05:30, outside the window
	if _, r := f.due(t, "canary"); r != autoNotReady {
		t.Fatal("outside the window")
	}
	f.now = f.now.Add(-3 * time.Hour)
	_ = f.srv.Reg.SetReport("canary", aptReport(f.now, 3, 0, nil))
	if _, r := f.due(t, "canary"); r != autoNotReady {
		t.Fatal("no security updates pending")
	}
	_ = f.srv.Reg.SetReport("canary", aptReport(f.now, 3, 1, nil))
	delete(f.srv.sessions, "canary")
	if _, r := f.due(t, "canary"); r != autoNotReady {
		t.Fatal("offline")
	}
}

// The host's time zone decides: 03:30 UTC is 23:30 in New York (outside 02:00-04:00), 06:30 UTC is 02:30 in New
// York (inside).
func TestAutoInstallWindowInTheHostsZone(t *testing.T) {
	f := newAutoFixture(t)
	e, _ := f.srv.Reg.Get("canary")
	e.Policy.TZ = "America/New_York"
	f.now = time.Date(2026, 10, 30, 3, 30, 0, 0, time.UTC) // 23:30 the evening before in New York: outside
	if _, r := f.srv.autoDue(e, f.now); r != autoNotReady {
		t.Fatalf("23:30 New York: %q", r)
	}
	f.now = time.Date(2026, 10, 30, 6, 30, 0, 0, time.UTC) // 02:30 in New York (summer time): inside
	_ = f.srv.Reg.SetReport("canary", aptReport(f.now, 5, 2, nil))
	e, _ = f.srv.Reg.Get("canary")
	e.Policy.TZ = "America/New_York"
	if _, r := f.srv.autoDue(e, f.now); r != autoReady {
		t.Fatalf("02:30 New York: %q", r)
	}
}

// install.auto_scope: the button in HA installs everything (default_scope all), install.auto only security updates.
// Agents before 0.8.2 announce no auto_scope - then default_scope, as before.
func TestAutoInstallOwnScope(t *testing.T) {
	f := newAutoFixture(t)
	e, _ := f.srv.Reg.Get("canary")
	e.Policy.AllowedScopes, e.Policy.DefaultScope, e.Policy.AutoScope = []string{"security", "all"}, "all", "security"
	if scope, r := f.srv.autoDue(e, f.now); r != autoReady || scope != "security" {
		t.Fatalf("auto_scope security: %q %q", scope, r)
	}
	_ = f.srv.Reg.SetReport("canary", aptReport(f.now, 5, 0, nil)) // updates pending, none of them security
	e, _ = f.srv.Reg.Get("canary")
	e.Policy.AllowedScopes, e.Policy.DefaultScope, e.Policy.AutoScope = []string{"security", "all"}, "all", "security"
	if _, r := f.srv.autoDue(e, f.now); r != autoNotReady {
		t.Fatalf("no security updates, but auto_scope security: %q", r)
	}
	e.Policy.AutoScope = "" // older agent: default_scope all
	if scope, r := f.srv.autoDue(e, f.now); r != autoReady || scope != "all" {
		t.Fatalf("without auto_scope: %q %q", scope, r)
	}
}

// Hosts that cannot take part say why (logged once by autoOnce); hosts without install.auto: hub are left alone.
func TestAutoInstallReasons(t *testing.T) {
	f := newAutoFixture(t)
	e, _ := f.srv.Reg.Get("canary")
	cases := map[string]func(p *wire.PolicySummary){
		autoNotAuto:  func(p *wire.PolicySummary) { p.Auto = "off" },
		autoNoWindow: func(p *wire.PolicySummary) { p.Window = nil },
		autoNoZone:   func(p *wire.PolicySummary) { p.TZ = "" },
		autoBadZone:  func(p *wire.PolicySummary) { p.TZ = "Mars/Olympus" },
		autoScope:    func(p *wire.PolicySummary) { p.DefaultScope = "app:gitlab" },
		autoBadWin: func(p *wire.PolicySummary) {
			p.Window = &wire.WindowInfo{Days: []string{"fri"}, Start: "2 Uhr", End: "04:00"}
		},
	}
	for want, change := range cases {
		x := e
		pol := e.Policy
		if pol.Window != nil {
			w := *pol.Window
			pol.Window = &w
		}
		change(&pol)
		x.Policy = pol
		if _, r := f.srv.autoDue(x, f.now); r != want {
			t.Errorf("%s: got %q", want, r)
		}
	}
	if issues := (&AutoInstallConfig{DelayHours: 24}).check(); len(issues) != 1 {
		t.Fatalf("auto_install without canary: %v", issues)
	}
}
