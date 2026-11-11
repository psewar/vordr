package main

import (
	"testing"

	"github.com/psewar/vordr/internal/policy"
)

// check-config names the container images either way: switched on, they mean traffic from the host to the
// registries (ADR-0113), and an operator checking agent.yaml must see it - and that the agent writes to the engine
// for the services it may update (ADR-0119).
func TestPackagesSummary(t *testing.T) {
	off, on := false, true
	cases := []struct {
		name   string
		pk     policy.Packages
		images []string
		want   string
	}{
		{"inventory off", policy.Packages{Inventory: &off, Containers: true}, nil, "packages: off"},
		{"default", policy.Packages{}, nil,
			"packages: well-known places true, 0 location(s) from agent.yaml, container images off"},
		{"containers on", policy.Packages{Containers: true, Locations: []policy.PackageLocation{{ID: "a", Path: "/a"}}},
			nil, "packages: well-known places true, 1 location(s) from agent.yaml, " +
				"container images on (compared with their registries, only reported)"},
		{"containers by button", policy.Packages{Containers: true}, []string{"stack/*", "edge/web"},
			"packages: well-known places true, 0 location(s) from agent.yaml, " +
				"container images on (compared with their registries; rebuilds installed by button for stack/*, edge/web)"},
		{"containers without outdated", policy.Packages{Containers: true, Outdated: &off, Detect: &off}, nil,
			"packages: well-known places false, 0 location(s) from agent.yaml, " +
				"container images on (listed only: outdated is off)"},
		{"outdated on, containers off", policy.Packages{Outdated: &on}, []string{"stack/*"},
			"packages: well-known places true, 0 location(s) from agent.yaml, container images off"},
	}
	for _, c := range cases {
		if got := packagesSummary(c.pk, c.images); got != c.want {
			t.Errorf("%s:\n got  %q\n want %q", c.name, got, c.want)
		}
	}
}
