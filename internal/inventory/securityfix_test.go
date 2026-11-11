package inventory

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/runner"
)

// pathPM stands in for npm in an npm folder with a lock file, by lock path: every run starts from the lock in its
// folder, resolve changes it for the command line and the --before it carries; a dry run writes the result as the lock,
// a run in place also installs it into node_modules.
type pathPM struct {
	t       *testing.T
	dir     string
	resolve func(lock map[string]string, args []string, before time.Time)
	calls   [][]string
}

func (f *pathPM) Run(_ context.Context, c Command) runner.Result {
	f.calls = append(f.calls, c.Args)
	lock := readPathLock(f.t, filepath.Join(c.Dir, "package-lock.json"))
	i := slices.Index(c.Args, "--before")
	before, err := time.Parse(time.RFC3339, c.Args[i+1])
	if err != nil {
		f.t.Fatalf("--before %q: %v", c.Args[i+1], err)
	}
	f.resolve(lock, c.Args, before)
	if !slices.Contains(c.Args, "--package-lock-only") {
		for p, v := range lock {
			name := p[strings.LastIndex(p, "node_modules/")+len("node_modules/"):]
			npmPkg(f.t, filepath.Join(f.dir, filepath.FromSlash(p)), name, v)
		}
		writePathLock(f.t, filepath.Join(f.dir, "package-lock.json"), lock)
		return runner.Result{}
	}
	writePathLock(f.t, filepath.Join(c.Dir, "package-lock.json"), lock)
	return runner.Result{}
}

// dryRuns: the command lines of the dry runs, in order.
func (f *pathPM) dryRuns() [][]string {
	var out [][]string
	for _, c := range f.calls {
		if slices.Contains(c, "--package-lock-only") {
			out = append(out, c)
		}
	}
	return out
}

func readPathLock(t *testing.T, path string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var lf struct {
		Packages map[string]struct {
			Version string `json:"version"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(raw, &lf); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for p, e := range lf.Packages {
		if p != "" {
			out[p] = e.Version
		}
	}
	return out
}

func writePathLock(t *testing.T, path string, pkgs map[string]string) {
	t.Helper()
	pk := map[string]any{"": map[string]any{"name": "node-red-project"}}
	for p, v := range pkgs {
		pk[p] = map[string]any{"version": v}
	}
	raw, _ := json.Marshal(map[string]any{"lockfileVersion": 3, "packages": pk})
	write(t, path, string(raw))
}

// specArg: name@version on an npm command line.
var specArg = regexp.MustCompile(`^(@?[^@/][^@]*)@([0-9][^@]*)$`)

// newest: the newest of these releases of debug published before the cutoff, as npm's --before chooses.
func newestDebug(cands []string, before time.Time) string {
	published := map[string]time.Time{"2.6.8": now.AddDate(0, 0, -900), "2.6.9": now.AddDate(0, 0, -3),
		"4.3.4": now.AddDate(0, 0, -100), "4.3.7": now.AddDate(0, 0, -4)}
	best := ""
	for _, v := range cands {
		if !published[v].After(before) {
			best = v // cands are in ascending order
		}
	}
	return best
}

// A location-wide update with a named fix and another package (review of ADR-0111): the other package's new
// dependency must not resolve at the fix's shorter age. The fix goes first, alone, at the shorter age; the rest
// follows at the minimum age. Without that split, debug 4.3.7 (4 days old) came along and the whole job was refused.
func TestNpmFixAndOtherPackageInOneJob(t *testing.T) {
	NpmCommand = func() (string, []string, bool) { return "npm", nil, true }
	defer func() { NpmCommand = npmCommand }()
	dir := filepath.Join(t.TempDir(), ".node-red")
	write(t, filepath.Join(dir, "package.json"), `{"dependencies":{"chalk":"^5","@example/nodes":"^4"}}`)
	old := map[string]string{"node_modules/chalk": "5.6.0", "node_modules/@example/nodes": "4.1.0",
		"node_modules/debug": "4.3.4"}
	for p, v := range old {
		npmPkg(t, filepath.Join(dir, filepath.FromSlash(p)), strings.TrimPrefix(p, "node_modules/"), v)
	}
	writePathLock(t, filepath.Join(dir, "package-lock.json"), old)
	pm := &pathPM{t: t, dir: dir, resolve: func(lock map[string]string, args []string, before time.Time) {
		for _, a := range args {
			if m := specArg.FindStringSubmatch(a); m != nil {
				lock["node_modules/"+m[1]] = m[2]
				if m[1] == "@example/nodes" { // its new release wants debug ^4: the newest before --before
					lock["node_modules/debug"] = newestDebug([]string{"4.3.4", "4.3.7"}, before)
				}
			}
		}
	}}
	_, reg := newFakeRegistry(t)
	day := 24 * time.Hour
	journal := 0
	u := &Updater{Exec: pm, Reg: reg, Now: func() time.Time { return now }, MinAge: 7 * day, SecurityAge: 2 * day,
		SecurityFix: []string{"chalk"}, Vet: vetAll(nil), TempDir: t.TempDir()}
	res := u.Update(context.Background(), read("node-red", KindNpmDir, dir), "", func() error { journal++; return nil })
	if res.Code != protocol.CodeOK || journal != 1 {
		t.Fatalf("%s %q, journal %d", res.Code, res.Detail, journal)
	}
	if !slices.Equal(res.Changed, []string{"chalk 5.6.0 -> 5.6.2", "@example/nodes 4.1.0 -> 5.0.1"}) {
		t.Errorf("changed %q", res.Changed)
	}
	dry := pm.dryRuns()
	if len(dry) != 2 {
		t.Fatalf("dry runs %q", dry)
	}
	at := func(c []string) string { return c[slices.Index(c, "--before")+1] }
	if !slices.Contains(dry[0], "chalk@5.6.2") || slices.Contains(dry[0], "@example/nodes@5.0.1") ||
		at(dry[0]) != now.Add(-2*day).Format(time.RFC3339) {
		t.Errorf("first part, the fix at the shorter age: %q", dry[0])
	}
	if !slices.Contains(dry[1], "@example/nodes@5.0.1") || slices.Contains(dry[1], "chalk@5.6.2") ||
		at(dry[1]) != now.Add(-7*day).Format(time.RFC3339) {
		t.Errorf("second part, the rest at the minimum age: %q", dry[1])
	}
	if got := readPathLock(t, filepath.Join(dir, "package-lock.json")); got["node_modules/debug"] != "4.3.4" ||
		got["node_modules/chalk"] != "5.6.2" {
		t.Errorf("afterwards %v", got)
	}
}

// A refresh of a named dependency moves every copy of it (review of ADR-0111). Only the release that takes the place
// of the copy with a finding may be as young as the shorter age; a copy without one keeps the minimum age.
func TestDepsFixOnlyWhereTheCopyHasAFinding(t *testing.T) {
	NpmCommand = func() (string, []string, bool) { return "npm", nil, true }
	defer func() { NpmCommand = npmCommand }()
	day := 24 * time.Hour
	for _, c := range []struct {
		name     string
		top      []string // releases of debug the top-level copy may move to
		want     protocol.Code
		installs bool
	}{
		// The clean copy would move to 4.3.7, 4 days old: refused, nothing installed.
		{"clean copy moves to a young release", []string{"4.3.4", "4.3.7"}, protocol.CodePrecheckVersionTooNew, false},
		// Only the copy with the finding moves: its fix, 3 days old, goes in.
		{"only the flagged copy moves", []string{"4.3.4"}, protocol.CodeOK, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), ".node-red")
			write(t, filepath.Join(dir, "package.json"), `{"dependencies":{"send":"^0.16"}}`)
			old := map[string]string{"node_modules/send": "0.16.0", "node_modules/send/node_modules/debug": "2.6.8",
				"node_modules/debug": "4.3.4"}
			for p, v := range old {
				npmPkg(t, filepath.Join(dir, filepath.FromSlash(p)), p[strings.LastIndex(p, "node_modules/")+13:], v)
			}
			writePathLock(t, filepath.Join(dir, "package-lock.json"), old)
			pm := &pathPM{t: t, dir: dir, resolve: func(lock map[string]string, args []string, before time.Time) {
				if slices.Contains(args, "debug") { // npm update debug: every copy within its dependents' range
					lock["node_modules/send/node_modules/debug"] = newestDebug([]string{"2.6.8", "2.6.9"}, before)
					lock["node_modules/debug"] = newestDebug(c.top, before)
				}
			}}
			_, reg := newFakeRegistry(t)
			u := &Updater{Exec: pm, Reg: reg, Now: func() time.Time { return now }, MinAge: 7 * day, SecurityAge: 2 * day,
				SecurityFix: []string{"debug"}, Vet: vetAll(map[string][]string{"debug 2.6.8": {"GHSA-d"}}),
				TempDir: t.TempDir()}
			res := u.RefreshDeps(context.Background(), read("node-red", KindNpmDir, dir), func() error { return nil })
			if res.Code != c.want {
				t.Fatalf("%s %q", res.Code, res.Detail)
			}
			if c.want != protocol.CodeOK && !strings.Contains(res.Detail, "debug 4.3.7") {
				t.Errorf("refusal names %q", res.Detail)
			}
			got := readPathLock(t, filepath.Join(dir, "package-lock.json"))
			if moved := got["node_modules/send/node_modules/debug"] == "2.6.9"; moved != c.installs {
				t.Errorf("installed %v, want %v: %v", moved, c.installs, got)
			}
			if dry := pm.dryRuns(); len(dry) == 0 || dry[0][slices.Index(dry[0], "--before")+1] != now.Add(-2*day).Format(time.RFC3339) {
				t.Errorf("dry run %q", dry)
			}
		})
	}
}
