package inventory

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/runner"
	"github.com/psewar/vordr/internal/versions"
)

// fakePM stands in for pip and npm: it answers a dry run with a given plan and an install by changing the files the
// inventory reads, the way the real ones would.
type fakePM struct {
	t        *testing.T
	site     string            // pip: site-packages
	nm       string            // npm: node_modules of the location
	plan     map[string]string // what the dry run resolves: name -> version (targets included)
	npmError string            // npm --json error code for the dry run
	install  bool              // install really changes the versions
	check    int               // pip check exit code
	calls    [][]string        // every command, as args
	lockOld  map[string]string // npm-dir: the lock before
	resolved map[string]string // npm: where the dry run says a planned name comes from (ADR-0128); none: no "resolved"
}

func (f *fakePM) Run(_ context.Context, c Command) runner.Result {
	f.calls = append(f.calls, c.Args)
	args := strings.Join(c.Args, " ")
	switch {
	case strings.Contains(args, "pip install --dry-run"):
		i := slices.Index(c.Args, "--report")
		var rep struct {
			Install []map[string]map[string]string `json:"install"`
		}
		for n, v := range f.plan {
			rep.Install = append(rep.Install, map[string]map[string]string{"metadata": {"name": n, "version": v}})
		}
		raw, _ := json.Marshal(rep)
		_ = os.WriteFile(c.Args[i+1], raw, 0o644)
	case strings.Contains(args, "pip install"):
		if f.install {
			for _, a := range c.Args {
				if n, v, ok := strings.Cut(a, "=="); ok {
					matches, _ := filepath.Glob(filepath.Join(f.site, n+"-*.dist-info"))
					for _, m := range matches {
						_ = os.RemoveAll(m)
					}
					distInfo(f.t, f.site, n, v)
				}
			}
		}
	case strings.Contains(args, "pip check"):
		return runner.Result{ExitCode: f.check, Stdout: []byte("broken requirement")}
	case strings.Contains(args, "--package-lock-only"):
		if f.npmError != "" {
			return runner.Result{ExitCode: 1, Stdout: []byte(`{"error":{"code":"` + f.npmError + `","summary":"x"}}`)}
		}
		pk := map[string]any{"": map[string]string{"name": "vordr-plan"}}
		for n, v := range f.plan {
			e := map[string]string{"version": v}
			if r := f.resolved[n]; r != "" {
				e["resolved"] = r
			}
			pk["node_modules/"+n] = e
		}
		for n, v := range f.lockOld {
			if _, ok := f.plan[n]; !ok {
				pk["node_modules/"+n] = map[string]string{"version": v}
			}
		}
		raw, _ := json.Marshal(map[string]any{"lockfileVersion": 3, "packages": pk})
		_ = os.WriteFile(filepath.Join(c.Dir, "package-lock.json"), raw, 0o644)
	case strings.HasPrefix(args, "install"):
		if f.install {
			for _, a := range c.Args {
				if i := strings.LastIndex(a, "@"); i > 0 && !strings.HasPrefix(a, "-") {
					npmPkg(f.t, filepath.Join(f.nm, a[:i]), a[:i], a[i+1:])
				}
			}
		}
	}
	return runner.Result{}
}

func (f *fakePM) last(prefix string) []string {
	for i := len(f.calls) - 1; i >= 0; i-- {
		if strings.HasPrefix(strings.Join(f.calls[i], " "), prefix) || strings.Contains(strings.Join(f.calls[i], " "), prefix) {
			return f.calls[i]
		}
	}
	return nil
}

// vetAll answers like the hub: covers the plan, flags what flagged names.
func vetAll(flagged map[string][]string) Vetter {
	return func(_ context.Context, _ versions.Ecosystem, plan []protocol.VetPackage) (protocol.Vet, bool) {
		v := protocol.Vet{}
		for _, p := range plan {
			p.Flagged = flagged[p.Name+" "+p.Version]
			v.Packages = append(v.Packages, p)
		}
		return v, true
	}
}

func venvLocation(t *testing.T) (Location, string) {
	venv := filepath.Join(t.TempDir(), "venv")
	write(t, filepath.Join(venv, "pyvenv.cfg"), "")
	write(t, filepath.Join(venv, "bin", "python"), "")
	site := filepath.Join(venv, "lib", "python3.11", "site-packages")
	distInfo(t, site, "requests", "2.31.0", "urllib3")
	distInfo(t, site, "urllib3", "2.2.2")
	return read("services-app", KindVenv, venv), site
}

func newUpdater(t *testing.T, pm *fakePM, vet Vetter) *Updater {
	_, reg := newFakeRegistry(t)
	return &Updater{Exec: pm, Reg: reg, Now: func() time.Time { return now }, MinAge: 7 * 24 * time.Hour, Vet: vet,
		TempDir: t.TempDir()}
}

func TestPipUpdate(t *testing.T) {
	journal := 0
	before := func() error { journal++; return nil }
	for _, c := range []struct {
		name     string
		plan     map[string]string
		flagged  map[string][]string
		vet      Vetter
		install  bool
		check    int
		sdist    bool
		want     protocol.Code
		journal  int
		installs bool
	}{
		// requests 2.31.0 -> candidate 2.32.3; pip pulls urllib3 2.5.0 along (60 days old).
		{name: "clean", plan: map[string]string{"requests": "2.32.3", "urllib3": "2.5.0"}, install: true,
			want: protocol.CodeOK, journal: 1, installs: true},
		// urllib3 2.6.0 got a new file yesterday: too young, though the release itself is older.
		{name: "dependency too new", plan: map[string]string{"requests": "2.32.3", "urllib3": "2.6.0"},
			want: protocol.CodePrecheckVersionTooNew},
		{name: "flagged dependency", plan: map[string]string{"requests": "2.32.3", "urllib3": "2.5.0"},
			flagged: map[string][]string{"urllib3 2.5.0": {"GHSA-xxxx"}}, want: protocol.CodePrecheckVersionFlagged},
		{name: "no answer from the hub", plan: map[string]string{"requests": "2.32.3"},
			vet: func(context.Context, versions.Ecosystem, []protocol.VetPackage) (protocol.Vet, bool) {
				return protocol.Vet{}, false
			},
			want: protocol.CodePrecheckVettingUnavailable},
		// "unavailable" counts even when the entries look clean: data the hub calls stale vouches for nothing.
		{name: "hub without data", plan: map[string]string{"requests": "2.32.3"},
			vet: func(_ context.Context, _ versions.Ecosystem, plan []protocol.VetPackage) (protocol.Vet, bool) {
				return protocol.Vet{Unavailable: "stale", Packages: plan}, true
			}, want: protocol.CodePrecheckVettingUnavailable},
		{name: "answer leaves an entry out", plan: map[string]string{"requests": "2.32.3", "urllib3": "2.5.0"},
			vet: func(_ context.Context, _ versions.Ecosystem, plan []protocol.VetPackage) (protocol.Vet, bool) {
				return protocol.Vet{Packages: plan[:1]}, true
			}, want: protocol.CodePrecheckVettingUnavailable},
		{name: "pip did not install", plan: map[string]string{"requests": "2.32.3"}, want: protocol.CodeFailedVerification,
			journal: 1, installs: true},
		{name: "pip check fails", plan: map[string]string{"requests": "2.32.3"}, install: true, check: 1,
			want: protocol.CodeFailedVerification, journal: 1, installs: true},
		{name: "sdist allowed", plan: map[string]string{"requests": "2.32.3"}, install: true, sdist: true,
			want: protocol.CodeOK, journal: 1, installs: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			journal = 0
			loc, site := venvLocation(t)
			pm := &fakePM{t: t, site: site, plan: c.plan, install: c.install, check: c.check}
			vet := c.vet
			if vet == nil {
				vet = vetAll(c.flagged)
			}
			u := newUpdater(t, pm, vet)
			u.Sdist = c.sdist
			res := u.Update(context.Background(), loc, "", before)
			if res.Code != c.want || journal != c.journal {
				t.Fatalf("code %s (want %s), journal %d: %s", res.Code, c.want, journal, res.Detail)
			}
			inst := pm.last("-m pip install --no-deps")
			if (inst != nil) != c.installs {
				t.Fatalf("install ran: %v", inst)
			}
			if inst == nil {
				return
			}
			// Exactly the vetted plan, pinned, nothing resolved anew; wheels only unless allowed.
			var pinned []string
			for _, a := range inst {
				if strings.Contains(a, "==") {
					pinned = append(pinned, a)
				}
			}
			slices.Sort(pinned)
			var want []string
			for n, v := range c.plan {
				want = append(want, n+"=="+v)
			}
			slices.Sort(want)
			if !slices.Equal(pinned, want) || slices.Contains(inst, "--only-binary=:all:") == c.sdist {
				t.Errorf("install args %v", inst)
			}
			if c.want == protocol.CodeOK && !slices.Equal(res.Changed, []string{"requests 2.31.0 -> 2.32.3"}) {
				t.Errorf("changed %v", res.Changed)
			}
		})
	}
}

// A fix the hub named takes its target at the shorter age (ADR-0111): requests 2.32.4 is two days old, the normal
// candidate 2.32.3 older than a week. Everything else in the plan keeps the minimum age, and without a shorter age in
// the agent's policy - or without the hub naming it - the normal candidate stays.
func TestPipSecurityFix(t *testing.T) {
	day := 24 * time.Hour
	for _, c := range []struct {
		name        string
		securityAge time.Duration
		fix         []string
		plan        map[string]string
		target      string
		want        protocol.Code
	}{
		{"named fix", 2 * day, []string{"requests"}, map[string]string{"requests": "2.32.4", "urllib3": "2.5.0"},
			"requests==2.32.4", protocol.CodeOK},
		{"named fix with a young dependency", 2 * day, []string{"requests"},
			map[string]string{"requests": "2.32.4", "urllib3": "2.6.0"}, "requests==2.32.4", protocol.CodePrecheckVersionTooNew},
		{"not named", 2 * day, nil, map[string]string{"requests": "2.32.3", "urllib3": "2.5.0"}, "requests==2.32.3",
			protocol.CodeOK},
		{"named, another package", 2 * day, []string{"urllib3"}, map[string]string{"requests": "2.32.3"},
			"requests==2.32.3", protocol.CodeOK},
		{"no shorter age in the policy", 0, []string{"requests"}, map[string]string{"requests": "2.32.3"},
			"requests==2.32.3", protocol.CodeOK},
	} {
		t.Run(c.name, func(t *testing.T) {
			loc, site := venvLocation(t)
			pm := &fakePM{t: t, site: site, plan: c.plan, install: true}
			u := newUpdater(t, pm, vetAll(nil))
			u.SecurityAge, u.SecurityFix = c.securityAge, c.fix
			res := u.Update(context.Background(), loc, "", func() error { return nil })
			if res.Code != c.want {
				t.Fatalf("code %s, want %s: %s", res.Code, c.want, res.Detail)
			}
			if dry := pm.last("pip install --dry-run"); !slices.Contains(dry, c.target) {
				t.Fatalf("dry run %v, want %s", dry, c.target)
			}
			if c.want == protocol.CodePrecheckVersionTooNew && !strings.Contains(res.Detail, "urllib3 2.6.0") {
				t.Errorf("refusal names %q", res.Detail)
			}
		})
	}
}

func TestUpdateRefusals(t *testing.T) {
	loc, site := venvLocation(t)
	pm := &fakePM{t: t, site: site}
	u := newUpdater(t, pm, vetAll(nil))
	never := func() error { t.Fatal("journal started"); return nil }
	if res := u.Update(context.Background(), loc, "flask", never); res.Code != protocol.CodeRejectedScopeNotAllowed {
		t.Errorf("unknown package: %s", res.Code)
	}
	if res := u.Update(context.Background(), loc, "urllib3", never); res.Code != protocol.CodeRejectedScopeNotAllowed {
		t.Errorf("a dependency is not updated on its own: %s", res.Code)
	}
	site2 := loc
	site2.Kind = KindPipSite
	if res := u.Update(context.Background(), site2, "", never); res.Code != protocol.CodeRejectedUnsupported {
		t.Errorf("pip-site: %s", res.Code)
	}
	// requests 2.32.3 installed: 2.32.4 exists but is two days old - waits, nothing runs.
	distInfo(t, site, "requests", "2.32.3")
	_ = os.RemoveAll(filepath.Join(site, "requests-2.31.0.dist-info"))
	res := u.Update(context.Background(), read(loc.ID, loc.Kind, loc.Path), "requests", never)
	if res.Code != protocol.CodePrecheckVersionTooNew || !strings.Contains(res.Detail, "2.32.4") || len(pm.calls) != 0 {
		t.Errorf("too new: %s %q, calls %v", res.Code, res.Detail, pm.calls)
	}
}

func npmGlobal(t *testing.T) (Location, string) {
	nm := filepath.Join(t.TempDir(), "usr", "lib", "node_modules")
	npmPkg(t, filepath.Join(nm, "@example", "nodes"), "@example/nodes", "4.1.0")
	npmPkg(t, filepath.Join(nm, "chalk"), "chalk", "5.6.0")
	return read("npm-global", KindNpmGlobal, nm), nm
}

func TestNpmUpdate(t *testing.T) {
	NpmCommand = func() (string, []string, bool) { return "npm", nil, true }
	defer func() { NpmCommand = npmCommand }()
	plan := map[string]string{"@example/nodes": "5.0.1", "debug": "4.4.2"}

	// debug 4.4.2 in the tree is malware (8 September 2025): the hub flags it, nothing is installed.
	loc, nm := npmGlobal(t)
	pm := &fakePM{t: t, nm: nm, plan: plan, install: true}
	u := newUpdater(t, pm, vetAll(map[string][]string{"debug 4.4.2": {"MAL-2025-46974"}}))
	res := u.Update(context.Background(), loc, "@example/nodes", func() error { t.Fatal("journal"); return nil })
	if res.Code != protocol.CodePrecheckVersionFlagged || !strings.Contains(res.Detail, "MAL-2025-46974") {
		t.Fatalf("flagged: %s %s", res.Code, res.Detail)
	}
	dry := pm.last("--package-lock-only")
	cutoff := now.Add(-7 * 24 * time.Hour).Format(time.RFC3339)
	if !slices.Contains(dry, "--ignore-scripts") || dry[slices.Index(dry, "--before")+1] != cutoff {
		t.Errorf("dry run %v", dry)
	}

	// Clean: installed globally with the same --before, without scripts.
	loc, nm = npmGlobal(t)
	pm = &fakePM{t: t, nm: nm, plan: plan, install: true}
	u = newUpdater(t, pm, vetAll(nil))
	if res := u.Update(context.Background(), loc, "", func() error { return nil }); res.Code != protocol.CodeOK ||
		!slices.Equal(res.Changed, []string{"@example/nodes 4.1.0 -> 5.0.1"}) {
		t.Fatalf("clean: %s %s %v", res.Code, res.Detail, res.Changed)
	}
	inst := pm.last("install --global")
	prefix := filepath.Dir(filepath.Dir(nm))
	if inst[slices.Index(inst, "--prefix")+1] != prefix || !slices.Contains(inst, "--ignore-scripts") ||
		inst[slices.Index(inst, "--before")+1] != cutoff || !slices.Contains(inst, "@example/nodes@5.0.1") ||
		slices.Contains(inst, "chalk@5.6.2") {
		t.Errorf("install %v", inst)
	}

	// A version the location already has is no new code (ADR-0105): npm resolves the whole tree anew, but the plan
	// leaves out what is installed, even what OSV flags - it is there either way. What is new is still vetted.
	loc, nm = npmGlobal(t)
	pm = &fakePM{t: t, nm: nm, plan: map[string]string{"@example/nodes": "5.0.1", "chalk": "5.6.0"}, install: true}
	var vetted []protocol.VetPackage
	u = newUpdater(t, pm, func(ctx context.Context, eco versions.Ecosystem, entries []protocol.VetPackage) (protocol.Vet, bool) {
		vetted = entries
		return vetAll(map[string][]string{"chalk 5.6.0": {"GHSA-chalk"}})(ctx, eco, entries)
	})
	if res := u.Update(context.Background(), loc, "@example/nodes", func() error { return nil }); res.Code != protocol.CodeOK ||
		len(vetted) != 1 || vetted[0].Name != "@example/nodes" || vetted[0].Version != "5.0.1" {
		t.Fatalf("installed version in the plan: %s %s, vetted %v", res.Code, res.Detail, vetted)
	}

	// Scripts allowed for the location: no --ignore-scripts at install (the dry run never runs them).
	loc, nm = npmGlobal(t)
	pm = &fakePM{t: t, nm: nm, plan: plan, install: true}
	u = newUpdater(t, pm, vetAll(nil))
	u.Scripts = true
	u.Update(context.Background(), loc, "@example/nodes", func() error { return nil })
	if slices.Contains(pm.last("install --global"), "--ignore-scripts") || !slices.Contains(pm.last("--package-lock-only"), "--ignore-scripts") {
		t.Errorf("scripts: %v", pm.calls)
	}

	// No release of a dependency old enough before --before: npm says ETARGET (as data), the update waits.
	loc, nm = npmGlobal(t)
	pm = &fakePM{t: t, nm: nm, plan: plan, npmError: "ETARGET"}
	u = newUpdater(t, pm, vetAll(nil))
	if res := u.Update(context.Background(), loc, "@example/nodes", func() error { return nil }); res.Code != protocol.CodePrecheckVersionTooNew {
		t.Errorf("ETARGET: %s", res.Code)
	}
	pm.npmError = "E404"
	if res := u.Update(context.Background(), loc, "@example/nodes", func() error { return nil }); res.Code != protocol.CodeFailedPackageManager {
		t.Errorf("other npm error: %s", res.Code)
	}
}

// npm and a fix the hub named (ADR-0111): --before moves to the shorter age, so that chalk 5.6.2 (three days old) can
// be resolved, and the resolved plan is checked: a dependency younger than the minimum age stops the run.
func TestNpmSecurityFix(t *testing.T) {
	NpmCommand = func() (string, []string, bool) { return "npm", nil, true }
	defer func() { NpmCommand = npmCommand }()
	day := 24 * time.Hour
	for _, c := range []struct {
		name string
		plan map[string]string
		want protocol.Code
	}{
		{"fix alone", map[string]string{"chalk": "5.6.2"}, protocol.CodeOK},
		{"fix with an old dependency", map[string]string{"chalk": "5.6.2", "ansi-styles": "6.2.3"}, protocol.CodeOK},
		{"fix with a young dependency", map[string]string{"chalk": "5.6.2", "ansi-styles": "6.2.4"},
			protocol.CodePrecheckVersionTooNew},
	} {
		t.Run(c.name, func(t *testing.T) {
			loc, nm := npmGlobal(t)
			pm := &fakePM{t: t, nm: nm, plan: c.plan, install: true}
			u := newUpdater(t, pm, vetAll(nil))
			u.SecurityAge, u.SecurityFix = 2*day, []string{"chalk"}
			res := u.Update(context.Background(), loc, "chalk", func() error { return nil })
			if res.Code != c.want {
				t.Fatalf("code %s, want %s: %s", res.Code, c.want, res.Detail)
			}
			dry := pm.last("--package-lock-only")
			if dry[slices.Index(dry, "--before")+1] != now.Add(-2*day).Format(time.RFC3339) {
				t.Errorf("dry run %v", dry)
			}
			if c.want == protocol.CodeOK && !slices.Contains(pm.last("install --global"), "chalk@5.6.2") {
				t.Errorf("install %v", pm.last("install --global"))
			}
			if c.want != protocol.CodeOK && !strings.Contains(res.Detail, "ansi-styles 6.2.4") {
				t.Errorf("refusal names %q", res.Detail)
			}
		})
	}
	// Not named: chalk 5.6.2 is too new, and --before stays at the minimum age.
	loc, nm := npmGlobal(t)
	pm := &fakePM{t: t, nm: nm, plan: map[string]string{"chalk": "5.6.2"}, install: true}
	u := newUpdater(t, pm, vetAll(nil))
	u.SecurityAge = 2 * day
	if res := u.Update(context.Background(), loc, "chalk", func() error { return nil }); res.Code != protocol.CodePrecheckVersionTooNew {
		t.Fatalf("not named: %s %s", res.Code, res.Detail)
	}
}

// An npm folder with a lock file: only what the update changes is the plan; the rest was vetted before (11.2).
func TestNpmDirPlanIsTheDifference(t *testing.T) {
	NpmCommand = func() (string, []string, bool) { return "npm", nil, true }
	defer func() { NpmCommand = npmCommand }()
	dir := filepath.Join(t.TempDir(), ".node-red")
	write(t, filepath.Join(dir, "package.json"), `{"dependencies":{"@example/nodes":"^4"}}`)
	npmPkg(t, filepath.Join(dir, "node_modules", "@example", "nodes"), "@example/nodes", "4.1.0")
	old := map[string]string{"@example/nodes": "4.1.0", "ms": "2.1.3"}
	pk := map[string]any{}
	for n, v := range old {
		pk["node_modules/"+n] = map[string]string{"version": v}
	}
	// example-dep 0.9.0 sits nested before and hoisted after the update: moved, not new - not part of the plan.
	pk["node_modules/@example/nodes/node_modules/example-dep"] = map[string]string{"version": "0.9.0"}
	old["example-dep"] = "0.9.0"
	raw, _ := json.Marshal(map[string]any{"packages": pk})
	write(t, filepath.Join(dir, "package-lock.json"), string(raw))
	pm := &fakePM{t: t, nm: filepath.Join(dir, "node_modules"), plan: map[string]string{"@example/nodes": "5.0.1"},
		lockOld: old, install: true}
	var vetted []protocol.VetPackage
	u := newUpdater(t, pm, func(ctx context.Context, eco versions.Ecosystem, plan []protocol.VetPackage) (protocol.Vet, bool) {
		vetted = plan
		return vetAll(nil)(ctx, eco, plan)
	})
	if res := u.Update(context.Background(), read("node-red", KindNpmDir, dir), "", func() error { return nil }); res.Code != protocol.CodeOK {
		t.Fatalf("%s %s", res.Code, res.Detail)
	}
	if len(vetted) != 1 || vetted[0].Name != "@example/nodes" || vetted[0].Version != "5.0.1" {
		t.Errorf("vetted %v", vetted)
	}
	if inst := pm.last("install --prefix"); inst[slices.Index(inst, "--prefix")+1] != dir {
		t.Errorf("install %v", inst)
	}
}
