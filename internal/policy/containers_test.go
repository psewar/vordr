package policy

import "testing"

// Container images are reported only where agent.yaml says so (ADR-0113): asking registries is new traffic from
// the host. Windows has no engine socket to read, and the images are part of the inventory.
func TestContainersOffByDefault(t *testing.T) {
	cfg, issues := Parse(nil)
	if len(issues) > 0 || cfg.Packages.Containers {
		t.Fatalf("default: containers %v, issues %v", cfg.Packages.Containers, issues)
	}
	cfg, issues = Parse([]byte("packages: {containers: true}\n"))
	if len(issues) > 0 || !cfg.Packages.Containers {
		t.Fatalf("on: containers %v, issues %v", cfg.Packages.Containers, issues)
	}
}

func TestContainersRefused(t *testing.T) {
	defer func(was string) { goos = was }(goos)
	for name, c := range map[string]struct{ system, raw string }{
		"windows":       {"windows", "packages: {containers: true}\n"},
		"inventory off": {"linux", "packages: {inventory: false, containers: true}\n"},
		"id taken":      {"linux", "packages: {containers: true, locations: [{id: containers, path: /opt/a}]}\n"},
	} {
		goos = c.system
		if _, issues := Parse([]byte(c.raw)); len(issues) != 1 || issues[0].Field != "packages.containers" {
			t.Errorf("%s: issues = %v, want exactly one on packages.containers", name, issues)
		}
	}
	goos = "windows"
	if _, issues := Parse([]byte("packages: {containers: false}\n")); len(issues) != 0 {
		t.Errorf("windows, off: issues = %v", issues)
	}
}
