package inventory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/versions"
)

var now = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

func ago(days float64) string {
	return now.Add(-time.Duration(days * 24 * float64(time.Hour))).Format(time.RFC3339)
}

// fakeRegistry answers like pypi.org (/pypi/<name>/json) and registry.npmjs.org (/<name>, scoped as @scope%2Fname).
type fakeRegistry struct {
	mu    sync.Mutex
	asked map[string]int
	docs  map[string]any
}

func (f *fakeRegistry) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := r.URL.EscapedPath()
	f.asked[path]++
	doc, ok := f.docs[path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	_ = json.NewEncoder(w).Encode(doc)
}

func pypiDoc(files map[string][]map[string]any) map[string]any {
	return map[string]any{"info": map[string]any{"version": "ignored"}, "releases": files}
}

func file(uploaded string, yanked bool) map[string]any {
	return map[string]any{"upload_time_iso_8601": uploaded, "yanked": yanked}
}

func newFakeRegistry(t *testing.T) (*fakeRegistry, Registries) {
	f := &fakeRegistry{asked: map[string]int{}, docs: map[string]any{
		"/pypi/requests/json": pypiDoc(map[string][]map[string]any{
			"2.31.0":    {file(ago(500), false)},
			"2.32.0":    {file(ago(30), false), file(ago(30), false)},
			"2.32.3":    {file(ago(10), false)},
			"2.32.4":    {file(ago(2), false)},                      // too new
			"2.32.5":    {file(ago(20), true), file(ago(20), true)}, // yanked (all files)
			"2.33.0rc1": {file(ago(1), false)},                      // pre-release
			"2.30.9":    {},                                         // no files: not installable
		}),
		// A wheel added to an old release yesterday makes the release as young as that file.
		"/pypi/urllib3/json": pypiDoc(map[string][]map[string]any{
			"2.2.2": {file(ago(400), false)},
			"2.5.0": {file(ago(60), false)},
			"2.6.0": {file(ago(40), false), file(ago(1), false)},
		}),
		"/chalk": map[string]any{
			"dist-tags": map[string]string{"latest": "5.6.2", "next": "6.0.0-beta.1"},
			"time":      map[string]string{"5.6.0": ago(400), "5.6.1": ago(30), "5.6.2": ago(3), "6.0.0-beta.1": ago(1), "7.0.0": ago(100)},
			"versions": map[string]any{
				"5.6.0":        map[string]any{},
				"5.6.1":        map[string]any{"deprecated": "compromised release, do not use"},
				"5.6.2":        map[string]any{},
				"6.0.0-beta.1": map[string]any{},
				"7.0.0":        map[string]any{}, // above the latest tag: another line, never offered
			},
		},
		"/@example%2Fnodes": map[string]any{
			"dist-tags": map[string]string{"latest": "5.0.1"},
			"time":      map[string]string{"4.1.0": ago(200), "5.0.0": ago(50), "5.0.1": ago(9)},
			"versions":  map[string]any{"4.1.0": map[string]any{}, "5.0.0": map[string]any{"deprecated": ""}, "5.0.1": map[string]any{}},
		},
		// Two lines of one dependency (review of ADR-0111): 2.6.9 fixes 2.6.8 and is 3 days old, 4.3.7 is 4 days old -
		// young enough for the shorter age, too young for the minimum age.
		"/debug": map[string]any{
			"dist-tags": map[string]string{"latest": "4.3.7"},
			"time":      map[string]string{"2.6.8": ago(900), "2.6.9": ago(3), "4.3.4": ago(100), "4.3.7": ago(4)},
			"versions": map[string]any{"2.6.8": map[string]any{}, "2.6.9": map[string]any{}, "4.3.4": map[string]any{},
				"4.3.7": map[string]any{}},
		},
		// A dependency released yesterday (ADR-0111: a fix must not bring it along).
		"/ansi-styles": map[string]any{
			"dist-tags": map[string]string{"latest": "6.2.4"},
			"time":      map[string]string{"6.2.3": ago(90), "6.2.4": ago(1)},
			"versions":  map[string]any{"6.2.3": map[string]any{}, "6.2.4": map[string]any{}},
		},
	}}
	ts := httptest.NewServer(f)
	t.Cleanup(ts.Close)
	return f, Registries{PyPI: ts.URL, Npm: ts.URL, Client: ts.Client()}
}

func TestPick(t *testing.T) {
	_, reg := newFakeRegistry(t)
	ctx := context.Background()
	week := 7 * 24 * time.Hour
	for _, c := range []struct {
		eco                       versions.Ecosystem
		name, installed           string
		latest, candidate, wantAt string
	}{
		// Newest eligible 2.32.4 is two days old; the candidate is 2.32.3. Yanked, pre-releases, empty releases never.
		{versions.PyPI, "requests", "2.31.0", "2.32.4", "2.32.3", ago(2)},
		{versions.PyPI, "requests", "2.32.3", "2.32.4", "", ago(2)},
		{versions.PyPI, "requests", "2.32.4", "", "", ""},
		{versions.PyPI, "urllib3", "2.2.2", "2.6.0", "2.5.0", ago(1)},
		// npm: deprecated 5.6.1 never, 5.6.2 too new, 7.0.0 above the latest tag, the beta a pre-release.
		{versions.Npm, "chalk", "5.6.0", "5.6.2", "", ago(3)},
		// "deprecated": "" means undeprecated.
		{versions.Npm, "@example/nodes", "4.1.0", "5.0.1", "5.0.1", ""},
	} {
		info, err := reg.Lookup(ctx, c.eco, c.name)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		latest, candidate, at := Pick(c.eco, c.installed, info, now, week)
		gotAt := ""
		if latest != candidate && !at.IsZero() {
			gotAt = at.Format(time.RFC3339)
		}
		if latest != c.latest || candidate != c.candidate || gotAt != c.wantAt {
			t.Errorf("%s %s: got latest %q candidate %q at %q", c.name, c.installed, latest, candidate, gotAt)
		}
	}
	if info, err := reg.Lookup(ctx, versions.PyPI, "does-not-exist"); err != nil || !info.NotFound {
		t.Errorf("404: %v, %+v", err, info)
	}
	// An installed version that does not parse gets nothing offered.
	info, _ := reg.Lookup(ctx, versions.PyPI, "requests")
	if l, c, _ := Pick(versions.PyPI, "2.31.0-final", info, now, week); l != "" || c != "" {
		t.Errorf("unparsed installed version: %q %q", l, c)
	}
}

// Registry documents are read key by key; whatever surrounds the few fields that count is skipped, nested or not.
func TestDecodeSkipsTheRest(t *testing.T) {
	npm := `{"_id":"x","readme":"` + strings.Repeat("r", 1<<20) + `","dist-tags":{"latest":"1.1.0"},
		"versions":{"1.0.0":{"name":"x","dependencies":{"a":"^1"},"scripts":{"postinstall":"evil"}},
		            "1.1.0":{"deprecated":"use 1.2","dist":{"tarball":"t","signatures":[{"sig":"s"}]}}},
		"users":{"a":true},"time":{"created":"2020-01-01T00:00:00Z","1.0.0":"2020-01-01T00:00:00Z","1.1.0":"2026-10-01T00:00:00Z"}}`
	info, err := decodeNpm(json.NewDecoder(strings.NewReader(npm)))
	if err != nil || info.LatestTag != "1.1.0" || len(info.Releases) != 2 {
		t.Fatalf("npm: %v %+v", err, info)
	}
	for _, r := range info.Releases {
		if r.Withdrawn != (r.Version == "1.1.0") || r.Published.IsZero() {
			t.Errorf("npm release %+v", r)
		}
	}
	pypi := `{"info":{"version":"2.0","description":"` + strings.Repeat("d", 1<<20) + `","classifiers":["a","b"]},
		"last_serial":1,"releases":{"1.0":[{"upload_time_iso_8601":"2020-01-01T00:00:00Z","yanked":false}],"2.0":[]},
		"urls":[],"vulnerabilities":[{"id":"x","aliases":[]}]}`
	info, err = decodePyPI(json.NewDecoder(strings.NewReader(pypi)))
	if err != nil || len(info.Releases) != 1 || info.Releases[0].Version != "1.0" {
		t.Fatalf("pypi: %v %+v", err, info)
	}
	if _, err := decodeNpm(json.NewDecoder(strings.NewReader(`["not an object"]`))); err == nil {
		t.Error("an array decoded as a registry document")
	}
}

// The security candidate (ADR-0111): the newest release public for the shorter age, reported only when it is newer
// than the candidate. requests 2.32.4 is two days old, 2.32.3 older than a week.
func TestOutdatedSecurityCandidate(t *testing.T) {
	_, reg := newFakeRegistry(t)
	day := 24 * time.Hour
	for age, want := range map[time.Duration]string{day: "2.32.4", 3 * day: "", 7 * day: ""} {
		locs := []Location{{ID: "venv", Ecosystem: versions.PyPI,
			Packages: []Package{{Name: "requests", Version: "2.31.0", Direct: true}}}}
		Outdated(context.Background(), reg, locs, now, 7*day, age, 10)
		p := locs[0].Packages[0]
		if p.Candidate != "2.32.3" || p.SecurityCandidate != want {
			t.Errorf("security age %s: candidate %q, security candidate %q, want %q", age, p.Candidate,
				p.SecurityCandidate, want)
		}
	}
}

func TestOutdated(t *testing.T) {
	f, reg := newFakeRegistry(t)
	locs := []Location{
		{ID: "venv-a", Ecosystem: versions.PyPI, Packages: []Package{
			{Name: "requests", Version: "2.31.0", Direct: true},
			{Name: "urllib3", Version: "2.2.2"}, // a dependency: not asked
			{Name: "gone", Version: "1.0", Direct: true},
		}},
		{ID: "venv-b", Ecosystem: versions.PyPI, Packages: []Package{{Name: "Requests", Version: "2.32.0", Direct: true}}},
		{ID: "node-red", Ecosystem: versions.Npm, Packages: []Package{{Name: "@example/nodes", Version: "4.1.0", Direct: true}}},
	}
	failed := Outdated(context.Background(), reg, locs, now, 7*24*time.Hour, 0, 10)
	a := locs[0].Packages
	// "gone" is not in the registry (404): a state, not a failure of the registry.
	if failed != 0 || locs[0].OutdatedFailed != 0 || locs[0].NotInRegistry != 1 || a[0].Candidate != "2.32.3" ||
		a[1].Latest != "" || a[2].Latest != "" {
		t.Errorf("venv-a: failed %d, %+v", failed, locs[0])
	}
	// A registry that answers with an error is a failure, counted as such.
	broken := reg
	broken.PyPI = "http://127.0.0.1:1" // nothing listens
	failing := []Location{{ID: "x", Ecosystem: versions.PyPI, Packages: []Package{{Name: "requests", Version: "2.31.0", Direct: true}}}}
	if n := Outdated(context.Background(), broken, failing, now, 7*24*time.Hour, 0, 10); n != 1 || failing[0].OutdatedFailed != 1 {
		t.Errorf("unreachable registry: %d, %+v", n, failing[0])
	}
	if b := locs[1].Packages[0]; b.Latest != "2.32.4" || b.Candidate != "2.32.3" {
		t.Errorf("venv-b: %+v", b)
	}
	if n := locs[2].Packages[0]; n.Candidate != "5.0.1" || !n.Major || n.LatestAt != nil {
		t.Errorf("node-red: %+v", n)
	}
	// requests is asked once for both locations (PEP 503: Requests is requests).
	if f.asked["/pypi/requests/json"] != 1 || f.asked["/pypi/Requests/json"] != 0 {
		t.Errorf("asked: %v", f.asked)
	}
	// Without a shorter age there is no security candidate.
	if a[0].SecurityCandidate != "" {
		t.Errorf("security candidate without a shorter age: %+v", a[0])
	}
	// The run stops asking at the limit; what is left counts as failed, not as up to date.
	locs[0].Packages[0].Latest, locs[0].Packages[0].Candidate = "", ""
	if failed := Outdated(context.Background(), reg, locs[:1], now, 7*24*time.Hour, 0, 1); failed != 1 ||
		!strings.HasPrefix(locs[0].Packages[0].Candidate, "2.32") {
		t.Errorf("limit: failed %d, %+v", failed, locs[0].Packages)
	}
}
