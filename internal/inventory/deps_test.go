package inventory

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/runner"
	"github.com/psewar/vordr/internal/versions"
)

// depsPM stands in for npm in an npm folder with a lock file: "update <names> --package-lock-only" writes the lock with
// those names at the releases their dependents allow (upgrades), plus what such an update brings along (extra);
// "update <names>" without it installs the same into node_modules.
type depsPM struct {
	t        *testing.T
	dir      string
	lock     map[string]string            // the installed lock: name -> version
	upgrades map[string]string            // the newest release the dependents allow, where newer
	extra    map[string]map[string]string // name -> what updating it brings along
	deps     map[string][]string          // who depends on what, as the lock says it
	skip     string                       // the install leaves this one out (npm resolved otherwise in place)
	calls    [][]string
}

// namesIn: the package names an npm command line carries (not options, not their values).
func namesIn(args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--prefix" || args[i] == "--registry" || args[i] == "--before":
			i++
		case strings.HasPrefix(args[i], "-") || args[i] == "update":
		default:
			out = append(out, args[i])
		}
	}
	return out
}

func (f *depsPM) resolved(names []string) map[string]string {
	out := map[string]string{}
	for n, v := range f.lock {
		out[n] = v
	}
	for _, n := range names {
		if v, ok := f.upgrades[n]; ok {
			out[n] = v
			for xn, xv := range f.extra[n] {
				out[xn] = xv
			}
		}
	}
	return out
}

func (f *depsPM) Run(_ context.Context, c Command) runner.Result {
	f.calls = append(f.calls, c.Args)
	names := namesIn(c.Args)
	got := f.resolved(names)
	if slices.Contains(c.Args, "--package-lock-only") {
		writeLock(f.t, filepath.Join(c.Dir, "package-lock.json"), got, f.deps)
		return runner.Result{}
	}
	for n, v := range got {
		if f.lock[n] != v && n != f.skip {
			npmPkg(f.t, filepath.Join(f.dir, "node_modules", n), n, v)
		}
	}
	return runner.Result{}
}

// writeLock writes a lock file with these packages; deps: name -> the names it depends on.
func writeLock(t *testing.T, path string, pkgs map[string]string, deps map[string][]string) {
	t.Helper()
	pk := map[string]any{"": map[string]any{"name": "node-red-project"}}
	for n, v := range pkgs {
		e := map[string]any{"version": v}
		if d := deps[n]; len(d) > 0 {
			m := map[string]string{}
			for _, x := range d {
				m[x] = "*"
			}
			e["dependencies"] = m
		}
		pk["node_modules/"+n] = e
	}
	raw, _ := json.Marshal(map[string]any{"lockfileVersion": 3, "packages": pk})
	write(t, path, string(raw))
}

// depsFolder: a Node-RED folder with one node of its own and four dependencies, three of them with findings.
func depsFolder(t *testing.T) (Location, *depsPM) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".node-red")
	write(t, filepath.Join(dir, "package.json"), `{"name":"node-red-project","dependencies":{"node-red-contrib-x":"^1"}}`)
	lock := map[string]string{"node-red-contrib-x": "1.0.0", "pkg-a": "2.0.7", "lib-c": "2.1.0", "lib-b": "1.3.3",
		"ms": "2.1.3"}
	for n, v := range lock {
		npmPkg(t, filepath.Join(dir, "node_modules", n), n, v)
	}
	deps := map[string][]string{"node-red-contrib-x": {"lib-c", "lib-b"}}
	writeLock(t, filepath.Join(dir, "package-lock.json"), lock, deps)
	pm := &depsPM{t: t, dir: dir, lock: lock, deps: deps,
		upgrades: map[string]string{"pkg-a": "2.0.8", "lib-b": "1.3.8"}} // lib-c: pinned by its dependent
	return read("node-red", KindNpmDir, dir), pm
}

// Findings in installed dependencies, as the hub's OSV data has them; lib-b's newest allowed release is flagged too.
var depsFlagged = map[string][]string{"pkg-a 2.0.7": {"GHSA-a"}, "lib-c 2.1.0": {"GHSA-c"},
	"lib-b 1.3.3": {"GHSA-b"}, "lib-b 1.3.8": {"GHSA-b"}}

func depsUpdater(pm CommandRunner, vet Vetter) *Updater {
	return &Updater{Exec: pm, Now: func() time.Time { return now }, MinAge: 7 * 24 * time.Hour, Vet: vet,
		Reg: Registries{Npm: "https://registry.npmjs.org"}}
}

// ADR-0104: the hub names the dependencies with findings; each moves to the newest release its dependents allow. One
// whose newest allowed release is flagged too stays, and the plan is made again without it; one that no release moves
// is named with what pins it. Nothing runs install scripts, and only the dependencies with findings are touched.
func TestRefreshDepsMovesOnlyWhatHasFindings(t *testing.T) {
	NpmCommand = func() (string, []string, bool) { return "npm", nil, true }
	defer func() { NpmCommand = npmCommand }()
	loc, pm := depsFolder(t)
	var asked [][]protocol.VetPackage
	vet := func(ctx context.Context, eco versions.Ecosystem, entries []protocol.VetPackage) (protocol.Vet, bool) {
		asked = append(asked, entries)
		return vetAll(depsFlagged)(ctx, eco, entries)
	}
	journal := 0
	res := depsUpdater(pm, vet).RefreshDeps(context.Background(), loc, func() error { journal++; return nil })
	if res.Code != protocol.CodeOK || !slices.Equal(res.Changed, []string{"pkg-a 2.0.7 -> 2.0.8"}) || journal != 1 {
		t.Fatalf("%s %q changed %q journal %d", res.Code, res.Detail, res.Changed, journal)
	}
	if !strings.Contains(res.Detail, "lib-c 2.1.0: no newer release that its dependents allow - needs a new release of node-red-contrib-x") ||
		!strings.Contains(res.Detail, "lib-b: its newest allowed release has a finding as well") {
		t.Errorf("detail %q", res.Detail)
	}
	// Asked first: what is installed, never the location's own packages; then each plan.
	if len(asked) != 3 || slices.ContainsFunc(asked[0], func(p protocol.VetPackage) bool { return p.Name == "node-red-contrib-x" }) {
		t.Fatalf("asked the hub %v", asked)
	}
	install := pm.calls[len(pm.calls)-1]
	if slices.Contains(install, "--package-lock-only") || !slices.Contains(install, "--ignore-scripts") ||
		!slices.Equal(namesIn(install), []string{"lib-c", "pkg-a"}) {
		t.Fatalf("install %q", install)
	}
	if got := read("node-red", KindNpmDir, loc.Path); !slices.Contains(pkgs(got), "pkg-a@2.0.8") ||
		!slices.Contains(pkgs(got), "lib-b@1.3.3") {
		t.Errorf("afterwards %v", pkgs(got))
	}
}

// What an update would bring along beyond the named dependencies is no single dependency to leave out: a finding there
// stops the run before anything changes.
func TestRefreshDepsStopsOnAFlaggedNewcomer(t *testing.T) {
	NpmCommand = func() (string, []string, bool) { return "npm", nil, true }
	defer func() { NpmCommand = npmCommand }()
	loc, pm := depsFolder(t)
	pm.extra = map[string]map[string]string{"pkg-a": {"forwarded": "0.2.0"}}
	flagged := map[string][]string{"pkg-a 2.0.7": {"GHSA-a"}, "forwarded 0.2.0": {"MAL-1"}}
	journal := 0
	res := depsUpdater(pm, vetAll(flagged)).RefreshDeps(context.Background(), loc, func() error { journal++; return nil })
	if res.Code != protocol.CodePrecheckVersionFlagged || !strings.Contains(res.Detail, "forwarded 0.2.0") || journal != 0 {
		t.Fatalf("%s %q journal %d", res.Code, res.Detail, journal)
	}
	for _, c := range pm.calls {
		if !slices.Contains(c, "--package-lock-only") {
			t.Fatalf("installed: %q", c)
		}
	}
}

// The location is read again: a planned entry that is not installed afterwards fails the run, whatever npm said.
func TestRefreshDepsChecksWhatIsInstalled(t *testing.T) {
	NpmCommand = func() (string, []string, bool) { return "npm", nil, true }
	defer func() { NpmCommand = npmCommand }()
	loc, pm := depsFolder(t)
	pm.skip = "pkg-a"
	res := depsUpdater(pm, vetAll(depsFlagged)).RefreshDeps(context.Background(), loc, func() error { return nil })
	if res.Code != protocol.CodeFailedVerification || !strings.Contains(res.Detail, "pkg-a 2.0.8") {
		t.Fatalf("%s %q", res.Code, res.Detail)
	}
}

// Nothing with a finding: no plan, no npm. A folder without a lock file, global npm: not refreshable (step 1).
func TestRefreshDepsOnlyWhereItCan(t *testing.T) {
	NpmCommand = func() (string, []string, bool) { return "npm", nil, true }
	defer func() { NpmCommand = npmCommand }()
	loc, pm := depsFolder(t)
	res := depsUpdater(pm, vetAll(nil)).RefreshDeps(context.Background(), loc, func() error { return nil })
	if res.Code != protocol.CodeOK || res.Detail != "no dependency here has a finding" || len(pm.calls) != 0 {
		t.Fatalf("%s %q calls %q", res.Code, res.Detail, pm.calls)
	}
	noLock := filepath.Join(t.TempDir(), "project")
	write(t, filepath.Join(noLock, "package.json"), `{"dependencies":{"a":"^1"}}`)
	for _, l := range []Location{read("project", KindNpmDir, noLock), {ID: "npm-global", Kind: KindNpmGlobal, Path: loc.Path}} {
		if Refreshable(l) {
			t.Errorf("%s refreshable", l.ID)
		}
		if res := depsUpdater(pm, vetAll(depsFlagged)).RefreshDeps(context.Background(), l, nil); res.Code != protocol.CodeRejectedUnsupported {
			t.Errorf("%s: %s", l.ID, res.Code)
		}
	}
	if !Refreshable(loc) {
		t.Error("an npm folder with a lock file is refreshable")
	}
}

// The dry run: named by the operator, planned like the real run, nothing installed and no hub asked.
func TestPlanDepsChangesNothing(t *testing.T) {
	NpmCommand = func() (string, []string, bool) { return "npm", nil, true }
	defer func() { NpmCommand = npmCommand }()
	loc, pm := depsFolder(t)
	res := depsUpdater(pm, nil).PlanDeps(context.Background(), loc, []string{"pkg-a", "lib-c"})
	if res.Code != protocol.CodeOK || !slices.Equal(res.Changed, []string{"pkg-a 2.0.7 -> 2.0.8"}) ||
		len(res.Plan) != 1 || !strings.Contains(res.Detail, "lib-c 2.1.0") {
		t.Fatalf("%s %q %q %v", res.Code, res.Detail, res.Changed, res.Plan)
	}
	if len(pm.calls) != 1 || !slices.Contains(pm.calls[0], "--package-lock-only") {
		t.Fatalf("calls %q", pm.calls)
	}
	if res := depsUpdater(pm, nil).PlanDeps(context.Background(), loc, []string{"--registry=evil"}); res.Code != protocol.CodeRejectedScopeNotAllowed {
		t.Fatalf("option as a name: %s", res.Code)
	}
}
