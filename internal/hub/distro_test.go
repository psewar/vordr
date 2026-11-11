package hub

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/inventory"
	"github.com/psewar/vordr/internal/osv"
	"github.com/psewar/vordr/internal/versions"
	"github.com/psewar/vordr/internal/wire"
)

// A distribution's dump as osv.dev writes it: the same CVE as DEBIAN-CVE and as the DSA that fixed it (upstream names
// it), one without a fix, one Debian rates "unimportant", a binNMU'd source package, and an entry of another release.
var debianEntries = []string{
	`{"id":"DEBIAN-CVE-2025-1","modified":"2026-11-01T00:00:00Z","upstream":["CVE-2025-1"],"affected":[{"package":{"ecosystem":"Debian:13","name":"openssl"},"ranges":[{"type":"ECOSYSTEM","events":[{"introduced":"0"},{"fixed":"3.5.2-1"}]}]}]}`,
	`{"id":"DSA-6000-1","modified":"2026-11-01T00:00:00Z","upstream":["CVE-2025-1","DEBIAN-CVE-2025-1"],"affected":[{"package":{"ecosystem":"Debian:13","name":"openssl"},"ranges":[{"type":"ECOSYSTEM","events":[{"introduced":"0"},{"fixed":"3.5.2-1"}]}]}]}`,
	`{"id":"DEBIAN-CVE-2025-2","modified":"2026-11-01T00:00:00Z","upstream":["CVE-2025-2"],"affected":[{"package":{"ecosystem":"Debian:13","name":"openssl"},"ranges":[{"type":"ECOSYSTEM","events":[{"introduced":"0"}]}]}]}`,
	`{"id":"DEBIAN-CVE-2010-0928","modified":"2026-11-01T00:00:00Z","upstream":["CVE-2010-0928"],"affected":[{"package":{"ecosystem":"Debian:13","name":"openssl"},"ranges":[{"type":"ECOSYSTEM","events":[{"introduced":"0"}]}],"ecosystem_specific":{"urgency":"unimportant"}}]}`,
	`{"id":"DEBIAN-CVE-2023-3","modified":"2026-11-01T00:00:00Z","upstream":["CVE-2023-3"],"affected":[{"package":{"ecosystem":"Debian:13","name":"zlib"},"ranges":[{"type":"ECOSYSTEM","events":[{"introduced":"0"},{"fixed":"1:1.3.dfsg-1+b1"}]}]}]}`,
	`{"id":"DEBIAN-CVE-2024-9","modified":"2026-11-01T00:00:00Z","upstream":["CVE-2024-9"],"affected":[{"package":{"ecosystem":"Debian:12","name":"openssl"},"ranges":[{"type":"ECOSYSTEM","events":[{"introduced":"0"},{"fixed":"9"}]}]}]}`,
}

var alpineEntries = []string{
	`{"id":"ALPINE-CVE-2025-5","modified":"2026-11-01T00:00:00Z","upstream":["CVE-2025-5"],"affected":[{"package":{"ecosystem":"Alpine:v3.23","name":"openssl"},"ranges":[{"type":"ECOSYSTEM","events":[{"introduced":"0"},{"fixed":"3.5.4-r1"}]}]}]}`,
}

func dumpOf(t *testing.T, entries []string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i, e := range entries {
		w, _ := zw.Create(filepath.Join("e", string(rune('a'+i))+".json"))
		_, _ = w.Write([]byte(e))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

var (
	debian13 = &inventory.ImageOS{ID: "debian", Version: "13", Packages: []inventory.OSPackage{
		{Name: "base-files", Version: "13.8"},
		{Name: "libssl3t64", Version: "3.5.1-1", Source: "openssl"},
		{Name: "openssl", Version: "3.5.1-1"},
		// a binNMU: the binary is +b1, the source not; the fix names the binNMU, so only the source version is affected
		{Name: "zlib1g", Version: "1:1.3.dfsg-1+b1", Source: "zlib", SourceVersion: "1:1.3.dfsg-1"},
	}}
	alpine323 = &inventory.ImageOS{ID: "alpine", Version: "3.23.4", Packages: []inventory.OSPackage{
		{Name: "libcrypto3", Version: "3.5.4-r0", Source: "openssl"}}}
)

func containerInventory(at time.Time, imgs ...inventory.Package) wire.Inventory {
	return wire.Inventory{T: wire.TInventory, CheckedAt: at, Locations: []inventory.Location{
		{ID: "containers", Kind: inventory.KindContainer, Ecosystem: "oci", Path: "/run/docker.sock", Packages: imgs}}}
}

func TestReleaseOf(t *testing.T) {
	for _, c := range []struct {
		os   *inventory.ImageOS
		want string
		code string
	}{
		{&inventory.ImageOS{ID: "debian", Version: "13"}, "Debian:13", ""},
		{&inventory.ImageOS{ID: "debian", Version: "12.4"}, "Debian:12", ""},
		{&inventory.ImageOS{ID: "alpine", Version: "3.23.4"}, "Alpine:v3.23", ""},
		{&inventory.ImageOS{ID: "debian"}, "", ImageUnsupportedRelease}, // testing: no VERSION_ID
		{&inventory.ImageOS{ID: "alpine", Version: "edge"}, "", ImageUnsupportedRelease},
		{&inventory.ImageOS{ID: "ubuntu", Version: "24.04"}, "", ImageUnsupportedDistribution},
		{&inventory.ImageOS{Code: inventory.CodeNoPackageDB}, "", inventory.CodeNoPackageDB},
		// cut by the agent: the rest is matched; left out to fit the message: nothing to match
		{&inventory.ImageOS{ID: "debian", Version: "13", Code: inventory.CodeTooManyPackages,
			Packages: []inventory.OSPackage{{Name: "x", Version: "1"}}}, "Debian:13", ""},
		{&inventory.ImageOS{ID: "debian", Version: "13", Code: inventory.CodeTooManyPackages}, "",
			inventory.CodeTooManyPackages},
		{nil, "", ImageNotRead},
	} {
		got, code := releaseOf(c.os)
		if string(got) != c.want || code != c.code {
			t.Errorf("%+v: %q %q, want %q %q", c.os, got, code, c.want, c.code)
		}
	}
}

// The whole way at the hub (ADR-0120): nothing downloaded while no image runs a distribution's release; then only the
// releases images run, one index each; the findings per image - one CVE counted once, "unimportant" left out, the
// source version of a binNMU; the reasons for images that were not matched; the data state; after a restart the
// findings come back from the indexes on disk; a new release fetches the dump anew.
func TestImageFindingsThroughTheHub(t *testing.T) {
	dir := t.TempDir()
	clk := &clock{t: time.Date(2026, 11, 6, 3, 0, 0, 0, time.UTC)}
	o := newOSVServer(t)
	o.publish("Debian", dumpOf(t, debianEntries))
	o.publish("Alpine", dumpOf(t, alpineEntries))
	p := newTestPackages(t, dir, o, []string{"PyPI", "Debian", "Alpine"}, clk)
	ctx := context.Background()
	if !p.ImageContentsOn() {
		t.Fatal("the hub with Debian in osv.ecosystems does not ask for image contents")
	}

	p.refreshAll(ctx, func() {})
	if o.gets["Debian"] != 0 || o.gets["Alpine"] != 0 {
		t.Fatalf("downloaded without an image that needs it: %v", o.gets)
	}
	inv := containerInventory(clk.now(),
		inventory.Package{Name: "docker.io/library/postgres", Version: "17", Digest: "sha256:a", Containers: []string{"db"}, OS: debian13},
		inventory.Package{Name: "docker.io/library/nginx", Version: "alpine", Digest: "sha256:b", OS: alpine323},
		inventory.Package{Name: "docker.io/library/webapp", Version: "local", OS: &inventory.ImageOS{ID: "ubuntu", Version: "24.04"}},
		inventory.Package{Name: "ghcr.io/org/static", Version: "1", Digest: "sha256:c", OS: &inventory.ImageOS{Code: inventory.CodeNoPackageDB}},
		inventory.Package{Name: "ghcr.io/org/old", Version: "1", Digest: "sha256:d"},
	)
	if err := p.SetInventory("h", inv); err != nil {
		t.Fatal(err)
	}
	if v := p.Containers("h"); v.Fixable != nil || codeOf(v, "postgres") != ImageNoData {
		t.Fatalf("before the data is there: %+v", v)
	}
	p.refreshAll(ctx, func() {})
	if o.full["Debian"] != 1 || o.full["Alpine"] != 1 || !p.hasIndex("Debian:13") || p.hasIndex("Debian:12") {
		t.Fatalf("downloads %v, Debian:13 %v, Debian:12 %v", o.full, p.hasIndex("Debian:13"), p.hasIndex("Debian:12"))
	}
	check := func(v *wire.ContainersView) {
		t.Helper()
		if v.Fixable == nil || *v.Fixable != 2 || v.Unfixed == nil || *v.Unfixed != 1 {
			t.Fatalf("counts: %+v", v)
		}
		pg := findingOf(v, "postgres")
		if pg.Release != "Debian:13" || pg.Fixable != 2 || pg.Unfixed != 1 ||
			!slices.Equal(pg.IDs, []string{"CVE-2023-3", "CVE-2025-1", "CVE-2025-2"}) {
			t.Errorf("debian image: %+v", pg)
		}
		if a := findingOf(v, "nginx"); a.Release != "Alpine:v3.23" || a.Fixable != 1 || a.Unfixed != 0 {
			t.Errorf("alpine image: %+v", a)
		}
		for name, code := range map[string]string{"webapp": ImageUnsupportedDistribution,
			"static": inventory.CodeNoPackageDB, "old": ImageNotRead} {
			if got := codeOf(v, name); got != code {
				t.Errorf("%s: code %q, want %q", name, got, code)
			}
		}
		if v.Vulnerable[0].Fixable == 0 {
			t.Errorf("fixable images first: %+v", v.Vulnerable)
		}
	}
	check(p.Containers("h"))
	hub := p.HubView()
	if !slices.ContainsFunc(hub, func(o wire.OSVView) bool { return o.Ecosystem == "Debian:13" && o.State == OSVOk }) ||
		slices.ContainsFunc(hub, func(o wire.OSVView) bool { return o.Ecosystem == "Debian:12" || o.Ecosystem == "Debian" }) {
		t.Errorf("hub view: %+v", hub)
	}

	// A restart: the findings come back from the indexes on disk, nothing downloaded.
	p2 := newTestPackages(t, dir, o, []string{"PyPI", "Debian", "Alpine"}, clk)
	p2.matchAll()
	check(p2.Containers("h"))
	p2.refreshAll(ctx, func() {})
	if o.full["Debian"] != 1 {
		t.Errorf("downloaded again within the day: %v", o.full)
	}
	// A Debian 12 image appears: its release has no index, so the dump comes anew, whatever its ETag.
	inv.Locations[0].Packages = append(inv.Locations[0].Packages, inventory.Package{Name: "docker.io/library/redis",
		Version: "7", Digest: "sha256:e", OS: &inventory.ImageOS{ID: "debian", Version: "12",
			Packages: []inventory.OSPackage{{Name: "openssl", Version: "3.0.1-1"}}}})
	_ = p2.SetInventory("h", inv)
	p2.refreshAll(ctx, func() {})
	if o.full["Debian"] != 2 || !p2.hasIndex("Debian:12") || findingOf(p2.Containers("h"), "redis").Fixable != 1 {
		t.Errorf("a new release: downloads %v, %+v", o.full, findingOf(p2.Containers("h"), "redis"))
	}
	// The postgres image is rebuilt with the fixed openssl: its next inventory alone, without new data, takes the
	// fixed CVE away (the release's index is matched again from disk).
	rebuilt := *debian13
	rebuilt.Packages = []inventory.OSPackage{{Name: "libssl3t64", Version: "3.5.2-1", Source: "openssl"},
		{Name: "zlib1g", Version: "1:1.3.dfsg-1+b1", Source: "zlib", SourceVersion: "1:1.3.dfsg-1"}}
	inv.Locations[0].Packages[0].OS = &rebuilt
	full := o.full["Debian"]
	_ = p2.SetInventory("h", inv)
	if pg := findingOf(p2.Containers("h"), "postgres"); pg.Fixable != 1 || pg.Unfixed != 1 || o.full["Debian"] != full {
		t.Errorf("after the rebuild: %+v, downloads %v", pg, o.full)
	}
}

// Without room for the dump beside the margin, the download is not written: a state of its own, nothing on disk.
func TestDistributionDumpNeedsRoom(t *testing.T) {
	was := freeSpace
	freeSpace = func(string) (uint64, bool) { return 1 << 20, true }
	defer func() { freeSpace = was }()
	dir := t.TempDir()
	clk := &clock{t: time.Date(2026, 11, 6, 3, 0, 0, 0, time.UTC)}
	o := newOSVServer(t)
	o.publish("Debian", dumpOf(t, debianEntries))
	p := newTestPackages(t, dir, o, []string{"Debian"}, clk)
	_ = p.SetInventory("h", containerInventory(clk.now(), inventory.Package{Name: "postgres", Version: "17",
		Digest: "sha256:a", OS: debian13}))
	p.refreshAll(context.Background(), func() {})
	if p.hasIndex("Debian:13") || p.meta["Debian"].Code != OSVNoSpace {
		t.Fatalf("index %v, code %q", p.hasIndex("Debian:13"), p.meta["Debian"].Code)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "osv", "*.part")); len(left) != 0 {
		t.Errorf("left on disk: %v", left)
	}
	if hub := p.HubView(); len(hub) != 1 || hub[0].Code != OSVNoSpace {
		t.Errorf("hub view: %+v", hub)
	}
}

// ADR-0130: a fixable advisory says whether a newer image is there to install. Only an image with a candidate (a
// rebuild or a newer tag old enough) counts in fixable_newer and comes first; one whose publisher has not rebuilt yet
// stays fixable, without "newer". Never compared: whether a newer image exists is unknown, so fixable_newer is nil.
func TestImageFindingsSayWhetherANewerImageIsThere(t *testing.T) {
	clk := &clock{t: time.Date(2026, 11, 6, 3, 0, 0, 0, time.UTC)}
	o := newOSVServer(t)
	o.publish("Debian", dumpOf(t, debianEntries))
	o.publish("Alpine", dumpOf(t, alpineEntries))
	p := newTestPackages(t, t.TempDir(), o, []string{"Debian", "Alpine"}, clk)
	imgs := []inventory.Package{
		// fixable, no newer image: sorted before the others by name, but must come after the one with a candidate
		{Name: "docker.io/library/nginx", Version: "alpine", Digest: "sha256:b", OS: alpine323},
		{Name: "docker.io/library/postgres", Version: "17", Digest: "sha256:a", OS: debian13, RebuiltCandidate: true,
			Rebuilt: "sha256:a2"},
		// a newer tag, but too new: no candidate
		{Name: "docker.io/library/redis", Version: "7", Digest: "sha256:c", OS: debian13, Latest: "8"},
		// a candidate, but nothing fixable inside: not counted
		{Name: "docker.io/library/static", Version: "1", Digest: "sha256:d", Candidate: "2", Latest: "2",
			OS: &inventory.ImageOS{ID: "debian", Version: "13", Packages: []inventory.OSPackage{{Name: "base-files", Version: "13.8"}}}},
	}
	inv := containerInventory(clk.now(), imgs...)
	_ = p.SetInventory("h", inv)
	p.refreshAll(context.Background(), func() {})
	v := p.Containers("h")
	if v.Fixable == nil || *v.Fixable != 3 || v.FixableNewer != nil {
		t.Fatalf("never compared: fixable %v, fixable_newer %v (want 3, nil)", v.Fixable, v.FixableNewer)
	}
	if findingOf(v, "postgres").Newer {
		t.Errorf("never compared, but newer: %+v", findingOf(v, "postgres"))
	}

	at := clk.now()
	inv.Locations[0].ComparedAt = &at
	_ = p.SetInventory("h", inv)
	v = p.Containers("h")
	if v.FixableNewer == nil || *v.FixableNewer != 1 || *v.Fixable != 3 {
		t.Fatalf("compared: fixable %v, fixable_newer %v (want 3, 1)", v.Fixable, v.FixableNewer)
	}
	if pg, ng, rd := findingOf(v, "postgres"), findingOf(v, "nginx"), findingOf(v, "redis"); !pg.Newer || ng.Newer ||
		rd.Newer || pg.Fixable == 0 || ng.Fixable == 0 {
		t.Errorf("newer: postgres %+v, nginx %+v, redis %+v", pg, ng, rd)
	}
	if len(v.Vulnerable) == 0 || filepath.Base(v.Vulnerable[0].Image) != "postgres" {
		t.Errorf("the image a newer one fixes comes first: %+v", v.Vulnerable)
	}
}

func findingOf(v *wire.ContainersView, name string) wire.ImageFindingsView {
	for _, f := range v.Vulnerable {
		if filepath.Base(f.Image) == name {
			return f
		}
	}
	return wire.ImageFindingsView{}
}

func codeOf(v *wire.ContainersView, name string) string { return findingOf(v, name).Code }

// The hub asks agents for image contents only with a distribution's dump in osv.ecosystems (ADR-0120).
func TestFeatureImageContents(t *testing.T) {
	clk := &clock{t: time.Date(2026, 11, 6, 3, 0, 0, 0, time.UTC)}
	o := newOSVServer(t)
	for ecos, want := range map[string]bool{"PyPI,npm": false, "PyPI,Alpine": true, "Debian": true} {
		s := &Server{Packages: newTestPackages(t, t.TempDir(), o, strings.Split(ecos, ","), clk)}
		if got := slices.Contains(s.features(), wire.FeatureImageContents); got != want {
			t.Errorf("%s: image_contents %v, want %v", ecos, got, want)
		}
	}
	if s := (&Server{Packages: newTestPackages(t, t.TempDir(), nil, nil, clk)}); slices.Contains(s.features(),
		wire.FeatureImageContents) {
		t.Error("announced without OSV data")
	}
}

// One CVE with an advisory that names a fix and one that does not (DEBIAN-CVE before the DSA caught up): fixable.
func TestOneCVEFixableWins(t *testing.T) {
	dump := dumpOf(t, []string{
		`{"id":"DEBIAN-CVE-2025-7","modified":"2026-11-01T00:00:00Z","upstream":["CVE-2025-7"],"affected":[{"package":{"ecosystem":"Debian:13","name":"curl"},"ranges":[{"type":"ECOSYSTEM","events":[{"introduced":"0"}]}]}]}`,
		`{"id":"DSA-6007-1","modified":"2026-11-01T00:00:00Z","upstream":["CVE-2025-7"],"affected":[{"package":{"ecosystem":"Debian:13","name":"curl"},"ranges":[{"type":"ECOSYSTEM","events":[{"introduced":"0"},{"fixed":"8.14.1-2"}]}]}]}`,
	})
	db, _, err := osv.Build(bytes.NewReader(dump), int64(len(dump)), "Debian:13", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	m := matchImage(&inventory.ImageOS{ID: "debian", Version: "13",
		Packages: []inventory.OSPackage{{Name: "curl", Version: "8.14.1-1"}}}, db)
	if !slices.Equal(m.Fixable, []string{"CVE-2025-7"}) || len(m.Unfixed) != 0 {
		t.Errorf("%+v", m)
	}
}

func viewsOf(hub []wire.OSVView) map[string]wire.OSVView {
	out := map[string]wire.OSVView{}
	for _, v := range hub {
		out[v.Ecosystem] = v
	}
	return out
}

func (o *osvServer) fullOf(eco string) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.full[eco]
}

var (
	debian11 = &inventory.ImageOS{ID: "debian", Version: "11", Packages: []inventory.OSPackage{{Name: "openssl", Version: "1.1.1-1"}}}
	debian12 = &inventory.ImageOS{ID: "debian", Version: "12", Packages: []inventory.OSPackage{{Name: "openssl", Version: "3.0.1-1"}}}
)

// Each release stands on its own: one the dump has no entries for is a state (no_entries, its
// images no_data) and the others are built; a release whose data would shrink sharply keeps its old index and shows
// its code (ADR-0085) while the others take the new dump; it is tried again after osvRetry, not before, and then only
// that release is built.
func TestReleasesStandOnTheirOwn(t *testing.T) {
	clk := &clock{t: time.Date(2026, 11, 6, 3, 0, 0, 0, time.UTC)}
	o := newOSVServer(t)
	o.publish("Debian", dumpOf(t, debianEntries))
	p := newTestPackages(t, t.TempDir(), o, []string{"Debian"}, clk)
	ctx := context.Background()
	_ = p.SetInventory("h", containerInventory(clk.now(),
		inventory.Package{Name: "a", Version: "1", Digest: "sha256:a", OS: debian11},
		inventory.Package{Name: "b", Version: "1", Digest: "sha256:b", OS: debian12},
		inventory.Package{Name: "c", Version: "1", Digest: "sha256:c", OS: debian13}))
	p.refreshAll(ctx, func() {})
	v := p.Containers("h")
	if codeOf(v, "a") != ImageNoData || findingOf(v, "b").Fixable != 1 || findingOf(v, "c").Fixable != 2 {
		t.Fatalf("after the first download: %+v", v.Vulnerable)
	}
	hub := viewsOf(p.HubView())
	if hub["Debian:11"].State != OSVMissing || hub["Debian:11"].Code != OSVNoEntries ||
		hub["Debian:12"].State != OSVOk || hub["Debian:12"].Code != "" || hub["Debian:13"].State != OSVOk {
		t.Fatalf("hub view: %+v", hub)
	}
	clk.add(osvRetry + time.Minute)
	p.refreshAll(ctx, func() {})
	if o.fullOf("Debian") != 1 {
		t.Fatalf("a release without entries brought the dump again: %d downloads", o.fullOf("Debian"))
	}

	// The next dump: Debian 13 shrinks to one entry of five, Debian 12 gains one.
	o.publish("Debian", dumpOf(t, []string{debianEntries[4], debianEntries[5],
		`{"id":"DEBIAN-CVE-2024-10","modified":"2026-11-02T00:00:00Z","upstream":["CVE-2024-10"],"affected":[{"package":{"ecosystem":"Debian:12","name":"openssl"},"ranges":[{"type":"ECOSYSTEM","events":[{"introduced":"0"}]}]}]}`}))
	clk.add(24 * time.Hour)
	p.refreshAll(ctx, func() {})
	v = p.Containers("h")
	if b := findingOf(v, "b"); b.Fixable != 1 || b.Unfixed != 1 {
		t.Errorf("Debian 12 did not take the new dump: %+v", b)
	}
	if c := findingOf(v, "c"); c.Fixable != 2 || c.Unfixed != 1 {
		t.Errorf("Debian 13 did not keep its data: %+v", c)
	}
	hub = viewsOf(p.HubView())
	if hub["Debian:13"].Code != OSVImplausible || hub["Debian:13"].State != OSVOk || hub["Debian:13"].Advisories != 5 ||
		hub["Debian:12"].Code != "" || hub["Debian:12"].Advisories != 2 {
		t.Errorf("hub view: %+v", hub)
	}
	// the data Debian 13 keeps is as old as its build, not as the last check
	if c := hub["Debian:13"].Checked; c == nil || !c.Equal(time.Date(2026, 11, 6, 3, 0, 0, 0, time.UTC)) {
		t.Errorf("Debian 13 checked %v", c)
	}
	full, built12 := o.fullOf("Debian"), p.meta["Debian"].Releases["Debian:12"].Built
	clk.add(osvRetry / 2)
	p.refreshAll(ctx, func() {})
	if o.fullOf("Debian") != full {
		t.Errorf("tried again before osvRetry")
	}
	clk.add(osvRetry/2 + time.Minute)
	p.refreshAll(ctx, func() {})
	if o.fullOf("Debian") != full+1 || !p.meta["Debian"].Releases["Debian:12"].Built.Equal(built12) {
		t.Errorf("the retry: %d downloads (want %d), Debian 12 built again %v", o.fullOf("Debian"), full+1,
			!p.meta["Debian"].Releases["Debian:12"].Built.Equal(built12))
	}
}

// A release's index counts only for the dump and the index version it was built from: a release
// whose index is of an older dump - it was not needed while the dump changed - or of an older index version brings the
// dump anew at once, not at the next refresh time; an unreadable index is forgotten and built anew.
func TestReleaseIndexOfAnotherDump(t *testing.T) {
	dir := t.TempDir()
	clk := &clock{t: time.Date(2026, 11, 6, 3, 0, 0, 0, time.UTC)}
	o := newOSVServer(t)
	o.publish("Debian", dumpOf(t, debianEntries))
	p := newTestPackages(t, dir, o, []string{"Debian"}, clk)
	ctx := context.Background()
	img12 := inventory.Package{Name: "b", Version: "1", Digest: "sha256:b", OS: debian12}
	img13 := inventory.Package{Name: "c", Version: "1", Digest: "sha256:c", OS: debian13}
	_ = p.SetInventory("h", containerInventory(clk.now(), img12, img13))
	p.refreshAll(ctx, func() {})

	_ = p.SetInventory("h", containerInventory(clk.now(), img13)) // Debian 12 no longer runs
	o.publish("Debian", dumpOf(t, debianEntries))
	clk.add(25 * time.Hour)
	p.refreshAll(ctx, func() {})
	if o.fullOf("Debian") != 2 || p.meta["Debian"].Releases["Debian:12"].ETag == p.meta["Debian"].ETag {
		t.Fatalf("setup: %d downloads, %+v", o.fullOf("Debian"), p.meta["Debian"])
	}
	_ = p.SetInventory("h", containerInventory(clk.now(), img12, img13)) // and runs again
	p.refreshAll(ctx, func() {})
	if o.fullOf("Debian") != 3 || p.meta["Debian"].Releases["Debian:12"].ETag != p.meta["Debian"].ETag {
		t.Errorf("an index of an older dump was taken as current: %d downloads", o.fullOf("Debian"))
	}

	rm := p.meta["Debian"].Releases["Debian:13"]
	rm.Index = osv.IndexVersion - 1
	p.meta["Debian"].Releases["Debian:13"] = rm
	p.refreshAll(ctx, func() {})
	if o.fullOf("Debian") != 4 || p.meta["Debian"].Releases["Debian:13"].Index != osv.IndexVersion {
		t.Errorf("an index of an older version was taken as current: %d downloads", o.fullOf("Debian"))
	}

	if err := os.WriteFile(p.indexPath("Debian:13"), []byte("not an index"), 0o600); err != nil {
		t.Fatal(err)
	}
	p2 := newTestPackages(t, dir, o, []string{"Debian"}, clk)
	p2.matchAll()
	if codeOf(p2.Containers("h"), "c") != ImageNoData || p2.hasIndex("Debian:13") {
		t.Errorf("an unreadable index: %+v, on disk %v", findingOf(p2.Containers("h"), "c"), p2.hasIndex("Debian:13"))
	}
	p2.refreshAll(ctx, func() {})
	if o.fullOf("Debian") != 5 || findingOf(p2.Containers("h"), "c").Fixable != 2 {
		t.Errorf("not built anew: %d downloads, %+v", o.fullOf("Debian"), findingOf(p2.Containers("h"), "c"))
	}
}

// Building a release's index waits for a match in progress, so no two indexes are in memory at once (ADR-0120).
func TestBuildWaitsForTheMatchLock(t *testing.T) {
	clk := &clock{t: time.Date(2026, 11, 6, 3, 0, 0, 0, time.UTC)}
	o := newOSVServer(t)
	o.publish("Debian", dumpOf(t, debianEntries))
	p := newTestPackages(t, t.TempDir(), o, []string{"Debian"}, clk)
	_ = p.SetInventory("h", containerInventory(clk.now(),
		inventory.Package{Name: "c", Version: "1", Digest: "sha256:c", OS: debian13}))
	p.matchMu.Lock()
	done := make(chan struct{})
	go func() { p.refreshAll(context.Background(), func() {}); close(done) }()
	for deadline := time.Now().Add(10 * time.Second); o.fullOf("Debian") == 0 && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	if p.hasIndex("Debian:13") {
		t.Error("built while a match held the lock")
	}
	p.matchMu.Unlock()
	<-done
	if !p.hasIndex("Debian:13") {
		t.Error("not built after the match")
	}
}

// Findings count by CVE: a DSA for two CVEs counts both, and its fix wins over a DEBIAN-CVE
// without one; a CVE Debian rates "unimportant" for a package stays out even when a DSA names it, but counts through
// another package it is not unimportant for; an advisory without a CVE counts under its own id.
func TestFindingsCountByCVE(t *testing.T) {
	dump := dumpOf(t, []string{
		`{"id":"DSA-7000-1","modified":"2026-11-01T00:00:00Z","upstream":["CVE-2026-1","CVE-2026-2"],"affected":[{"package":{"ecosystem":"Debian:13","name":"libx"},"ranges":[{"type":"ECOSYSTEM","events":[{"introduced":"0"},{"fixed":"2.0-1"}]}]}]}`,
		`{"id":"DEBIAN-CVE-2026-2","modified":"2026-11-01T00:00:00Z","upstream":["CVE-2026-2"],"affected":[{"package":{"ecosystem":"Debian:13","name":"libx"},"ranges":[{"type":"ECOSYSTEM","events":[{"introduced":"0"}]}]}]}`,
		`{"id":"DEBIAN-CVE-2026-3","modified":"2026-11-01T00:00:00Z","upstream":["CVE-2026-3"],"affected":[{"package":{"ecosystem":"Debian:13","name":"libx"},"ranges":[{"type":"ECOSYSTEM","events":[{"introduced":"0"}]}],"ecosystem_specific":{"urgency":"unimportant"}}]}`,
		`{"id":"DSA-7001-1","modified":"2026-11-01T00:00:00Z","upstream":["CVE-2026-3"],"affected":[{"package":{"ecosystem":"Debian:13","name":"libx"},"ranges":[{"type":"ECOSYSTEM","events":[{"introduced":"0"},{"fixed":"2.0-1"}]}]}]}`,
		`{"id":"DEBIAN-CVE-2026-4","modified":"2026-11-01T00:00:00Z","upstream":["CVE-2026-4"],"affected":[{"package":{"ecosystem":"Debian:13","name":"liby"},"ranges":[{"type":"ECOSYSTEM","events":[{"introduced":"0"}]}],"ecosystem_specific":{"urgency":"unimportant"}},{"package":{"ecosystem":"Debian:13","name":"libz"},"ranges":[{"type":"ECOSYSTEM","events":[{"introduced":"0"}]}]}]}`,
		`{"id":"DSA-7002-1","modified":"2026-11-01T00:00:00Z","affected":[{"package":{"ecosystem":"Debian:13","name":"libz"},"ranges":[{"type":"ECOSYSTEM","events":[{"introduced":"0"},{"fixed":"2.0-1"}]}]}]}`,
	})
	db, _, err := osv.Build(bytes.NewReader(dump), int64(len(dump)), "Debian:13", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	m := matchImage(&inventory.ImageOS{ID: "debian", Version: "13", Packages: []inventory.OSPackage{
		{Name: "libx", Version: "1.0-1"}, {Name: "liby", Version: "1.0-1"}, {Name: "libz", Version: "1.0-1"}}}, db)
	if !slices.Equal(m.Fixable, []string{"CVE-2026-1", "CVE-2026-2", "DSA-7002-1"}) ||
		!slices.Equal(m.Unfixed, []string{"CVE-2026-4"}) {
		t.Errorf("fixable %v, unfixed %v", m.Fixable, m.Unfixed)
	}
}

// The kernel's own packages in an image are matched but not counted (ADR-0122): a container runs the host's kernel.
// Their advisories show as kernel, fixed or not, "unimportant" ones not at all; a CVE another package of the image has
// too counts there. Packages that only have linux in their name count as before. An image with nothing but kernel
// advisories is listed with them, and counts neither as fixable nor as unfixed.
func TestKernelFindingsAreLeftOut(t *testing.T) {
	entry := func(id, eco, cve, fixed, urgency string, pkgs ...string) string {
		events := `[{"introduced":"0"}]`
		if fixed != "" {
			events = `[{"introduced":"0"},{"fixed":"` + fixed + `"}]`
		}
		extra := ""
		if urgency != "" {
			extra = `,"ecosystem_specific":{"urgency":"` + urgency + `"}`
		}
		var affected []string
		for _, p := range pkgs {
			affected = append(affected, `{"package":{"ecosystem":"`+eco+`","name":"`+p+`"},"ranges":[{"type":"ECOSYSTEM","events":`+
				events+`}]`+extra+`}`)
		}
		return `{"id":"` + id + `","modified":"2026-11-01T00:00:00Z","upstream":["` + cve + `"],"affected":[` +
			strings.Join(affected, ",") + `]}`
	}
	dir := t.TempDir()
	clk := &clock{t: time.Date(2026, 11, 6, 3, 0, 0, 0, time.UTC)}
	o := newOSVServer(t)
	o.publish("Debian", dumpOf(t, []string{
		entry("DSA-8000-1", "Debian:13", "CVE-2026-10", "6.12.200-1", "", "linux"),
		entry("DEBIAN-CVE-2026-11", "Debian:13", "CVE-2026-11", "", "", "linux"),
		entry("DEBIAN-CVE-2026-12", "Debian:13", "CVE-2026-12", "", "unimportant", "linux"),
		entry("DSA-8001-1", "Debian:13", "CVE-2026-12", "6.12.200-1", "", "linux"), // unimportant wins, also here
		entry("DEBIAN-CVE-2026-13", "Debian:13", "CVE-2026-13", "", "", "linux", "libx"),
		entry("DEBIAN-CVE-2026-14", "Debian:13", "CVE-2026-14", "6.12.200-1", "", "linux-6.12"),
		entry("DEBIAN-CVE-2026-15", "Debian:13", "CVE-2026-15", "2.41-6", "", "util-linux"),
		entry("DEBIAN-CVE-2026-16", "Debian:13", "CVE-2026-16", "", "", "linux-base"),
	}))
	o.publish("Alpine", dumpOf(t, []string{
		entry("ALPINE-CVE-2026-20", "Alpine:v3.23", "CVE-2026-20", "6.12.2-r0", "", "linux-lts"),
		entry("ALPINE-CVE-2026-21", "Alpine:v3.23", "CVE-2026-21", "1.7.1-r0", "", "linux-pam"),
	}))
	p := newTestPackages(t, dir, o, []string{"Debian", "Alpine"}, clk)
	headers := inventory.OSPackage{Name: "linux-libc-dev", Version: "6.12.100-1", Source: "linux"}
	builder := &inventory.ImageOS{ID: "debian", Version: "13", Packages: []inventory.OSPackage{headers,
		{Name: "linux-headers-6.12", Version: "6.12.100-1", Source: "linux-6.12"},
		{Name: "libx", Version: "1.0-1"}, {Name: "util-linux", Version: "2.41-5"}, {Name: "linux-base", Version: "4.12"}}}
	only := &inventory.ImageOS{ID: "debian", Version: "13", Packages: []inventory.OSPackage{headers}}
	small := &inventory.ImageOS{ID: "alpine", Version: "3.23.4", Packages: []inventory.OSPackage{
		{Name: "linux-lts", Version: "6.12.1-r0", Source: "linux-lts"}, {Name: "linux-pam", Version: "1.7.0-r0"}}}
	_ = p.SetInventory("h", containerInventory(clk.now(),
		inventory.Package{Name: "builder", Version: "1", Digest: "sha256:a", OS: builder},
		inventory.Package{Name: "headers", Version: "1", Digest: "sha256:b", OS: only},
		inventory.Package{Name: "small", Version: "1", Digest: "sha256:c", OS: small}))
	p.refreshAll(context.Background(), func() {})

	v := p.Containers("h")
	if v.Fixable == nil || *v.Fixable != 2 || v.Unfixed == nil || *v.Unfixed != 1 {
		t.Fatalf("counts: fixable %v, unfixed %v", v.Fixable, v.Unfixed)
	}
	if b := findingOf(v, "builder"); b.Fixable != 1 || b.Unfixed != 2 || b.Kernel != 3 ||
		!slices.Equal(b.IDs, []string{"CVE-2026-15", "CVE-2026-13", "CVE-2026-16"}) {
		t.Errorf("builder: %+v", b)
	}
	if h := findingOf(v, "headers"); h.Image == "" || h.Fixable != 0 || h.Unfixed != 0 || h.Kernel != 3 || len(h.IDs) != 0 {
		t.Errorf("only the kernel's headers: %+v", h)
	}
	if s := findingOf(v, "small"); s.Fixable != 1 || s.Unfixed != 0 || s.Kernel != 1 ||
		!slices.Equal(s.IDs, []string{"CVE-2026-21"}) {
		t.Errorf("alpine: %+v", s)
	}
}

// Which source packages are the kernel (ADR-0122), as the OSV dumps name them.
func TestKernelSource(t *testing.T) {
	for _, c := range []struct {
		eco, name string
		want      bool
	}{
		{"Debian", "linux", true}, {"Debian", "linux-6.12", true}, {"Debian", "linux-2.6.24", true},
		{"Debian", "util-linux", false}, {"Debian", "linux-base", false}, {"Debian", "linux-atm", false},
		{"Debian", "linux-latest-4.9", false}, {"Debian", "linux-6", false}, {"Debian", "linux-6.x", false},
		{"Debian", "linuxptp", false},
		{"Alpine", "linux-lts", true}, {"Alpine", "linux-grsec", true}, {"Alpine", "linux-pam", false},
		{"Alpine", "util-linux", false}, {"Alpine", "linux", false},
		{"PyPI", "linux", false},
	} {
		if got := kernelSource(versions.Ecosystem(c.eco), c.name); got != c.want {
			t.Errorf("%s %s: %v, want %v", c.eco, c.name, got, c.want)
		}
	}
}
