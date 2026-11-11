package inventory

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/psewar/vordr/internal/versions"
)

// maxManifest bounds one METADATA or package.json; real ones are a few kB.
const maxManifest = 1 << 20

// collector keeps each name and version once per location; nodes: every npm package folder it read (ADR-0129).
type collector struct {
	loc   *Location
	index map[[2]string]int
	nodes []npmNode
}

func newCollector(loc *Location) *collector { return &collector{loc: loc, index: map[[2]string]int{}} }

func (c *collector) add(p Package) {
	if !validEntry(c.loc.Ecosystem, p.Name, p.Version) {
		c.loc.Skipped++
		return
	}
	key := [2]string{p.Name, p.Version}
	if i, ok := c.index[key]; ok {
		c.loc.Packages[i].Direct = c.loc.Packages[i].Direct || p.Direct
		if p.Direct {
			c.loc.Packages[i].Via = ""
		}
		return
	}
	if len(c.loc.Packages) >= MaxPackages {
		c.loc.Code = CodeTooManyPackages
		return
	}
	c.index[key] = len(c.loc.Packages)
	c.loc.Packages = append(c.loc.Packages, p)
}

// --- Python -------------------------------------------------------------------------------------------------------

// sitePackages: the folders a location's distributions live in.
func sitePackages(loc *Location) []string {
	if loc.Kind == KindPipSite {
		return []string{loc.Path}
	}
	// Linux and macOS: lib/python3.X/site-packages; Windows: Lib\site-packages (Glob matches case as the
	// file system does).
	var out []string
	for _, pattern := range []string{filepath.Join("lib", "python*", "site-packages"), filepath.Join("Lib", "site-packages")} {
		m, _ := filepath.Glob(filepath.Join(loc.Path, pattern))
		for _, p := range m {
			if !containsPath(out, p) {
				out = append(out, p)
			}
		}
	}
	return out
}

func containsPath(list []string, p string) bool {
	for _, q := range list {
		if strings.EqualFold(filepath.Clean(q), filepath.Clean(p)) {
			return true
		}
	}
	return false
}

type dist struct {
	name, version string
	requires      []string  // normalized names this one needs (extras left out)
	specs         []depSpec // of those, the ones with a specifier that holds everywhere (ADR-0129)
}

func readPython(loc *Location) {
	sites := sitePackages(loc)
	if len(sites) == 0 {
		loc.Code = CodeUnreadable
		return
	}
	var dists []dist
	for _, site := range sites {
		entries, err := os.ReadDir(site)
		if err != nil {
			loc.Code = CodeUnreadable
			continue
		}
		for _, e := range entries {
			var meta string
			switch name := e.Name(); {
			case strings.HasSuffix(name, ".dist-info") && e.IsDir():
				meta = filepath.Join(site, name, "METADATA")
			case strings.HasSuffix(name, ".egg-info") && e.IsDir():
				meta = filepath.Join(site, name, "PKG-INFO")
			case strings.HasSuffix(name, ".egg-info"):
				meta = filepath.Join(site, name) // an old-style egg-info file is PKG-INFO itself
			default:
				continue
			}
			if d, ok := readMetadata(meta); ok {
				dists = append(dists, d)
			} else {
				loc.Skipped++
			}
		}
	}
	required := map[string]bool{}
	for _, d := range dists {
		for _, r := range d.requires {
			required[r] = true
		}
	}
	c := newCollector(loc)
	for _, d := range dists {
		c.add(Package{Name: d.name, Version: d.version, Direct: !required[versions.NormalizeName(versions.PyPI, d.name)]})
	}
	pythonHolds(loc, dists)
}

// readMetadata reads the header of a METADATA/PKG-INFO file (email header format): Name, Version, Requires-Dist.
func readMetadata(path string) (dist, bool) {
	f, err := os.Open(path)
	if err != nil {
		return dist{}, false
	}
	defer f.Close()
	var d dist
	sc := bufio.NewScanner(io.LimitReader(f, maxManifest))
	sc.Buffer(make([]byte, 64<<10), 64<<10)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			break // the body (long description) starts
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.ToLower(key) {
		case "name":
			d.name = value
		case "version":
			d.version = value
		case "requires-dist":
			if r, ok := requirementName(value); ok {
				d.requires = append(d.requires, r)
				if spec, ok := requirementSpec(value); ok {
					d.specs = append(d.specs, depSpec{r, spec})
				}
			}
		}
	}
	return d, d.name != "" && d.version != ""
}

// requirementName: the distribution a Requires-Dist line names, unless only an extra needs it ("x; extra == 'y'").
func requirementName(req string) (string, bool) {
	spec, marker, _ := strings.Cut(req, ";")
	if strings.Contains(strings.ReplaceAll(marker, " ", ""), "extra==") {
		return "", false
	}
	end := strings.IndexFunc(spec, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.')
	})
	if end < 0 {
		end = len(spec)
	}
	name := strings.TrimSpace(spec[:end])
	return versions.NormalizeName(versions.PyPI, name), name != ""
}

// --- npm ----------------------------------------------------------------------------------------------------------

type packageJSON struct {
	Name                 string            `json:"name"`
	Version              string            `json:"version"`
	Dependencies         map[string]string `json:"dependencies"`
	DevDependencies      map[string]string `json:"devDependencies"`
	OptionalDependencies map[string]string `json:"optionalDependencies"`
	// a list of names or true; npm reads both spellings
	BundleDependencies  json.RawMessage `json:"bundleDependencies"`
	BundledDependencies json.RawMessage `json:"bundledDependencies"`
}

func readPackageJSON(path string) (packageJSON, bool) {
	f, err := os.Open(path)
	if err != nil {
		return packageJSON{}, false
	}
	defer f.Close()
	var p packageJSON
	if json.NewDecoder(io.LimitReader(f, maxManifest)).Decode(&p) != nil {
		return packageJSON{}, false
	}
	return p, true
}

// maxDepth bounds nesting below node_modules (npm hoists; real trees are a few levels deep).
const maxDepth = 32

func readNpm(loc *Location) {
	root := loc.Path
	direct := map[string]bool{} // npm-dir: what package.json asks for
	if loc.Kind == KindNpmDir {
		root = filepath.Join(loc.Path, "node_modules")
		if p, ok := readPackageJSON(filepath.Join(loc.Path, "package.json")); ok {
			for _, deps := range []map[string]string{p.Dependencies, p.DevDependencies, p.OptionalDependencies} {
				for name := range deps {
					direct[name] = true
				}
			}
		}
	}
	if _, err := os.ReadDir(root); err != nil {
		loc.Code = CodeUnreadable
		return
	}
	c := newCollector(loc)
	walkNodeModules(c, root, "", 0, func(dirName string) bool {
		return loc.Kind == KindNpmGlobal || direct[dirName]
	})
	npmHolds(loc, c.nodes, filepath.Clean(filepath.Dir(root)))
}

// walkNodeModules reads every package folder below nm. Top-level ones (depth 0) are direct when isDirect says so;
// nested ones carry the top-level package they belong to. Linked packages (npm link) are read but not descended
// into: they are someone's working copy.
func walkNodeModules(c *collector, nm, via string, depth int, isDirect func(string) bool) {
	if depth > maxDepth || c.loc.Code == CodeTooManyPackages {
		return
	}
	entries, err := os.ReadDir(nm)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue // .bin, .package-lock.json, .cache
		}
		if strings.HasPrefix(name, "@") {
			scoped, err := os.ReadDir(filepath.Join(nm, name))
			if err != nil {
				continue
			}
			for _, s := range scoped {
				visitPackage(c, filepath.Join(nm, name, s.Name()), name+"/"+s.Name(), via, depth, isDirect)
			}
			continue
		}
		visitPackage(c, filepath.Join(nm, name), name, via, depth, isDirect)
	}
}

func visitPackage(c *collector, dir, dirName, via string, depth int, isDirect func(string) bool) {
	p, ok := readPackageJSON(filepath.Join(dir, "package.json"))
	if !ok {
		return // not a package (a stray folder)
	}
	name := p.Name
	if name == "" {
		name = dirName
	}
	pkg := Package{Name: name, Version: p.Version}
	if validEntry(c.loc.Ecosystem, name, p.Version) {
		deps := map[string]string{}
		for _, m := range []map[string]string{p.Dependencies, p.OptionalDependencies} {
			for k, v := range m {
				deps[k] = v
			}
		}
		c.nodes = append(c.nodes, npmNode{dir: filepath.Clean(dir), name: name, version: p.Version, deps: deps,
			bundles: bundles(p.BundleDependencies, p.BundledDependencies)})
	}
	top := via
	if depth == 0 {
		pkg.Direct = isDirect(dirName)
		top = name
	} else {
		pkg.Via = via
	}
	c.add(pkg)
	if fi, err := os.Lstat(dir); err == nil && fi.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 { // Windows: junctions
		return
	}
	walkNodeModules(c, filepath.Join(dir, "node_modules"), top, depth+1, isDirect)
}
