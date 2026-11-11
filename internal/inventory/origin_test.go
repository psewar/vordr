package inventory

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/runner"
	"github.com/psewar/vordr/internal/versions"
)

// ADR-0128: every npm call of an update and a dependency refresh lets npm 12 fetch registry tarballs.
func TestNpmCallsAllowRegistryTarballs(t *testing.T) {
	NpmCommand = func() (string, []string, bool) { return "npm", nil, true }
	defer func() { NpmCommand = npmCommand }()
	loc, nm := npmGlobal(t)
	pm := &fakePM{t: t, nm: nm, plan: map[string]string{"@example/nodes": "5.0.1"}, install: true}
	if res := newUpdater(t, pm, vetAll(nil)).Update(context.Background(), loc, "", func() error { return nil }); res.Code != protocol.CodeOK {
		t.Fatalf("%s %s", res.Code, res.Detail)
	}
	if len(pm.calls) != 2 {
		t.Fatalf("calls %q", pm.calls)
	}
	for _, c := range pm.calls {
		if !slices.Contains(c, "--allow-remote=all") {
			t.Errorf("without --allow-remote=all: %q", c)
		}
	}

	dloc, dpm := depsFolder(t)
	if res := depsUpdater(dpm, vetAll(depsFlagged)).RefreshDeps(context.Background(), dloc, func() error { return nil }); res.Code != protocol.CodeOK {
		t.Fatalf("refresh: %s %s", res.Code, res.Detail)
	}
	for _, c := range dpm.calls {
		if !slices.Contains(c, "--allow-remote=all") {
			t.Errorf("refresh without --allow-remote=all: %q", c)
		}
	}
}

// ADR-0128: an entry the update brings from another host, from git or from a file ends the run before the hub is asked
// and before anything is installed. Registry tarballs, bundled entries (no "resolved") and what is installed already
// pass.
func TestNpmPlanFromAnotherHostIsRefused(t *testing.T) {
	NpmCommand = func() (string, []string, bool) { return "npm", nil, true }
	defer func() { NpmCommand = npmCommand }()
	plan := map[string]string{"@example/nodes": "5.0.1", "debug": "4.4.3", "chalk": "5.6.0"}
	for _, c := range []struct {
		name     string
		resolved func(reg string) map[string]string
		want     protocol.Code
	}{
		{"registry", func(reg string) map[string]string {
			return map[string]string{"@example/nodes": reg + "/@example/nodes/-/nodes-5.0.1.tgz", "debug": reg + "/debug/-/debug-4.4.3.tgz"}
		}, protocol.CodeOK},
		{"bundled, without resolved", func(string) map[string]string { return nil }, protocol.CodeOK},
		{"installed already, from anywhere", func(reg string) map[string]string {
			return map[string]string{"chalk": "https://cdn.example.org/chalk-5.6.0.tgz"}
		}, protocol.CodeOK},
		{"another host", func(reg string) map[string]string {
			return map[string]string{"debug": "https://cdn.example.org/debug-4.4.3.tgz"}
		}, protocol.CodePrecheckNotFromRegistry},
		{"git", func(reg string) map[string]string {
			return map[string]string{"debug": "git+ssh://git@example.org/debug.git#0123abc"}
		}, protocol.CodePrecheckNotFromRegistry},
		{"a file", func(reg string) map[string]string {
			return map[string]string{"debug": "file:../debug"}
		}, protocol.CodePrecheckNotFromRegistry},
	} {
		t.Run(c.name, func(t *testing.T) {
			loc, nm := npmGlobal(t)
			pm := &fakePM{t: t, nm: nm, plan: plan, install: true}
			asked := false
			u := newUpdater(t, pm, func(ctx context.Context, eco versions.Ecosystem, entries []protocol.VetPackage) (protocol.Vet, bool) {
				asked = true
				return vetAll(nil)(ctx, eco, entries)
			})
			pm.resolved = c.resolved(u.Reg.Npm)
			journal := 0
			res := u.Update(context.Background(), loc, "", func() error { journal++; return nil })
			if res.Code != c.want {
				t.Fatalf("code %s, want %s: %s", res.Code, c.want, res.Detail)
			}
			if c.want == protocol.CodeOK {
				return
			}
			if asked || journal != 0 || pm.last("install --global") != nil {
				t.Errorf("went on: vet %v, journal %d, calls %q", asked, journal, pm.calls)
			}
			if !strings.Contains(res.Detail, "debug 4.4.3 from ") || strings.Contains(res.Detail, "chalk") {
				t.Errorf("detail %q", res.Detail)
			}
		})
	}
}

// originPM adds "resolved" to the lock the dependency refresh's dry run writes.
type originPM struct {
	*depsPM
	from map[string]string
}

func (o originPM) Run(ctx context.Context, c Command) runner.Result {
	r := o.depsPM.Run(ctx, c)
	if !slices.Contains(c.Args, "--package-lock-only") {
		return r
	}
	path := filepath.Join(c.Dir, "package-lock.json")
	var lf map[string]any
	raw, _ := os.ReadFile(path)
	if err := json.Unmarshal(raw, &lf); err != nil {
		o.t.Fatal(err)
	}
	pk := lf["packages"].(map[string]any)
	for n, u := range o.from {
		if e, ok := pk["node_modules/"+n].(map[string]any); ok {
			e["resolved"] = u
		}
	}
	raw, _ = json.Marshal(lf)
	write(o.t, path, string(raw))
	return r
}

// The same for a dependency refresh: a refreshed dependency from another host stops it before anything changes.
func TestRefreshDepsFromAnotherHostIsRefused(t *testing.T) {
	NpmCommand = func() (string, []string, bool) { return "npm", nil, true }
	defer func() { NpmCommand = npmCommand }()
	loc, pm := depsFolder(t)
	o := originPM{pm, map[string]string{"pkg-a": "https://cdn.example.org/pkg-a-2.0.8.tgz"}}
	journal := 0
	res := depsUpdater(o, vetAll(depsFlagged)).RefreshDeps(context.Background(), loc, func() error { journal++; return nil })
	if res.Code != protocol.CodePrecheckNotFromRegistry || !strings.Contains(res.Detail, "pkg-a 2.0.8") || journal != 0 {
		t.Fatalf("%s %q journal %d", res.Code, res.Detail, journal)
	}
	for _, c := range pm.calls {
		if !slices.Contains(c, "--package-lock-only") {
			t.Fatalf("installed: %q", c)
		}
	}
	// From the registry: the refresh goes on.
	loc, pm = depsFolder(t)
	o = originPM{pm, map[string]string{"pkg-a": "https://registry.npmjs.org/pkg-a/-/pkg-a-2.0.8.tgz"}}
	if res := depsUpdater(o, vetAll(depsFlagged)).RefreshDeps(context.Background(), loc, func() error { return nil }); res.Code != protocol.CodeOK {
		t.Fatalf("registry: %s %q", res.Code, res.Detail)
	}
}

func TestOnRegistry(t *testing.T) {
	for _, c := range []struct {
		resolved, registry string
		want               bool
	}{
		{"https://registry.npmjs.org/npm/-/npm-10.2.0.tgz", "https://registry.npmjs.org", true},
		{"https://registry.npmjs.org/npm/-/npm-10.2.0.tgz", "https://registry.npmjs.org/", true},
		{"https://REGISTRY.npmjs.org/a/-/a-1.0.0.tgz", "https://registry.npmjs.org", true},
		{"https://mirror.example.org/npm/a/-/a-1.0.0.tgz", "https://mirror.example.org/npm/", true},
		{"https://mirror.example.org/other/a-1.0.0.tgz", "https://mirror.example.org/npm/", false},
		{"https://mirror.example.org/npm/../other/a-1.0.0.tgz", "https://mirror.example.org/npm/", false},
		{"https://registry.npmjs.org.example.test/a/-/a-1.0.0.tgz", "https://registry.npmjs.org", false},
		{"http://registry.npmjs.org/a/-/a-1.0.0.tgz", "https://registry.npmjs.org", false},
		{"https://user@registry.npmjs.org/a/-/a-1.0.0.tgz", "https://registry.npmjs.org", false},
		{"git+https://registry.npmjs.org/a.git", "https://registry.npmjs.org", false},
		{"file:../a", "https://registry.npmjs.org", false},
		{"%zz", "https://registry.npmjs.org", false},
	} {
		if got := onRegistry(c.resolved, c.registry); got != c.want {
			t.Errorf("onRegistry(%q, %q) = %v", c.resolved, c.registry, got)
		}
	}
}
