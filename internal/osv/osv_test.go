package osv

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/versions"
)

// dump zips testdata/<eco>/*.json the way osv.dev publishes all.zip. The files are real entries of October 2026
// (sources: GitHub Advisory Database CC-BY 4.0, PyPA advisory database CC-BY 4.0, OpenSSF malicious-packages
// Apache-2.0, EEF CNA), chosen for what makes ranges hard: pre-releases, post-releases, last_affected, several
// ranges, versions lists only, withdrawn malware.
func dump(t *testing.T, eco versions.Ecosystem) *DB {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	files, _ := filepath.Glob(filepath.Join("testdata", string(eco), "*.json"))
	if len(files) == 0 {
		t.Fatalf("no testdata for %s", eco)
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		w, _ := zw.Create(filepath.Base(f))
		_, _ = w.Write(raw)
	}
	w, _ := zw.Create("broken.json")
	_, _ = w.Write([]byte("{not json"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	db, skipped, err := Build(bytes.NewReader(buf.Bytes()), int64(buf.Len()), eco, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if skipped != 1 {
		t.Errorf("skipped %d, want the broken file only", skipped)
	}
	return db
}

func ids(found []Finding) []string {
	var out []string
	for _, f := range found {
		out = append(out, f.ID)
	}
	slices.Sort(out)
	return out
}

func check(t *testing.T, db *DB, cases map[[2]string][]string) {
	t.Helper()
	for k, want := range cases {
		found, undecided := db.Lookup(k[0], k[1])
		if got := ids(found); !slices.Equal(got, want) {
			t.Errorf("%s %s %s: got %v, want %v", db.Ecosystem, k[0], k[1], got, want)
		}
		if undecided != 0 {
			t.Errorf("%s %s %s: %d undecided", db.Ecosystem, k[0], k[1], undecided)
		}
	}
}

func TestNpm(t *testing.T) {
	db := dump(t, versions.Npm)
	check(t, db, map[[2]string][]string{
		// The compromise of 8 September 2025: malicious releases of chalk and debug, the versions around them clean.
		{"chalk", "5.6.1"}:  {"MAL-2025-46969"},
		{"chalk", "5.6.0"}:  nil,
		{"chalk", "5.6.2"}:  nil,
		{"debug", "4.4.2"}:  {"GHSA-4x49-vf9v-38px", "MAL-2025-46974"},
		{"debug", "4.4.3"}:  nil,
		{"debug", "4.3.0"}:  {"GHSA-gxpj-cx7g-858c"},
		{"debug", "4.3.1"}:  nil,
		{"debug", "2.6.8"}:  {"GHSA-gxpj-cx7g-858c"},
		{"debug", "2.6.9"}:  nil,
		{"debug", "3.2.6"}:  {"GHSA-gxpj-cx7g-858c"},
		{"debug", "3.2.7"}:  nil,
		{"axios", "1.14.1"}: {"MAL-2026-2307"},
		{"axios", "0.30.4"}: {"MAL-2026-2307"},
		{"axios", "1.14.0"}: nil,
		// Pre-release bounds.
		{"h3", "2.0.0-alpha.1"}:             nil,
		{"h3", "2.0.0-beta.0"}:              {"GHSA-26f5-8h2x-34xh"},
		{"h3", "2.0.0"}:                     {"GHSA-26f5-8h2x-34xh"},
		{"h3", "2.0.1-rc.8"}:                {"GHSA-26f5-8h2x-34xh"},
		{"h3", "2.0.1-rc.9"}:                nil,
		{"h3", "2.0.1"}:                     nil,
		{"payload", "4.0.0-canary.33"}:      {"GHSA-238x-w2j9-gwwr"},
		{"payload", "4.0.0-canary.34"}:      nil,
		{"payload", "3.89.1"}:               {"GHSA-238x-w2j9-gwwr"},
		{"payload", "3.90.0"}:               nil,
		{"payload", "4.0.0"}:                nil,
		{"strapi-admin", "3.0.0-beta.18.3"}: {"GHSA-23fp-fmrv-f5px"},
		{"strapi-admin", "3.0.0-beta.18.4"}: nil,
		{"strapi-admin", "2.0.0"}:           {"GHSA-23fp-fmrv-f5px"},
		// last_affected is affected itself.
		{"is-http2", "1.2.0"}: {"GHSA-2275-rpf5-xv8h"},
		{"is-http2", "1.2.1"}: nil,
		// Several ranges of one advisory.
		{"phoenix", "1.2.0-rc.0"}: {"EEF-CVE-2026-56812"},
		{"phoenix", "1.1.9"}:      nil,
		{"phoenix", "1.5.15"}:     nil,
		{"phoenix", "1.6.16"}:     {"EEF-CVE-2026-56812"},
		{"phoenix", "1.7.0-rc.1"}: {"EEF-CVE-2026-56812"},
		{"phoenix", "1.8.9"}:      nil,
		// Malware: by versions list, and for every version (also with a second "introduced" further up).
		{"@epic-mod-market/ui", "1.9.9"}:    {"MAL-2022-250"},
		{"@epic-mod-market/ui", "1.9.8"}:    nil,
		{"cxp-jquery", "1.0.0"}:             {"MAL-2021-1"},
		{"cxp-jquery", "not-a-version"}:     {"MAL-2021-1"},
		{"adobe-commerce-magento", "0.0.1"}: {"MAL-2025-3606"},
		// Withdrawn malware reports count for nothing.
		{"drata", "1.0.0"}: nil,
		// npm names are exact.
		{"Chalk", "5.6.1"}: nil,
	})
	if db.Withdrawn != 1 || db.Malicious != 7 || db.Entries != 13 {
		t.Errorf("counts: %d entries, %d malicious, %d withdrawn", db.Entries, db.Malicious, db.Withdrawn)
	}
	if f, _ := db.Lookup("chalk", "5.6.1"); !f[0].Malicious || !slices.Equal(f[0].Aliases, []string{"GHSA-2v46-p5h4-248w"}) {
		t.Errorf("chalk 5.6.1: %+v", f)
	}
	if f, _ := db.Lookup("debug", "4.3.0"); f[0].Malicious || f[0].Severity != "LOW" || !slices.Equal(f[0].Fixed, []string{"4.3.1"}) {
		t.Errorf("debug 4.3.0: %+v", f)
	}
}

func TestPyPI(t *testing.T) {
	db := dump(t, versions.PyPI)
	check(t, db, map[[2]string][]string{
		{"requests", "2.31.0"}: {"GHSA-9wx4-h78v-vm56"},
		{"Requests", "2.31.0"}: {"GHSA-9wx4-h78v-vm56"}, // PEP 503: case does not matter
		{"requests", "2.32.0"}: nil,
		{"requests", "2.32"}:   nil, // 2.32 is 2.32.0
		{"urllib3", "1.26.18"}: {"GHSA-34jh-p97f-mpxf"},
		{"urllib3", "1.26.19"}: nil,
		{"urllib3", "2.0.0"}:   {"GHSA-34jh-p97f-mpxf"},
		{"urllib3", "2.2.1"}:   {"GHSA-34jh-p97f-mpxf"},
		{"urllib3", "2.2.2"}:   nil,
		// Pre-releases as bounds: wagtail from 2.8rc1, plone 4.3a0 to 4.3b1 (4.3.dev0 lies before 4.3a0).
		{"wagtail", "2.8b1"}:  nil,
		{"wagtail", "2.8rc1"}: {"GHSA-2473-9hgq-j7xw"},
		{"wagtail", "2.9.2"}:  {"GHSA-2473-9hgq-j7xw"},
		{"wagtail", "2.9.3"}:  nil,
		{"wagtail", "2.7.3"}:  {"GHSA-2473-9hgq-j7xw"},
		{"plone", "4.3a2"}:    {"GHSA-25jh-5h5r-h33m"},
		{"plone", "4.3b1"}:    nil,
		{"plone", "4.3.dev0"}: nil,
		{"zodb3", "3.8.2"}:    {"PYSEC-2009-10"},
		{"zodb3", "3.8.3"}:    nil,
		{"zodb3", "3.9.0b5"}:  {"PYSEC-2009-10"},
		{"zodb3", "3.9.0rc1"}: {"PYSEC-2009-10"}, // rc and c are the same: rc1 < c2
		{"zodb3", "3.9.0c2"}:  nil,
		// Post-releases: "0.4.6-1" is 0.4.6.post1; skia-python is fixed in 144.0.post1.
		{"ocrodjvu", "0.4.6"}:          nil,
		{"ocrodjvu", "0.4.6.post1"}:    {"GHSA-5pjj-7m4p-wfh2"},
		{"ocrodjvu", "0.4.6-2"}:        nil,
		{"skia-python", "144.0"}:       {"GHSA-2mhw-8qcg-gr96"},
		{"skia-python", "144.0.post0"}: {"GHSA-2mhw-8qcg-gr96"},
		{"skia-python", "144.0.post1"}: nil,
		{"sglang", "0.4.1.post6"}:      nil,
		{"sglang", "0.4.1.post7"}:      {"GHSA-36m8-w8qf-g76p"},
		{"sglang", "0.5.12"}:           {"GHSA-36m8-w8qf-g76p"},
		{"sglang", "0.5.13"}:           nil,
		{"pyblade", "0.1.8a0"}:         {"GHSA-23jg-5f8m-gw8c"},
		{"pyblade", "0.2.0a0"}:         {"GHSA-23jg-5f8m-gw8c"},
		{"pyblade", "0.2.0"}:           nil,
		// Calendar versions with last_affected.
		{"youtube-dl", "2021.12.17"}: {"GHSA-22fp-mf44-f2mq"},
		{"youtube-dl", "2014.12.31"}: nil,
		// As written: introduced 1.1.5.post1, fixed 1.1.5 - by the schema's rule every version from 1.1.5.post1 on.
		{"dydx-v4-client", "1.1.5"}:       nil,
		{"dydx-v4-client", "1.1.5.post1"}: {"GHSA-4f84-67cv-qrv3"},
		// Malware.
		{"security-util-py", "0.0.5"}: {"MAL-2023-10"},
		{"security-util-py", "0.0.4"}: nil,
		{"ascii2text", "1.0"}:         {"MAL-2022-7421"},
		{"lodestone", "0.0.58"}:       nil, // withdrawn
	})
	if f, _ := db.Lookup("urllib3", "1.26.18"); !slices.Equal(f[0].Fixed, []string{"1.26.19", "2.2.2"}) ||
		f[0].Severity != "MODERATE" {
		t.Errorf("urllib3 1.26.18: %+v", f)
	}
}

func TestUndecided(t *testing.T) {
	db := dump(t, versions.PyPI)
	// A version pip cannot read: requests has ranges only for it, so neither yes nor no.
	if found, undecided := db.Lookup("requests", "2.31.0-final"); len(found) != 0 || undecided != 1 {
		t.Errorf("got %v, %d undecided", ids(found), undecided)
	}
	// Malware for every version decides even then.
	if found, undecided := db.Lookup("ascii2text", "weird"); len(found) != 1 || undecided != 0 {
		t.Errorf("got %v, %d undecided", ids(found), undecided)
	}
}

func TestRangeRules(t *testing.T) {
	ev := func(kv ...string) []Event {
		var out []Event
		for i := 0; i < len(kv); i += 2 {
			out = append(out, Event{Kind: kv[i], Version: kv[i+1]})
		}
		return out
	}
	for _, c := range []struct {
		v    string
		evs  []Event
		want bool
	}{
		{"1.0.0", ev(Introduced, "1.0.0", Fixed, "1.0.0"), false}, // empty: introduced and fixed at once
		{"1.0.0", ev(Fixed, "1.0.0", Introduced, "1.0.0"), false}, // in either order in the file
		{"1.2.0", ev(Introduced, "0", Limit, "1.2.0"), false},     // the limit itself is outside
		{"1.5.0", ev(Fixed, "2.0.0", Introduced, "1.0.0"), true},  // order in the file does not matter
		{"2.0.0", ev(Fixed, "2.0.0", Introduced, "1.0.0"), false},
		{"1.5.0", ev(Introduced, "0", Limit, "1.2.0"), false},
		{"1.1.0", ev(Introduced, "0", Limit, "1.2.0"), true},
		{"9.0.0", ev(Introduced, "0", Limit, "*"), true},
		{"0.9.0", ev(Introduced, "1.0.0"), false},
		{"3.0.0", ev(Introduced, "1.0.0", Fixed, "2.0.0", Introduced, "3.0.0"), true},
	} {
		got, ok := inRange(versions.Npm, c.v, c.evs)
		if !ok || got != c.want {
			t.Errorf("%s in %v: got %v (ok %v)", c.v, c.evs, got, ok)
		}
	}
}

func TestSaveLoad(t *testing.T) {
	db := dump(t, versions.Npm)
	path := filepath.Join(t.TempDir(), "npm.idx")
	if err := db.Save(path); err != nil {
		t.Fatal(err)
	}
	got, current, err := Load(path)
	if err != nil || !current {
		t.Fatalf("load: %v (current %v)", err, current)
	}
	if f, _ := got.Lookup("chalk", "5.6.1"); len(f) != 1 || got.Entries != db.Entries || !got.Modified.Equal(db.Modified) {
		t.Errorf("after load: %v, %d entries", ids(f), got.Entries)
	}
}

// Versions the ecosystem cannot order (apk-tools 2 and 3 disagree: 8.2.0015 against 8.2.002) leave an advisory
// undecided - not "fixed" because Compare's zero passed for equal.
func TestUnorderableVersionsStayUndecided(t *testing.T) {
	if _, ok := versions.Compare("Alpine:v3.23", "8.2.0015-r0", "8.2.002-r0"); ok {
		t.Fatal("the fixture needs a pair apk-tools 2 and 3 order differently")
	}
	db := &DB{Ecosystem: "Alpine:v3.23", Packages: map[string][]Affected{"vim": {{ID: "ALPINE-CVE-2025-1",
		Ranges: [][]Event{{{Kind: Introduced, Version: "0"}, {Kind: Fixed, Version: "8.2.002-r0"}}}}}}}
	found, undecided := db.Lookup("vim", "8.2.0015-r0")
	if len(found) != 0 || undecided != 1 {
		t.Errorf("found %+v, undecided %d", found, undecided)
	}
	db.Packages["vim"][0].Ranges = [][]Event{{{Kind: Introduced, Version: "8.2.002-r0"}}}
	if found, undecided = db.Lookup("vim", "8.2.0015-r0"); len(found) != 0 || undecided != 1 {
		t.Errorf("introduced at an unorderable version: found %+v, undecided %d", found, undecided)
	}
	// The installed version orders against both bounds, but the bounds not against each other: the range itself has
	// no order, so neither "inside" nor "outside" holds.
	if _, ok := versions.Compare("Alpine:v3.23", "9.0-r0", "8.2.0015-r0"); !ok {
		t.Fatal("the fixture needs a version both generations order against the bounds")
	}
	db.Packages["vim"][0].Ranges = [][]Event{{{Kind: Introduced, Version: "8.2.0015-r0"}, {Kind: Fixed, Version: "8.2.002-r0"}}}
	if found, undecided = db.Lookup("vim", "9.0-r0"); len(found) != 0 || undecided != 1 {
		t.Errorf("bounds without an order: found %+v, undecided %d", found, undecided)
	}
}
