package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/inventory"
	"github.com/psewar/vordr/internal/kev"
	"github.com/psewar/vordr/internal/osv"
	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/versions"
	"github.com/psewar/vordr/internal/wire"
)

// The KEV feeds here are synthetic. They list CVE ids that the OSV test data names (debug 4.3.0: CVE-2017-16137;
// requests 2.31.0: CVE-2024-35195; debug 4.4.2: CVE-2025-59144) plus made-up ones; whether CISA lists them does not
// matter to the tests.

type kevEntry struct{ cve, added, ransomware string }

// kevFeed writes a catalog in the shape of known_exploited_vulnerabilities.json.
func kevFeed(t *testing.T, version string, entries ...kevEntry) []byte {
	t.Helper()
	vulns := []map[string]any{}
	for _, e := range entries {
		v := map[string]any{"cveID": e.cve, "vendorProject": "Example", "product": "Example",
			"vulnerabilityName": "Example", "dateAdded": e.added, "shortDescription": "Example",
			"requiredAction": "Example", "dueDate": "2026-12-31", "notes": "", "cwes": []string{}}
		if e.ransomware != "" {
			v["knownRansomwareCampaignUse"] = e.ransomware
		}
		vulns = append(vulns, v)
	}
	raw, err := json.Marshal(map[string]any{"title": "CISA Catalog of Known Exploited Vulnerabilities",
		"catalogVersion": version, "dateReleased": "2026-11-10T18:00:00.123Z", "count": len(vulns),
		"vulnerabilities": vulns})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// filler: made-up entries, so that a catalog has a size the plausibility rule can compare.
func filler(n int) []kevEntry {
	out := make([]kevEntry, n)
	for i := range out {
		out[i] = kevEntry{cve: fmt.Sprintf("CVE-2020-%05d", 10000+i), added: "2021-11-03", ransomware: "Unknown"}
	}
	return out
}

// baseCatalog lists the CVE of debug 4.3.0 (with ransomware use) and of debug 4.4.2, not the one of requests 2.31.0.
func baseCatalog(t *testing.T, version string) []byte {
	return kevFeed(t, version, append(filler(18),
		kevEntry{"CVE-2017-16137", "2026-11-09", "Known"},
		kevEntry{"CVE-2025-59144", "2025-09-10", "Unknown"})...)
}

// kevServer serves a feed with an ETag and a Last-Modified. honour says which validators it answers with 304
// ("both", "etag", "modified"); status other than 0 is answered instead of the feed.
type kevServer struct {
	ts      *httptest.Server
	mu      sync.Mutex
	feed    []byte
	version int
	honour  string
	status  int
	gets    int
	full    int // 200 answers
	last    http.Header
}

func newKEVServer(t *testing.T, feed []byte) *kevServer {
	ks := &kevServer{feed: feed, honour: "both"}
	ks.ts = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ks.mu.Lock()
		defer ks.mu.Unlock()
		ks.gets++
		ks.last = r.Header.Clone()
		etag := fmt.Sprintf(`"kev-%d"`, ks.version)
		modified := time.Date(2026, 11, 10, 18, 0, 0, 0, time.UTC).Add(time.Duration(ks.version) * time.Hour).
			Format(http.TimeFormat)
		byETag := ks.honour != "modified" && r.Header.Get("If-None-Match") == etag
		byDate := ks.honour != "etag" && r.Header.Get("If-Modified-Since") == modified
		switch {
		case ks.status != 0:
			w.WriteHeader(ks.status)
		case r.URL.Path != "/feeds/known_exploited_vulnerabilities.json":
			http.NotFound(w, r)
		case byETag || byDate:
			w.WriteHeader(http.StatusNotModified)
		default:
			ks.full++
			w.Header().Set("ETag", etag)
			w.Header().Set("Last-Modified", modified)
			_, _ = w.Write(ks.feed)
		}
	}))
	t.Cleanup(ks.ts.Close)
	return ks
}

// publish replaces the feed, as CISA does when it adds entries.
func (ks *kevServer) publish(feed []byte) {
	ks.mu.Lock()
	ks.feed = feed
	ks.version++
	ks.mu.Unlock()
}

func (ks *kevServer) set(f func(ks *kevServer)) {
	ks.mu.Lock()
	f(ks)
	ks.mu.Unlock()
}

func (ks *kevServer) counts() (gets, full int) {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	return ks.gets, ks.full
}

// validators: the If-None-Match and If-Modified-Since of the last request.
func (ks *kevServer) validators() (etag, modified string) {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	return ks.last.Get("If-None-Match"), ks.last.Get("If-Modified-Since")
}

func newTestKEV(t *testing.T, dir string, ks *kevServer, clk *clock) *KEV {
	t.Helper()
	cfg := KEVConfig{URL: ks.ts.URL + "/feeds/known_exploited_vulnerabilities.json"}
	cfg.applyDefaults()
	if issues := cfg.check(true); len(issues) > 0 {
		t.Fatal(issues)
	}
	k, err := NewKEV(dir, cfg, clk.now, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	k.client = ks.ts.Client()
	return k
}

// kevPackages: packages with the OSV data of PyPI and npm loaded, and the inventory of testInventory plus debug 4.4.2
// (malware, two advisories that both name CVE-2025-59144) on host-a.
func kevPackages(t *testing.T, dir string, clk *clock) *Packages {
	t.Helper()
	p := newTestPackages(t, dir, newOSVServer(t), []string{"PyPI", "npm"}, clk)
	ctx := context.Background()
	if !p.Refresh(ctx, versions.PyPI) || !p.Refresh(ctx, versions.Npm) {
		t.Fatal("no OSV data")
	}
	inv := testInventory(clk.now())
	inv.Locations[0].Packages = append(inv.Locations[0].Packages,
		inventory.Package{Name: "debug", Version: "4.4.2", Via: "other-cli"})
	if err := p.SetInventory("host-a", inv); err != nil {
		t.Fatal(err)
	}
	return p
}

// marks lists the findings as "<name> <version>[ kev:<cves> <added>[ ransomware]]", in the view's order.
func marks(v *wire.PackagesView) []string {
	var out []string
	for _, f := range v.Findings {
		s := f.Name + " " + f.Version
		if f.KnownExploited {
			s += " kev:" + strings.Join(f.KEVCVEs, ",") + " " + f.KEVAdded
			if f.KEVRansomware {
				s += " ransomware"
			}
		}
		out = append(out, s)
	}
	return out
}

func TestKEVMarksFindings(t *testing.T) {
	clk := &clock{t: time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)}
	dir := t.TempDir()
	p := kevPackages(t, dir, clk)
	ks := newKEVServer(t, baseCatalog(t, "2026.11.10"))
	k := newTestKEV(t, dir, ks, clk)
	p.SetKEV(k)

	// No catalog yet: nothing marked, the count unknown, the state missing.
	v := p.View("host-a")
	if v.KnownExploited != nil || slices.ContainsFunc(v.Findings, func(f wire.FindingView) bool { return f.KnownExploited }) {
		t.Fatalf("marked without a catalog: %v %v", v.KnownExploited, marks(v))
	}
	if kv := p.KEVView(); kv == nil || kv.State != OSVMissing || kv.Checked != nil {
		t.Errorf("before the first fetch: %+v", kv)
	}

	if !k.due() || !k.Refresh(context.Background()) {
		t.Fatal("first refresh brought no catalog")
	}
	v = p.View("host-a")
	// Malware first, the known exploited one of it first; then the known exploited debug 4.3.0 (LOW) before requests
	// (MODERATE), which the catalog does not list. debug 4.4.2 names CVE-2025-59144 in two advisories: listed once.
	want := []string{
		"debug 4.4.2 kev:CVE-2025-59144 2025-09-10",
		"chalk 5.6.1",
		"debug 4.3.0 kev:CVE-2017-16137 2026-11-09 ransomware",
		"Requests 2.31.0",
	}
	if got := marks(v); !slices.Equal(got, want) {
		t.Errorf("findings:\n got %q\nwant %q", got, want)
	}
	if v.KnownExploited == nil || *v.KnownExploited != 2 || *v.Malicious != 2 || *v.Vulnerable != 2 {
		t.Errorf("counts: known exploited %v, malicious %v, vulnerable %v", v.KnownExploited, *v.Malicious, *v.Vulnerable)
	}
	kv := p.KEVView()
	if kv.State != OSVOk || kv.Code != "" || kv.CatalogVersion != "2026.11.10" || kv.Entries != 20 ||
		kv.Checked == nil || !kv.Checked.Equal(clk.now()) || kv.Released == nil ||
		!kv.Released.Equal(time.Date(2026, 11, 10, 18, 0, 0, 123000000, time.UTC)) {
		t.Errorf("kev view: %+v", kv)
	}

	// A new release that also lists the CVE of requests marks it on the next view, without a new inventory.
	ks.publish(kevFeed(t, "2026.11.12", append(filler(18),
		kevEntry{"CVE-2017-16137", "2026-11-09", "Known"},
		kevEntry{"CVE-2025-59144", "2025-09-10", "Unknown"},
		kevEntry{"CVE-2024-35195", "2026-11-12", ""})...))
	clk.add(25 * time.Hour)
	if !k.Refresh(context.Background()) {
		t.Fatal("a new release changed nothing")
	}
	// Both vulnerable ones are known exploited now: the rating orders them again.
	want = []string{
		"debug 4.4.2 kev:CVE-2025-59144 2025-09-10",
		"chalk 5.6.1",
		"Requests 2.31.0 kev:CVE-2024-35195 2026-11-12",
		"debug 4.3.0 kev:CVE-2017-16137 2026-11-09 ransomware",
	}
	if v := p.View("host-a"); *v.KnownExploited != 3 || !slices.Equal(marks(v), want) {
		t.Errorf("after the new release: %v %q", *v.KnownExploited, marks(v))
	}
}

// Run fetches at once and reports the change; it ends with its context.
func TestKEVRun(t *testing.T) {
	clk := &clock{t: time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)}
	ks := newKEVServer(t, baseCatalog(t, "2026.11.10"))
	k := newTestKEV(t, t.TempDir(), ks, clk)
	ctx, cancel := context.WithCancel(context.Background())
	changed, done := make(chan struct{}, 1), make(chan struct{})
	go func() {
		k.Run(ctx, func() { changed <- struct{}{} })
		close(done)
	}()
	select {
	case <-changed:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not fetch")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not end with its context")
	}
	if k.Catalog() == nil {
		t.Error("no catalog after Run")
	}
}

// Marking takes every CVE of every advisory of a finding: ids, aliases and upstream.
func TestKEVMarksFromEveryAlias(t *testing.T) {
	cat := mustCatalog(t, kevFeed(t, "1",
		kevEntry{"CVE-2026-2222", "2026-05-01", "Unknown"},
		kevEntry{"CVE-2026-3333", "2026-03-01", "Known"},
		kevEntry{"CVE-2026-4444", "2026-01-01", "Known"}))
	found := []osv.Finding{
		{ID: "GHSA-aaaa-bbbb-cccc", Aliases: []string{"CVE-2026-1111", "GHSA-dddd-eeee-ffff"}},
		{ID: "PYSEC-2026-1", Aliases: []string{"CVE-2026-2222"}, Upstream: []string{"CVE-2026-3333"}},
		{ID: "CVE-2026-2222"},
	}
	fv := wire.FindingView{Name: "x"}
	markKnownExploited(&fv, found, cat)
	if !fv.KnownExploited || !slices.Equal(fv.KEVCVEs, []string{"CVE-2026-2222", "CVE-2026-3333"}) ||
		fv.KEVAdded != "2026-03-01" || !fv.KEVRansomware {
		t.Errorf("several aliases: %+v", fv)
	}
	fv = wire.FindingView{Name: "y"}
	markKnownExploited(&fv, found[:1], cat)
	if fv.KnownExploited || fv.KEVCVEs != nil || fv.KEVAdded != "" || fv.KEVRansomware {
		t.Errorf("no listed CVE: %+v", fv)
	}
}

func mustCatalog(t *testing.T, raw []byte) *kev.Catalog {
	t.Helper()
	cat, err := kev.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

// A conditional refresh: the hub sends both validators of the last answer, so a server that honours only one of them
// still answers 304. An unchanged catalog costs one request and changes nothing.
func TestKEVConditionalRefresh(t *testing.T) {
	for _, honour := range []string{"both", "etag", "modified"} {
		t.Run(honour, func(t *testing.T) {
			clk := &clock{t: time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)}
			ks := newKEVServer(t, baseCatalog(t, "2026.11.10"))
			ks.set(func(ks *kevServer) { ks.honour = honour })
			k := newTestKEV(t, t.TempDir(), ks, clk)
			ctx := context.Background()
			if !k.Refresh(ctx) {
				t.Fatal("first refresh")
			}
			if etag, modified := ks.validators(); etag != "" || modified != "" {
				t.Errorf("validators without a catalog: %q %q", etag, modified)
			}
			cat := k.Catalog()
			if k.due() {
				t.Error("due right after the check")
			}
			clk.add(23 * time.Hour)
			if k.due() {
				t.Error("due before refresh_hours")
			}
			clk.add(2 * time.Hour)
			if !k.due() {
				t.Fatal("not due after refresh_hours")
			}
			if k.Refresh(ctx) {
				t.Error("an unchanged catalog counted as a change")
			}
			if gets, full := ks.counts(); gets != 2 || full != 1 {
				t.Errorf("gets %d, full answers %d", gets, full)
			}
			if etag, modified := ks.validators(); etag != `"kev-0"` || modified != "Tue, 10 Nov 2026 18:00:00 GMT" {
				t.Errorf("validators: %q %q", etag, modified)
			}
			if k.Catalog() != cat || k.View().Checked == nil || !k.View().Checked.Equal(clk.now()) || k.due() {
				t.Errorf("not modified must keep the catalog and count as checked: %+v", k.View())
			}
		})
	}
}

// What goes wrong keeps the catalog in use: an unreachable server, a feed that is not the catalog, one that lost most
// entries. The state says it as a code; the marks stay; after stale_hours without a good answer the state is stale.
func TestKEVKeepsTheCatalogWhenAFetchFails(t *testing.T) {
	clk := &clock{t: time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)}
	dir := t.TempDir()
	p := kevPackages(t, dir, clk)
	ks := newKEVServer(t, baseCatalog(t, "2026.11.10"))
	k := newTestKEV(t, dir, ks, clk)
	p.SetKEV(k)
	ctx := context.Background()
	k.Refresh(ctx)
	cat := k.Catalog()

	prev := ""
	for _, c := range []struct {
		name string
		set  func(ks *kevServer)
		code string
	}{
		{"server error", func(ks *kevServer) { ks.status = http.StatusInternalServerError }, KEVDownloadFailed},
		{"not found", func(ks *kevServer) { ks.status = http.StatusNotFound }, KEVDownloadFailed},
		{"not JSON", func(ks *kevServer) { ks.status, ks.feed = 0, []byte("<html>maintenance</html>") }, KEVBadFeed},
		{"cut off", func(ks *kevServer) { f := baseCatalog(t, "x"); ks.feed = f[:len(f)/2] }, KEVBadFeed},
		{"count differs", func(ks *kevServer) {
			ks.feed = []byte(strings.Replace(string(baseCatalog(t, "x")), `"count":20`, `"count":21`, 1))
		}, KEVBadFeed},
		{"another format", func(ks *kevServer) {
			ks.feed = []byte(strings.ReplaceAll(string(baseCatalog(t, "x")), `"cveID"`, `"cve"`))
		}, KEVImplausible},
		{"far fewer entries", func(ks *kevServer) { ks.feed = kevFeed(t, "x", filler(17)...) }, KEVImplausible},
	} {
		ks.set(func(ks *kevServer) { c.set(ks); ks.version++ })
		clk.add(25 * time.Hour)
		if !k.due() {
			t.Fatalf("%s: not due", c.name)
		}
		// Refresh reports a change exactly when the code changed: the catalog stayed.
		if changed := k.Refresh(ctx); changed != (c.code != prev) {
			t.Errorf("%s: changed %v after code %q", c.name, changed, prev)
		}
		prev = c.code
		kv := k.View()
		if k.Catalog() != cat || kv.Code != c.code || kv.CatalogVersion != "2026.11.10" {
			t.Errorf("%s: %+v", c.name, kv)
		}
		if v := p.View("host-a"); v.KnownExploited == nil || *v.KnownExploited != 2 {
			t.Errorf("%s: marks lost: %v", c.name, marks(v))
		}
		if k.due() {
			t.Errorf("%s: retried within the hour", c.name)
		}
		clk.add(61 * time.Minute)
		if !k.due() {
			t.Errorf("%s: not retried after the hour", c.name)
		}
	}
	// Seven failed days in all: older than stale_hours, still marking.
	if kv := k.View(); kv.State != OSVStale {
		t.Errorf("stale: %+v", kv)
	}
	if v := p.View("host-a"); v.KnownExploited == nil || *v.KnownExploited != 2 {
		t.Errorf("stale catalog lost its marks: %v", marks(v))
	}
	// 90 % of the entries is taken (one CISA removed), and so is a good answer after the failures.
	ks.set(func(ks *kevServer) {
		ks.feed = kevFeed(t, "2026.11.16", append(filler(16), kevEntry{"CVE-2017-16137", "2026-11-09", "Known"},
			kevEntry{"CVE-2025-59144", "2025-09-10", "Unknown"})...)
		ks.version++
	})
	clk.add(2 * time.Hour)
	if !k.Refresh(ctx) {
		t.Fatal("a good catalog after the failures")
	}
	if kv := k.View(); kv.State != OSVOk || kv.Code != "" || kv.Entries != 18 || kv.CatalogVersion != "2026.11.16" {
		t.Errorf("after recovery: %+v", kv)
	}
}

// The first catalog has nothing to compare with: only "it has entries" applies, and 304 without a catalog is no
// answer to keep.
func TestKEVFirstCatalog(t *testing.T) {
	clk := &clock{t: time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)}
	ks := newKEVServer(t, kevFeed(t, "empty"))
	k := newTestKEV(t, t.TempDir(), ks, clk)
	if !k.Refresh(context.Background()) || k.Catalog() != nil || k.View().Code != KEVImplausible ||
		k.View().State != OSVMissing {
		t.Errorf("empty first catalog: %+v", k.View())
	}
	ks.set(func(ks *kevServer) { ks.status = http.StatusNotModified })
	clk.add(2 * time.Hour)
	if !k.Refresh(context.Background()) || k.Catalog() != nil || k.View().Code != KEVDownloadFailed {
		t.Errorf("304 without a catalog: %+v", k.View())
	}
	if _, err := os.Stat(filepath.Join(k.dir, "catalog.json")); !os.IsNotExist(err) {
		t.Error("a refused catalog was kept on disk")
	}
}

// The catalog survives a restart: no fetch until refresh_hours have passed since the last check. A catalog file that
// no longer reads is fetched anew at once.
func TestKEVSurvivesARestart(t *testing.T) {
	clk := &clock{t: time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)}
	dir := t.TempDir()
	ks := newKEVServer(t, baseCatalog(t, "2026.11.10"))
	k := newTestKEV(t, dir, ks, clk)
	k.Refresh(context.Background())

	clk.add(time.Hour)
	k2 := newTestKEV(t, dir, ks, clk)
	if k2.Catalog() == nil || len(k2.Catalog().Entries) != 20 || k2.due() || k2.View().State != OSVOk {
		t.Fatalf("after restart: %+v", k2.View())
	}
	p := kevPackages(t, t.TempDir(), clk)
	p.SetKEV(k2)
	if v := p.View("host-a"); v.KnownExploited == nil || *v.KnownExploited != 2 {
		t.Errorf("marks after restart: %v", marks(v))
	}
	if gets, _ := ks.counts(); gets != 1 {
		t.Errorf("restart fetched: %d requests", gets)
	}

	if err := os.WriteFile(filepath.Join(dir, "kev", "catalog.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	k3 := newTestKEV(t, dir, ks, clk)
	if k3.Catalog() != nil || !k3.due() {
		t.Fatalf("unreadable catalog: %+v", k3.View())
	}
	if !k3.Refresh(context.Background()) || k3.Catalog() == nil {
		t.Error("not fetched anew")
	}
	if etag, modified := ks.validators(); etag != "" || modified != "" {
		t.Errorf("validators for a catalog the hub does not have: %q %q", etag, modified)
	}

	// A catalog file that is gone: the meta file still has validators, but a 304 would leave the hub without data.
	if err := os.Remove(filepath.Join(dir, "kev", "catalog.json")); err != nil {
		t.Fatal(err)
	}
	k4 := newTestKEV(t, dir, ks, clk)
	if k4.Catalog() != nil || !k4.due() || !k4.Refresh(context.Background()) || k4.Catalog() == nil {
		t.Fatalf("missing catalog: %+v", k4.View())
	}
	if etag, modified := ks.validators(); etag != "" || modified != "" {
		t.Errorf("validators without a catalog file: %q %q", etag, modified)
	}
}

// KEV off (no kev section): nothing is fetched, nothing marked, the counts and the hub view leave it out. The host's
// problem is the same with and without the marks: KEV changes no problem logic (ADR-0168).
func TestKEVOffAndTheHostView(t *testing.T) {
	f := newViewFixture(t)
	clk := &clock{t: f.now}
	f.srv.Packages = kevPackages(t, t.TempDir(), clk)
	f.host(t, "host-a", []protocol.Verb{protocol.VerbCheck}, wire.PolicySummary{}, true)
	_ = f.srv.Reg.SetReport("host-a", aptReport(f.now, 1, 0, nil))

	off := f.view(t, "host-a", f.now)
	if off.Packages == nil || off.Packages.KnownExploited != nil || f.srv.HubView(nil).KEV != nil {
		t.Fatalf("KEV off: %+v", off.Packages)
	}
	if slices.ContainsFunc(off.Packages.Findings, func(f wire.FindingView) bool { return f.KnownExploited }) {
		t.Error("marked with KEV off")
	}
	raw, _ := json.Marshal(off)
	if strings.Contains(string(raw), "known_exploited") || strings.Contains(string(raw), "kev") {
		t.Errorf("KEV off shows in the view: %s", raw)
	}

	ks := newKEVServer(t, baseCatalog(t, "2026.11.10"))
	k := newTestKEV(t, t.TempDir(), ks, clk)
	f.srv.Packages.SetKEV(k)
	if hv := f.srv.HubView(nil); hv.KEV == nil || hv.KEV.State != OSVMissing {
		t.Errorf("hub view before the first fetch: %+v", hv.KEV)
	}
	k.Refresh(context.Background())
	on := f.view(t, "host-a", f.now)
	if on.Packages.KnownExploited == nil || *on.Packages.KnownExploited != 2 {
		t.Errorf("host view: %+v", on.Packages)
	}
	if hv := f.srv.HubView(nil); hv.KEV == nil || hv.KEV.State != OSVOk || hv.KEV.Entries != 20 {
		t.Errorf("hub view: %+v", hv.KEV)
	}
	if !slices.Equal(on.Problem.Reasons, off.Problem.Reasons) {
		t.Errorf("KEV changed the problem: %v, was %v", on.Problem.Reasons, off.Problem.Reasons)
	}
	raw, _ = json.Marshal(on)
	for _, field := range []string{`"known_exploited":2`, `"known_exploited":true`, `"kev_cves":["CVE-2017-16137"]`,
		`"kev_added":"2026-11-09"`, `"kev_ransomware":true`} {
		if !strings.Contains(string(raw), field) {
			t.Errorf("%s missing in the host view", field)
		}
	}
}

func TestKEVConfigCheck(t *testing.T) {
	cfg, issues := ParseConfig([]byte("tls_cert: x\ntls_key: y\nosv: {}\nkev: {}\n"))
	if cfg.KEV == nil || cfg.KEV.URL != defaultKEVURL || cfg.KEV.RefreshHours != 24 || cfg.KEV.StaleHours != 72 {
		t.Errorf("defaults: %+v", cfg.KEV)
	}
	if n := kevIssues(issues); len(n) > 0 {
		t.Errorf("issues with the defaults: %v", n)
	}
	cfg, issues = ParseConfig([]byte("tls_cert: x\ntls_key: y\nosv: {}\nkev: {url: 'https://mirror.example/kev.json', " +
		"refresh_hours: 6, stale_hours: 24}\n"))
	if n := kevIssues(issues); len(n) > 0 || cfg.KEV.URL != "https://mirror.example/kev.json" || cfg.KEV.RefreshHours != 6 {
		t.Errorf("a mirror: %+v %v", cfg.KEV, n)
	}
	if cfg, _ := ParseConfig([]byte("tls_cert: x\ntls_key: y\nosv: {}\n")); cfg.KEV != nil {
		t.Error("KEV on without a kev section")
	}

	_, issues = ParseConfig([]byte("tls_cert: x\ntls_key: y\nkev: {url: 'http://mirror.example/kev.json', " +
		"refresh_hours: 200, stale_hours: 2}\n"))
	want := []string{"kev: needs the osv section", "kev.url: must be an https:// URL", "kev.refresh_hours:",
		"kev.stale_hours:"}
	got := kevIssues(issues)
	if len(got) != len(want) {
		t.Fatalf("want %d kev issues, got %v", len(want), got)
	}
	for i, w := range want {
		if !strings.HasPrefix(got[i], w) {
			t.Errorf("issue %d: %q, want %q...", i, got[i], w)
		}
	}
	for _, bad := range []string{"kev: {url: 'https://'}", "kev: {url: 'ftp://mirror.example/kev.json'}",
		"kev: {refresh_hours: -1}", "kev: {stale_hours: 721}", "kev: {unknown: 1}",
		"kev: {refresh_hours: 48, stale_hours: 24}"} {
		_, issues := ParseConfig([]byte("tls_cert: x\ntls_key: y\nosv: {}\n" + bad + "\n"))
		if !slices.ContainsFunc(issues, func(i string) bool { return strings.Contains(strings.ToLower(i), "kev") }) {
			t.Errorf("%s accepted: %v", bad, issues)
		}
	}
}

// The example file shows the kev section, and it passes as it stands.
func TestKEVInTheExampleConfig(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "hub.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, issues := ParseConfig(raw)
	if cfg.KEV == nil || cfg.OSV == nil || cfg.KEV.URL != defaultKEVURL || cfg.KEV.RefreshHours != 24 {
		t.Errorf("example: %+v", cfg.KEV)
	}
	if n := kevIssues(issues); len(n) > 0 {
		t.Errorf("example: %v", n)
	}
}

func kevIssues(issues []string) []string {
	var out []string
	for _, i := range issues {
		if strings.HasPrefix(i, "kev") {
			out = append(out, i)
		}
	}
	return out
}
