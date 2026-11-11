package inventory

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func distInfo(t *testing.T, site, name, version string, requires ...string) {
	t.Helper()
	meta := "Metadata-Version: 2.1\nName: " + name + "\nVersion: " + version + "\n"
	for _, r := range requires {
		meta += "Requires-Dist: " + r + "\n"
	}
	meta += "\nName: not-the-header\nlong description\n"
	write(t, filepath.Join(site, name+"-"+version+".dist-info", "METADATA"), meta)
}

func npmPkg(t *testing.T, dir, name, version string) {
	t.Helper()
	write(t, filepath.Join(dir, "package.json"), `{"name":"`+name+`","version":"`+version+`"}`)
}

func pkgs(loc Location) []string {
	var out []string
	for _, p := range loc.Packages {
		s := p.Name + "@" + p.Version
		if p.Direct {
			s += "!"
		}
		if p.Via != "" {
			s += "<" + p.Via
		}
		out = append(out, s)
	}
	return out
}

func TestVenv(t *testing.T) {
	venv := filepath.Join(t.TempDir(), "venv")
	write(t, filepath.Join(venv, "pyvenv.cfg"), "home = /usr/bin\n")
	site := filepath.Join(venv, "lib", "python3.11", "site-packages")
	distInfo(t, site, "requests", "2.31.0", "urllib3 (<3,>=1.21.1)", "charset_normalizer<4,>=2",
		"PySocks!=1.5.7,>=1.5.6; extra == 'socks'")
	distInfo(t, site, "urllib3", "2.2.1")
	distInfo(t, site, "charset-normalizer", "3.3.2")
	distInfo(t, site, "PySocks", "1.7.1") // only the "socks" extra of requests needs it
	write(t, filepath.Join(site, "legacy.egg-info"), "Metadata-Version: 1.0\nName: legacy\nVersion: 0.1\n")
	write(t, filepath.Join(site, "broken-1.0.dist-info", "METADATA"), "Name: broken\n") // no version
	loc := read("venv-x", KindVenv, venv)
	want := []string{"PySocks@1.7.1!", "charset-normalizer@3.3.2", "legacy@0.1!", "requests@2.31.0!", "urllib3@2.2.1"}
	if got := pkgs(loc); !slices.Equal(got, want) || loc.Ecosystem != "PyPI" || loc.Skipped != 1 || loc.Code != "" {
		t.Errorf("got %v (skipped %d, code %q)", got, loc.Skipped, loc.Code)
	}
}

func TestNpmGlobal(t *testing.T) {
	nm := filepath.Join(t.TempDir(), "node_modules")
	npmPkg(t, filepath.Join(nm, "node-red"), "node-red", "3.1.0")
	npmPkg(t, filepath.Join(nm, "node-red", "node_modules", "debug"), "debug", "4.4.2")
	npmPkg(t, filepath.Join(nm, "node-red", "node_modules", "debug", "node_modules", "ms"), "ms", "2.1.3")
	npmPkg(t, filepath.Join(nm, "@scope", "tool"), "@scope/tool", "1.0.0")
	npmPkg(t, filepath.Join(nm, "@scope", "tool", "node_modules", "debug"), "debug", "4.4.2") // same version again
	npmPkg(t, filepath.Join(nm, "evil"), "evil; rm -rf /", "1.0.0")                           // never leaves the host
	write(t, filepath.Join(nm, ".bin", "node-red"), "#!/bin/sh\n")
	write(t, filepath.Join(nm, "stray", "README"), "no package.json")
	loc := read("npm-global", KindNpmGlobal, nm)
	want := []string{"@scope/tool@1.0.0!", "debug@4.4.2<@scope/tool", "ms@2.1.3<node-red", "node-red@3.1.0!"} // first seen: @scope sorts first
	if got := pkgs(loc); !slices.Equal(got, want) || loc.Ecosystem != "npm" || loc.Skipped != 1 {
		t.Errorf("got %v (skipped %d)", got, loc.Skipped)
	}
}

func TestNodeRedFolder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".node-red")
	write(t, filepath.Join(dir, "package.json"), `{"name":"node-red-project","dependencies":{"node-red-contrib-x":"~1.0.0"}}`)
	npmPkg(t, filepath.Join(dir, "node_modules", "node-red-contrib-x"), "node-red-contrib-x", "1.0.3")
	npmPkg(t, filepath.Join(dir, "node_modules", "example-dep"), "example-dep", "1.2.0") // hoisted dependency
	loc := read("node-red", KindNpmDir, dir)
	want := []string{"example-dep@1.2.0", "node-red-contrib-x@1.0.3!"}
	if got := pkgs(loc); !slices.Equal(got, want) {
		t.Errorf("got %v", got)
	}
}

func TestLinkedPackageNotDescended(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "work", "mytool")
	npmPkg(t, work, "mytool", "0.0.1")
	npmPkg(t, filepath.Join(work, "node_modules", "devdep"), "devdep", "9.9.9")
	nm := filepath.Join(root, "node_modules")
	if err := os.MkdirAll(nm, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(work, filepath.Join(nm, "mytool")); err != nil {
		t.Skip("no symlinks here:", err)
	}
	if got := pkgs(read("npm-global", KindNpmGlobal, nm)); !slices.Equal(got, []string{"mytool@0.0.1!"}) {
		t.Errorf("got %v", got)
	}
}

func TestConfiguredGlob(t *testing.T) {
	repos := t.TempDir()
	write(t, filepath.Join(repos, "alpha", ".venv", "pyvenv.cfg"), "")
	distInfo(t, filepath.Join(repos, "alpha", ".venv", "lib", "python3.12", "site-packages"), "jinja2", "3.1.4")
	write(t, filepath.Join(repos, "beta", "package.json"), `{"devDependencies":{"chalk":"^5"}}`)
	npmPkg(t, filepath.Join(repos, "beta", "node_modules", "chalk"), "chalk", "5.6.1")
	write(t, filepath.Join(repos, "Both Kinds", "venv", "pyvenv.cfg"), "")
	write(t, filepath.Join(repos, "Both Kinds", "package.json"), `{}`)
	write(t, filepath.Join(repos, "Both Kinds", "node_modules", ".package-lock.json"), `{}`)
	write(t, filepath.Join(repos, "empty", "README"), "")
	locs := Collect(Options{Configured: []Configured{{ID: "repos", Path: filepath.Join(repos, "*")}}})
	var got []string
	for _, l := range locs {
		got = append(got, l.ID+" "+l.Kind+" "+filepath.Base(l.Path))
	}
	want := []string{"repos-both-kinds-py venv venv", "repos-both-kinds-npm npm-dir Both Kinds",
		"repos-alpha venv .venv", "repos-beta npm-dir beta"} // in the order of the glob: upper case first
	if !slices.Equal(got, want) {
		t.Errorf("got %v", got)
	}
	if p := locs[3].Packages; len(p) != 1 || p[0].Name != "chalk" || !p[0].Direct {
		t.Errorf("beta: %+v", p)
	}
}

// /opt/*/venv: the id comes from the folder the "*" matched, not from "venv".
func TestConfiguredIDFromTheStar(t *testing.T) {
	opt := t.TempDir()
	for _, svc := range []string{"web-app", "Other_App"} {
		write(t, filepath.Join(opt, svc, "venv", "pyvenv.cfg"), "")
	}
	var got []string
	for _, l := range Collect(Options{Configured: []Configured{{ID: "services", Path: filepath.Join(opt, "*", "venv")}}}) {
		got = append(got, l.ID)
	}
	if want := []string{"services-other_app", "services-web-app"}; !slices.Equal(got, want) {
		t.Errorf("got %v", got)
	}
}

func TestDetectAndIDs(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "usr-local", "python3.11", "dist-packages", "README"), "") // empty: no location
	distInfo(t, filepath.Join(root, "usr-local", "python3.12", "dist-packages"), "pip", "24.0")
	write(t, filepath.Join(root, "home", "ops", ".node-red", "package.json"), "{}")
	npmPkg(t, filepath.Join(root, "home", "ops", ".node-red", "node_modules", "x"), "x", "1.0.0")
	write(t, filepath.Join(root, "home", "alice", ".node-red", "package.json"), "{}") // never installed: no node_modules
	write(t, filepath.Join(root, "pipx", "venvs", "Black Tool", "pyvenv.cfg"), "")
	ps := []place{
		{filepath.Join(root, "usr-local", "python3*", "dist-packages"), KindPipSite, func([]string) string { return "pip-system" }},
		{filepath.Join(root, "home", "*", ".node-red"), KindNpmDir, func(p []string) string { return "node-red-" + p[0] }},
		{filepath.Join(root, "pipx", "venvs", "*"), KindVenv, func(p []string) string { return "pipx-" + p[0] }},
	}
	var got []string
	for _, f := range detect(ps) {
		got = append(got, f.ID+" "+f.Kind)
	}
	if want := []string{"pip-system pip-site", "node-red-ops npm-dir", "pipx-black-tool venv"}; !slices.Equal(got, want) {
		t.Errorf("got %v", got)
	}
	if id := trimID("venv-" + "a-very-long-project-name-that-goes-on-and-on"); len(id) > 32 || !ValidID(id) {
		t.Errorf("trimID: %q", id)
	}
}

func TestWindowsPlaces(t *testing.T) {
	ps := places("windows", "D:", `D:\Program Files`)
	byKind := map[string]int{}
	for _, p := range ps {
		byKind[p.kind]++
	}
	if byKind[KindNpmGlobal] != 2 || byKind[KindPipSite] != 5 || byKind[KindVenv] != 2 || byKind[KindNpmDir] != 1 {
		t.Errorf("places: %v", byKind)
	}
	// starParts takes the user and the interpreter folder from a match.
	parts := starParts(`D:\Users\*\AppData\Local\Programs\Python\*\Lib\site-packages`,
		`D:\Users\bob\AppData\Local\Programs\Python\Python312\Lib\site-packages`)
	if !slices.Equal(parts, []string{"bob", "Python312"}) || ps[1].id(parts) != "python312-bob" {
		t.Errorf("parts %v -> %q", parts, ps[1].id(parts))
	}
	// MSIX app containers (ADR-0079): the id names the app and the user, like the place outside the container.
	for _, c := range []struct{ path, kind, id string }{
		{`D:\Users\bob\AppData\Local\Packages\Example.App_0a1b2c3d4e5f6\LocalCache\Roaming\Python\Python312\site-packages`,
			KindPipSite, "app-pip-user-python312-bob"},
		{`D:\Users\alice\AppData\Local\Packages\Microsoft.PowerShell_8wekyb3d8bbwe\LocalCache\Roaming\npm\node_modules`,
			KindNpmGlobal, "powershell-npm-global-alice"},
		{`D:\Users\bob\AppData\Local\Packages\PythonSoftwareFoundation.Python.3.12_qbz5n2kfra8p0\LocalCache\local-packages\Python312\site-packages`,
			KindPipSite, "store-pip-user-python312-bob"},
		{`D:\Users\bob\AppData\Roaming\Python\Python312\site-packages`, KindPipSite, "pip-user-python312-bob"},
	} {
		var ids []string
		for _, p := range ps {
			if p.kind == c.kind && matchWindows(p.pattern, c.path) {
				ids = append(ids, p.id(starParts(p.pattern, c.path)))
			}
		}
		if !slices.Equal(ids, []string{c.id}) {
			t.Errorf("%s: ids %v, want [%s]", c.path, ids, c.id)
		}
	}
}

// matchWindows matches a Windows pattern against a Windows path element by element, ignoring case as Windows does,
// so the test also runs on Linux.
func matchWindows(pattern, path string) bool {
	pp, mp := strings.Split(pattern, `\`), strings.Split(path, `\`)
	if len(pp) != len(mp) {
		return false
	}
	for i := range pp {
		if ok, err := filepath.Match(strings.ToLower(pp[i]), strings.ToLower(mp[i])); err != nil || !ok {
			return false
		}
	}
	return true
}

func TestAppName(t *testing.T) {
	for family, want := range map[string]string{
		"Example.App_0a1b2c3d4e5f6":                          "app",
		"Microsoft.PowerShell_8wekyb3d8bbwe":                 "powershell",
		"PythonSoftwareFoundation.Python.3.12_qbz5n2kfra8p0": "python-3-12",
		"1234ABCD.ExampleDesktop_0a1b2c3d4e5f6":              "exampledesktop",
		"NoPublisherID":                                      "nopublisherid",
		"Trailing._x":                                        "trailing",
	} {
		if got := appName(family); got != want {
			t.Errorf("appName(%q) = %q, want %q", family, got, want)
		}
	}
}

// On Windows: the container places find what an MSIX app wrote, and a path agent.yaml also names - here in other
// letter case - is read once, under the configured id.
func TestWindowsContainersDetected(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows paths")
	}
	drive := t.TempDir()
	box := filepath.Join(drive, "Users", "bob", "AppData", "Local", "Packages", "Example.App_0a1b2c3d4e5f6", "LocalCache")
	site := filepath.Join(box, "Roaming", "Python", "Python312", "site-packages")
	distInfo(t, site, "aiohttp", "3.9.0", "multidict<7.0,>=4.5")
	distInfo(t, site, "multidict", "6.0.4")
	npmPkg(t, filepath.Join(box, "Roaming", "npm", "node_modules", "example-cli"), "example-cli", "1.2.0")
	write(t, filepath.Join(drive, "Users", "bob", "AppData", "Local", "Packages", "Empty_x", "LocalCache",
		"Roaming", "Python", "Python312", "site-packages", "README"), "") // no dist-info: no location
	detected := func() []Found { return detect(places("windows", drive, filepath.Join(drive, "Program Files"))) }
	var got []string
	for _, f := range detected() {
		got = append(got, f.ID+" "+f.Kind)
	}
	if want := []string{"app-npm-global-bob npm-global", "app-pip-user-python312-bob pip-site"}; !slices.Equal(got, want) {
		t.Errorf("detected %v", got)
	}
	configured := Configured{ID: "app-pip-user", Path: filepath.Join(box, "roaming", "Python", "*", "site-packages")}
	got = nil
	for _, l := range collect(Options{Detect: true, Configured: []Configured{configured}}, detected) {
		got = append(got, l.ID+" "+strings.Join(pkgs(l), ","))
	}
	if want := []string{"app-pip-user-python312 aiohttp@3.9.0!,multidict@6.0.4", "app-npm-global-bob example-cli@1.2.0!"}; !slices.Equal(got, want) {
		t.Errorf("collected %v", got)
	}
}
