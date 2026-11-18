// An external test: the hub reads the inventory's types, so a test inside the package could not import the hub.
package inventory_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/hub"
	"github.com/psewar/vordr/internal/inventory"
	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/versions"
	"github.com/psewar/vordr/internal/wire"
)

// A refresh of dependencies (ADR-0104) offers the hub every installed dependency of the location; what it offers is a
// vet request the hub takes (ADR-0196), also where a package.json holds a version with a control character - that
// entry stays on the host.
func TestARefreshOffersOnlyWhatTheHubTakes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "project")
	files := map[string]any{
		"package.json":                    map[string]any{"dependencies": map[string]string{"debug": "^4.4.0"}},
		"package-lock.json":               map[string]any{"lockfileVersion": 3, "packages": map[string]any{}},
		"node_modules/debug/package.json": map[string]string{"name": "debug", "version": "4.4.3"},
		"node_modules/ms/package.json":    map[string]string{"name": "ms", "version": "2.1.3"},
		"node_modules/@scope/" + "long-name-of-a-dependency/package.json": map[string]string{
			"name": "@scope/long-name-of-a-dependency", "version": "1.0.0-beta.1+build.7"},
		"node_modules/evil/package.json": map[string]string{"name": "evil", "version": "1.0.0\x1b[2J"},
	}
	for name, content := range files {
		raw, _ := json.Marshal(content)
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	locs := inventory.Collect(inventory.Options{Configured: []inventory.Configured{{ID: "project", Path: dir}}})
	if len(locs) != 1 || locs[0].Skipped != 1 || !inventory.Refreshable(locs[0]) {
		t.Fatalf("location: %+v", locs)
	}
	var offered []protocol.VetPackage
	u := &inventory.Updater{Now: time.Now, Vet: func(_ context.Context, eco versions.Ecosystem,
		plan []protocol.VetPackage) (protocol.Vet, bool) {
		offered = plan
		if miss := hub.VetNotTaken(wire.Vet{T: wire.TVet, JobID: "0123456789abcdef01234567", Ecosystem: string(eco),
			Packages: plan}); miss != nil {
			t.Errorf("the hub refuses the refresh's request for %v", miss)
		}
		return protocol.Vet{Packages: plan}, true
	}}
	if res := u.RefreshDeps(context.Background(), locs[0], nil); res.Code != protocol.CodeOK {
		t.Fatalf("refresh: %s %s", res.Code, res.Detail)
	}
	if len(offered) != 2 {
		t.Fatalf("offered %v", offered)
	}
	for _, p := range offered {
		if p.Name == "evil" {
			t.Fatalf("an entry with a control character offered: %v", offered)
		}
	}
}
