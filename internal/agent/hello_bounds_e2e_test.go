package agent

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/psewar/vordr/internal/facts"
	"github.com/psewar/vordr/internal/hub"
	"github.com/psewar/vordr/internal/identity"
	"github.com/psewar/vordr/internal/policy"
	"github.com/psewar/vordr/internal/runner"
	"github.com/psewar/vordr/internal/wire"
)

// richConfig is an agent.yaml with every part of the policy summary the hello carries: apps, each kind of scope
// check-config takes, install and reboot window - each with a day repeated, as check-config takes it, the install
// window with eight entries -, the longest minimum interval, agent updates. hubKey is any valid key.
func richConfig(hubKey string) string {
	cfg := `install:
  allowed_scopes: [security, all, "app:gitlab", "app:` + strings.Repeat("a", 32) + `"`
	if runtime.GOOS != "windows" {
		// Container images are read on Linux only; their longest names, and a pattern.
		cfg += `, "image:stack/*", "image:*/web.1", "image:` + strings.Repeat("p", 56) + "/" + strings.Repeat("S", 56) + `"`
	}
	cfg += `]
  default_scope: all
  auto: ` + map[bool]string{true: "off", false: "hub"}[runtime.GOOS == "windows"] + `
  auto_scope: security
  window: {days: [mon, tue, wed, thu, fri, sat, sun, mon], start: "22:30", end: "04:15"}
  min_interval_h: 168
reboot:
  allowed: true
  window: {days: [sat, sun, sat], start: "03:00", end: "04:00"}
  window_only: true
agent_update: {enabled: true, public_key: "` + hubKey + `"}
apps:
  - {id: gitlab, name: "GitLab Community Edition (Omnibus)", source: apt-package, package: gitlab-ce}
  - {id: ` + strings.Repeat("a", 32) + `, name: "Notizen für alle – Ärzte, Öfen & Übersicht", source: winget-package, package: jqlang.jq}
`
	if runtime.GOOS != "windows" {
		cfg += "packages: {containers: true}\n"
	}
	return cfg
}

// The most important compatibility check of ADR-0191: what the real agent sends today - its own builder of the hello,
// with the facts of the machine the test runs on, the example configuration and one with every part of the policy, the
// versions build.sh gives, every verb, jobs running and results held - reaches the hub over a real tunnel without a
// field dropped. A bound that cut one off would make an existing agent lose it at the next hub.
func TestTheRealAgentsHelloFitsTheHubsBounds(t *testing.T) {
	example, err := os.ReadFile(filepath.Join("..", "..", "agent.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	h := startHub(t, 5)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	host := facts.Collect(ctx, runner.Exec{})
	for _, c := range []struct {
		node, yaml, version string
	}{
		{"example-host", string(example), "0.0.0-pr"},
		{"rich-host", richConfig(h.key.PublicB64()), "0.48.0"},
		{strings.Repeat("n", 63), richConfig(h.key.PublicB64()), "dev"},
	} {
		cfg, issues := policy.Parse([]byte(c.yaml))
		if len(issues) > 0 {
			t.Fatalf("%s: agent.yaml: %v", c.node, issues)
		}
		dir := t.TempDir()
		cfg.Node, cfg.StateDir = c.node, dir
		cfg.Hub = policy.Hub{URL: h.url, PublicKey: h.key.PublicB64(), CAFile: h.caFile}
		id, _, err := identity.LoadOrCreate(dir)
		if err != nil {
			t.Fatal(err)
		}
		agent := NewClient(cfg, id, c.version, host, &fakeChecker{}, quiet)
		agent.Installer = &fakeInstaller{}
		agent.Rebooter = (&fakeHost{boot: "boot-1"}).rebooter()
		agent.Updater = &SelfUpdate{Exe: filepath.Join(dir, "vordr-agent")}
		agent.Backups = (&fakeHypervisor{}).backups()
		// Job ids as the hub makes them (24 hex digits), running and held.
		agent.setRunning("0123456789abcdef01234567", true)
		agent.setRunning("89abcdef0123456701234567", true)
		agent.unsent = map[string]wire.JobStatus{"fedcba9876543210fedcba98": {T: wire.TJobStatus,
			JobID: "fedcba9876543210fedcba98", Code: "ok"}}
		go agent.session(ctx)
		waitFor(t, c.node+" pending", func() bool { return state(h.reg, c.node) == wire.StatePendingApproval })

		e, _ := h.reg.Get(c.node)
		if e.AgentVersion != c.version || !reflect.DeepEqual(e.Facts, host) ||
			!reflect.DeepEqual(e.Verbs, agent.Verbs()) || len(e.Verbs) != 5 {
			t.Errorf("%s: stored %q %+v %v, sent %q %+v %v", c.node, e.AgentVersion, e.Facts, e.Verbs, c.version, host,
				agent.Verbs())
		}
		if sent := Summary(cfg, true); !reflect.DeepEqual(e.Policy, sent) {
			t.Errorf("%s: stored policy %+v, sent %+v", c.node, e.Policy, sent)
		}
	}
	for _, event := range []string{"hello_fields_dropped", "reboot_window_refused"} {
		raw, _, err := h.events.Read(hub.EventFilter{Event: event})
		if err != nil || len(raw) != 0 {
			t.Errorf("%s: %d %v", event, len(raw), err)
			for _, l := range raw {
				t.Log(string(l))
			}
		}
	}
	if e, _ := h.reg.Get("rich-host"); e.Policy.RebootWindow == nil || len(e.Policy.RebootWindow.Days) != 3 ||
		e.Policy.Window == nil || len(e.Policy.Window.Days) != 8 || len(e.Policy.Apps) != 2 {
		t.Fatalf("the rich policy did not reach the hub whole: %+v", e.Policy)
	}
}
