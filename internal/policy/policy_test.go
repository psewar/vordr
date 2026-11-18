package policy

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"slices"
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
		"window_only without window":     "reboot: {window_only: true}\n",
		"idle check relative":            "reboot: {idle_check: [vordr-idle]}\n",
		"idle check missing":             "reboot: {idle_check: [/nonexistent/vordr-idle]}\n",
		"idle check empty entry":         "reboot: {idle_check: [/nonexistent/vordr-idle, '']}\n",
		"idle check too long":            "reboot: {idle_check: [/a, b, c, d, e, f, g, h, i, j, k, l, m, n, o, p, q]}\n",
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

// A reboot kept inside its window and held by an idle check (ADR-0154, ADR-0155): the command is a file that is there
// and executable, taken with its arguments as they stand.
func TestRebootWindowOnlyAndIdleCheck(t *testing.T) {
	dir := t.TempDir()
	idle := filepath.Join(dir, "vordr-idle")
	if err := os.WriteFile(idle, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	raw := "reboot: {allowed: true, window: {days: [sun], start: '03:00', end: '04:00'}, window_only: true, " +
		"idle_check: ['" + idle + "', --quiet]}\n"
	cfg, issues := Parse([]byte(raw))
	if len(issues) > 0 {
		t.Fatalf("issues: %v", issues)
	}
	if !cfg.Reboot.WindowOnly || len(cfg.Reboot.IdleCheck) != 2 || cfg.Reboot.IdleCheck[1] != "--quiet" {
		t.Fatalf("reboot: %+v", cfg.Reboot)
	}
	if goos == "windows" {
		return // no execute bit to take away
	}
	plain := filepath.Join(dir, "not-executable")
	if err := os.WriteFile(plain, []byte("exit 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, issues := Parse([]byte("reboot: {idle_check: ['" + plain + "']}\n")); len(issues) != 1 ||
		issues[0].Field != "reboot.idle_check" {
		t.Fatalf("not executable: issues = %v", issues)
	}
	if _, issues := Parse([]byte("reboot: {idle_check: ['" + dir + "']}\n")); len(issues) != 1 {
		t.Fatalf("a folder: issues = %v", issues)
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

// When a closed window opens next (ADR-0187): later the same day, on the next of its days, a week later, past midnight,
// in the host's own zone, and where daylight saving time skips or repeats the start - always the first minute the
// agent's own Contains says yes, never earlier.
func TestWindowOpens(t *testing.T) {
	zone := func(name string) *time.Location {
		loc, err := time.LoadLocation(name)
		if err != nil {
			t.Fatal(err)
		}
		return loc
	}
	paris, newYork := zone("Europe/Paris"), zone("America/New_York")
	utc := func(month time.Month, day, h, m int) time.Time {
		return time.Date(2026, month, day, h, m, 0, 0, time.UTC)
	}
	every := []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}
	workdays := &Window{Days: []string{"mon", "tue", "wed", "thu", "fri"}, Start: "06:00", End: "16:00"}
	monday := &Window{Days: []string{"mon"}, Start: "02:00", End: "05:00"}
	overMidnight := &Window{Days: []string{"sat"}, Start: "23:00", End: "02:00"}
	// 2026-11-16 is a Monday. In Paris, summer time of 2026 begins at 01:00 UTC of 29 March (02:00 becomes 03:00) and
	// ends at 01:00 UTC of 25 October (03:00 becomes 02:00).
	at := func(loc *time.Location, month time.Month, day, h, m int) time.Time {
		return time.Date(2026, month, day, h, m, 0, 0, loc)
	}
	cases := []struct {
		name string
		w    *Window
		t    time.Time
		want time.Time
	}{
		{"later the same day", workdays, at(paris, 11, 16, 5, 59), at(paris, 11, 16, 6, 0)},
		{"seconds before the start", workdays, at(paris, 11, 16, 5, 59).Add(30 * time.Second), at(paris, 11, 16, 6, 0)},
		{"the next morning", workdays, at(paris, 11, 16, 20, 0), at(paris, 11, 17, 6, 0)},
		{"after friday comes monday", workdays, at(paris, 11, 20, 16, 0), at(paris, 11, 23, 6, 0)},
		{"a week later", monday, at(paris, 11, 16, 5, 0), at(paris, 11, 23, 2, 0)},
		{"over midnight, before it", overMidnight, at(paris, 11, 21, 22, 0), at(paris, 11, 21, 23, 0)},
		{"over midnight, after it", overMidnight, at(paris, 11, 22, 2, 0), at(paris, 11, 28, 23, 0)},
		{"in the host's zone", workdays, at(newYork, 11, 20, 17, 0), utc(11, 23, 11, 0)},
		// The clock jumps from 02:00 to 03:00: a start at 02:30 never shows, the window is open from 03:00 on.
		{"start skipped", &Window{Days: every, Start: "02:30", End: "04:00"}, utc(3, 29, 0, 0).In(paris),
			utc(3, 29, 1, 0)},
		// The clock shows 02:00 to 03:00 twice: the window opens at the first 02:30, and again at the second.
		{"start repeated, first", &Window{Days: every, Start: "02:30", End: "02:45"}, utc(10, 25, 0, 0).In(paris),
			utc(10, 25, 0, 30)},
		{"start repeated, second", &Window{Days: every, Start: "02:30", End: "02:45"}, utc(10, 25, 0, 50).In(paris),
			utc(10, 25, 1, 30)},
		// Over midnight, ending in the repeated hour: after the first 02:30 the window is closed, and at the second 02:00
		// it is open again, still as the night of the day before.
		{"back over midnight", &Window{Days: every, Start: "23:00", End: "02:30"}, utc(10, 25, 0, 40).In(paris),
			utc(10, 25, 1, 0)},
		{"back over midnight, one night", &Window{Days: []string{"sat"}, Start: "23:00", End: "02:30"},
			utc(10, 25, 0, 40).In(paris), utc(10, 25, 1, 0)},
		{"back over midnight in New York", &Window{Days: every, Start: "23:00", End: "01:30"},
			utc(11, 1, 5, 40).In(newYork), utc(11, 1, 6, 0)},
		{"back over midnight in New York, one night", &Window{Days: []string{"sat"}, Start: "23:00", End: "01:30"},
			utc(11, 1, 5, 40).In(newYork), utc(11, 1, 6, 0)},
		// A weekly window inside the hour the change to summer time skips: that week it never opens, the next it does.
		{"a week skipped", &Window{Days: []string{"sun"}, Start: "02:00", End: "02:30"}, at(paris, 3, 23, 12, 0),
			utc(4, 5, 0, 0)},
		{"a week skipped in New York", &Window{Days: []string{"sun"}, Start: "02:00", End: "02:30"},
			at(newYork, 3, 2, 12, 0), utc(3, 15, 6, 0)},
	}
	for _, c := range cases {
		if c.w.Contains(c.t) {
			t.Fatalf("%s: the window is open at %s", c.name, c.t)
		}
		got, ok := c.w.Opens(c.t)
		if !ok || !got.Equal(c.want) {
			t.Errorf("%s: opens %s %v, want %s", c.name, got, ok, c.want)
			continue
		}
		before := got.Add(-time.Minute)
		if !c.w.Contains(got) || (before.After(c.t) && c.w.Contains(before)) {
			t.Errorf("%s: %s is not the first open minute", c.name, got)
		}
	}
	if _, ok := (&Window{Days: []string{"someday"}, Start: "02:00", End: "03:00"}).Opens(at(paris, 11, 16, 0, 0)); ok {
		t.Fatal("a window without a valid day opens")
	}
}

// Opens is the first whole minute after t at which the window says yes, by construction (ADR-0187). Checked against a
// minute-by-minute search with a reading of the window of its own: around each change of daylight saving time of 2026
// and once a week through the year, in zones that change forward and back, by an hour and by half an hour, north and
// south, for windows over midnight, of one night, and inside the hour a change skips or repeats.
func TestWindowOpensIsTheFirstMinuteTheWindowSaysYes(t *testing.T) {
	every := []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}
	windows := []*Window{
		{Days: every, Start: "23:00", End: "02:30"},
		{Days: []string{"sat"}, Start: "23:00", End: "02:30"},
		{Days: []string{"sun"}, Start: "02:00", End: "02:30"},
		{Days: every, Start: "02:30", End: "02:45"},
		{Days: []string{"mon", "tue", "wed", "thu", "fri"}, Start: "06:00", End: "16:00"},
	}
	// The window as its definition reads, looked up anew for each minute.
	says := func(w *Window, u time.Time) bool {
		s, _ := minutes(w.Start)
		e, _ := minutes(w.End)
		m := u.Hour()*60 + u.Minute()
		on := func(d time.Weekday) bool { return slices.Contains(w.Days, dayNames[d]) }
		if s < e {
			return on(u.Weekday()) && s <= m && m < e
		}
		return (on(u.Weekday()) && m >= s) || (on((u.Weekday()+6)%7) && m < e)
	}
	year := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, name := range []string{"Europe/Paris", "America/New_York", "Australia/Sydney", "Australia/Lord_Howe"} {
		loc, err := time.LoadLocation(name)
		if err != nil {
			t.Fatal(err)
		}
		var probes []time.Time
		changes := 0
		for u := year; u.Year() == 2026; u = u.Add(time.Hour) {
			_, before := u.In(loc).Zone()
			_, after := u.Add(time.Hour).In(loc).Zone()
			if before != after {
				changes++
				for p := u.Add(-36 * time.Hour); p.Before(u.Add(36 * time.Hour)); p = p.Add(20 * time.Minute) {
					probes = append(probes, p.In(loc))
				}
			}
		}
		if changes != 2 {
			t.Fatalf("%s: %d changes of daylight saving time in 2026", name, changes)
		}
		for p := year.Add(5*time.Hour + 7*time.Minute); p.Year() == 2026; p = p.Add(7*24*time.Hour + 61*time.Minute) {
			probes = append(probes, p.In(loc))
		}
		slices.SortFunc(probes, func(a, b time.Time) int { return a.Compare(b) })
		for _, w := range windows {
			// The first minute the window says yes after each probe, found by walking forward; probes come in order.
			var next time.Time
			for _, p := range probes {
				if from := p.Truncate(time.Minute).Add(time.Minute); next.Before(from) {
					for next = from; !says(w, next); next = next.Add(time.Minute) {
						if next.Sub(from) > opensWithin {
							t.Fatalf("%s %v: never opens after %s", name, w, p)
						}
					}
				}
				got, ok := w.Opens(p)
				if !ok || !got.Equal(next) || got.Location() != loc {
					t.Fatalf("%s %v at %s: opens %s %v, the window first says yes at %s", name, w,
						p.Format("Mon 2006-01-02 15:04 MST"), got.Format("Mon 01-02 15:04 MST"), ok,
						next.Format("Mon 01-02 15:04 MST"))
				}
				if w.Contains(p) != says(w, p) {
					t.Fatalf("%s %v: Contains at %s differs from the definition", name, w, p)
				}
			}
		}
	}
}
