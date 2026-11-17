package kev

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// A synthetic feed in the shape of known_exploited_vulnerabilities.json; the CVE ids are made up for the test.
const feedOK = `{
  "title": "CISA Catalog of Known Exploited Vulnerabilities",
  "catalogVersion": "2026.11.10",
  "dateReleased": "2026-11-10T18:00:00.1234Z",
  "count": 5,
  "vulnerabilities": [
    {"cveID": "CVE-2026-0001", "vendorProject": "Example", "product": "Widget", "vulnerabilityName": "x",
     "dateAdded": "2026-11-10", "shortDescription": "x", "requiredAction": "x", "dueDate": "2026-12-01",
     "knownRansomwareCampaignUse": "Known", "notes": "", "cwes": ["CWE-20"]},
    {"cveID": "CVE-2025-0002", "dateAdded": "2025-03-01", "knownRansomwareCampaignUse": "Unknown"},
    {"cveID": "CVE-2025-0002", "dateAdded": "2025-02-01"},
    {"cveID": "CVE-2026-123456", "dateAdded": "2026-01-15"},
    {"cveID": "cve-2024-0003", "dateAdded": "2024-01-01"}
  ]
}`

func TestParse(t *testing.T) {
	c, err := Parse([]byte(feedOK))
	if err != nil {
		t.Fatal(err)
	}
	if c.Version != "2026.11.10" || !c.Released.Equal(time.Date(2026, 11, 10, 18, 0, 0, 123400000, time.UTC)) {
		t.Errorf("catalog: %s %v", c.Version, c.Released)
	}
	// The lowercase id does not match the schema's pattern: left out and counted. The CVE listed twice counts once,
	// with its earliest date.
	if len(c.Entries) != 3 || c.Skipped != 1 {
		t.Errorf("entries %d, skipped %d", len(c.Entries), c.Skipped)
	}
	if e := c.Entries["CVE-2026-0001"]; e.Added != "2026-11-10" || !e.Ransomware {
		t.Errorf("CVE-2026-0001: %+v", e)
	}
	if e := c.Entries["CVE-2025-0002"]; e.Added != "2025-02-01" || e.Ransomware {
		t.Errorf("CVE-2025-0002: %+v", e)
	}
	if _, ok := c.Entries["CVE-2026-123456"]; !ok {
		t.Error("an id with six digits after the year is valid")
	}
	if s := c.String(); !strings.Contains(s, "3 CVEs") || !strings.Contains(s, "1 entries unreadable") {
		t.Errorf("String: %s", s)
	}
}

func TestParseRefusesWhatIsNotTheCatalog(t *testing.T) {
	for name, raw := range map[string]string{
		"not JSON":         `<html>maintenance</html>`,
		"cut off":          feedOK[:len(feedOK)/2],
		"another document": `{"title": "something else"}`,
		"no version":       `{"dateReleased": "2026-11-10T18:00:00Z", "count": 0, "vulnerabilities": []}`,
		"no release date":  `{"catalogVersion": "1", "count": 0, "vulnerabilities": []}`,
		"no count":         `{"catalogVersion": "1", "dateReleased": "2026-11-10T18:00:00Z", "vulnerabilities": []}`,
		"no entries":       `{"catalogVersion": "1", "dateReleased": "2026-11-10T18:00:00Z", "count": 0}`,
		"count differs": `{"catalogVersion": "1", "dateReleased": "2026-11-10T18:00:00Z", "count": 2, "vulnerabilities": ` +
			`[{"cveID": "CVE-2026-0001", "dateAdded": "2026-11-10"}]}`,
		"date unreadable": `{"catalogVersion": "1", "dateReleased": "10.11.2026", "count": 0, "vulnerabilities": []}`,
	} {
		if c, err := Parse([]byte(raw)); err == nil {
			t.Errorf("%s: taken as %+v", name, c)
		}
	}
	// An entry with a date that does not read is left out, the rest of the catalog stays.
	c, err := Parse([]byte(`{"catalogVersion": "1", "dateReleased": "2026-11-10T18:00:00Z", "count": 3, ` +
		`"vulnerabilities": [{"cveID": "CVE-2026-0001", "dateAdded": "2026-11-10"}, {"cveID": "CVE-2026-0002", ` +
		`"dateAdded": "10.11.2026"}, {"cveID": "CVE-2026-0003", "dateAdded": "2026-11-1"}]}`))
	if err != nil || len(c.Entries) != 1 || c.Skipped != 2 {
		t.Errorf("unreadable dates: %+v, %v", c, err)
	}
}

func TestMatch(t *testing.T) {
	c, err := Parse([]byte(feedOK))
	if err != nil {
		t.Fatal(err)
	}
	// Several aliases of one finding: the listed ones in the given order, the earliest date, ransomware from either.
	m := c.Match([]string{"CVE-2020-9999", "CVE-2025-0002", "CVE-2026-0001", "CVE-2025-0002"})
	if !m.Listed() || !slices.Equal(m.CVEs, []string{"CVE-2025-0002", "CVE-2026-0001"}) || m.Added != "2025-02-01" ||
		!m.Ransomware {
		t.Errorf("several: %+v", m)
	}
	if m := c.Match([]string{"CVE-2020-9999"}); m.Listed() || m.Added != "" || m.Ransomware {
		t.Errorf("not listed: %+v", m)
	}
	if m := c.Match(nil); m.Listed() {
		t.Errorf("no CVEs: %+v", m)
	}
	if m := c.Match([]string{"CVE-2026-123456"}); !m.Listed() || m.Ransomware || m.Added != "2026-01-15" {
		t.Errorf("without ransomware: %+v", m)
	}
}
