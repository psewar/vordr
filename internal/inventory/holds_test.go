package inventory

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
)

// npmJSON writes a package folder with dependencies and, optionally, bundleDependencies.
func npmJSON(t *testing.T, dir, name, version string, deps map[string]string, bundle any) {
	t.Helper()
	p := map[string]any{"name": name, "version": version}
	if deps != nil {
		p["dependencies"] = deps
	}
	if bundle != nil {
		p["bundleDependencies"] = bundle
	}
	raw, _ := json.Marshal(p)
	write(t, filepath.Join(dir, "package.json"), string(raw))
}

func holdsOf(loc Location) map[string][]Hold {
	out := map[string][]Hold{}
	for _, p := range loc.Packages {
		if len(p.Holds) > 0 {
			out[p.Name+"@"+p.Version] = p.Holds
		}
	}
	return out
}

// ADR-0129: each copy gets the range of the packages whose dependency resolves to it, the tightest one, and the
// outermost bundle it sits in. Ranges without an upper end, or that do not read, hold nothing.
func TestNpmHolds(t *testing.T) {
	nm := filepath.Join(t.TempDir(), "lib", "node_modules")
	npmJSON(t, filepath.Join(nm, "app"), "app", "1.0.0", map[string]string{"lib": "1.20.8", "shared": "^2.0.0"}, nil)
	npmJSON(t, filepath.Join(nm, "app", "node_modules", "lib"), "lib", "1.20.8", nil, nil) // app's own copy
	npmJSON(t, filepath.Join(nm, "lib"), "lib", "1.21.0", nil, nil)
	npmJSON(t, filepath.Join(nm, "other"), "other", "1.0.0",
		map[string]string{"lib": "~1.21.0", "shared": "~2.1.0", "x": "latest", "y": ">=1"}, nil)
	npmJSON(t, filepath.Join(nm, "shared"), "shared", "2.1.3", nil, nil)
	npmJSON(t, filepath.Join(nm, "x"), "x", "1.0.0", nil, nil)
	npmJSON(t, filepath.Join(nm, "y"), "y", "1.0.0", nil, nil)
	npmJSON(t, filepath.Join(nm, "tool"), "tool", "3.0.0", map[string]string{"inner": "^1.0.0"}, []string{"inner"})
	npmJSON(t, filepath.Join(nm, "tool", "node_modules", "inner"), "inner", "1.0.0", map[string]string{"deep": "^0.1.0"}, true)
	npmJSON(t, filepath.Join(nm, "tool", "node_modules", "inner", "node_modules", "deep"), "deep", "0.1.0", nil, nil)
	npmJSON(t, filepath.Join(nm, "@s", "a"), "@s/a", "1.0.0", map[string]string{"@s/b": "1.0.0"}, nil)
	npmJSON(t, filepath.Join(nm, "@s", "b"), "@s/b", "1.0.0", nil, nil)

	got := holdsOf(read("npm-global", KindNpmGlobal, nm))
	want := map[string][]Hold{
		"lib@1.20.8":   {{By: "app@1.0.0", Range: "1.20.8"}},
		"lib@1.21.0":   {{By: "other@1.0.0", Range: "~1.21.0"}},
		"shared@2.1.3": {{By: "other@1.0.0", Range: "~2.1.0"}},
		"inner@1.0.0":  {{By: "tool@3.0.0", Range: "^1.0.0"}, {By: "tool@3.0.0", Bundled: true}},
		// deep sits below inner, which bundles too: the outermost bundle counts
		"deep@0.1.0": {{By: "inner@1.0.0", Range: "^0.1.0"}, {By: "tool@3.0.0", Bundled: true}},
		"@s/b@1.0.0": {{By: "@s/a@1.0.0", Range: "1.0.0"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("holds\n got %v\nwant %v", got, want)
	}

	// A Node-RED folder: the folder's own package.json is no dependent (an update rewrites it); a node pins its
	// dependency.
	dir := filepath.Join(t.TempDir(), ".node-red")
	write(t, filepath.Join(dir, "package.json"), `{"dependencies":{"node-x":"~1.2.0"}}`)
	npmJSON(t, filepath.Join(dir, "node_modules", "node-x"), "node-x", "1.2.0", map[string]string{"lib-c": "2.1.0"}, nil)
	npmJSON(t, filepath.Join(dir, "node_modules", "lib-c"), "lib-c", "2.1.0", nil, nil)
	got = holdsOf(read("node-red", KindNpmDir, dir))
	if !reflect.DeepEqual(got, map[string][]Hold{"lib-c@2.1.0": {{By: "node-x@1.2.0", Range: "2.1.0"}}}) {
		t.Errorf("node-red folder: %v", got)
	}
}

// ADR-0129: Python - the ranges of Requires-Dist without a marker, and for an environment in a project folder the
// project's lock and requirements files; the tightest one counts.
func TestPythonHolds(t *testing.T) {
	proj := filepath.Join(t.TempDir(), "project")
	venv := filepath.Join(proj, ".venv")
	write(t, filepath.Join(venv, "pyvenv.cfg"), "")
	site := filepath.Join(venv, "lib", "python3.12", "site-packages")
	distInfo(t, site, "homeassistant", "2026.10.4", "Pillow==12.3.0", "aiohttp>=3.9",
		"typing-extensions>=4; python_version < '3.11'", "foo[bar]<2", "pillow-heif==1.0; extra == 'heif'")
	distInfo(t, site, "Pillow", "12.3.0")
	distInfo(t, site, "aiohttp", "3.14.4", "multidict<7.0,>=4.5")
	distInfo(t, site, "multidict", "6.7.1")
	distInfo(t, site, "asyncssh", "2.19.0")
	distInfo(t, site, "cryptography", "48.0.0")
	distInfo(t, site, "numpy", "2.1.0")
	distInfo(t, site, "typing_extensions", "4.15.0")
	distInfo(t, site, "requests", "2.32.0", "urllib3 (<3,>=1.21.1)")
	distInfo(t, site, "urllib3", "2.5.0")
	distInfo(t, site, "foo", "1.0")
	write(t, filepath.Join(proj, "uv.lock"), `version = 1

[[package]]
name = "cryptography"
version = "48.0.0"
source = { registry = "https://pypi.org/simple" }
dependencies = [
    { name = "cffi" },
]

[package.optional-dependencies]
ssh = [
    { name = "bcrypt" },
]

[[package]]
name = "numpy"
version = "1.26.4"

[[package]]
name = "numpy"
version = "2.1.0"
`)
	write(t, filepath.Join(proj, "requirements_ha_test.txt"), "# tests\n-r requirements.txt\nasyncssh>=2.14.0,<2.20.0  # pinned\n"+
		"pillow==11.0.0 ; python_version > '3'\nfoo @ https://example.org/foo.whl\n")
	write(t, filepath.Join(proj, "Pipfile.lock"), `{"_meta": {"pipfile-spec": 6, "hash": {"sha256": "x"}},
		"default": {"requests": {"version": "==2.32.0"}}, "develop": {}}`)

	got := holdsOf(read("project", KindVenv, venv))
	want := map[string][]Hold{
		"Pillow@12.3.0":       {{By: "homeassistant@2026.10.4", Range: "==12.3.0"}},
		"multidict@6.7.1":     {{By: "aiohttp@3.14.4", Range: "<7.0,>=4.5"}},
		"asyncssh@2.19.0":     {{By: "requirements_ha_test.txt", Range: ">=2.14.0,<2.20.0"}},
		"cryptography@48.0.0": {{By: "uv.lock", Range: "==48.0.0"}},
		"urllib3@2.5.0":       {{By: "requests@2.32.0", Range: "(<3,>=1.21.1)"}},
		"requests@2.32.0":     {{By: "Pipfile.lock", Range: "==2.32.0"}},
		"foo@1.0":             {{By: "homeassistant@2026.10.4", Range: "<2"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("holds\n got %v\nwant %v", got, want)
	}

	// An environment that is no project folder's (.venv, venv): the files next to it are someone else's.
	svc := filepath.Join(t.TempDir(), "svc")
	write(t, filepath.Join(svc, "pyvenv.cfg"), "")
	distInfo(t, filepath.Join(svc, "lib", "python3.12", "site-packages"), "cryptography", "48.0.0")
	write(t, filepath.Join(filepath.Dir(svc), "uv.lock"), "[[package]]\nname = \"cryptography\"\nversion = \"48.0.0\"\n")
	if got := holdsOf(read("svc", KindVenv, svc)); len(got) != 0 {
		t.Errorf("not a project: %v", got)
	}
}
