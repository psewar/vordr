package inventory

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/psewar/vordr/internal/source"
	"github.com/psewar/vordr/internal/versions"
)

// Outdated (design 11.3): for each package installed for its own sake (direct), the agent asks the package's registry
// which releases exist and when they were published. From that it picks two versions:
//   - Latest: the newest release an update could reach - stable, not yanked (PyPI) or deprecated (npm), and for npm
//     not above the "latest" tag (a higher version without that tag belongs to another line).
//   - Candidate: the newest such release that has been public for at least min_age_days. Only a candidate is ever
//     installed (11.4): most malicious releases are found and pulled within days, so a fresh one is not taken.
// Only names and versions go to the registry, as pip and npm send them anyway; the package list goes nowhere else.

// Release is one version as the registry lists it.
type Release struct {
	Version   string
	Published time.Time // PyPI: the newest upload of the release - files can be added to an old release later
	Withdrawn bool      // yanked (PyPI, all files) or deprecated (npm)
}

// RegistryInfo is what a registry says about one package.
type RegistryInfo struct {
	Releases  []Release
	LatestTag string // npm's dist-tags.latest; "" for PyPI
	NotFound  bool   // the registry does not have the package
}

// Registries are the package indexes the agent asks; defaults: pypi.org and registry.npmjs.org.
type Registries struct {
	PyPI   string
	Npm    string
	Client *http.Client
}

// maxRegistryDoc bounds one answer; npm's full document of a package with thousands of releases has tens of MB.
const maxRegistryDoc = 64 << 20

// Lookup asks the registry of eco about one package.
func (r Registries) Lookup(ctx context.Context, eco versions.Ecosystem, name string) (RegistryInfo, error) {
	var u string
	switch eco {
	case versions.PyPI:
		u = strings.TrimSuffix(r.PyPI, "/") + "/pypi/" + url.PathEscape(name) + "/json"
	case versions.Npm:
		u = strings.TrimSuffix(r.Npm, "/") + "/" + strings.Replace(url.PathEscape(name), "%40", "@", 1)
	default:
		return RegistryInfo{}, fmt.Errorf("no registry for %s", eco)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return RegistryInfo{}, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := r.Client.Do(req)
	if err != nil {
		return RegistryInfo{}, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		// Not an error of the registry: the package is not there (installed from a file, a git URL, a private
		// index). Said as a state, so that it is not counted as a registry that failed.
		return RegistryInfo{NotFound: true}, nil
	default:
		return RegistryInfo{}, fmt.Errorf("%s: HTTP %d", u, resp.StatusCode)
	}
	dec := json.NewDecoder(io.LimitReader(resp.Body, maxRegistryDoc))
	if eco == versions.PyPI {
		return decodePyPI(dec)
	}
	return decodeNpm(dec)
}

// Registry documents are read key by key: npm's full document holds every release's package.json (tens of MB for
// npm itself), and decoding it whole takes several times that, a lot for a small host. Only one value is in memory
// at a time this way.

// eachKey calls fn for every key of the object the decoder stands at; fn must consume the value.
func eachKey(dec *json.Decoder, fn func(key string) error) error {
	t, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := t.(json.Delim); !ok || d != '{' {
		return fmt.Errorf("expected an object, found %v", t)
	}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return err
		}
		key, _ := t.(string) // object keys are strings
		if err := fn(key); err != nil {
			return err
		}
	}
	_, err = dec.Token() // the closing brace
	return err
}

// skipValue consumes one value without keeping it.
func skipValue(dec *json.Decoder) error {
	depth := 0
	for {
		t, err := dec.Token()
		if err != nil {
			return err
		}
		if d, ok := t.(json.Delim); ok {
			if d == '{' || d == '[' {
				depth++
			} else {
				depth--
			}
		}
		if depth == 0 {
			return nil
		}
	}
}

func decodePyPI(dec *json.Decoder) (RegistryInfo, error) {
	var info RegistryInfo
	err := eachKey(dec, func(key string) error {
		if key != "releases" {
			return skipValue(dec) // info holds the whole README
		}
		return eachKey(dec, func(v string) error {
			var files []struct {
				Uploaded time.Time `json:"upload_time_iso_8601"`
				Yanked   bool      `json:"yanked"`
			}
			if err := dec.Decode(&files); err != nil {
				return err
			}
			if len(files) == 0 {
				return nil // a release without files cannot be installed
			}
			rel := Release{Version: v, Withdrawn: true}
			for _, f := range files {
				if f.Uploaded.After(rel.Published) {
					rel.Published = f.Uploaded
				}
				rel.Withdrawn = rel.Withdrawn && f.Yanked
			}
			info.Releases = append(info.Releases, rel)
			return nil
		})
	})
	return info, err
}

func decodeNpm(dec *json.Decoder) (RegistryInfo, error) {
	var tags, times map[string]string
	deprecated := map[string]bool{}
	err := eachKey(dec, func(key string) error {
		switch key {
		case "dist-tags":
			return dec.Decode(&tags)
		case "time":
			return dec.Decode(&times)
		case "versions":
			return eachKey(dec, func(v string) error {
				var meta struct {
					Deprecated json.RawMessage `json:"deprecated"`
				}
				if err := dec.Decode(&meta); err != nil {
					return err
				}
				var why string
				deprecated[v] = len(meta.Deprecated) > 0 && string(meta.Deprecated) != "false" &&
					(json.Unmarshal(meta.Deprecated, &why) != nil || why != "") // npm sets "" to undeprecate
				return nil
			})
		}
		return skipValue(dec)
	})
	if err != nil {
		return RegistryInfo{}, err
	}
	info := RegistryInfo{LatestTag: tags["latest"]}
	for v, dep := range deprecated {
		published, err := time.Parse(time.RFC3339, times[v])
		if err != nil {
			continue // no publish time: its age is unknown, so it can never be a candidate
		}
		info.Releases = append(info.Releases, Release{Version: v, Published: published, Withdrawn: dep})
	}
	return info, nil
}

// Pick chooses Latest and Candidate for an installed version (see the top of this file); "" where there is none.
// latestAt: when Latest was published, so that one can tell when it becomes a candidate.
func Pick(eco versions.Ecosystem, installed string, info RegistryInfo, now time.Time, minAge time.Duration) (latest,
	candidate string, latestAt time.Time) {
	if !versions.Valid(eco, installed) {
		return "", "", time.Time{}
	}
	eligible := func(rel Release) bool {
		pre, ok := versions.Prerelease(eco, rel.Version)
		if !ok || pre || rel.Withdrawn {
			return false
		}
		if c, _ := versions.Compare(eco, rel.Version, installed); c <= 0 {
			return false
		}
		if info.LatestTag != "" && versions.Valid(eco, info.LatestTag) {
			if c, _ := versions.Compare(eco, rel.Version, info.LatestTag); c > 0 {
				return false
			}
		}
		return true
	}
	rels := slices.Clone(info.Releases)
	slices.SortFunc(rels, func(a, b Release) int {
		c, _ := versions.Compare(eco, b.Version, a.Version) // newest first; invalid ones are filtered out anyway
		return c
	})
	for _, rel := range rels {
		if !eligible(rel) {
			continue
		}
		if latest == "" {
			latest, latestAt = rel.Version, rel.Published
		}
		if !rel.Published.IsZero() && now.Sub(rel.Published) >= minAge {
			return latest, rel.Version, latestAt
		}
	}
	return latest, "", latestAt
}

// Outdated fills Latest, Candidate, SecurityCandidate and Major of the direct packages of every location, asking
// each registry once per name. It returns how many lookups failed; failed packages keep no versions, and the location
// says so (outdated_failed). At most maxLookups names are asked per run. securityAge: min_age_days_security
// (ADR-0111); 0 reports no security candidates.
func Outdated(ctx context.Context, reg Registries, locs []Location, now time.Time, minAge, securityAge time.Duration,
	maxLookups int) int {
	type key struct {
		eco  versions.Ecosystem
		name string
	}
	cache := map[key]*RegistryInfo{}
	failed := 0
	for i := range locs {
		loc := &locs[i]
		if loc.Kind == KindContainer { // images have registries of their own (package images, ADR-0113)
			continue
		}
		loc.OutdatedFailed, loc.NotInRegistry = 0, 0
		for j := range loc.Packages {
			p := &loc.Packages[j]
			if !p.Direct {
				continue
			}
			k := key{loc.Ecosystem, versions.NormalizeName(loc.Ecosystem, p.Name)}
			info, seen := cache[k]
			if !seen {
				if len(cache) >= maxLookups || ctx.Err() != nil {
					loc.OutdatedFailed++
					failed++
					continue
				}
				got, err := reg.Lookup(ctx, loc.Ecosystem, p.Name)
				if err != nil {
					cache[k] = nil
				} else {
					cache[k] = &got
				}
				info = cache[k]
			}
			if info == nil {
				loc.OutdatedFailed++
				failed++
				continue
			}
			if info.NotFound {
				loc.NotInRegistry++
				continue
			}
			latest, candidate, latestAt := Pick(loc.Ecosystem, p.Version, *info, now, minAge)
			p.Latest, p.Candidate, p.Major = latest, candidate, latest != "" && source.MajorJump(p.Version, latest)
			p.SecurityCandidate = ""
			if securityAge > 0 && securityAge < minAge && candidate != latest {
				if _, sec, _ := Pick(loc.Ecosystem, p.Version, *info, now, securityAge); sec != candidate {
					p.SecurityCandidate = sec
				}
			}
			if latest != "" && candidate != latest {
				at := latestAt.UTC()
				p.LatestAt = &at
			}
		}
	}
	return failed
}
