package agent

import (
	"testing"

	"github.com/psewar/vordr/internal/policy"
	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/source"
)

// The agent announces its install window and time zone, so the hub can plan install.auto: hub runs inside it.
func TestSummaryAnnouncesWindowAndZone(t *testing.T) {
	old := zoneName
	zoneName = func() string { return "America/New_York" }
	defer func() { zoneName = old }()
	cfg := policy.Config{Install: policy.Install{AllowedScopes: []string{"security"}, DefaultScope: "security",
		Auto: policy.AutoHub, Window: &policy.Window{Days: []string{"sun"}, Start: "02:00", End: "04:00"}}}
	s := Summary(cfg, true)
	if s.Window == nil || s.Window.Start != "02:00" || s.Window.End != "04:00" || s.Window.Days[0] != "sun" ||
		s.TZ != "America/New_York" {
		t.Fatalf("summary: %+v", s)
	}
	cfg.Install.Window = nil
	if s := Summary(cfg, true); s.Window != nil || s.TZ != "" {
		t.Fatalf("without a window nothing to plan: %+v", s)
	}
}

// The agent tells the hub its minimum interval and when its last run that changed packages started (ADR-0110): the
// interval in the summary, the start in the hello and with the final status of that run - the same time its own check
// counts from.
func TestAgentReportsItsInterval(t *testing.T) {
	inst := &fakeInstaller{result: source.InstallResult{Code: protocol.CodeOK}, change: true}
	c := installClient(t, inst, allowAll) // min_interval_h 6
	if s := Summary(c.Cfg, true); s.MinIntervalH != 6 {
		t.Fatalf("summary: %+v", s)
	}
	if at := c.lastInstallAt(); at != nil {
		t.Fatalf("before any run: %v", at)
	}
	var out outbox
	c.install(installJob("j1", "all"), out.send)
	jr, ok := c.readJournal()
	if !ok || jr.Code != protocol.CodeOK {
		t.Fatalf("journal: %+v %v", jr, ok)
	}
	if st := jr.status(); st.InstallStarted == nil || !st.InstallStarted.Equal(jr.Started) {
		t.Fatalf("final status: %+v", st)
	}
	if at := c.lastInstallAt(); at == nil || !at.Equal(jr.Started) {
		t.Fatalf("for the hello: %v, journal %v", at, jr.Started)
	}
	// The agent's own check is unchanged: a second install within the interval is refused.
	c.install(installJob("j2", "all"), out.send)
	if got := out.final().Code; got != protocol.CodeRejectedRateLimited || inst.calls != 1 {
		t.Fatalf("second install: %s, installer calls %d", got, inst.calls)
	}
}

// The agent reports its reboot window and reboot.window_only with its time zone (ADR-0187), so that Home Assistant
// can say when a reboot runs. Not without reboot.allowed (no reboot through Vördr at all), not without a reboot window
// (an invalid configuration: check-config requires one with reboot.allowed), and not to a hub that does not take it.
func TestSummaryReportsTheRebootWindow(t *testing.T) {
	old := zoneName
	zoneName = func() string { return "Europe/Paris" }
	defer func() { zoneName = old }()
	win := &policy.Window{Days: []string{"mon", "tue"}, Start: "06:00", End: "16:00"}
	cfg := policy.Config{Reboot: policy.Reboot{Allowed: true, Window: win, WindowOnly: true}}
	s := Summary(cfg, true)
	if w := s.RebootWindow; w == nil || len(w.Days) != 2 || w.Days[1] != "tue" || w.Start != "06:00" ||
		w.End != "16:00" || !s.RebootWindowOnly || s.TZ != "Europe/Paris" || s.Window != nil {
		t.Fatalf("summary: %+v", s)
	}
	cfg.Reboot.WindowOnly = false
	if s := Summary(cfg, true); s.RebootWindow == nil || s.RebootWindowOnly {
		t.Fatalf("without window_only: %+v", s)
	}
	for name, c := range map[string]struct {
		cfg policy.Config
		hub bool
	}{
		"reboot not allowed": {policy.Config{Reboot: policy.Reboot{Window: win, WindowOnly: true}}, true},
		"no reboot window":   {policy.Config{Reboot: policy.Reboot{Allowed: true}}, true},
		"an older hub":       {cfg, false},
	} {
		if s := Summary(c.cfg, c.hub); s.RebootWindow != nil || s.RebootWindowOnly || s.TZ != "" {
			t.Errorf("%s: %+v", name, s)
		}
	}
}

// install.auto_scope reaches the hub, which picks the scope of automatic runs from it.
func TestSummaryAnnouncesAutoScope(t *testing.T) {
	cfg := policy.Config{Install: policy.Install{AllowedScopes: []string{"security", "all"}, DefaultScope: "all",
		Auto: policy.AutoHub, AutoScope: "security"}}
	if s := Summary(cfg, true); s.AutoScope != "security" || s.DefaultScope != "all" {
		t.Fatalf("summary: %+v", s)
	}
}
