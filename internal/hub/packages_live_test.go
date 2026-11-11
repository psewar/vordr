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
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/versions"
	"github.com/psewar/vordr/internal/wire"
)

// TestLiveMatching runs the hub's own path - download, index, match - on real data, when VORDR_OSV_DUMPS names a
// folder with PyPI-all.zip and npm-all.zip (served here as osv.dev serves them) and VORDR_INVENTORIES a folder with
// inventories as "vordr-agent packages" prints them (<node>.json). It writes each host view next to them
// (<node>.view.json) and logs counts, times and heap: the dry run of design 11.2.
func TestLiveMatching(t *testing.T) {
	dumps, invDir := os.Getenv("VORDR_OSV_DUMPS"), os.Getenv("VORDR_INVENTORIES")
	if dumps == "" || invDir == "" {
		t.Skip("VORDR_OSV_DUMPS or VORDR_INVENTORIES not set")
	}
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		eco := filepath.Base(filepath.Dir(r.URL.Path))
		http.ServeFile(w, r, filepath.Join(dumps, eco+"-all.zip"))
	}))
	defer ts.Close()
	cfg := &OSVConfig{URL: ts.URL}
	cfg.applyDefaults()
	state := t.TempDir()
	p, err := NewPackages(state, cfg, time.Now, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	p.client = ts.Client()
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	for _, e := range cfg.Ecosystems {
		start := time.Now()
		if !p.Refresh(context.Background(), versions.Ecosystem(e)) {
			t.Fatalf("%s: no data: %+v", e, p.HubView())
		}
		idx, _ := os.Stat(p.indexPath(versions.Ecosystem(e)))
		t.Logf("%s: download+index %s, index %.1f MB", e, time.Since(start).Round(time.Millisecond), float64(idx.Size())/1e6)
	}
	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	t.Logf("heap with both indexes: +%.1f MB; osv dir after the run: %s", float64(after.HeapAlloc-before.HeapAlloc)/1e6,
		dirSize(filepath.Join(state, "osv")))

	files, _ := filepath.Glob(filepath.Join(invDir, "*.json"))
	for _, f := range files {
		if strings.HasSuffix(f, ".view.json") || strings.HasSuffix(f, ".findings.json") {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var inv wire.Inventory
		if err := json.Unmarshal(raw, &inv); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		node := strings.TrimSuffix(filepath.Base(f), ".json")
		if err := p.SetInventory(node, inv); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		v := p.View(node)
		took := time.Since(start)
		out, _ := json.MarshalIndent(v, "", "  ")
		if err := os.WriteFile(filepath.Join(invDir, node+".view.json"), out, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %d locations, %d packages, message %d kB, match %s: %d malicious, %d vulnerable, %d undecided",
			node, len(v.Locations), v.Total, len(raw)/1024, took.Round(time.Millisecond), deref(v.Malicious),
			deref(v.Vulnerable), v.Undecided)
		// Every package with every advisory id, uncapped, for comparing with another implementation.
		all := map[string][]string{}
		for _, loc := range inv.Locations {
			for _, pkg := range loc.Packages {
				found, _ := p.dbs[loc.Ecosystem].Lookup(pkg.Name, pkg.Version)
				for _, f := range found {
					key := loc.ID + " " + pkg.Name + " " + pkg.Version
					all[key] = append(all[key], f.ID)
				}
			}
		}
		out, _ = json.MarshalIndent(all, "", " ")
		if err := os.WriteFile(filepath.Join(invDir, node+".findings.json"), out, 0o644); err != nil {
			t.Fatal(err)
		}
		for _, fv := range v.Findings {
			t.Logf("   %s %s %s %s %v fixed:%s via:%s", fv.Location, fv.Name, fv.Version, fv.Severity, fv.Advisories,
				fv.FixedIn, fv.Via)
		}
	}
}

func deref(p *int) int {
	if p == nil {
		return -1
	}
	return *p
}

// dirSize lists a folder's files and their total size (what stays on the hub's disk).
func dirSize(dir string) string {
	var sum int64
	var names []string
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if fi, err := e.Info(); err == nil {
			sum += fi.Size()
			names = append(names, e.Name())
		}
	}
	return fmt.Sprintf("%s = %.1f MB", strings.Join(names, ", "), float64(sum)/1e6)
}
