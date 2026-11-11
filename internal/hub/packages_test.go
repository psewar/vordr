package hub

import (
	"archive/zip"
	"bytes"
	"context"
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
	"github.com/psewar/vordr/internal/osv"
	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/versions"
	"github.com/psewar/vordr/internal/wire"
)

// osvZip zips the real OSV entries of internal/osv/testdata the way osv.dev publishes <ecosystem>/all.zip.
func osvZip(t *testing.T, eco string) []byte {
	t.Helper()
	return osvZipWithout(t, eco)
}

// osvZipWithout is osvZip without the named files (a shrunken dump). The path is relative to the package: without
// the files the test would serve an empty dump and prove nothing, so it stops instead.
func osvZipWithout(t *testing.T, eco string, leave ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	files, _ := filepath.Glob(filepath.Join("..", "osv", "testdata", eco, "*.json"))
	if len(files) == 0 {
		t.Fatalf("no OSV testdata for %s in ../osv/testdata - run the test from internal/hub", eco)
	}
	for _, f := range files {
		if slices.Contains(leave, filepath.Base(f)) {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		w, _ := zw.Create(filepath.Base(f))
		_, _ = w.Write(raw)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// osvServer serves the dumps with an ETag and answers 304 to a matching If-None-Match; fail makes it answer 500.
type osvServer struct {
	ts      *httptest.Server
	mu      sync.Mutex
	gets    map[string]int
	full    map[string]int // 200 answers
	fail    bool
	dumps   map[string][]byte
	version map[string]int // publish raises it: a new dump has a new ETag
}

// publish replaces an ecosystem's dump, as osv.dev does every day.
func (o *osvServer) publish(eco string, dump []byte) {
	o.mu.Lock()
	o.dumps[eco] = dump
	o.version[eco]++
	o.mu.Unlock()
}

func newOSVServer(t *testing.T) *osvServer {
	o := &osvServer{gets: map[string]int{}, full: map[string]int{}, version: map[string]int{},
		dumps: map[string][]byte{"PyPI": osvZip(t, "PyPI"), "npm": osvZip(t, "npm")}}
	o.ts = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o.mu.Lock()
		defer o.mu.Unlock()
		eco := filepath.Base(filepath.Dir(r.URL.Path))
		o.gets[eco]++
		dump, ok := o.dumps[eco]
		etag := fmt.Sprintf(`"v%d-%s"`, o.version[eco]+1, eco)
		switch {
		case o.fail:
			w.WriteHeader(http.StatusInternalServerError)
		case !ok || filepath.Base(r.URL.Path) != "all.zip":
			http.NotFound(w, r)
		case r.Header.Get("If-None-Match") == etag:
			w.WriteHeader(http.StatusNotModified)
		default:
			o.full[eco]++
			w.Header().Set("ETag", etag)
			_, _ = w.Write(dump)
		}
	}))
	t.Cleanup(o.ts.Close)
	return o
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newTestPackages(t *testing.T, dir string, o *osvServer, ecos []string, clk *clock) *Packages {
	t.Helper()
	var cfg *OSVConfig
	if ecos != nil {
		cfg = &OSVConfig{URL: o.ts.URL, Ecosystems: ecos}
		cfg.applyDefaults()
		if issues := cfg.check(); len(issues) > 0 {
			t.Fatal(issues)
		}
	}
	p, err := NewPackages(dir, cfg, clk.now, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	if o != nil {
		p.client = o.ts.Client()
	}
	return p
}

func testInventory(at time.Time) wire.Inventory {
	return wire.Inventory{T: wire.TInventory, CheckedAt: at, Locations: []inventory.Location{
		{ID: "npm-global", Kind: inventory.KindNpmGlobal, Ecosystem: versions.Npm, Path: "/usr/lib/node_modules",
			Packages: []inventory.Package{
				{Name: "debug", Version: "4.3.0", Via: "some-cli"}, // before the malware: the order is the hub's
				{Name: "chalk", Version: "5.6.1", Via: "some-cli"},
				{Name: "axios", Version: "1.14.0", Direct: true},
				{Name: "some-cli", Version: "1.0.0", Direct: true},
			}},
		{ID: "venv-app", Kind: inventory.KindVenv, Ecosystem: versions.PyPI, Path: "/opt/app/venv",
			Packages: []inventory.Package{
				{Name: "Requests", Version: "2.31.0", Direct: true},
				{Name: "urllib3", Version: "2.2.2"},
			}},
	}}
}

func TestPackagesOSVAndMatching(t *testing.T) {
	dir := t.TempDir()
	o := newOSVServer(t)
	clk := &clock{t: time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)}
	p := newTestPackages(t, dir, o, []string{"PyPI", "npm"}, clk)

	if hv := p.HubView(); len(hv) != 2 || hv[0].State != OSVMissing || hv[1].State != OSVMissing {
		t.Fatalf("before the first download: %+v", hv)
	}
	if err := p.SetInventory("host-a", testInventory(clk.now())); err != nil {
		t.Fatal(err)
	}
	if v := p.View("host-a"); v.Vulnerable != nil || v.Malicious != nil || v.Total != 6 {
		t.Fatalf("without data the counts are unknown, not zero: %+v", v)
	}

	ctx := context.Background()
	if !p.Refresh(ctx, versions.PyPI) || !p.Refresh(ctx, versions.Npm) {
		t.Fatal("first refresh brought no data")
	}
	v := p.View("host-a")
	if v.Malicious == nil || *v.Malicious != 1 || *v.Vulnerable != 2 {
		t.Fatalf("counts: %+v", v)
	}
	var got []string
	for _, f := range v.Findings {
		got = append(got, f.Location+" "+f.Name+" "+f.Version+" fixed:"+f.FixedIn)
	}
	// Malware first; then by rating: requests (MODERATE) before debug (LOW).
	want := []string{"npm-global chalk 5.6.1 fixed:", "venv-app Requests 2.31.0 fixed:2.32.0", "npm-global debug 4.3.0 fixed:4.3.1"}
	if !slices.Equal(got, want) {
		t.Errorf("findings: %v", got)
	}
	if f := v.Findings[0]; !f.Malicious || f.Via != "some-cli" || !slices.Equal(f.Advisories, []string{"MAL-2025-46969"}) {
		t.Errorf("chalk: %+v", f)
	}
	if l := v.Locations[0]; l.Malicious != 1 || l.Vulnerable != 1 || l.Packages != 4 {
		t.Errorf("location: %+v", l)
	}
	hv := p.HubView()
	if hv[0].State != OSVOk || hv[0].Advisories != 13 || hv[1].Malicious != 7 || hv[1].Checked == nil {
		t.Errorf("hub view: %+v", hv)
	}

	// The next day: unchanged dumps cost a request each, no download.
	clk.add(25 * time.Hour)
	if !p.due(versions.Npm) || p.Refresh(ctx, versions.Npm) || o.full["npm"] != 1 || o.gets["npm"] != 2 {
		t.Errorf("not modified: full %d, gets %d", o.full["npm"], o.gets["npm"])
	}
	if p.due(versions.Npm) {
		t.Error("due again right after the check")
	}

	// A failing source keeps the data, says why, and is retried after an hour, not every tick.
	o.mu.Lock()
	o.fail = true
	o.mu.Unlock()
	clk.add(25 * time.Hour)
	if p.Refresh(ctx, versions.PyPI) {
		t.Error("a failed refresh changed the data")
	}
	if hv := p.HubView(); hv[0].State != OSVOk || hv[0].Code != OSVDownloadFailed {
		t.Errorf("after a failure: %+v", hv[0])
	}
	if p.due(versions.PyPI) {
		t.Error("retried within the hour")
	}
	clk.add(2 * time.Hour)
	if !p.due(versions.PyPI) {
		t.Error("not retried after the hour")
	}
	clk.add(48 * time.Hour) // 3 days without good data
	if hv := p.HubView(); hv[0].State != OSVStale {
		t.Errorf("stale: %+v", hv[0])
	}

	// A restart keeps inventory and index; the counts come back without a download.
	o.mu.Lock()
	o.fail = false
	o.mu.Unlock()
	p2 := newTestPackages(t, dir, o, []string{"PyPI", "npm"}, clk)
	if v := p2.View("host-a"); v == nil || v.Malicious == nil || *v.Malicious != 1 || *v.Vulnerable != 2 {
		t.Fatalf("after restart: %+v", v)
	}
	if !p2.Refresh(ctx, versions.PyPI) && o.full["PyPI"] != 1 {
		t.Error("refresh after restart")
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "osv", "*.part")); len(files) != 0 {
		t.Errorf("dump left on disk: %v", files)
	}

	p2.Forget("host-a")
	if p2.View("host-a") != nil {
		t.Error("forgotten host still has packages")
	}
	if _, err := os.Stat(filepath.Join(dir, "packages", "host-a.json")); !os.IsNotExist(err) {
		t.Error("inventory file left behind")
	}
}

// The host view: malware comes first among the problem reasons (the most urgent thing to do), advisories last; the
// hub view carries the state of the data; a forgotten host loses its inventory.
func TestPackagesInHostView(t *testing.T) {
	f := newViewFixture(t)
	o := newOSVServer(t)
	clk := &clock{t: f.now}
	f.srv.Packages = newTestPackages(t, t.TempDir(), o, []string{"PyPI", "npm"}, clk)
	ctx := context.Background()
	f.srv.Packages.Refresh(ctx, versions.PyPI)
	f.srv.Packages.Refresh(ctx, versions.Npm)
	fp := f.host(t, "echo", []protocol.Verb{protocol.VerbCheck}, wire.PolicySummary{}, true)
	_ = f.srv.Reg.SetReport("echo", aptReport(f.now, 1, 0, nil))

	if v := f.view(t, "echo", f.now); v.Packages != nil || v.Problem != nil {
		t.Fatalf("no inventory yet: %+v / %+v", v.Packages, v.Problem)
	}
	_ = f.srv.Packages.SetInventory("echo", testInventory(f.now))
	later := f.now.Add(8 * 24 * time.Hour) // the apt update is stale by now as well
	v := f.view(t, "echo", later)
	if v.Packages == nil || v.Problem == nil {
		t.Fatalf("view: %+v", v)
	}
	if want := []string{ReasonMaliciousPackage, ReasonOffline, ReasonStale, ReasonVulnerablePackage}; !slices.Equal(v.Problem.Reasons, want) ||
		v.Problem.Reason != ReasonMaliciousPackage {
		t.Errorf("reasons: %v", v.Problem.Reasons)
	}
	if hv := f.srv.HubView(nil); len(hv.OSV) != 2 || hv.OSV[1].Ecosystem != "npm" || hv.OSV[1].State != OSVOk {
		t.Errorf("hub view: %+v", hv.OSV)
	}
	// Only vulnerable: the reason comes last, after what the host itself reports.
	inv := testInventory(f.now)
	inv.Locations = inv.Locations[1:]
	_ = f.srv.Packages.SetInventory("echo", inv)
	if v := f.view(t, "echo", f.now); v.Problem == nil || !slices.Equal(v.Problem.Reasons, []string{ReasonVulnerablePackage}) {
		t.Errorf("vulnerable only: %+v", v.Problem)
	}
	if resp := f.srv.Admin(AdminRequest{Cmd: "forget", Node: "echo", Fingerprint: fp}); resp.Code != AdminOK {
		t.Fatal(resp.Code)
	}
	if f.srv.Packages.View("echo") != nil {
		t.Error("forget left the inventory")
	}
}

// debug 4.4.2 (8 September 2025): a GHSA entry with MAL- alias says "fixed in 4.4.3". For malware the answer is to
// remove it, so no fixed version is offered.
func TestMalwareNamesNoFixedVersion(t *testing.T) {
	o := newOSVServer(t)
	clk := &clock{t: time.Now()}
	p := newTestPackages(t, t.TempDir(), o, []string{"npm"}, clk)
	p.Refresh(context.Background(), versions.Npm)
	_ = p.SetInventory("h", wire.Inventory{Locations: []inventory.Location{{ID: "x", Ecosystem: versions.Npm,
		Packages: []inventory.Package{{Name: "debug", Version: "4.4.2"}}}}})
	v := p.View("h")
	if len(v.Findings) != 1 || !v.Findings[0].Malicious || v.Findings[0].FixedIn != "" || len(v.Findings[0].Advisories) != 2 {
		t.Errorf("debug 4.4.2: %+v", v.Findings)
	}
	// Even when every advisory names a fix: malware gets none, a vulnerability the highest of the lowest fixes.
	mal := findingView("x", "debug", "4.4.2", "", []osv.Finding{{ID: "GHSA-4x49-vf9v-38px", Malicious: true, Fixed: []string{"4.4.3"}}}, versions.Npm)
	vul := findingView("x", "debug", "4.3.0", "", []osv.Finding{{ID: "A", Fixed: []string{"4.3.1"}}, {ID: "B", Fixed: []string{"4.3.5", "5.0.0"}}}, versions.Npm)
	if mal.FixedIn != "" || vul.FixedIn != "4.3.5" {
		t.Errorf("fixed_in: malware %q, vulnerable %q", mal.FixedIn, vul.FixedIn)
	}
}

// Outdated packages (11.3): counted and listed, those an update can take first; a candidate OSV flags is marked -
// axios 1.14.1 was malware (MAL-2026-2307), a fresh release of exactly the kind the minimum age keeps out.
func TestOutdatedInView(t *testing.T) {
	o := newOSVServer(t)
	clk := &clock{t: time.Now()}
	p := newTestPackages(t, t.TempDir(), o, []string{"PyPI", "npm"}, clk)
	ctx := context.Background()
	p.Refresh(ctx, versions.PyPI)
	p.Refresh(ctx, versions.Npm)
	inv := testInventory(clk.now())
	if _ = p.SetInventory("h", inv); p.View("h").Outdated != nil {
		t.Fatal("an agent that does not ask the registries has unknown outdated, not zero")
	}
	at := clk.now().Add(-time.Hour)
	waiting := clk.now().Add(-2 * 24 * time.Hour)
	inv.OutdatedAt, inv.MinAgeDays = &at, 7
	npm := inv.Locations[0].Packages
	npm[2].Latest, npm[2].Candidate = "1.14.1", "1.14.1" // axios: flagged candidate
	npm[1].Latest, npm[1].LatestAt = "5.6.2", &waiting   // chalk: too new, no candidate yet
	inv.Locations[1].Packages[0].Latest = "2.32.4"       // Requests
	inv.Locations[1].Packages[0].Candidate = "2.32.3"    // ready
	inv.Locations[1].Packages[0].Major = false
	_ = p.SetInventory("h", inv)
	v := p.View("h")
	if v.Outdated == nil || *v.Outdated != 3 || v.MinAgeDays != 7 || !v.OutdatedAt.Equal(at) {
		t.Fatalf("view: %+v", v)
	}
	var got []string
	for _, u := range v.Updates {
		got = append(got, u.Name+" "+u.Candidate+" "+strings.Join(u.CandidateFlagged, ","))
	}
	if want := []string{"Requests 2.32.3 ", "axios 1.14.1 MAL-2026-2307", "chalk  "}; !slices.Equal(got, want) {
		t.Errorf("updates: %q", got)
	}
	if v.Locations[0].Outdated != 2 || v.Locations[1].Outdated != 1 || v.Updates[2].LatestAt == nil {
		t.Errorf("locations: %+v / %+v", v.Locations, v.Updates[2])
	}
}

// Vetting (11.4): only for a running update of packages; only with fresh data; a flagged candidate is not offered.
// A dump without entries, or with far fewer entries or malware entries than the data in use, does not replace the data
// (ADR-0085): the hub keeps flagging what it flagged, says why, retries after an hour, and takes the next good dump.
func TestImplausibleDumpKeepsTheData(t *testing.T) {
	o := newOSVServer(t)
	clk := &clock{t: time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)}
	ctx := context.Background()
	empty := func() []byte {
		var buf bytes.Buffer
		if err := zip.NewWriter(&buf).Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}()

	// No data yet and an empty dump: still no data, and the reason.
	o.publish("npm", empty)
	p := newTestPackages(t, t.TempDir(), o, []string{"npm"}, clk)
	if p.Refresh(ctx, versions.Npm) {
		t.Error("an empty dump became the data")
	}
	if hv := p.HubView(); hv[0].State != OSVMissing || hv[0].Code != OSVImplausible {
		t.Errorf("empty first dump: %+v", hv[0])
	}

	// Good data: 13 entries, 7 of them malware.
	o.publish("npm", osvZip(t, "npm"))
	clk.add(2 * time.Hour)
	if !p.Refresh(ctx, versions.Npm) {
		t.Fatal("the good dump was not taken")
	}
	if hv := p.HubView(); hv[0].Advisories != 13 || hv[0].Malicious != 7 || hv[0].Code != "" {
		t.Fatalf("good data: %+v", hv[0])
	}
	plan := []protocol.VetPackage{{Name: "debug", Version: "4.4.2"}}
	for _, c := range []struct {
		name string
		dump []byte
	}{
		{"empty", empty},
		{"two GHSA entries fewer (11 of 13)", osvZipWithout(t, "npm", "GHSA-2275-rpf5-xv8h.json", "GHSA-238x-w2j9-gwwr.json")},
		{"one malware entry fewer (12 of 13 entries, 6 of 7 malware)", osvZipWithout(t, "npm", "MAL-2021-1.json")},
	} {
		o.publish("npm", c.dump)
		clk.add(2 * time.Hour)
		if p.Refresh(ctx, versions.Npm) {
			t.Errorf("%s: became the data", c.name)
		}
		hv := p.HubView()
		if hv[0].State != OSVOk || hv[0].Code != OSVImplausible || hv[0].Advisories != 13 || hv[0].Malicious != 7 {
			t.Errorf("%s: %+v", c.name, hv[0])
		}
		if got, why := p.Vet(versions.Npm, plan); why != "" || !got[0].Malicious {
			t.Errorf("%s: debug 4.4.2 no longer flagged: %q %+v", c.name, why, got)
		}
		if p.due(versions.Npm) {
			t.Errorf("%s: retried within the hour", c.name)
		}
	}

	// A small change is normal and taken: one GHSA entry fewer (12 of 13, 92 %), all malware entries there.
	o.publish("npm", osvZipWithout(t, "npm", "GHSA-2275-rpf5-xv8h.json"))
	clk.add(2 * time.Hour)
	if !p.Refresh(ctx, versions.Npm) || p.HubView()[0].Code != "" || p.HubView()[0].Advisories != 12 {
		t.Errorf("small change after implausible dumps: %+v", p.HubView()[0])
	}
	// And the full one again, under a new ETag.
	o.publish("npm", osvZip(t, "npm"))
	clk.add(2 * time.Hour)
	if !p.Refresh(ctx, versions.Npm) || p.HubView()[0].Advisories != 13 {
		t.Errorf("full dump again: %+v", p.HubView()[0])
	}
}

func TestVetAndUpdatable(t *testing.T) {
	f := newViewFixture(t)
	o := newOSVServer(t)
	clk := &clock{t: f.now}
	p := newTestPackages(t, t.TempDir(), o, []string{"npm"}, clk)
	f.srv.Packages = p
	plan := []protocol.VetPackage{{Name: "debug", Version: "4.4.2"}, {Name: "ms", Version: "2.1.3"}}
	if _, why := p.Vet(versions.Npm, plan); why != OSVMissing {
		t.Errorf("no data: %q", why)
	}
	p.Refresh(context.Background(), versions.Npm)
	got, why := p.Vet(versions.Npm, plan)
	if why != "" || len(got) != 2 || !got[0].Malicious || len(got[1].Flagged) != 0 {
		t.Errorf("vet: %q %+v", why, got)
	}
	clk.add(73 * time.Hour)
	if _, why := p.Vet(versions.Npm, plan); why != OSVStale {
		t.Errorf("stale: %q", why)
	}
	clk.add(-73 * time.Hour)

	// The hub answers only for a running update of packages of that host.
	f.host(t, "echo", []protocol.Verb{protocol.VerbCheck, protocol.VerbInstall}, wire.PolicySummary{}, true)
	req := wire.Vet{T: wire.TVet, JobID: "j1", Ecosystem: "npm", Packages: plan}
	verify := func() protocol.Vet {
		r := f.srv.vet("echo", req)
		v, ok := protocol.VerifyVet(r.Vet, f.srv.Key.Public(), "echo", "j1", clk.now())
		if !ok {
			t.Fatal("unsigned or wrong answer")
		}
		return v
	}
	if v := verify(); v.Unavailable != "no_running_update" || len(v.Packages) != 0 {
		t.Errorf("no run: %+v", v)
	}
	f.srv.storeRun("echo", Run{JobID: "j1", Verb: protocol.VerbInstall, Scope: "all", Started: f.now})
	if v := verify(); v.Unavailable != "no_running_update" {
		t.Errorf("apt run: %+v", v)
	}
	f.srv.storeRun("echo", Run{JobID: "j1", Verb: protocol.VerbInstall, Scope: "pkg:npm-global", Started: f.now})
	if v := verify(); v.Unavailable != "" || !v.Covers(plan) || !v.Packages[0].Malicious {
		t.Errorf("pkg run: %+v", v)
	}
	// A refresh of dependencies asks too (ADR-0104): which installed ones have findings, then its plan.
	f.srv.storeRun("echo", Run{JobID: "j1", Verb: protocol.VerbInstall, Scope: "deps:npm-global", Started: f.now})
	if v := verify(); v.Unavailable != "" || !v.Covers(plan) {
		t.Errorf("deps run: %+v", v)
	}

	// Updatable: the location must be updatable, a named package needs a candidate OSV does not flag.
	inv := testInventory(f.now)
	inv.Locations[0].Update = true
	inv.Locations[0].Packages[2].Candidate = "1.14.1" // axios: malware
	inv.Locations[0].Packages[3].Candidate = "1.0.1"  // some-cli: clean
	_ = p.SetInventory("echo", inv)
	for _, c := range []struct {
		loc, name string
		want      bool
	}{{"npm-global", "", true}, {"npm-global", "some-cli", true}, {"npm-global", "axios", false},
		{"npm-global", "chalk", false}, {"venv-app", "", false}} {
		if got := p.Updatable("echo", c.loc, c.name); got != c.want {
			t.Errorf("Updatable(%s, %s) = %v", c.loc, c.name, got)
		}
	}

	// refresh_deps: only where the agent reported it can (Deps), never for an agent that does not say so.
	if code := refreshDeps(f, "echo", "npm-global"); code != AdminNotAllowed {
		t.Errorf("refresh without Deps: %s", code)
	}
	inv.Locations[0].Deps = true
	_ = p.SetInventory("echo", inv)
	if got := p.RefreshableLocations("echo"); !slices.Equal(got, []string{"npm-global"}) {
		t.Errorf("refreshable %v", got)
	}
	if code := refreshDeps(f, "echo", "npm-global"); code != AdminOK {
		t.Errorf("refresh with Deps: %s", code)
	}
	for _, loc := range []string{"venv-app", "../etc", ""} {
		if code := refreshDeps(f, "echo", loc); code == AdminOK {
			t.Errorf("refresh %q: %s", loc, code)
		}
	}
	if v := f.view(t, "echo", f.now); !slices.Equal(v.Capabilities.DependencyRefresh, []string{"npm-global"}) {
		t.Errorf("capabilities %+v", v.Capabilities)
	}
}

// refreshDeps maps the HA command and says what the hub would do with it.
func refreshDeps(f *viewFixture, node, loc string) AdminCode {
	verb, scope, code := f.srv.Resolve(node, "refresh_deps:"+loc)
	if code == AdminOK && (verb != protocol.VerbInstall || scope != "deps:"+loc) {
		return AdminBadRequest
	}
	return code
}

// Data for one ecosystem only: a host with packages of the other has unknown counts, never "none".
func TestPackagesPartialData(t *testing.T) {
	o := newOSVServer(t)
	clk := &clock{t: time.Now()}
	p := newTestPackages(t, t.TempDir(), o, []string{"PyPI"}, clk)
	if !p.Refresh(context.Background(), versions.PyPI) {
		t.Fatal("no data")
	}
	_ = p.SetInventory("h", testInventory(clk.now()))
	if v := p.View("h"); v.Vulnerable != nil || len(v.Findings) != 1 || v.Findings[0].Name != "Requests" {
		t.Errorf("view: %+v", v)
	}
	inv := testInventory(clk.now())
	inv.Locations = inv.Locations[1:] // Python only
	_ = p.SetInventory("h", inv)
	if v := p.View("h"); v.Vulnerable == nil || *v.Vulnerable != 1 {
		t.Errorf("Python only: %+v", v)
	}
}

func TestOSVConfigCheck(t *testing.T) {
	cfg, issues := ParseConfig([]byte("tls_cert: x\ntls_key: y\nosv: {}\n"))
	if cfg.OSV == nil || cfg.OSV.URL != defaultOSVURL || cfg.OSV.RefreshHours != 24 || cfg.OSV.StaleHours != 72 ||
		!slices.Equal(cfg.OSV.Ecosystems, []string{"PyPI", "npm"}) {
		t.Errorf("defaults: %+v", cfg.OSV)
	}
	for _, i := range issues {
		if len(i) > 4 && i[:4] == "osv." {
			t.Errorf("issue with defaults: %s", i)
		}
	}
	_, issues = ParseConfig([]byte("tls_cert: x\ntls_key: y\nosv: {url: 'http://x', ecosystems: [PyPI, PyPI, Go], " +
		"refresh_hours: 200, stale_hours: 2}\n"))
	n := 0
	for _, i := range issues {
		if len(i) > 4 && i[:4] == "osv." {
			n++
		}
	}
	if n != 5 {
		t.Errorf("want 5 osv issues, got %v", issues)
	}
}
