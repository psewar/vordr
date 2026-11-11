package agent

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
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

	"github.com/psewar/vordr/internal/facts"
	"github.com/psewar/vordr/internal/hub"
	"github.com/psewar/vordr/internal/inventory"
	"github.com/psewar/vordr/internal/policy"
	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/runner"
	"github.com/psewar/vordr/internal/versions"
	"github.com/psewar/vordr/internal/wire"
)

// osvServer serves internal/osv/testdata/<eco> as <eco>/all.zip (plain HTTP: the test hands NewPackages the URL). The
// path is relative to the package: a test binary started elsewhere would serve empty dumps and prove nothing, so it
// stops (30.10.2026: "flagged plan: ok" from a binary run outside internal/agent).
func osvServer(t *testing.T) *httptest.Server {
	t.Helper()
	if files, _ := filepath.Glob(filepath.Join("..", "osv", "testdata", "*", "*.json")); len(files) == 0 {
		t.Fatal("no OSV testdata in ../osv/testdata - run the test from internal/agent")
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		eco := filepath.Base(filepath.Dir(r.URL.Path))
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		files, _ := filepath.Glob(filepath.Join("..", "osv", "testdata", eco, "*.json"))
		for _, f := range files {
			raw, _ := os.ReadFile(f)
			fw, _ := zw.Create(filepath.Base(f))
			_, _ = fw.Write(raw)
		}
		_ = zw.Close()
		_, _ = w.Write(buf.Bytes())
	}))
	t.Cleanup(ts.Close)
	return ts
}

// fakeNpm resolves the plan given in plan and "installs" it by writing package.json files.
type fakeNpm struct {
	mu   sync.Mutex
	nm   string
	plan map[string]string
}

func (f *fakeNpm) Run(_ context.Context, c inventory.Command) runner.Result {
	f.mu.Lock()
	defer f.mu.Unlock()
	args := strings.Join(c.Args, " ")
	switch {
	case strings.Contains(args, "--package-lock-only"):
		pk := map[string]any{}
		for n, v := range f.plan {
			pk["node_modules/"+n] = map[string]string{"version": v}
		}
		raw, _ := json.Marshal(map[string]any{"packages": pk})
		_ = os.WriteFile(filepath.Join(c.Dir, "package-lock.json"), raw, 0o644)
	case strings.HasPrefix(args, "install --global"):
		for n, v := range f.plan {
			_ = os.MkdirAll(filepath.Join(f.nm, n), 0o755)
			_ = os.WriteFile(filepath.Join(f.nm, n, "package.json"), []byte(`{"name":"`+n+`","version":"`+v+`"}`), 0o644)
		}
	}
	return runner.Result{}
}

// The agent's own gates, before any package manager runs: a policy that allows updates there, a hub that vets.
func TestPackageRunGates(t *testing.T) {
	c := NewClient(policy.Config{Node: "h", StateDir: t.TempDir()}, nil, "test", facts.Host{}, &fakeChecker{}, quiet)
	job := protocol.Job{ID: "j1", Verb: protocol.VerbInstall, Scope: "pkg:npm-global"}
	sc, _ := protocol.ParseScope(job.Scope)
	if code, _, _, _ := c.packageRun(job, sc); code != protocol.CodeRejectedNotAllowed {
		t.Errorf("no packages.update: %s", code)
	}
	nm := filepath.Join(t.TempDir(), "node_modules")
	writeFile(t, filepath.Join(nm, "x", "package.json"), `{"name":"x","version":"1.0.0"}`)
	c.Packages = &PackageUpdates{Cfg: policy.Packages{Update: []string{"node-*"}, MinAgeDays: 7},
		Collect: func() []inventory.Location {
			return inventory.Collect(inventory.Options{Configured: []inventory.Configured{{ID: "npm-global", Path: nm}}})
		}}
	if code, _, _, _ := c.packageRun(job, sc); code != protocol.CodeRejectedScopeNotAllowed {
		t.Errorf("location not in packages.update: %s", code)
	}
	c.Packages.Cfg.Update = []string{"npm-*"}
	if code, _, _, _ := c.packageRun(job, sc); code != protocol.CodePrecheckVettingUnavailable {
		t.Errorf("hub without vet: %s", code)
	}
	closed := closedWindow()
	c.Cfg.Install.Window = &closed
	if code, _, _, _ := c.packageRun(job, sc); code != protocol.CodeRejectedOutsideWindow {
		t.Errorf("window closed: %s", code)
	}
	asked := job
	asked.OutsideWindow = "ops: fits now" // through the window (ADR-0087), on to the next gate
	if code, _, _, _ := c.packageRun(asked, sc); code != protocol.CodePrecheckVettingUnavailable {
		t.Errorf("window closed, with a reason: %s", code)
	}
	c.Cfg.Install.Window = nil
	c.setFeatures([]string{wire.FeatureInventory, wire.FeatureVet})
	other := protocol.Scope{Kind: protocol.ScopePkg, Location: "npm-other"}
	if code, _, _, _ := c.packageRun(job, other); code != protocol.CodeRejectedScopeNotAllowed {
		t.Errorf("unknown location: %s", code)
	}
	if verbs := c.Verbs(); len(verbs) != 2 || verbs[1] != protocol.VerbInstall {
		t.Errorf("verbs with package updates only: %v", verbs)
	}
	// deps (ADR-0104) goes through the same gates and needs a location it can refresh: global npm is none (step 1).
	deps := protocol.Job{ID: "j2", Verb: protocol.VerbInstall, Scope: "deps:npm-global"}
	dsc, _ := protocol.ParseScope(deps.Scope)
	if code, detail, _, _ := c.packageRun(deps, dsc); code != protocol.CodeRejectedUnsupported ||
		!strings.Contains(detail, "lock file") {
		t.Errorf("deps for global npm: %s %q", code, detail)
	}
	// An install job with that scope goes there too, not to the system's installer.
	if code, detail, _, _ := c.installRun(deps); code != protocol.CodeRejectedUnsupported || !strings.Contains(detail, "lock file") {
		t.Errorf("install job with a deps scope: %s %q", code, detail)
	}
	c.Packages.Cfg.Update = []string{"node-*"}
	if code, _, _, _ := c.packageRun(deps, dsc); code != protocol.CodeRejectedScopeNotAllowed {
		t.Errorf("deps for a location not in packages.update: %s", code)
	}
}

func TestPackageUpdateThroughTheHub(t *testing.T) {
	inventory.NpmCommand = func() (string, []string, bool) { return "npm", nil, true }
	defer func() { inventory.NpmCommand = inventory.DefaultNpmCommand }()
	h := startHub(t, 5)
	osv := osvServer(t)
	pk, err := hub.NewPackages(t.TempDir(), &hub.OSVConfig{URL: osv.URL, Ecosystems: []string{"npm"}, RefreshHours: 24,
		StaleHours: 72}, time.Now, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	if !pk.Refresh(context.Background(), versions.Npm) {
		t.Fatal("no OSV data")
	}
	h.srv.Packages = pk

	// npm registry: @example/nodes 2.0.1 is 30 days old - the candidate.
	reg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		old := time.Now().Add(-30 * 24 * time.Hour).Format(time.RFC3339)
		_ = json.NewEncoder(w).Encode(map[string]any{"dist-tags": map[string]string{"latest": "2.0.1"},
			"time":     map[string]string{"1.4.0": old, "2.0.1": old},
			"versions": map[string]any{"1.4.0": map[string]any{}, "2.0.1": map[string]any{}}})
	}))
	defer reg.Close()
	nm := filepath.Join(t.TempDir(), "lib", "node_modules")
	writeFile(t, filepath.Join(nm, "@example", "nodes", "package.json"), `{"name":"@example/nodes","version":"1.4.0"}`)
	npm := &fakeNpm{nm: nm, plan: map[string]string{"@example/nodes": "2.0.1", "debug": "4.4.2"}}
	cfg := policy.Packages{Update: []string{"npm-global"}, MinAgeDays: 7}
	c := h.client(t, "echo", &fakeChecker{})
	c.Packages = &PackageUpdates{Cfg: cfg, Exec: npm, Reg: inventory.Registries{Npm: reg.URL, Client: reg.Client()},
		Collect: func() []inventory.Location {
			l := inventory.Collect(inventory.Options{Configured: []inventory.Configured{{ID: "npm-global", Path: nm}}})
			return l
		}}
	c.Inventory = func(context.Context, Wants) wire.Inventory {
		locs := c.Packages.Collect()
		locs[0].Update = true
		locs[0].Packages[0].Latest, locs[0].Packages[0].Candidate = "2.0.1", "2.0.1"
		return wire.Inventory{T: wire.TInventory, CheckedAt: time.Now().UTC(), Locations: locs}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	waitFor(t, "pending", func() bool { return state(h.reg, "echo") == wire.StatePendingApproval })
	h.srv.Admin(hub.AdminRequest{Cmd: "approve", Node: "echo", Fingerprint: c.ID.Fingerprint()})
	waitFor(t, "inventory", func() bool { return pk.View("echo") != nil })

	// Home Assistant's command resolves to a pkg scope of a location the host reported as updatable.
	verb, scope, code := h.srv.Resolve("echo", "install_pkg:npm-global/@example/nodes")
	if code != hub.AdminOK || verb != protocol.VerbInstall || scope != "pkg:npm-global/@example/nodes" {
		t.Fatalf("resolve: %s %s %s", verb, scope, code)
	}
	for cmd, want := range map[string]hub.AdminCode{"install_pkg:node-red": hub.AdminNotAllowed,
		"install_pkg:npm-global/chalk": hub.AdminNotAllowed, "install_pkg:npm-global/--registry=x": hub.AdminBadRequest} {
		if _, _, code := h.srv.Resolve("echo", cmd); code != want {
			t.Errorf("%s: %s, want %s", cmd, code, want)
		}
	}
	e, _ := h.reg.Get("echo")
	if caps := h.srv.HostView(e, time.Now()).Capabilities.PackageUpdates; len(caps) != 1 || caps[0] != "npm-global" {
		t.Errorf("capabilities: %v", caps)
	}

	// The plan holds debug 4.4.2 (malware, 8 September 2025): the hub's signed answer flags it, nothing is installed.
	jctx, jcancel := context.WithTimeout(ctx, 20*time.Second)
	defer jcancel()
	st, code := h.srv.SendJob(jctx, "echo", verb, scope)
	if code != hub.AdminOK || st.Code != protocol.CodePrecheckVersionFlagged || !strings.Contains(st.Detail, "MAL-2025-46974") {
		t.Fatalf("flagged plan: %s %+v", code, st)
	}
	if v := c.Packages.Collect()[0].Packages[0].Version; v != "1.4.0" {
		t.Fatalf("installed although flagged: %s", v)
	}

	// The same with a clean plan goes through, and the new version is there.
	npm.mu.Lock()
	npm.plan["debug"] = "4.4.3"
	npm.mu.Unlock()
	st, code = h.srv.SendJob(jctx, "echo", verb, scope)
	if code != hub.AdminOK || st.Code != protocol.CodeOK || st.Summary == nil ||
		st.Summary.Packages[0] != "@example/nodes 1.4.0 -> 2.0.1" {
		t.Fatalf("clean plan: %s %+v", code, st)
	}
}
