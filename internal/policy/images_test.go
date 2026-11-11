package policy

import (
	"slices"
	"strings"
	"testing"
)

// ADR-0119: Compose services are allowed one by one (or by pattern) in install.allowed_scopes; nothing by default.
func TestImageScopesInThePolicy(t *testing.T) {
	ok := "packages: {containers: true}\n" +
		"install: {allowed_scopes: ['image:stack/*', 'image:edge/web.alt'], window: {days: [mon], start: '02:00', end: '04:00'}}\n"
	c, issues := Parse([]byte(ok))
	if len(issues) > 0 {
		t.Fatalf("image scopes alone, no default_scope: %v", issues)
	}
	if got := c.Install.ImagePatterns(); !slices.Equal(got, []string{"stack/*", "edge/web.alt"}) {
		t.Fatalf("patterns %v", got)
	}
	for svc, want := range map[[2]string]bool{
		{"stack", "web"}: true, {"stack", "db"}: true, {"edge", "web.alt"}: true,
		{"edge", "webXalt"}: false, {"stackx", "web"}: false, {"other", "app"}: false,
	} {
		if got := c.Install.AllowsImage(svc[0], svc[1]); got != want {
			t.Errorf("AllowsImage(%s/%s) = %v", svc[0], svc[1], got)
		}
	}
	// With system packages too, default_scope is still required, and stays a system scope.
	mixed := "packages: {containers: true}\ninstall: {allowed_scopes: [security, 'image:stack/*'], default_scope: security}\n"
	if c, issues := Parse([]byte(mixed)); len(issues) > 0 || !c.Install.AllowsImage("stack", "x") {
		t.Fatalf("mixed: %v", issues)
	}
	// Nothing by default: an existing configuration allows no service.
	if c, issues := Parse([]byte("install: {allowed_scopes: [all], default_scope: all}\n")); len(issues) > 0 ||
		len(c.Install.ImagePatterns()) != 0 || c.Install.AllowsImage("any", "thing") {
		t.Fatalf("no image scopes: %v %v", issues, c.Install.ImagePatterns())
	}
}

func TestImageScopeIssues(t *testing.T) {
	cases := map[string]string{
		"without containers":     "install: {allowed_scopes: ['image:stack/web']}\n",
		"without outdated":       "packages: {containers: true, outdated: false}\ninstall: {allowed_scopes: ['image:stack/web']}\n",
		"no service part":        "packages: {containers: true}\ninstall: {allowed_scopes: ['image:stack']}\n",
		"capital project":        "packages: {containers: true}\ninstall: {allowed_scopes: ['image:Stack/web']}\n",
		"option-like":            "packages: {containers: true}\ninstall: {allowed_scopes: ['image:-stack/web']}\n",
		"other glob syntax":      "packages: {containers: true}\ninstall: {allowed_scopes: ['image:stack/w?b']}\n",
		"brackets":               "packages: {containers: true}\ninstall: {allowed_scopes: ['image:stack/[wd]eb']}\n",
		"three parts":            "packages: {containers: true}\ninstall: {allowed_scopes: ['image:stack/w/x']}\n",
		"images as entry":        "packages: {containers: true}\ninstall: {allowed_scopes: [security, images], default_scope: security}\n",
		"default is an image":    "packages: {containers: true}\ninstall: {allowed_scopes: ['image:stack/w'], default_scope: 'image:stack/w'}\n",
		"system without default": "packages: {containers: true}\ninstall: {allowed_scopes: [security, 'image:stack/w']}\n",
		"too long":               "packages: {containers: true}\ninstall: {allowed_scopes: ['image:stack/" + strings.Repeat("g", 57) + "']}\n",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, issues := Parse([]byte(raw)); len(issues) == 0 {
				t.Fatalf("no issue for %q", raw)
			}
		})
	}
}
