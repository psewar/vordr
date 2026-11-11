package inventory

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/psewar/vordr/internal/versions"
)

// Holds (ADR-0129): what keeps a package below a version, read from what is installed - the hub sets it against the
// version that fixes a finding. Only the tightest range counts (a fix above it cannot come in under any of them), and
// the bundle a package came in, whatever its range.

// Hold is one thing that keeps a package below a version.
type Hold struct {
	// By: an installed package, "<name>@<version>", or a project file next to a virtual environment ("uv.lock",
	// "requirements.txt").
	By      string `json:"by"`
	Range   string `json:"range,omitempty"`   // as By writes it: "^13.0.0", "1.20.8", "==12.3.0", "<2.20.0,>=2.14.0"
	Bundled bool   `json:"bundled,omitempty"` // it came inside By's tarball (npm bundleDependencies)
}

// holder collects the tightest range and the bundle per package. npm keys by name and version (each installed copy is
// resolved on its own); Python by normalized name, since a virtual environment has one copy of each.
type holder struct {
	eco    versions.Ecosystem
	bound  map[[2]string]versions.Bound
	tight  map[[2]string]Hold
	bundle map[[2]string]Hold
}

func newHolder(eco versions.Ecosystem) *holder {
	return &holder{eco: eco, bound: map[[2]string]versions.Bound{}, tight: map[[2]string]Hold{},
		bundle: map[[2]string]Hold{}}
}

// add keeps rng for key when it ends below what key has so far. A range without an upper end, or one that does not
// read, holds nothing.
func (h *holder) add(key [2]string, by, rng string) {
	b, ok := versions.UpperBound(h.eco, rng)
	if !ok || b.None {
		return
	}
	if cur, seen := h.bound[key]; seen && !versions.Tighter(h.eco, b, cur) {
		return
	}
	h.bound[key], h.tight[key] = b, Hold{By: by, Range: strings.TrimSpace(rng)}
}

func (h *holder) addBundle(key [2]string, by string) {
	if _, ok := h.bundle[key]; !ok {
		h.bundle[key] = Hold{By: by, Bundled: true}
	}
}

func (h *holder) key(name, version string) [2]string {
	if h.eco == versions.PyPI {
		return [2]string{versions.NormalizeName(versions.PyPI, name), ""}
	}
	return [2]string{name, version}
}

func (h *holder) apply(loc *Location) {
	for i := range loc.Packages {
		p := &loc.Packages[i]
		k := h.key(p.Name, p.Version)
		if t, ok := h.tight[k]; ok {
			p.Holds = append(p.Holds, t)
		}
		if b, ok := h.bundle[k]; ok {
			p.Holds = append(p.Holds, b)
		}
	}
}

// --- npm ------------------------------------------------------------------------------------------------------------

// npmNode is one installed package folder as the walk found it.
type npmNode struct {
	dir           string
	name, version string
	deps          map[string]string // dependencies and optionalDependencies
	bundles       bool              // bundleDependencies names any, or is true
}

// npmHolds: for each dependency of each installed package, the copy Node.js resolves it to - the nearest
// node_modules/<name> from the dependent's folder upwards - gets the dependent's range; a package below one that
// bundles came in its tarball. top is the folder whose node_modules is the location's.
func npmHolds(loc *Location, nodes []npmNode, top string) {
	at := map[string]int{}
	for i, n := range nodes {
		at[filepath.Clean(n.dir)] = i
	}
	h := newHolder(versions.Npm)
	for _, n := range nodes {
		for dep, rng := range n.deps {
			if j, ok := resolveNpm(at, n.dir, dep, top); ok {
				h.add(h.key(nodes[j].name, nodes[j].version), n.name+"@"+n.version, rng)
			}
		}
	}
	for _, n := range nodes {
		if j, ok := bundler(at, nodes, n.dir, top); ok {
			h.addBundle(h.key(n.name, n.version), nodes[j].name+"@"+nodes[j].version)
		}
	}
	h.apply(loc)
}

// parentPackage: the package folder whose node_modules holds dir (<pkg>/node_modules/[@scope/]<name>); ok is false at
// the top of the location.
func parentPackage(dir, top string) (string, bool) {
	nm := filepath.Dir(dir)
	if strings.HasPrefix(filepath.Base(nm), "@") {
		nm = filepath.Dir(nm)
	}
	if filepath.Base(nm) != "node_modules" {
		return "", false
	}
	p := filepath.Dir(nm)
	if len(p) <= len(top) {
		return "", false
	}
	return p, true
}

func resolveNpm(at map[string]int, from, dep, top string) (int, bool) {
	cur := from
	for {
		if j, ok := at[filepath.Join(cur, "node_modules", filepath.FromSlash(dep))]; ok {
			return j, true
		}
		if cur == top {
			return 0, false
		}
		up, ok := parentPackage(cur, top)
		if !ok {
			up = top // a top-level package: its next stop is the location's node_modules
		}
		cur = up
	}
}

// bundler: the outermost package above dir that bundles its dependencies.
func bundler(at map[string]int, nodes []npmNode, dir, top string) (int, bool) {
	found := -1
	for cur := dir; ; {
		p, ok := parentPackage(cur, top)
		if !ok {
			break
		}
		j, ok := at[p]
		if !ok {
			break
		}
		if nodes[j].bundles {
			found = j
		}
		cur = p
	}
	return found, found >= 0
}

// bundles reads bundleDependencies (or its old spelling): a list of names, or true for all.
func bundles(raw ...json.RawMessage) bool {
	for _, r := range raw {
		var all bool
		if json.Unmarshal(r, &all) == nil && all {
			return true
		}
		var names []string
		if json.Unmarshal(r, &names) == nil && len(names) > 0 {
			return true
		}
	}
	return false
}

// --- Python ---------------------------------------------------------------------------------------------------------

type depSpec struct{ name, spec string }

// requirementSpec: the specifier of a Requires-Dist line that holds everywhere - none with an environment marker, an
// extra or a direct reference ("name @ URL"), and none without a specifier.
func requirementSpec(req string) (string, bool) {
	if strings.ContainsAny(req, ";@") {
		return "", false
	}
	rest := strings.TrimLeftFunc(req, func(r rune) bool {
		return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.'
	})
	rest = strings.TrimSpace(rest)
	if strings.HasPrefix(rest, "[") { // extras of the dependency itself
		i := strings.IndexByte(rest, ']')
		if i < 0 {
			return "", false
		}
		rest = strings.TrimSpace(rest[i+1:])
	}
	return rest, rest != ""
}

// pythonHolds: the ranges of the distributions in the environment, and, for one in a project folder (.venv, venv), the
// project's lock and requirements files.
func pythonHolds(loc *Location, dists []dist) {
	h := newHolder(versions.PyPI)
	for _, d := range dists {
		for _, s := range d.specs {
			h.add(h.key(s.name, ""), d.name+"@"+d.version, s.spec)
		}
	}
	if loc.Kind == KindVenv {
		if base := filepath.Base(loc.Path); base == ".venv" || base == "venv" {
			projectHolds(h, filepath.Dir(loc.Path))
		}
	}
	h.apply(loc)
}

// maxProjectFile bounds a lock or requirements file; a lock of a large project has a few MB.
const maxProjectFile = 16 << 20

func projectHolds(h *holder, dir string) {
	for _, f := range []string{"uv.lock", "poetry.lock", "pdm.lock"} {
		for name, version := range tomlLockPins(filepath.Join(dir, f)) {
			h.add(h.key(name, ""), f, "=="+version)
		}
	}
	for name, spec := range pipfileLockPins(filepath.Join(dir, "Pipfile.lock")) {
		h.add(h.key(name, ""), "Pipfile.lock", spec)
	}
	reqs, _ := filepath.Glob(filepath.Join(dir, "requirements*.txt"))
	slices.Sort(reqs)
	for _, r := range reqs {
		for _, s := range requirementsFile(r) {
			h.add(h.key(s.name, ""), filepath.Base(r), s.spec)
		}
	}
}

// tomlLockPins: name -> version from the [[package]] tables of uv.lock, poetry.lock and pdm.lock. A name listed with
// more than one version is left out: which one applies depends on the environment.
func tomlLockPins(path string) map[string]string {
	f, err := os.Open(path)
	if err != nil {
		return nil // no such file in this project
	}
	defer f.Close()
	pins, multi := map[string]string{}, map[string]bool{}
	var name, version string
	in := false
	flush := func() {
		if in && name != "" && version != "" {
			k := versions.NormalizeName(versions.PyPI, name)
			if v, ok := pins[k]; ok && v != version {
				multi[k] = true
			}
			pins[k] = version
		}
		name, version, in = "", "", false
	}
	sc := bufio.NewScanner(io.LimitReader(f, maxProjectFile))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "[[package]]":
			flush()
			in = true
			continue
		case strings.HasPrefix(line, "["):
			flush() // another table: the package's own keys are over
			continue
		case !in:
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		switch strings.TrimSpace(k) {
		case "name":
			if name == "" {
				name = v
			}
		case "version":
			if version == "" {
				version = v
			}
		}
	}
	flush()
	for k := range multi {
		delete(pins, k)
	}
	return pins
}

// pipfileLockPins: name -> specifier ("==1.2.3") from Pipfile.lock.
func pipfileLockPins(path string) map[string]string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil // no such file in this project
	}
	var lf map[string]json.RawMessage // "_meta" has another shape than the sections
	if json.Unmarshal(raw, &lf) != nil {
		return nil
	}
	out := map[string]string{}
	for _, section := range []string{"default", "develop"} {
		var pkgs map[string]struct {
			Version string `json:"version"`
		}
		if json.Unmarshal(lf[section], &pkgs) != nil {
			continue
		}
		for name, e := range pkgs {
			if e.Version != "" {
				out[name] = e.Version
			}
		}
	}
	return out
}

// requirementsFile: the requirements with a specifier, without options, includes, direct references and markers.
func requirementsFile(path string) []depSpec {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []depSpec
	sc := bufio.NewScanner(io.LimitReader(f, maxProjectFile))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line, _, _ := strings.Cut(sc.Text(), "#")
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "-") || strings.Contains(line, "://") {
			continue
		}
		name, ok := requirementName(line)
		if !ok {
			continue
		}
		if spec, ok := requirementSpec(line); ok {
			out = append(out, depSpec{name, spec})
		}
	}
	return out
}
