package agent

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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/hub"
	"github.com/psewar/vordr/internal/inventory"
	"github.com/psewar/vordr/internal/policy"
	"github.com/psewar/vordr/internal/versions"
	"github.com/psewar/vordr/internal/wire"
)

type countingInventory struct {
	mu   sync.Mutex
	n    int
	last Wants // what the last call was told the hub takes
}

func (c *countingInventory) read(_ context.Context, w Wants) wire.Inventory {
	c.mu.Lock()
	c.n++
	c.last = w
	c.mu.Unlock()
	return wire.Inventory{T: wire.TInventory, CheckedAt: time.Now().UTC(), Locations: []inventory.Location{{
		ID: "node-red", Kind: inventory.KindNpmDir, Ecosystem: versions.Npm, Path: "/srv/app/.node-red",
		Packages: []inventory.Package{{Name: "lib-a", Version: "1.2.0"}, {Name: "node-red-contrib-x", Version: "1.0.3", Direct: true}},
	}}}
}

func (c *countingInventory) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

// A hub that announces inventories gets one after every check; it keeps it and shows it in the host view.
func TestInventoryToAHubThatAcceptsIt(t *testing.T) {
	h := startHub(t, 5)
	pk, err := hub.NewPackages(t.TempDir(), nil, time.Now, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	h.srv.Packages = pk
	inv := &countingInventory{}
	c := h.client(t, "echo", &fakeChecker{})
	c.Inventory = inv.read
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	waitFor(t, "pending", func() bool { return state(h.reg, "echo") == wire.StatePendingApproval })
	if inv.calls() != 0 {
		t.Fatal("inventory before approval")
	}
	h.srv.Admin(hub.AdminRequest{Cmd: "approve", Node: "echo", Fingerprint: c.ID.Fingerprint()})
	waitFor(t, "inventory at the hub", func() bool { return pk.View("echo") != nil })
	e, _ := h.reg.Get("echo")
	v := h.srv.HostView(e, time.Now()).Packages
	// No osv section: the hub keeps the inventory, but has nothing to match it against - unknown, not zero.
	if v == nil || v.Total != 2 || v.Vulnerable != nil || len(v.Locations) != 1 || v.Locations[0].ID != "node-red" {
		t.Fatalf("view: %+v", v)
	}
	inv.mu.Lock()
	defer inv.mu.Unlock()
	if !inv.last.Containers {
		t.Error("a hub with packages announces containers (ADR-0113), the inventory was not told")
	}
	if inv.last.ImageContents {
		t.Error("a hub without OSV data does not match image contents (ADR-0120), the inventory was told it does")
	}
}

// An older hub (no features in its status) drops the connection on a message type it does not know. The agent must
// not send one: the inventory is never even read.
func TestNoInventoryToAnOlderHub(t *testing.T) {
	h := startHub(t, 5)
	inv := &countingInventory{}
	checker := &fakeChecker{}
	c := h.client(t, "golf", checker)
	c.Inventory = inv.read
	c.SetCheckMinInterval(0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	waitFor(t, "pending", func() bool { return state(h.reg, "golf") == wire.StatePendingApproval })
	h.srv.Admin(hub.AdminRequest{Cmd: "approve", Node: "golf", Fingerprint: c.ID.Fingerprint()})
	waitFor(t, "first report", func() bool { e, _ := h.reg.Get("golf"); return e.LastReport != nil })
	jctx, jcancel := context.WithTimeout(ctx, 5*time.Second)
	defer jcancel()
	if _, code := h.srv.SendJob(jctx, "golf", "check", ""); code != hub.AdminOK {
		t.Fatalf("second check: %s", code)
	}
	if inv.calls() != 0 || checker.calls() != 2 {
		t.Fatalf("inventory read %d times, checks %d", inv.calls(), checker.calls())
	}
	if online, _ := h.srv.Online("golf"); !online {
		t.Fatal("connection dropped")
	}
}

// The registries are asked at most every outdated_hours; in between the answers are reused, and a package that
// changed version in between waits for the next run instead of keeping a stale answer.
func TestOutdatedCache(t *testing.T) {
	asked := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked++
		_, _ = w.Write([]byte(`{"releases":{"1.0":[{"upload_time_iso_8601":"2026-01-01T00:00:00Z"}],` +
			`"1.1":[{"upload_time_iso_8601":"2026-09-01T00:00:00Z"}]}}`))
	}))
	defer ts.Close()
	reg := inventory.Registries{PyPI: ts.URL, Client: ts.Client()}
	locs := func(v string) []inventory.Location {
		return []inventory.Location{{ID: "v", Ecosystem: "PyPI", Packages: []inventory.Package{{Name: "x", Version: v, Direct: true}}}}
	}
	o := &outdatedCache{}
	t0 := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	week, day := 7*24*time.Hour, 24*time.Hour
	l := locs("1.0")
	at := o.apply(context.Background(), reg, l, t0, week, 0, day)
	if asked != 1 || l[0].Packages[0].Candidate != "1.1" || !at.Equal(t0) {
		t.Fatalf("first run: asked %d, %+v", asked, l[0].Packages[0])
	}
	l = locs("1.0")
	o.apply(context.Background(), reg, l, t0.Add(6*time.Hour), week, 0, day)
	if asked != 1 || l[0].Packages[0].Candidate != "1.1" {
		t.Errorf("from the cache: asked %d, %+v", asked, l[0].Packages[0])
	}
	l = locs("1.1") // updated in between: no stale "1.1 is newer"
	o.apply(context.Background(), reg, l, t0.Add(7*time.Hour), week, 0, day)
	if asked != 1 || l[0].Packages[0].Latest != "" {
		t.Errorf("changed version: %+v", l[0].Packages[0])
	}
	o.apply(context.Background(), reg, locs("1.0"), t0.Add(25*time.Hour), week, 0, day)
	if asked != 2 {
		t.Errorf("after a day: asked %d", asked)
	}
}

func TestFitShortensTheLargestLocation(t *testing.T) {
	inv := wire.Inventory{T: wire.TInventory, Locations: []inventory.Location{
		{ID: "small", Packages: make([]inventory.Package, 10)},
		{ID: "big", Packages: make([]inventory.Package, 4000)},
	}}
	for i := range inv.Locations[1].Packages {
		inv.Locations[1].Packages[i] = inventory.Package{Name: strings.Repeat("p", 40), Version: "1.0.0"}
	}
	fit(&inv, 64<<10)
	raw, _ := json.Marshal(inv)
	if len(raw) > 64<<10 || inv.Locations[0].Code != "" || len(inv.Locations[0].Packages) != 10 ||
		inv.Locations[1].Code != inventory.CodeTooManyPackages || len(inv.Locations[1].Packages) == 0 {
		t.Errorf("%d bytes, small %d (%q), big %d (%q)", len(raw), len(inv.Locations[0].Packages),
			inv.Locations[0].Code, len(inv.Locations[1].Packages), inv.Locations[1].Code)
	}
}

// By size, not by count: images carrying thousands of packages inside outweigh a pip location with more entries. The
// packages inside the largest image go first; every image stays, and so does the pip location; what the cache holds
// is not touched.
func TestFitDropsImageContentsFirst(t *testing.T) {
	inside := func(n int) *inventory.ImageOS {
		os := &inventory.ImageOS{ID: "debian", Version: "13"}
		for i := range n {
			os.Packages = append(os.Packages, inventory.OSPackage{Name: fmt.Sprintf("lib%04d", i), Version: "1.2.3-4+deb13u1"})
		}
		return os
	}
	big, small := inside(3000), inside(50)
	inv := wire.Inventory{T: wire.TInventory, Locations: []inventory.Location{
		{ID: "pip", Packages: make([]inventory.Package, 300)},
		{ID: "containers", Kind: inventory.KindContainer, Packages: []inventory.Package{
			{Name: "a", OS: small}, {Name: "b", OS: big}, {Name: "c", OS: inside(2000)}}},
	}}
	for i := range inv.Locations[0].Packages {
		inv.Locations[0].Packages[i] = inventory.Package{Name: fmt.Sprintf("package-%03d", i), Version: "1.0.0"}
	}
	fit(&inv, 160<<10)
	raw, _ := json.Marshal(inv)
	imgs := inv.Locations[1].Packages
	if len(raw) > 160<<10 || inv.Locations[0].Code != "" || len(inv.Locations[0].Packages) != 300 ||
		inv.Locations[1].Code != "" || len(imgs) != 3 {
		t.Fatalf("%d bytes, pip %d (%q), images %d (%q)", len(raw), len(inv.Locations[0].Packages),
			inv.Locations[0].Code, len(imgs), inv.Locations[1].Code)
	}
	if imgs[1].OS.Code != inventory.CodeTooManyPackages || imgs[1].OS.Packages != nil || imgs[1].OS.ID != "debian" ||
		len(imgs[0].OS.Packages) != 50 {
		t.Errorf("images: a %d, b %+v", len(imgs[0].OS.Packages), imgs[1].OS.Code)
	}
	if len(big.Packages) != 3000 || big.Code != "" {
		t.Error("the cache's contents were changed")
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func parsePolicy(t *testing.T, yaml string) policy.Config {
	t.Helper()
	cfg, issues := policy.Parse([]byte("node: test\n" + yaml + "\n"))
	if len(issues) > 0 {
		t.Fatalf("%s: %v", yaml, issues)
	}
	return cfg
}

// The policy's packages section drives what is read: off means an empty list, configured locations are read.
func TestInventoryFromPolicy(t *testing.T) {
	dir := t.TempDir()
	venv := filepath.Join(dir, "svc", "venv")
	writeFile(t, filepath.Join(venv, "pyvenv.cfg"), "")
	writeFile(t, filepath.Join(venv, "lib", "python3.11", "site-packages", "requests-2.31.0.dist-info", "METADATA"),
		"Name: requests\nVersion: 2.31.0\n")
	for _, c := range []struct {
		yaml string
		want int
	}{
		{"packages: {detect: false, outdated: false, locations: [{id: svc, path: '" + filepath.Join(dir, "*", "venv") + "'}]}", 1},
		{"packages: {inventory: false, locations: [{id: svc, path: '" + venv + "'}]}", 0},
	} {
		cfg := parsePolicy(t, c.yaml)
		inv := Inventory(cfg, t.TempDir(), "test", nil, time.Now)(context.Background(), Wants{Containers: true})
		if len(inv.Locations) != c.want {
			t.Errorf("%s: %+v", c.yaml, inv.Locations)
		}
		if c.want == 1 && (inv.Locations[0].ID != "svc-svc" || len(inv.Locations[0].Packages) != 1) {
			t.Errorf("location: %+v", inv.Locations[0])
		}
	}
}
