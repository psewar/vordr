// Package osv matches installed packages against the OSV data of their ecosystem (https://ossf.github.io/osv-schema/),
// locally: the hub downloads the full dump of each ecosystem and keeps a compact index, so no package list of a host
// ever leaves the network (design/architecture.md, section 11.2). Entries with an id "MAL-" (OpenSSF malicious
// packages) mark versions that are malware, not merely vulnerable.
package osv

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/psewar/vordr/internal/versions"
)

// Event kinds of an OSV range.
const (
	Introduced   = "introduced"
	Fixed        = "fixed"
	LastAffected = "last_affected"
	Limit        = "limit"
)

// DB is the index of one ecosystem's dump.
type DB struct {
	Ecosystem versions.Ecosystem
	Built     time.Time // when the index was built from the dump
	Modified  time.Time // the newest "modified" of any entry: how current the data is
	Entries   int       // advisories that name the ecosystem (withdrawn ones not counted)
	Malicious int       // of those, MAL- entries
	Withdrawn int       // left out: withdrawn by their source (e.g. a false malware report)
	// Packages: normalized package name -> what affects it.
	Packages map[string][]Affected
}

// Affected is one advisory's statement about one package.
type Affected struct {
	ID        string
	Aliases   []string // CVE and GHSA ids, for people who look them up
	Malicious bool
	Severity  string // the source's own rating where it has one (GHSA: LOW, MODERATE, HIGH, CRITICAL)
	// Upstream: the CVE ids the advisory restates (OSV's upstream field). A distribution publishes one CVE several
	// times (Debian: DEBIAN-CVE-x and the DSA or DLA that fixed it); counted once by these (ADR-0120).
	Upstream []string
	// Urgency: the distribution's own rating of the package's exposure (Debian's ecosystem_specific.urgency, e.g.
	// "unimportant": not a security problem in Debian's build).
	Urgency string
	// All: every version is affected (a range introduced at "0" and never fixed) - most malware entries; then Ranges
	// and Versions are not needed.
	All      bool
	Ranges   [][]Event
	Versions []string
}

// Event is one point of an OSV range.
type Event struct {
	Kind    string
	Version string
}

// Finding is an advisory that applies to an installed version.
type Finding struct {
	ID        string   `json:"id"`
	Aliases   []string `json:"aliases,omitempty"`
	Malicious bool     `json:"malicious,omitempty"`
	Severity  string   `json:"severity,omitempty"`
	// Fixed: the versions the advisory names as fixing it (the lowest one above the installed version first).
	Fixed []string `json:"fixed,omitempty"`
	// Upstream and Urgency as in Affected (distributions, ADR-0120).
	Upstream []string `json:"upstream,omitempty"`
	Urgency  string   `json:"urgency,omitempty"`
}

// CVEs: the CVE ids a finding stands for - its own id, its aliases and upstream - so that one CVE published several
// times counts once. Empty for an advisory without one.
func (f Finding) CVEs() []string {
	var out []string
	for _, id := range append(append([]string{f.ID}, f.Aliases...), f.Upstream...) {
		if strings.HasPrefix(id, "CVE-") && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// MaxEntrySize bounds one JSON file of the dump; the largest one in PyPI and npm (October 2026) has well under 1 MB.
const MaxEntrySize = 16 << 20

type record struct {
	ID               string          `json:"id"`
	Modified         time.Time       `json:"modified"`
	Withdrawn        string          `json:"withdrawn"`
	Aliases          []string        `json:"aliases"`
	Upstream         []string        `json:"upstream"`
	DatabaseSpecific json.RawMessage `json:"database_specific"`
	Affected         []struct {
		Package struct {
			Ecosystem string `json:"ecosystem"`
			Name      string `json:"name"`
		} `json:"package"`
		Ranges []struct {
			Type   string              `json:"type"`
			Events []map[string]string `json:"events"`
		} `json:"ranges"`
		Versions          []string `json:"versions"`
		EcosystemSpecific struct {
			Urgency string `json:"urgency"`
		} `json:"ecosystem_specific"`
	} `json:"affected"`
}

// Build reads the dump of one ecosystem (all.zip as osv.dev publishes it) into an index. Entries that do not parse
// count as skipped; one broken file does not stop the rest.
func Build(r io.ReaderAt, size int64, eco versions.Ecosystem, now time.Time) (*DB, int, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, 0, err
	}
	db := &DB{Ecosystem: eco, Built: now.UTC(), Packages: map[string][]Affected{}}
	skipped := 0
	for _, f := range zr.File {
		if f.UncompressedSize64 > MaxEntrySize || !strings.HasSuffix(f.Name, ".json") {
			skipped++
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, 0, err
		}
		var rec record
		err = json.NewDecoder(io.LimitReader(rc, MaxEntrySize)).Decode(&rec)
		rc.Close()
		if err != nil || rec.ID == "" {
			skipped++
			continue
		}
		db.add(rec)
	}
	return db, skipped, nil
}

func (db *DB) add(rec record) {
	named := false
	malicious := strings.HasPrefix(rec.ID, "MAL-")
	var aliases, upstream []string
	for _, a := range rec.Aliases {
		malicious = malicious || strings.HasPrefix(a, "MAL-")
		if strings.HasPrefix(a, "CVE-") || strings.HasPrefix(a, "GHSA-") {
			aliases = append(aliases, a)
		}
	}
	for _, u := range rec.Upstream {
		if strings.HasPrefix(u, "CVE-") {
			upstream = append(upstream, u)
		}
	}
	for _, a := range rec.Affected {
		if versions.Ecosystem(a.Package.Ecosystem) != db.Ecosystem || a.Package.Name == "" {
			continue
		}
		named = true
		if rec.Withdrawn != "" {
			continue
		}
		af := Affected{ID: rec.ID, Aliases: aliases, Malicious: malicious, Severity: severity(rec.DatabaseSpecific),
			Versions: a.Versions, Upstream: upstream, Urgency: a.EcosystemSpecific.Urgency}
		for _, rg := range a.Ranges {
			if rg.Type != "ECOSYSTEM" && rg.Type != "SEMVER" {
				continue // GIT ranges name commits, not versions
			}
			var evs []Event
			for _, e := range rg.Events {
				for k, v := range e {
					evs = append(evs, Event{Kind: k, Version: v})
				}
			}
			af.Ranges = append(af.Ranges, evs)
		}
		if everyVersion(af.Ranges) {
			af.All, af.Ranges, af.Versions = true, nil, nil
		}
		if !af.All && len(af.Ranges) == 0 && len(af.Versions) == 0 {
			continue // nothing that names a version (only GIT ranges)
		}
		name := versions.NormalizeName(db.Ecosystem, a.Package.Name)
		db.Packages[name] = append(db.Packages[name], af)
	}
	if !named {
		return
	}
	if rec.Withdrawn != "" {
		db.Withdrawn++
		return
	}
	db.Entries++
	if malicious {
		db.Malicious++
	}
	if rec.Modified.After(db.Modified) {
		db.Modified = rec.Modified.UTC()
	}
}

// everyVersion: some range is introduced at "0" (also when it repeats "introduced" further up) and none of its
// events ever ends it.
func everyVersion(ranges [][]Event) bool {
	for _, evs := range ranges {
		zero, ends := false, false
		for _, e := range evs {
			switch e.Kind {
			case Introduced:
				zero = zero || e.Version == "0"
			default:
				ends = true
			}
		}
		if zero && !ends {
			return true
		}
	}
	return false
}

func severity(raw json.RawMessage) string {
	var ds struct {
		Severity json.RawMessage `json:"severity"`
	}
	var s string
	if len(raw) == 0 || json.Unmarshal(raw, &ds) != nil || json.Unmarshal(ds.Severity, &s) != nil {
		return "" // no rating, or not as a word
	}
	return strings.ToUpper(s)
}

// Lookup returns the advisories that apply to version v of package name. undecided counts advisories that may apply
// but whose versions do not parse in this ecosystem's order; the caller reports them, it does not count them as
// either.
//
// An advisory can state several ranges for one package in separate entries (urllib3: one for 1.x, one for 2.x); it
// is one finding, and its fixed versions come from all of them.
func (db *DB) Lookup(name, v string) (found []Finding, undecided int) {
	all := db.Packages[versions.NormalizeName(db.Ecosystem, name)]
	hits := map[string]bool{}
	open := map[string]bool{}
	for _, af := range all {
		hit, decided := af.affects(db.Ecosystem, v)
		switch {
		case hit && !hits[af.ID]:
			hits[af.ID] = true
			found = append(found, af.finding(db.Ecosystem, v, all))
		case !hit && !decided:
			open[af.ID] = true
		}
	}
	for id := range open {
		if !hits[id] {
			undecided++
		}
	}
	return found, undecided
}

// Affects reports whether version v of the package is affected; decided is false when v or an event does not parse
// and no versions entry names v.
func (af Affected) affects(eco versions.Ecosystem, v string) (hit, decided bool) {
	if af.All {
		return true, true
	}
	for _, listed := range af.Versions {
		if listed == v {
			return true, true
		}
		if c, ok := versions.Compare(eco, listed, v); ok && c == 0 {
			return true, true // PyPI: 1.0 is 1.0.0
		}
	}
	decided = true
	for _, evs := range af.Ranges {
		in, ok := inRange(eco, v, evs)
		if in {
			return true, true
		}
		decided = decided && ok
	}
	return false, decided
}

// inRange evaluates one range as the OSV schema describes it: sort the events by version; walking up, "introduced"
// at or below v makes it affected, "fixed" at or below and "last_affected" below make it unaffected; "limit" events
// cap the range (v must lie below one of them).
func inRange(eco versions.Ecosystem, v string, evs []Event) (affected, ok bool) {
	if !versions.Valid(eco, v) {
		return false, false
	}
	type point struct {
		Event
		zero bool // introduced "0": below every version
	}
	pts := make([]point, 0, len(evs))
	var limits []string
	for _, e := range evs {
		if e.Kind == Limit {
			limits = append(limits, e.Version)
			continue
		}
		zero := e.Kind == Introduced && e.Version == "0"
		if !zero && !versions.Valid(eco, e.Version) {
			return false, false
		}
		pts = append(pts, point{e, zero})
	}
	// Every event must order against v and against the others. Valid versions the ecosystem cannot order (apk-tools 2
	// and 3 disagree on a few, ADR-0120) leave the range undecided, instead of passing for equal.
	for i, a := range pts {
		if a.zero {
			continue
		}
		if _, ok := versions.Compare(eco, v, a.Version); !ok {
			return false, false
		}
		for _, b := range pts[i+1:] {
			if _, ok := versions.Compare(eco, a.Version, b.Version); !b.zero && !ok {
				return false, false
			}
		}
	}
	slices.SortStableFunc(pts, func(a, b point) int {
		switch {
		case a.zero && b.zero:
			return 0
		case a.zero:
			return -1
		case b.zero:
			return 1
		}
		c, _ := versions.Compare(eco, a.Version, b.Version)
		if c == 0 && a.Kind != b.Kind { // the same version: introduced first, so "introduced X, fixed X" is empty
			if a.Kind == Introduced {
				return -1
			}
			if b.Kind == Introduced {
				return 1
			}
		}
		return c
	})
	for _, p := range pts {
		if p.zero {
			affected = true
			continue
		}
		c, _ := versions.Compare(eco, v, p.Version)
		switch p.Kind {
		case Introduced:
			if c >= 0 {
				affected = true
			}
		case Fixed:
			if c >= 0 {
				affected = false
			}
		case LastAffected:
			if c > 0 {
				affected = false
			}
		}
	}
	if affected && len(limits) > 0 {
		below := false
		for _, l := range limits {
			c, okL := versions.Compare(eco, v, l)
			if !okL && l != "*" {
				return false, false
			}
			below = below || l == "*" || c < 0
		}
		affected = below
	}
	return affected, true
}

// finding: the advisory as it applies to version v; all: every entry of the package, for the fixed versions of the
// same advisory.
func (af Affected) finding(eco versions.Ecosystem, v string, all []Affected) Finding {
	f := Finding{ID: af.ID, Aliases: af.Aliases, Malicious: af.Malicious, Severity: af.Severity, Upstream: af.Upstream,
		Urgency: af.Urgency}
	var above []string
	for _, other := range all {
		if other.ID != af.ID {
			continue
		}
		for _, evs := range other.Ranges {
			for _, e := range evs {
				if e.Kind == Fixed && !slices.Contains(above, e.Version) {
					if c, ok := versions.Compare(eco, e.Version, v); ok && c > 0 {
						above = append(above, e.Version)
					}
				}
			}
		}
	}
	slices.SortFunc(above, func(a, b string) int { c, _ := versions.Compare(eco, a, b); return c })
	f.Fixed = above
	return f
}

// String names the index for logs: "npm: 230000 advisories (220000 malicious), data of 2023-11-14".
func (db *DB) String() string {
	return fmt.Sprintf("%s: %d advisories (%d malicious), data of %s", db.Ecosystem, db.Entries, db.Malicious,
		db.Modified.Format("2006-01-02 15:04 MST"))
}
