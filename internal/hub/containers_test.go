package hub

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/inventory"
	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/versions"
	"github.com/psewar/vordr/internal/wire"
)

// withContainers adds a container location; compared: when its images were compared with the registries (nil: never).
func withContainers(inv wire.Inventory, at time.Time, compared *time.Time) wire.Inventory {
	built := at.Add(-10 * 24 * time.Hour)
	young := at.Add(-24 * time.Hour)
	inv.Locations = append(inv.Locations, inventory.Location{ID: "containers", Kind: inventory.KindContainer,
		Ecosystem: versions.OCI, Path: "/var/run/docker.sock", NotInRegistry: 1, OutdatedFailed: 1, ComparedAt: compared,
		Packages: []inventory.Package{
			{Name: "docker.io/stack/db", Version: "3", Digest: "sha256:a", Direct: true, Containers: []string{"db"},
				Rebuilt: "sha256:b", RebuiltAt: &built, RebuiltCandidate: true, Latest: "5", Candidate: "4", Major: true},
			{Name: "ghcr.io/org/app", Version: "1.4.2", Digest: "sha256:c", Direct: true, Latest: "1.4.3", LatestAt: &young},
			{Name: "ghcr.io/org/quiet", Version: "1.0", Digest: "sha256:d", Direct: true},
			{Name: "docker.io/library/app", Version: "dev", Direct: true}, // built locally
			{Name: "docker.io/stack/logs", Version: "2.1.0", Digest: "sha256:e", Direct: true, Containers: []string{"logs"},
				CompareFailed: inventory.CompareTagListTooLong}, // its comparison got no answer (ADR-0126)
		}})
	return inv
}

// The hub keeps the container location like any inventory (ADR-0113), but apart from the pip/npm packages: it is no
// OSV ecosystem, so it must neither make their counts unknown nor count among them.
func TestContainersView(t *testing.T) {
	o := newOSVServer(t)
	clk := &clock{t: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)}
	p := newTestPackages(t, t.TempDir(), o, []string{"PyPI", "npm"}, clk)
	ctx := context.Background()
	p.Refresh(ctx, versions.PyPI)
	p.Refresh(ctx, versions.Npm)
	at := clk.now()
	inv := testInventory(at)
	inv.OutdatedAt, inv.MinAgeDays = &at, 7
	if err := p.SetInventory("h", inv); err != nil {
		t.Fatal(err)
	}
	if p.Containers("h") != nil {
		t.Error("containers view without a container location")
	}
	before := *p.View("h")
	if err := p.SetInventory("h", withContainers(inv, at, &at)); err != nil {
		t.Fatal(err)
	}
	after := p.View("h")
	if after.Total != before.Total || len(after.Locations) != len(before.Locations) || after.Vulnerable == nil ||
		*after.Vulnerable != *before.Vulnerable || *after.Outdated != *before.Outdated {
		t.Errorf("the images changed the package view: %+v, before %+v", after, before)
	}
	cv := p.Containers("h")
	if cv == nil || cv.Images != 5 || cv.Local != 1 || cv.NotInRegistry != 1 || cv.Outdated == nil || *cv.Outdated != 2 ||
		*cv.Ready != 1 || cv.MinAgeDays != 7 || len(cv.Updates) != 2 || cv.ComparedAt == nil || !cv.ComparedAt.Equal(at) {
		t.Fatalf("containers view: %+v", cv)
	}
	// The failed one with why (ADR-0126), not among the outdated.
	if cf := cv.CompareFailed; cv.OutdatedFailed != 1 || len(cf) != 1 || cf[0].Image != "docker.io/stack/logs" ||
		cf[0].Tag != "2.1.0" || !slices.Equal(cf[0].Containers, []string{"logs"}) || cf[0].Code != inventory.CompareTagListTooLong {
		t.Errorf("compare failed: %d %+v", cv.OutdatedFailed, cv.CompareFailed)
	}
	db, app := cv.Updates[0], cv.Updates[1] // the one with a candidate first
	if db.Image != "docker.io/stack/db" || !db.Rebuilt || !db.RebuiltReady || db.Candidate != "4" || !db.Major ||
		!slices.Equal(db.Containers, []string{"db"}) {
		t.Errorf("db: %+v", db)
	}
	if app.Latest != "1.4.3" || app.Candidate != "" || app.LatestAt == nil || app.Rebuilt {
		t.Errorf("app: %+v", app)
	}
	// Images never compared (the engine did not answer at the first run, or the hub kept no images then): the counts
	// are unknown, not zero - even though the pip/npm packages were asked about.
	_ = p.SetInventory("h", withContainers(inv, at, nil))
	if cv := p.Containers("h"); cv.Outdated != nil || cv.Ready != nil || cv.Updates != nil {
		t.Errorf("not compared: %+v", cv)
	}
}

// ADR-0131: the smaller step within the running major version is passed on, and alone makes an image ready - a newer
// major version that is too new with an old enough tag of the running one is something to install. Without it, a too
// new major version is outdated but not ready.
func TestSameMajorInContainersView(t *testing.T) {
	clk := &clock{t: time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)}
	p := newTestPackages(t, t.TempDir(), nil, nil, clk)
	at := clk.now()
	young := at.Add(-24 * time.Hour)
	inv := wire.Inventory{T: wire.TInventory, CheckedAt: at, OutdatedAt: &at, MinAgeDays: 7, Locations: []inventory.Location{
		{ID: "containers", Kind: inventory.KindContainer, Ecosystem: versions.OCI, Path: "/run/docker.sock", ComparedAt: &at,
			Packages: []inventory.Package{
				{Name: "docker.io/stack/web", Version: "2.2.0", Digest: "sha256:a", Direct: true,
					Latest: "3.1.0", Candidate: "3.1.0", Major: true, SameMajor: "2.4.0"},
				{Name: "docker.io/stack/db", Version: "5.5", Digest: "sha256:b", Direct: true,
					Latest: "6.2", LatestAt: &young, Major: true, SameMajor: "5.10"},
				{Name: "docker.io/library/cache", Version: "7", Digest: "sha256:c", Direct: true,
					Latest: "8", LatestAt: &young, Major: true},
			}}}}
	if err := p.SetInventory("h", inv); err != nil {
		t.Fatal(err)
	}
	cv := p.Containers("h")
	if cv.Outdated == nil || *cv.Outdated != 3 || *cv.Ready != 2 {
		t.Fatalf("outdated %v ready %v, want 3 and 2", cv.Outdated, cv.Ready)
	}
	got := map[string]wire.ImageUpdateView{}
	for _, u := range cv.Updates {
		got[u.Tag] = u
	}
	if got["2.2.0"].SameMajor != "2.4.0" || got["2.2.0"].Candidate != "3.1.0" || got["5.5"].SameMajor != "5.10" ||
		got["5.5"].Candidate != "" || got["7"].SameMajor != "" {
		t.Errorf("same major: %+v", cv.Updates)
	}
	if cv.Updates[2].Tag != "7" {
		t.Errorf("the one without anything old enough comes last: %+v", cv.Updates)
	}
}

// Agents learn from the status that this hub keeps images; the host view carries them, and outdated images are no
// problem of the host (only reported).
func TestContainersInHostView(t *testing.T) {
	f := newViewFixture(t)
	clk := &clock{t: f.now}
	f.srv.Packages = newTestPackages(t, t.TempDir(), nil, nil, clk)
	if !slices.Contains(f.srv.features(), wire.FeatureContainers) {
		t.Errorf("features: %v", f.srv.features())
	}
	f.host(t, "echo", []protocol.Verb{protocol.VerbCheck}, wire.PolicySummary{}, true)
	_ = f.srv.Reg.SetReport("echo", aptReport(f.now, 0, 0, nil))
	at := f.now
	inv := wire.Inventory{T: wire.TInventory, CheckedAt: at, OutdatedAt: &at, MinAgeDays: 7,
		Locations: []inventory.Location{}}
	_ = f.srv.Packages.SetInventory("echo", withContainers(inv, at, &at))
	v := f.view(t, "echo", f.now)
	if v.Containers == nil || v.Containers.Images != 5 || v.Problem != nil { // a failed comparison is no problem either
		t.Fatalf("view: containers %+v, problem %+v", v.Containers, v.Problem)
	}
	if v.Packages == nil || v.Packages.Total != 0 || len(v.Packages.Locations) != 0 {
		t.Errorf("packages view counts the images: %+v", v.Packages)
	}
}
