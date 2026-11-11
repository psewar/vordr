package inventory

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Found is a well-known location that exists on this host and holds packages.
type Found struct {
	ID, Kind, Path string
}

// place is a pattern of well-known locations: glob, kind, and how the id follows from the match.
type place struct {
	pattern string
	kind    string
	id      func(parts []string) string // parts: the values the "*" of the pattern matched, in order
}

// Detected returns the well-known locations of this operating system that exist and are not empty: global npm,
// pip outside the distribution's own folder, pip --user, pipx apps, Node-RED user folders; under Windows also global
// npm and pip --user inside MSIX app containers (ADR-0079). Folders a distribution's package manager owns
// (/usr/lib/python3/dist-packages under Debian) are left out: apt keeps those, and their versions carry backported
// fixes that OSV's upstream ranges do not know.
func Detected() []Found {
	return detect(places(runtime.GOOS, os.Getenv("SystemDrive"), os.Getenv("ProgramFiles")))
}

func places(goos, systemDrive, programFiles string) []place {
	fixed := func(id string) func([]string) string { return func([]string) string { return id } }
	if goos == "windows" {
		if systemDrive == "" {
			systemDrive = "C:"
		}
		if programFiles == "" {
			programFiles = systemDrive + `\Program Files`
		}
		users := systemDrive + `\Users\*`
		// What a process of an MSIX app such as PowerShell from the Store writes to AppData lands in the
		// app's container; processes outside the app, this service included, do not see it at the usual path.
		containers := users + `\AppData\Local\Packages\*\LocalCache`
		pyID := func(prefix string) func([]string) string {
			return func(p []string) string { return trimID(prefix + "-" + strings.ToLower(p[len(p)-1]) + userPart(p[0])) }
		}
		return []place{
			{users + `\AppData\Roaming\npm\node_modules`, KindNpmGlobal, func(p []string) string { return "npm-global" + userPart(p[0]) }},
			{users + `\AppData\Local\Programs\Python\*\Lib\site-packages`, KindPipSite, func(p []string) string {
				return trimID(strings.ToLower(p[1]) + userPart(p[0]))
			}},
			{programFiles + `\*\Lib\site-packages`, KindPipSite, func(p []string) string { return trimID(strings.ToLower(p[0])) }},
			{users + `\AppData\Roaming\Python\*\site-packages`, KindPipSite, pyID("pip-user")},
			{users + `\pipx\venvs\*`, KindVenv, func(p []string) string { return trimID("pipx-" + p[1]) }},
			{users + `\.local\pipx\venvs\*`, KindVenv, func(p []string) string { return trimID("pipx-" + p[1]) }},
			{users + `\.node-red`, KindNpmDir, func(p []string) string { return trimID("node-red" + userPart(p[0])) }},
			{containers + `\Roaming\npm\node_modules`, KindNpmGlobal, func(p []string) string {
				return trimID(appName(p[1]) + "-npm-global" + userPart(p[0]))
			}},
			{containers + `\Roaming\Python\*\site-packages`, KindPipSite, func(p []string) string {
				return trimID(appName(p[1]) + "-pip-user-" + strings.ToLower(p[2]) + userPart(p[0]))
			}},
			// Python from the Microsoft Store puts pip --user here, not under Roaming.
			{users + `\AppData\Local\Packages\PythonSoftwareFoundation.Python.*\LocalCache\local-packages\*\site-packages`,
				KindPipSite, pyID("store-pip-user")},
		}
	}
	return []place{
		{"/usr/lib/node_modules", KindNpmGlobal, fixed("npm-global")},
		{"/usr/local/lib/node_modules", KindNpmGlobal, fixed("npm-global")},
		{"/usr/local/lib/python3*/dist-packages", KindPipSite, fixed("pip-system")},
		{"/usr/local/lib/python3*/site-packages", KindPipSite, fixed("pip-system")},
		{"/root/.local/lib/python3*/site-packages", KindPipSite, fixed("pip-user-root")},
		{"/home/*/.local/lib/python3*/site-packages", KindPipSite, func(p []string) string { return trimID("pip-user-" + p[0]) }},
		{"/root/.local/share/pipx/venvs/*", KindVenv, func(p []string) string { return trimID("pipx-" + p[0]) }},
		{"/home/*/.local/share/pipx/venvs/*", KindVenv, func(p []string) string { return trimID("pipx-" + p[1]) }},
		{"/opt/pipx/venvs/*", KindVenv, func(p []string) string { return trimID("pipx-" + p[0]) }},
		{"/root/.node-red", KindNpmDir, fixed("node-red")},
		{"/home/*/.node-red", KindNpmDir, func(p []string) string { return trimID("node-red-" + p[0]) }},
	}
}

// userPart: "-<user>" for a folder below a user profile, so two users' locations get different ids.
func userPart(user string) string {
	return "-" + sanitize(user)
}

// appName: the app part of an MSIX package family name (<publisher>.<name>_<publisher id>), as an id:
// Example.App_0a1b2c3d4e5f6 gives app, Microsoft.PowerShell_8wekyb3d8bbwe gives powershell.
func appName(family string) string {
	name, _, _ := strings.Cut(family, "_")
	if _, rest, ok := strings.Cut(name, "."); ok && rest != "" {
		name = rest
	}
	return sanitize(name)
}

func detect(ps []place) []Found {
	var out []Found
	for _, p := range ps {
		matches, _ := filepath.Glob(p.pattern) // the patterns are fixed and valid
		for _, m := range matches {
			if !holdsPackages(p.kind, m) {
				continue
			}
			out = append(out, Found{ID: trimID(p.id(starParts(p.pattern, m))), Kind: p.kind, Path: m})
		}
	}
	return out
}

// starParts returns what each "*" of pattern matched in path (pattern and path have the same number of elements).
func starParts(pattern, path string) []string {
	sep := func(r rune) bool { return r == '/' || r == '\\' } // Windows patterns also in tests on Linux
	pp := strings.FieldsFunc(pattern, sep)
	mp := strings.FieldsFunc(path, sep)
	var out []string
	for i, seg := range pp {
		if i < len(mp) && strings.Contains(seg, "*") {
			out = append(out, mp[i])
		}
	}
	return out
}

// holdsPackages: an empty /usr/local/lib/python3.11/dist-packages exists on every Debian host and is no location.
func holdsPackages(kind, dir string) bool {
	switch kind {
	case KindNpmDir:
		return exists(filepath.Join(dir, "package.json")) && exists(filepath.Join(dir, "node_modules"))
	case KindVenv:
		return exists(filepath.Join(dir, "pyvenv.cfg"))
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		n := e.Name()
		if kind == KindNpmGlobal && !strings.HasPrefix(n, ".") ||
			kind == KindPipSite && (strings.HasSuffix(n, ".dist-info") || strings.HasSuffix(n, ".egg-info")) {
			return true
		}
	}
	return false
}
