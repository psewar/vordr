package policy

import (
	"fmt"
	"strings"
	"testing"

	"github.com/psewar/vordr/internal/protocol"
)

// edgeApps is the apps section with n apps named name.
func edgeApps(n int, name string) string {
	var b strings.Builder
	b.WriteString("apps:\n")
	for i := range n {
		fmt.Fprintf(&b, "  - {id: app-%d, name: %q, source: apt-package, package: pkg-%d}\n", i, name, i)
	}
	return b.String()
}

// edgeDays is a list of n day names, each day repeated as check-config takes it.
func edgeDays(n int) string {
	days := make([]string, n)
	for i := range days {
		days[i] = dayNames[i%7]
	}
	return "[" + strings.Join(days, ", ") + "]"
}

// check-config refuses what the hub would drop from the hello (ADR-0191, "Bad"; ADR-0195), with the hub's own bounds
// from protocol: at each bound agent.yaml passes, one step beyond it is refused with the field's issue.
func TestCheckConfigRefusesWhatTheHubDrops(t *testing.T) {
	if protocol.MaxApps != 64 || protocol.MaxAppNameLen != 255 || protocol.MaxAllowedScopes != 128 ||
		protocol.MaxWindowDays != 64 {
		t.Fatal("the bounds changed: the cases below are written for 64, 255, 128 and 64")
	}
	name255 := strings.Repeat("Ä", 127) + "x" // 255 bytes, 128 characters
	scopes := func(n int) string {
		return "install: {allowed_scopes: [" + strings.Repeat("security, ", n-1) + "security], default_scope: security}\n"
	}
	installWindow := func(n int) string {
		return "install: {window: {days: " + edgeDays(n) + ", start: '22:00', end: '05:00'}}\n"
	}
	rebootWindow := func(n int) string {
		return "reboot: {allowed: true, window: {days: " + edgeDays(n) + ", start: '03:00', end: '04:00'}}\n"
	}
	for name, c := range map[string]struct {
		yaml  string
		issue string // the field of the one issue; "" for none
	}{
		"64 apps":                           {edgeApps(64, "Notes"), ""},
		"65 apps":                           {edgeApps(65, "Notes"), "apps"},
		"a name of 255 bytes":               {edgeApps(1, name255), ""},
		"a name of 256 bytes":               {edgeApps(1, name255+"x"), "apps[0].name"},
		"a name with umlauts and a dash":    {edgeApps(1, "Notizen für alle – Ärzte"), ""},
		"a name with a tab":                 {edgeApps(1, "Git\tLab"), "apps[0].name"},
		"a name with a line break":          {edgeApps(1, "Git\nLab"), "apps[0].name"},
		"a name with an escape":             {edgeApps(1, "Git\x1b[2JLab"), "apps[0].name"},
		"128 allowed scopes":                {scopes(128), ""},
		"129 allowed scopes":                {scopes(129), "install.allowed_scopes"},
		"64 entries in the install window":  {installWindow(64), ""},
		"65 entries in the install window":  {installWindow(65), "install.window.days"},
		"a day twice in the install window": {installWindow(8), ""},
		"64 entries in the reboot window":   {rebootWindow(64), ""},
		"65 entries in the reboot window":   {rebootWindow(65), "reboot.window.days"},
	} {
		_, issues := Parse([]byte("node: host-a\n" + c.yaml))
		switch {
		case c.issue == "" && len(issues) > 0:
			t.Errorf("%s: %v", name, issues)
		case c.issue != "" && (len(issues) != 1 || issues[0].Field != c.issue):
			t.Errorf("%s: %v, want one issue of %s", name, issues, c.issue)
		}
	}
}
