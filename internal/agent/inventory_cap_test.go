package agent

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/inventory"
	"github.com/psewar/vordr/internal/policy"
	"github.com/psewar/vordr/internal/protocol"
)

// A pattern of agent.yaml that matches more folders than the hub keeps locations of one inventory (ADR-0195): the
// agent reports the first protocol.MaxInventoryLocations - one fewer with the images, which are a location of their
// own - and says so in its log. Fewer are reported as found.
func TestTheAgentReportsNoMoreLocationsThanTheHubKeeps(t *testing.T) {
	dir := t.TempDir()
	for i := range protocol.MaxInventoryLocations + 6 {
		p := filepath.Join(dir, "projects", fmt.Sprintf("app-%04d", i))
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "pyvenv.cfg"), []byte("home = /usr/bin\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	read := func(pattern string, containers bool) (int, string) {
		t.Helper()
		yaml := fmt.Sprintf("node: dev-box\npackages: {detect: false, outdated: false, containers: %v, "+
			"locations: [{id: p, path: '%s'}]}\n", containers, filepath.Join(dir, "projects", pattern))
		cfg, issues := policy.Parse([]byte(yaml))
		if len(issues) > 0 {
			t.Fatalf("agent.yaml: %v", issues)
		}
		var logged bytes.Buffer
		inv := Inventory(cfg, t.TempDir(), "test", log.New(&logged, "", 0), time.Now)(context.Background(),
			Wants{Containers: containers})
		for _, l := range inv.Locations {
			if l.Kind == inventory.KindContainer {
				continue
			}
			if !strings.HasPrefix(l.ID, "p-app-") {
				t.Fatalf("location %+v", l)
			}
		}
		return len(inv.Locations), logged.String()
	}
	if n, logged := read("*", false); n != protocol.MaxInventoryLocations ||
		!strings.Contains(logged, fmt.Sprintf("%d locations found, the first %d reported", protocol.MaxInventoryLocations+6,
			protocol.MaxInventoryLocations)) {
		t.Errorf("%d locations, log %q", n, logged)
	}
	if n, logged := read("app-00*", false); n != 100 || logged != "" {
		t.Errorf("%d locations, log %q", n, logged)
	}
	if runtime.GOOS == "windows" {
		return // the images are read on Linux only
	}
	old := containerSocket
	containerSocket = filepath.Join(t.TempDir(), "no-engine.sock")
	defer func() { containerSocket = old }()
	if n, logged := read("*", true); n != protocol.MaxInventoryLocations ||
		!strings.Contains(logged, fmt.Sprintf("the first %d reported", protocol.MaxInventoryLocations-1)) {
		t.Errorf("with the images: %d locations, log %q", n, logged)
	}
}
