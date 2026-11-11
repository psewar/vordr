//go:build !windows

package agent

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/images"
	"github.com/psewar/vordr/internal/inventory"
)

// fakeEngineSocket answers the three Engine API reads for one running container.
func fakeEngineSocket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "eng")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "e.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	id, img := strings.Repeat("a", 64), "sha256:"+strings.Repeat("1", 64)
	compose := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(compose, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/containers/json":
			_ = json.NewEncoder(w).Encode([]map[string]string{{"Id": id}})
		case "/containers/" + id + "/json":
			_ = json.NewEncoder(w).Encode(map[string]any{"Name": "/db", "Image": img,
				"State": map[string]string{"Status": "running"},
				"Config": map[string]any{"Image": "postgres:16", "Labels": map[string]string{
					"com.docker.compose.project": "stack", "com.docker.compose.service": "db",
					"com.docker.compose.project.working_dir": dir, "com.docker.compose.project.config_files": compose,
					"com.docker.compose.config-hash": "h"}}})
		case "/images/" + img + "/json":
			_ = json.NewEncoder(w).Encode(map[string]any{"RepoDigests": []string{"postgres@sha256:" + strings.Repeat("9", 64)},
				"Created": "2026-10-01T00:00:00Z", "Os": "linux", "Architecture": "amd64"})
		default:
			http.NotFound(w, r)
		}
	})}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { srv.Close() })
	return sock
}

// With packages.containers the images come along as the location "containers" - but only for a hub that keeps them
// (ADR-0113): to any other hub the inventory is what it was before.
func TestInventoryWithContainers(t *testing.T) {
	defer func(was string) { containerSocket = was }(containerSocket)
	containerSocket = fakeEngineSocket(t)
	cfg := parsePolicy(t, "packages: {detect: false, outdated: false, containers: true}")
	read := Inventory(cfg, t.TempDir(), "test", nil, time.Now)
	inv := read(context.Background(), Wants{Containers: true})
	if len(inv.Locations) != 1 || inv.Locations[0].ID != images.LocationID ||
		inv.Locations[0].Kind != inventory.KindContainer || len(inv.Locations[0].Packages) != 1 ||
		inv.Locations[0].Packages[0].Name != "docker.io/library/postgres" {
		t.Fatalf("with a hub that keeps images: %+v", inv.Locations)
	}
	if u := inv.Locations[0].Packages[0].Updatable; len(u) != 0 {
		t.Errorf("no image entry in allowed_scopes, yet updatable: %v", u)
	}
	// ADR-0119: a service install.allowed_scopes names is reported as updatable with its image.
	allowed := cfg
	allowed.Install.AllowedScopes = []string{"image:stack/*"}
	inv = Inventory(allowed, t.TempDir(), "test", nil, time.Now)(context.Background(), Wants{Containers: true})
	if u := inv.Locations[0].Packages[0].Updatable; !slices.Equal(u, []string{"stack/db"}) {
		t.Errorf("updatable %v, want [stack/db]", u)
	}
	if inv := read(context.Background(), Wants{}); len(inv.Locations) != 0 {
		t.Errorf("an older hub got images: %+v", inv.Locations)
	}
	off := parsePolicy(t, "packages: {detect: false, outdated: false}")
	if inv := Inventory(off, t.TempDir(), "test", nil, time.Now)(context.Background(), Wants{Containers: true}); len(inv.Locations) != 0 {
		t.Errorf("containers off by default, got %+v", inv.Locations)
	}
}

// stubImages stands in for the registries: it counts comparisons and marks every image as rebuilt, but one whose
// name says "huge", whose tag list is too long (ADR-0126). Every rebuilt one also gets a new major version with a
// smaller step beside it (ADR-0131).
type stubImages struct{ runs int }

func (s *stubImages) Outdated(_ context.Context, _ []images.Running, loc *inventory.Location, _ time.Time,
	_ time.Duration, _ int) {
	s.runs++
	loc.OutdatedFailed = 0
	for i := range loc.Packages {
		if strings.Contains(loc.Packages[i].Name, "huge") {
			loc.Packages[i].CompareFailed = inventory.CompareTagListTooLong
			loc.OutdatedFailed++
			continue
		}
		loc.Packages[i].Rebuilt = "sha256:new"
		loc.Packages[i].Latest, loc.Packages[i].Candidate, loc.Packages[i].Major = "18", "18", true
		loc.Packages[i].SameMajor = "16.1"
	}
}

func (s *stubImages) Save() error { return nil }

// The images are compared on their own schedule: a check whose engine did not answer compares nothing and says so
// (no compared_at), and the next check with images compares at once instead of waiting outdated_hours.
func TestImagesComparedOnTheirOwnSchedule(t *testing.T) {
	stub := &stubImages{}
	c := &imagesCache{reg: stub}
	t0 := time.Date(2026, 11, 5, 8, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	img := []inventory.Package{{Name: "docker.io/library/postgres", Version: "16", Digest: "sha256:a", Direct: true},
		{Name: "docker.io/org/huge", Version: "1.4.2", Digest: "sha256:b", Direct: true}}
	down := inventory.Location{Kind: inventory.KindContainer, Code: images.CodeEngineUnreachable, Packages: []inventory.Package{}}
	c.apply(context.Background(), &down, nil, t0, 7*day, day, true, nil)
	if stub.runs != 0 || down.ComparedAt != nil {
		t.Fatalf("engine down: compared %d times, compared_at %v", stub.runs, down.ComparedAt)
	}
	up := inventory.Location{Kind: inventory.KindContainer, Packages: slices.Clone(img)}
	c.apply(context.Background(), &up, nil, t0.Add(time.Hour), 7*day, day, true, nil)
	if stub.runs != 1 || up.ComparedAt == nil || up.Packages[0].Rebuilt == "" {
		t.Fatalf("engine back: compared %d times, %+v", stub.runs, up)
	}
	again := inventory.Location{Kind: inventory.KindContainer, Packages: slices.Clone(img)}
	c.apply(context.Background(), &again, nil, t0.Add(2*time.Hour), 7*day, day, true, nil)
	if stub.runs != 1 || again.Packages[0].Rebuilt == "" || !again.ComparedAt.Equal(*up.ComparedAt) {
		t.Errorf("within the day: compared %d times, %+v", stub.runs, again)
	}
	// The smaller step comes from the cache like the candidate (ADR-0131).
	if p := again.Packages[0]; p.Candidate != "18" || p.SameMajor != "16.1" {
		t.Errorf("same major, from the cache: %+v", p)
	}
	// From the cache too, a failed one says why (ADR-0126).
	if again.Packages[1].CompareFailed != inventory.CompareTagListTooLong || again.OutdatedFailed != 1 ||
		again.Packages[0].CompareFailed != "" {
		t.Errorf("why, from the cache: %+v", again)
	}
}
