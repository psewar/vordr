// Package inventory finds the packages of language package managers on a host - pip, pipx and virtual environments
// for Python, npm for Node.js (global, Node-RED, projects) - by reading what they installed: the metadata in
// site-packages and the package.json files under node_modules. It starts no package manager and asks no registry;
// the hub matches the result against its OSV data (design/architecture.md, section 11.2).
package inventory

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/versions"
)

// Kinds of location.
const (
	KindNpmGlobal = "npm-global" // node_modules of "npm install -g"
	KindNpmDir    = "npm-dir"    // a folder with package.json and node_modules: Node-RED's user folder, a project
	KindPipSite   = "pip-site"   // site-packages of an interpreter (pip system-wide or --user)
	KindVenv      = "venv"       // a virtual environment (pyvenv.cfg), pipx apps included
	// KindContainer: the images of the running containers (ADR-0113, package images), ecosystem oci. Only reported
	// to a hub that announces it (wire.FeatureContainers), and never updated by Vördr.
	KindContainer = "container"
)

// Location codes: what kept a location from being read completely. Empty: read.
const (
	CodeUnreadable      = "unreadable"        // the folder could not be read
	CodeTooManyPackages = "too_many_packages" // more than MaxPackages; the rest is left out
)

// MaxPackages bounds one location; a project folder with a few thousand npm packages is normal, this is not.
const MaxPackages = 20000

// Location is one place packages are installed, with what is installed there.
type Location struct {
	ID        string             `json:"id"` // stable, like an app id: npm-global, node-red, venv-web-app
	Kind      string             `json:"kind"`
	Ecosystem versions.Ecosystem `json:"ecosystem"`
	Path      string             `json:"path"`
	// Code says why the list is incomplete; Skipped counts entries with a name or version no registry would accept
	// (a broken or hostile package.json), which never leave the host.
	Code     string    `json:"code,omitempty"`
	Skipped  int       `json:"skipped,omitempty"`
	Packages []Package `json:"packages"`
	// OutdatedFailed: direct packages whose registry gave no answer in the last outdated run (11.3);
	// NotInRegistry: those the registry does not have (installed from a file, a git URL, a private index).
	OutdatedFailed int `json:"outdated_failed,omitempty"`
	NotInRegistry  int `json:"not_in_registry,omitempty"`
	// Update: the local policy lets Home Assistant update packages here (packages.update), and this agent can (11.4).
	Update bool `json:"update,omitempty"`
	// Deps: Update, and this agent can also refresh the dependencies here that have findings (ADR-0104): so far an npm
	// folder with a lock file (Refreshable). An agent before that does not report it, and the hub sends it no such job.
	Deps bool `json:"deps,omitempty"`
	// ComparedAt (kind container, ADR-0113): when the images were last compared with their registries; nil while
	// they never were - then whether any is outdated is unknown, not "none".
	ComparedAt *time.Time `json:"compared_at,omitempty"`
}

// Updatable reports whether this agent can update packages of a location of this kind: virtual environments
// (pipx included) and npm. pip outside a virtual environment is left to the system or the user.
func Updatable(kind string) bool {
	return kind == KindVenv || kind == KindNpmGlobal || kind == KindNpmDir
}

// Package is one installed package. Each name and version appears once per location.
type Package struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	// Direct: installed for its own sake - a top-level global npm package, a dependency named in package.json, a
	// Python distribution no other one in the environment requires. The others came along as dependencies.
	Direct bool `json:"direct,omitempty"`
	// Via: for an npm package nested below a top-level one, that top-level package (updating it is what helps).
	Via string `json:"via,omitempty"`
	// Holds (ADR-0129): what keeps it below a version - the tightest range among what depends on it here, and the
	// bundle it came in. At most two; none when nothing with an upper end holds it.
	Holds []Hold `json:"holds,omitempty"`
	// Latest and Candidate (direct packages, 11.3): the newest release an update could reach, and the newest one
	// public for at least min_age_days - the only kind an update installs. Empty: none newer, or not asked.
	Latest    string `json:"latest,omitempty"`
	Candidate string `json:"candidate,omitempty"`
	// SecurityCandidate: the newest release public for at least min_age_days_security, when it is newer than
	// Candidate (ADR-0111). Only the hub can tell whether it fixes an advisory; an update takes it only for the packages
	// the hub names in the job.
	SecurityCandidate string `json:"security_candidate,omitempty"`
	// LatestAt: when Latest was published, while it is still too new to be the candidate.
	LatestAt *time.Time `json:"latest_at,omitempty"`
	Major    bool       `json:"major,omitempty"` // Latest changes the major version

	// Container images (kind container, ADR-0113): Name is the repository with its registry, Version the tag, and
	// Latest/Candidate a newer tag of the same form. A name and tag can appear once per image they point to.
	// Digest: the repo digest the running image was pulled by; "" when it was built locally (never compared).
	Digest     string   `json:"digest,omitempty"`
	Containers []string `json:"containers,omitempty"` // the running containers that use the image
	// Rebuilt: the registry's digest of the same tag, when it points to another build now; RebuiltAt: when that
	// build was made (nil: unknown); RebuiltCandidate: it is at least min_age_days old.
	Rebuilt          string     `json:"rebuilt,omitempty"`
	RebuiltAt        *time.Time `json:"rebuilt_at,omitempty"`
	RebuiltCandidate bool       `json:"rebuilt_candidate,omitempty"`
	// SameMajor (ADR-0131): when Candidate is a new major version, or Latest is one and there is no Candidate, the
	// newest tag of the running major version that is at least min_age_days old - the smaller step. Empty otherwise.
	SameMajor string `json:"same_major,omitempty"`
	// OS: what the running image carries inside, read from one of its containers (ADR-0120). nil: not read - the hub
	// did not ask for it, or the image waits for its next comparison.
	OS *ImageOS `json:"os,omitempty"`
	// Updatable (kind container, ADR-0119): the Compose services running the image whose update the local policy
	// allows, "<project>/<service>". Only for an image the agent can move: a tag without a pinned digest, pulled from
	// its registry.
	Updatable []string `json:"updatable,omitempty"`
	// CompareFailed (kind container, ADR-0126): why the image's comparison with its registry got no answer in the
	// last run, one of the Compare codes; such an image counts in the location's OutdatedFailed. Empty: answered.
	CompareFailed string `json:"compare_failed,omitempty"`
}

// Why an image's comparison with its registry got no answer (Package.CompareFailed, ADR-0126).
const (
	CompareNoAnswer       = "no_answer"         // network, server error, a malformed reply, the run's time ran out
	CompareRateLimited    = "rate_limited"      // the registry said 429
	CompareRequestBudget  = "request_budget"    // the run's requests were used up before it came to this image
	CompareManifestShare  = "manifest_share"    // the run's manifest downloads for this registry were used up
	CompareTagListTooLong = "tag_list_too_long" // more tags than the agent reads (ADR-0121)
)

// ImageOS: the distribution inside an image and its packages, as the image's own package database lists them.
type ImageOS struct {
	ID      string `json:"id,omitempty"`      // os-release ID: debian, alpine, ubuntu
	Version string `json:"version,omitempty"` // os-release VERSION_ID: 13, 3.23.4, 24.04
	// Code: why Packages is missing or short - a state, not a text (ADR-0007). "": all of them read.
	Code     string      `json:"code,omitempty"`
	Packages []OSPackage `json:"packages,omitempty"`
}

// Codes of ImageOS, besides CodeUnreadable (the engine did not hand the files out) and CodeTooManyPackages (more than
// the agent reports per image; the rest is left out).
const (
	CodeNoPackageDB    = "no_package_db"   // neither a dpkg nor an apk database: distroless, scratch, another kind
	CodeUnknownRelease = "unknown_release" // no os-release: the distribution and its release are unknown
)

// OSPackage: one installed package of an image. Source and SourceVersion name the source package when it differs
// (dpkg's Source field, apk's origin): distributions write their advisories for those.
type OSPackage struct {
	Name          string `json:"name"`
	Version       string `json:"version"`
	Source        string `json:"source,omitempty"`
	SourceVersion string `json:"source_version,omitempty"`
}

// Configured is a location agent.yaml names: Path may contain "*", then every match is a location of its own.
type Configured struct {
	ID   string
	Path string
}

// Options says where to look.
type Options struct {
	Detect     bool         // also the well-known places (Detected)
	Configured []Configured // from agent.yaml
	Now        func() time.Time
}

// Collect reads every location. Locations found twice (detected and configured) are read once, under the
// configured id.
func Collect(o Options) []Location { return collect(o, Detected) }

// collect is Collect with the well-known places as a parameter (tests).
func collect(o Options, detected func() []Found) []Location {
	type candidate struct{ id, kind, path string }
	var cands []candidate
	for _, c := range o.Configured {
		for _, m := range expand(c) {
			for _, k := range classify(m.path) {
				id := m.id
				if k.suffix != "" {
					id = trimID(id + "-" + k.suffix)
				}
				cands = append(cands, candidate{id, k.kind, k.path})
			}
		}
	}
	if o.Detect {
		for _, d := range detected() {
			cands = append(cands, candidate{d.ID, d.Kind, d.Path})
		}
	}
	seenPath := map[string]bool{}
	seenID := map[string]int{}
	var out []Location
	for _, c := range cands {
		key := filepath.Clean(c.path)
		if runtime.GOOS == "windows" { // Windows ignores case: agent.yaml may write a path differently than Glob finds it
			key = strings.ToLower(key)
		}
		if seenPath[key] {
			continue
		}
		seenPath[key] = true
		id := c.id
		if n := seenID[id]; n > 0 { // two detected places with the same name: number them
			id = trimID(id + "-" + strconv.Itoa(n+1))
		}
		seenID[c.id]++
		out = append(out, read(id, c.kind, c.path))
	}
	return out
}

func read(id, kind, path string) Location {
	loc := Location{ID: id, Kind: kind, Path: path, Packages: []Package{}}
	switch kind {
	case KindNpmGlobal, KindNpmDir:
		loc.Ecosystem = versions.Npm
		readNpm(&loc)
	default:
		loc.Ecosystem = versions.PyPI
		readPython(&loc)
	}
	slices.SortFunc(loc.Packages, func(a, b Package) int {
		if c := strings.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return strings.Compare(a.Version, b.Version)
	})
	return loc
}

type match struct{ id, path string }

// expand resolves a configured path; with "*" each match gets "<id>-<what the * matched>": /opt/*/venv gives
// services-web-app for /opt/web-app/venv, not services-venv for every service.
func expand(c Configured) []match {
	if !strings.Contains(c.Path, "*") {
		return []match{{c.ID, c.Path}}
	}
	paths, _ := filepath.Glob(c.Path) // the pattern was checked by check-config; no match is no location
	var out []match
	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			out = append(out, match{trimID(c.ID + "-" + strings.Join(starParts(c.Path, p), "-")), p})
		}
	}
	return out
}

type kindAt struct{ kind, path, suffix string }

// classify says what a configured folder holds: a virtual environment, a project with one (.venv, venv), an npm
// folder, a site-packages folder. A project with both Python and npm packages yields two locations, "-py" and "-npm".
func classify(dir string) []kindAt {
	var out []kindAt
	base := strings.ToLower(filepath.Base(dir))
	switch {
	case exists(filepath.Join(dir, "pyvenv.cfg")):
		out = append(out, kindAt{KindVenv, dir, ""})
	case exists(filepath.Join(dir, ".venv", "pyvenv.cfg")):
		out = append(out, kindAt{KindVenv, filepath.Join(dir, ".venv"), ""})
	case exists(filepath.Join(dir, "venv", "pyvenv.cfg")):
		out = append(out, kindAt{KindVenv, filepath.Join(dir, "venv"), ""})
	case base == "site-packages" || base == "dist-packages":
		out = append(out, kindAt{KindPipSite, dir, ""})
	}
	switch {
	case filepath.Base(dir) == "node_modules":
		out = append(out, kindAt{KindNpmGlobal, dir, ""})
	case exists(filepath.Join(dir, "package.json")) && exists(filepath.Join(dir, "node_modules")):
		out = append(out, kindAt{KindNpmDir, dir, ""})
	}
	if len(out) == 2 {
		out[0].suffix, out[1].suffix = "py", "npm"
	}
	return out
}

var notID = regexp.MustCompile(`[^a-z0-9_-]+`)

func sanitize(s string) string {
	s = strings.Trim(notID.ReplaceAllString(strings.ToLower(s), "-"), "-_")
	if s == "" {
		return "x"
	}
	return s
}

// trimID keeps an id within what protocol.ValidAppID allows (32 characters).
func trimID(id string) string {
	id = sanitize(id)
	if len(id) > 32 {
		id = strings.TrimRight(id[:32], "-_")
	}
	return id
}

// ValidID reports whether id can name a location (the rules of app ids).
func ValidID(id string) bool { return protocol.ValidAppID(id) }

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

var (
	// PyPI: PEP 508 names. npm: the registry's rules, old mixed-case names included.
	pypiName = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._-]{0,98}[A-Za-z0-9])?$`)
	npmName  = regexp.MustCompile(`^(?:@[A-Za-z0-9][A-Za-z0-9-._~]{0,99}/)?[A-Za-z0-9][A-Za-z0-9-._~]{0,213}$`) // never a leading "-": no option
)

// validEntry: names and versions become arguments of pip and npm later (updates) and travel to the hub; anything
// else stays on the host.
func validEntry(eco versions.Ecosystem, name, version string) bool {
	if len(version) == 0 || len(version) > 64 || strings.ContainsAny(version, " \t\r\n\"'`$\\") {
		return false
	}
	if eco == versions.Npm {
		return npmName.MatchString(name) && len(name) <= 214
	}
	return pypiName.MatchString(name)
}
