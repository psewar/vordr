// Package kev reads the catalog of Known Exploited Vulnerabilities that CISA publishes
// (https://www.cisa.gov/known-exploited-vulnerabilities-catalog, licence CC0 1.0): CVEs with reliable evidence that
// they are exploited. The hub fetches the whole catalog, like the OSV dumps, and looks CVE ids up locally, so no
// finding of a host leaves the network (ADR-0168).
package kev

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"time"
)

// MaxFeedSize bounds the JSON feed; it has about 1.9 MB (November 2026).
const MaxFeedSize = 64 << 20

// RansomwareKnown is the value of knownRansomwareCampaignUse when CISA knows the CVE to be used in ransomware
// campaigns; the other value the schema describes is "Unknown".
const RansomwareKnown = "Known"

// Entry is what the catalog says about one CVE.
type Entry struct {
	Added      string // dateAdded: when the CVE entered the catalog, YYYY-MM-DD (not when its exploitation began)
	Ransomware bool   // knownRansomwareCampaignUse is "Known"
}

// Catalog is one release of the catalog.
type Catalog struct {
	Version  string           // catalogVersion
	Released time.Time        // dateReleased
	Entries  map[string]Entry // by CVE id
	// Skipped: entries left out because their cveID or dateAdded does not read; counted, never dropped silently.
	Skipped int
}

// cveID is the pattern of the schema (known_exploited_vulnerabilities_schema.json).
var cveID = regexp.MustCompile(`^CVE-[0-9]{4}-[0-9]{4,19}$`)

type feed struct {
	CatalogVersion  string    `json:"catalogVersion"`
	DateReleased    time.Time `json:"dateReleased"`
	Count           *int      `json:"count"`
	Vulnerabilities *[]struct {
		CVEID                      string `json:"cveID"`
		DateAdded                  string `json:"dateAdded"`
		KnownRansomwareCampaignUse string `json:"knownRansomwareCampaignUse"`
	} `json:"vulnerabilities"`
}

// Parse reads the JSON feed. It refuses what is not the catalog: no catalogVersion, dateReleased, count or
// vulnerabilities, or a count that differs from the number of entries (a truncated or partial feed). An entry whose
// cveID or dateAdded does not read is left out and counted in Skipped; a CVE listed twice keeps its earliest date and
// the ransomware mark of either.
func Parse(raw []byte) (*Catalog, error) {
	var f feed
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, err
	}
	switch {
	case f.CatalogVersion == "":
		return nil, errors.New("no catalogVersion")
	case f.DateReleased.IsZero():
		return nil, errors.New("no dateReleased")
	case f.Count == nil || f.Vulnerabilities == nil:
		return nil, errors.New("no count or no vulnerabilities")
	case *f.Count != len(*f.Vulnerabilities):
		return nil, fmt.Errorf("count %d, but %d entries", *f.Count, len(*f.Vulnerabilities))
	}
	c := &Catalog{Version: f.CatalogVersion, Released: f.DateReleased.UTC(), Entries: map[string]Entry{}}
	for _, v := range *f.Vulnerabilities {
		if !cveID.MatchString(v.CVEID) || !validDate(v.DateAdded) {
			c.Skipped++
			continue
		}
		e := Entry{Added: v.DateAdded, Ransomware: v.KnownRansomwareCampaignUse == RansomwareKnown}
		if old, ok := c.Entries[v.CVEID]; ok {
			e.Added = min(e.Added, old.Added) // YYYY-MM-DD orders as text
			e.Ransomware = e.Ransomware || old.Ransomware
		}
		c.Entries[v.CVEID] = e
	}
	return c, nil
}

func validDate(s string) bool {
	_, err := time.Parse(time.DateOnly, s)
	return err == nil && len(s) == len(time.DateOnly)
}

// Match is what the catalog says about the CVEs of one finding.
type Match struct {
	CVEs       []string // the ones it lists, in the order given
	Added      string   // the earliest date one of them was added
	Ransomware bool     // one of them is known to be used in ransomware campaigns
}

// Listed reports whether the catalog lists any of the CVEs.
func (m Match) Listed() bool { return len(m.CVEs) > 0 }

// Match looks the CVE ids of one finding up; ids it does not list, or that are given twice, count once or not at all.
func (c *Catalog) Match(cves []string) Match {
	var m Match
	for _, id := range cves {
		e, ok := c.Entries[id]
		if !ok || slices.Contains(m.CVEs, id) {
			continue
		}
		m.CVEs = append(m.CVEs, id)
		if m.Added == "" || e.Added < m.Added {
			m.Added = e.Added
		}
		m.Ransomware = m.Ransomware || e.Ransomware
	}
	return m
}

// String names the catalog for logs: "catalog 2026.11.13 of 2026-11-13: 1781 CVEs".
func (c *Catalog) String() string {
	s := fmt.Sprintf("catalog %s of %s: %d CVEs", c.Version, c.Released.Format(time.DateOnly), len(c.Entries))
	if c.Skipped > 0 {
		s += fmt.Sprintf(" (%d entries unreadable, left out)", c.Skipped)
	}
	return s
}
