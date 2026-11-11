//go:build !windows

package images

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/inventory"
)

// fakeEngine answers like the Docker Engine API on a unix socket. It refuses everything but GET: reading the
// inventory must never change a container.
type fakeEngine struct {
	mu      sync.Mutex
	methods []string
	fail    bool
}

func startEngine(t *testing.T, e http.Handler) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "engine") // socket paths are short on Linux: not under t.TempDir's long name
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "e.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: e, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { srv.Close() })
	return sock
}

var (
	ctrA, ctrB, ctrC, ctrD = strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64), strings.Repeat("d", 64)
	ctrE, ctrF             = strings.Repeat("e", 64), strings.Repeat("f", 64)
	imgPG                  = "sha256:" + strings.Repeat("1", 64)
	imgApp                 = "sha256:" + strings.Repeat("2", 64)
	imgWeb                 = "sha256:" + strings.Repeat("3", 64)
	digestPG               = "sha256:" + strings.Repeat("9", 64)
	digestWeb              = "sha256:" + strings.Repeat("8", 64)
)

func (e *fakeEngine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	e.methods = append(e.methods, r.Method)
	fail := e.fail
	e.mu.Unlock()
	if r.Method != http.MethodGet || fail {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	var v any
	switch r.URL.Path {
	case "/containers/json":
		// ctrE was removed after the list: its inspection answers 404, and it is skipped, not the whole list
		v = []map[string]string{{"Id": ctrA}, {"Id": ctrE}, {"Id": ctrB}, {"Id": ctrC}, {"Id": ctrD}, {"Id": ctrF}}
	case "/containers/" + ctrA + "/json":
		v = map[string]any{"Name": "/db", "Image": imgPG, "Config": map[string]string{"Image": "postgres:16"}}
	case "/containers/" + ctrB + "/json": // a second container of the same image
		v = map[string]any{"Name": "/db-replica", "Image": imgPG, "Config": map[string]string{"Image": "postgres:16"}}
	case "/containers/" + ctrC + "/json":
		v = map[string]any{"Name": "/app", "Image": imgApp, "Config": map[string]string{"Image": "app:dev"}}
	case "/containers/" + ctrD + "/json": // created from an image id: no reference to ask about
		v = map[string]any{"Name": "/adhoc", "Image": imgApp, "Config": map[string]string{"Image": imgApp}}
	case "/containers/" + ctrF + "/json":
		v = map[string]any{"Name": "/web", "Image": imgWeb, "Config": map[string]string{"Image": "web:local"}}
	case "/images/" + imgPG + "/json": // pulled; labels of its own do not make it local
		v = map[string]any{"RepoDigests": []string{"postgres@" + digestPG}, "Created": "2026-10-01T00:00:00Z",
			"Os": "linux", "Architecture": "arm64", "Variant": "v8",
			"Config": map[string]any{"Labels": map[string]string{"maintainer": "someone"}}}
	case "/images/" + imgApp + "/json": // built locally, classic image store: no repo digest
		v = map[string]any{"Created": "2026-10-02T00:00:00Z", "Os": "linux", "Architecture": "arm64"}
	case "/images/" + imgWeb + "/json": // built by Compose, containerd image store: a repo digest all the same
		v = map[string]any{"RepoDigests": []string{"web@" + digestWeb}, "Created": "2026-10-03T00:00:00Z",
			"Os": "linux", "Architecture": "arm64", "Config": map[string]any{"Labels": map[string]string{
				"com.docker.compose.project": "web", "com.docker.compose.service": "api"}}}
	default:
		http.NotFound(w, r)
		return
	}
	_ = json.NewEncoder(w).Encode(v)
}

func TestEngineRunning(t *testing.T) {
	e := &fakeEngine{}
	sock := startEngine(t, e)
	run, code := NewEngine(sock).Running(context.Background())
	if code != "" || len(run) != 4 {
		t.Fatalf("code %q, %+v", code, run)
	}
	pg := run[0]
	if pg.Reference != "postgres:16" || pg.ImageID != imgPG || strings.Join(pg.Containers, ",") != "db,db-replica" ||
		pg.Platform != (Platform{"linux", "arm64", "v8"}) || !pg.Created.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) ||
		pg.BuiltLocally {
		t.Errorf("postgres: %+v", pg)
	}
	if web := run[3]; web.Reference != "web:local" || !web.BuiltLocally {
		t.Errorf("a Compose build with a repo digest (containerd image store) counts as built locally: %+v", web)
	}
	loc := Location(run, code, sock)
	if loc.ID != LocationID || loc.Kind != inventory.KindContainer || loc.Ecosystem != "oci" || loc.Skipped != 1 ||
		len(loc.Packages) != 3 {
		t.Fatalf("location: %+v", loc)
	}
	db := pkg(t, loc, "postgres")
	if db.Name != "docker.io/library/postgres" || db.Version != "16" || db.Digest != digestPG || len(db.Containers) != 2 {
		t.Errorf("postgres package: %+v", db)
	}
	if app := pkg(t, loc, "app"); app.Name != "docker.io/library/app" || app.Digest != "" { // built locally: never compared
		t.Errorf("local image: %+v", app)
	}
	if web := pkg(t, loc, "web"); web.Digest != "" || web.Version != "local" { // ADR-0115
		t.Errorf("Compose build: %+v", web)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, m := range e.methods {
		if m != http.MethodGet {
			t.Errorf("the engine was sent %s", m)
		}
	}
}

func TestEngineStates(t *testing.T) {
	if _, code := NewEngine(filepath.Join(t.TempDir(), "none.sock")).Running(context.Background()); code != CodeNoEngine {
		t.Errorf("no socket: %q", code)
	}
	plain := filepath.Join(t.TempDir(), "plain")
	_ = os.WriteFile(plain, nil, 0o600)
	if _, code := NewEngine(plain).Running(context.Background()); code != CodeNoEngine {
		t.Errorf("a file, not a socket: %q", code)
	}
	sock := startEngine(t, &fakeEngine{fail: true})
	if run, code := NewEngine(sock).Running(context.Background()); code != CodeEngineUnreachable || run != nil {
		t.Errorf("engine errors: %q %+v", code, run)
	}
}
