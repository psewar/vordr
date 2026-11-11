package policy

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExampleIsValid(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "agent.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, issues := Parse(raw)
	if len(issues) > 0 {
		t.Fatalf("agent.example.yaml: %v", issues)
	}
	if cfg.Node != "example-host" || cfg.Install.DefaultScope != "security" || cfg.Reboot.Allowed {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestEmptyFileIsStrict(t *testing.T) {
	cfg, issues := Parse(nil)
	if len(issues) > 0 {
		t.Fatalf("issues: %v", issues)
	}
	if len(cfg.Install.AllowedScopes) != 0 || cfg.Install.Auto != AutoOff || cfg.Reboot.Allowed ||
		cfg.AgentUpdate.Enabled || cfg.HubKey() != nil {
		t.Fatalf("defaults are not strict: %+v", cfg)
	}
	if cfg.Install.MinIntervalH != 6 || cfg.Install.TimeoutMin != 60 || cfg.StateDir != DefaultStateDir {
		t.Fatalf("defaults: %+v", cfg)
	}
	if cfg.Packages.MinAgeDays != 7 || cfg.Packages.MinAgeDaysSecurity != 2 {
		t.Fatalf("minimum ages: %+v", cfg.Packages)
	}
}

// The shorter age for fixes (ADR-0111) defaults to 2 days, never above min_age_days, and cannot be switched off.
func TestMinAgeDaysSecurity(t *testing.T) {
	for raw, want := range map[string]int{
		"packages: {min_age_days: 1}\n":                           1, // the default never exceeds the normal age
		"packages: {min_age_days: 14}\n":                          2,
		"packages: {min_age_days: 7, min_age_days_security: 1}\n": 1,
		"packages: {min_age_days: 7, min_age_days_security: 7}\n": 7,
	} {
		cfg, issues := Parse([]byte(raw))
		if len(issues) > 0 || cfg.Packages.MinAgeDaysSecurity != want {
			t.Errorf("%q: %d, %v; want %d", raw, cfg.Packages.MinAgeDaysSecurity, issues, want)
		}
	}
}

func key(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(pub)
}

func TestValidHubAndAgentUpdate(t *testing.T) {
	raw := "node: h1\nhub: {url: wss://hub.example/v1/tunnel, public_key: " + key(t) + "}\n" +
		"agent_update: {enabled: true, public_key: " + key(t) + ", backup_public_key: " + key(t) + "}\n"
	cfg, issues := Parse([]byte(raw))
	if len(issues) > 0 {
		t.Fatalf("issues: %v", issues)
	}
	if cfg.HubKey() == nil {
		t.Fatal("hub key not readable")
	}
	if len(cfg.AgentUpdate.Keys()) != 2 {
		t.Fatal("operator key and backup key")
	}
	ok := "node: h1\nhealth: [{systemd: app-watchdog.timer}, {docker: web}, {http: 'https://127.0.0.1:8443/api/version'}]\n"
	if c, issues := Parse([]byte(ok)); len(issues) > 0 || len(c.Health) != 3 || c.Health[1].String() != "docker web" {
		t.Fatalf("health: %v %+v", issues, c.Health)
	}
	// winget ids keep their case and dots; Debian's rules do not apply to them.
	win := "node: host-a\napps: [{id: jq, name: jq, source: winget-package, package: jqlang.jq}, " +
		"{id: tool, name: Example Tool, source: winget-package, package: Contoso.ExampleTool.LTS}]\n" +
		"install: {allowed_scopes: ['app:jq'], default_scope: 'app:jq'}\n"
	if c, issues := Parse([]byte(win)); len(issues) > 0 || c.Apps[1].Package != "Contoso.ExampleTool.LTS" {
		t.Fatalf("winget apps: %v %+v", issues, c.Apps)
	}
}

func TestIssues(t *testing.T) {
	cases := map[string]string{
		"unknown key (typo)":             "instal: {auto: hub}\n",
		"hub over ws":                    "hub: {url: ws://hub.example/v1/tunnel, public_key: KEY}\n",
		"hub without key":                "hub: {url: wss://hub.example/v1/tunnel}\n",
		"scope typo":                     "install: {allowed_scopes: [securty], default_scope: securty}\n",
		"app scope without app":          "install: {allowed_scopes: ['app:gitlab'], default_scope: 'app:gitlab'}\n",
		"allowed without default":        "install: {allowed_scopes: [security]}\n",
		"default not allowed":            "install: {allowed_scopes: [security], default_scope: all}\n",
		"auto without window":            "install: {allowed_scopes: [security], default_scope: security, auto: hub}\n",
		"auto unknown":                   "install: {auto: always}\n",
		"auto_scope unknown":             "install: {allowed_scopes: [security, all], default_scope: all, auto_scope: everything}\n",
		"auto_scope not allowed":         "install: {allowed_scopes: [security], default_scope: security, auto_scope: all}\n",
		"auto_scope an app":              "install: {allowed_scopes: [security, all], default_scope: all, auto_scope: 'app:a'}\n",
		"window bad day":                 "install: {window: {days: [monday], start: '02:00', end: '05:00'}}\n",
		"window bad time":                "install: {window: {days: [mon], start: '2 Uhr', end: '05:00'}}\n",
		"window empty":                   "install: {window: {days: [mon], start: '02:00', end: '02:00'}}\n",
		"interval out of range":          "install: {min_interval_h: 500}\n",
		"unknown install mode":           "install: {mode: dist-upgrade}\n",
		"reboot without window":          "reboot: {allowed: true}\n",
		"agent update without key":       "agent_update: {enabled: true}\n",
		"backup key not a key":           "agent_update: {enabled: true, public_key: KEY, backup_public_key: abc}\n",
		"app duplicate":                  "apps: [{id: a, name: A, source: apt-package, package: a}, {id: a, name: A, source: apt-package, package: b}]\n",
		"app unknown source":             "apps: [{id: a, name: A, source: snap, package: a}]\n",
		"app package with shell":         "apps: [{id: a, name: A, source: apt-package, package: 'a; rm -rf /'}]\n",
		"winget id with shell":           "apps: [{id: a, name: A, source: winget-package, package: 'jq; Remove-Item C:'}]\n",
		"winget id with quote":           "apps: [{id: a, name: A, source: winget-package, package: \"jq'x\"}]\n",
		"node with uppercase and spaces": "node: 'My Host'\n",
		"health two kinds in one":        "health: [{systemd: a.service, docker: b}]\n",
		"health none set":                "health: [{}]\n",
		"health unit with an option":     "health: [{systemd: '--now'}]\n",
		"health unit with shell":         "health: [{systemd: 'a.service; reboot'}]\n",
		"health container with space":    "health: [{docker: 'a b'}]\n",
		"health not http":                "health: [{http: 'file:///etc/passwd'}]\n",
		"package location id twice":      "packages: {locations: [{id: a, path: /opt/a}, {id: a, path: /opt/b}]}\n",
		"package location bad id":        "packages: {locations: [{id: 'My Venv', path: /opt/a}]}\n",
		"package location relative":      "packages: {locations: [{id: a, path: opt/a}]}\n",
		"package location bad pattern":   "packages: {locations: [{id: a, path: '/opt/[a'}]}\n",
		"packages typo":                  "packages: {inventroy: false}\n",
		"min age negative":               "packages: {min_age_days: -1}\n",
		"min age too long":               "packages: {min_age_days: 91}\n",
		"security age negative":          "packages: {min_age_days_security: -1}\n",
		"security age above min age":     "packages: {min_age_days: 7, min_age_days_security: 8}\n",
		"outdated too often":             "packages: {outdated_hours: 1}\n",
		"registry over http":             "packages: {registries: {pypi: 'http://pypi.example'}}\n",
		"pkg scope in install":           "install: {allowed_scopes: ['pkg:npm-global'], default_scope: 'pkg:npm-global'}\n",
		"update pattern with shell":      "packages: {update: ['npm-global; rm']}\n",
		"allow_scripts bad pattern":      "packages: {allow_scripts: ['[x']}\n",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			raw = strings.ReplaceAll(raw, "KEY", key(t))
			if _, issues := Parse([]byte(raw)); len(issues) == 0 {
				t.Fatalf("no issue for %q", raw)
			}
		})
	}
}

func TestAutoLocalIsRefused(t *testing.T) {
	// Designed, not built: a host with "local" would never install, and nothing would say so (ADR-0074).
	raw := "install: {allowed_scopes: [security], default_scope: security, auto: local, " +
		"window: {days: [mon], start: '02:00', end: '04:00'}}\n"
	if _, issues := Parse([]byte(raw)); len(issues) != 1 || issues[0].Field != "install.auto" {
		t.Fatalf("issues = %v, want exactly one on install.auto", issues)
	}
}

func TestAutoHubIsRefusedOnWindows(t *testing.T) {
	// Windows installs by button only (ADR-0086); the same file is fine on Linux.
	raw := "install: {allowed_scopes: [security], default_scope: security, auto: hub, " +
		"window: {days: [mon], start: '02:00', end: '04:00'}}\n"
	defer func(was string) { goos = was }(goos)
	for system, want := range map[string]int{"linux": 0, "windows": 1} {
		goos = system
		if _, issues := Parse([]byte(raw)); len(issues) != want || (want == 1 && issues[0].Field != "install.auto") {
			t.Errorf("%s: issues = %v, want %d on install.auto", system, issues, want)
		}
	}
	goos = "windows"
	if _, issues := Parse([]byte(strings.Replace(raw, "auto: hub", "auto: \"off\"", 1))); len(issues) != 0 {
		t.Errorf("windows, auto off: issues = %v", issues)
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, issues, found, err := Load(filepath.Join(t.TempDir(), "agent.yaml"))
	if found || err != nil || issues != nil {
		t.Fatalf("found=%v err=%v issues=%v", found, err, issues)
	}
}

func TestWindowContains(t *testing.T) {
	at := func(day, hhmm string) time.Time {
		// 2026-10-26 is a Monday.
		d := map[string]int{"mon": 26, "tue": 27, "wed": 28, "thu": 29, "fri": 30, "sat": 31, "sun": 1}[day]
		month := time.October
		if day == "sun" {
			month = time.November
		}
		h, _ := time.Parse("15:04", hhmm)
		return time.Date(2026, month, d, h.Hour(), h.Minute(), 0, 0, time.Local)
	}
	night := &Window{Days: []string{"mon"}, Start: "02:00", End: "05:00"}
	overMidnight := &Window{Days: []string{"sat"}, Start: "23:00", End: "02:00"}
	cases := []struct {
		w    *Window
		t    time.Time
		want bool
	}{
		{night, at("mon", "02:00"), true},
		{night, at("mon", "04:59"), true},
		{night, at("mon", "05:00"), false},
		{night, at("mon", "01:59"), false},
		{night, at("tue", "03:00"), false},
		{overMidnight, at("sat", "23:30"), true},
		{overMidnight, at("sun", "01:00"), true},
		{overMidnight, at("sun", "02:00"), false},
		{overMidnight, at("sat", "01:00"), false},
		{overMidnight, at("fri", "23:30"), false},
	}
	for _, c := range cases {
		if got := c.w.Contains(c.t); got != c.want {
			t.Errorf("%v.Contains(%s) = %v, want %v", c.w, c.t.Format("Mon 15:04"), got, c.want)
		}
	}
}
