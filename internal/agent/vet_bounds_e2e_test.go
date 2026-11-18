package agent

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/hub"
	"github.com/psewar/vordr/internal/inventory"
	"github.com/psewar/vordr/internal/policy"
	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/runner"
	"github.com/psewar/vordr/internal/versions"
	"github.com/psewar/vordr/internal/wire"
)

// vetHub is a hub with OSV data for eco, over a real tunnel.
func vetHub(t *testing.T, eco versions.Ecosystem) (*testHub, *hub.Packages) {
	t.Helper()
	h := startHub(t, 5)
	pk, err := hub.NewPackages(t.TempDir(), &hub.OSVConfig{URL: osvServer(t).URL, Ecosystems: []string{string(eco)},
		RefreshHours: 24, StaleHours: 72}, time.Now, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	if !pk.Refresh(context.Background(), eco) {
		t.Fatal("no OSV data")
	}
	h.srv.Packages = pk
	return h, pk
}

// runAgent runs c until the test ends. Before the folders, servers and settings the test registered earlier are
// cleaned up, it stops the agent and waits for its loop and for the install runs it tracks (WaitRuns): an agent still
// writing its state into a folder would make the folder's removal fail. Goroutines the agent does not track, as the
// check it starts after an install, are not waited for; register what they need before calling runAgent.
func runAgent(t *testing.T, c *Client) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.Run(ctx)
	}()
	t.Cleanup(func() { // registered after the folders: it runs before their removal
		cancel()
		<-done
		c.WaitRuns()
	})
	return ctx
}

// vetEvents: the hub's events about vet requests, as lines.
func vetEvents(t *testing.T, h *testHub, event string) []string {
	t.Helper()
	raw, _, err := h.events.Read(hub.EventFilter{Event: event})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range raw {
		out = append(out, string(l))
	}
	return out
}

// An agent that gets vet_out_of_bounds stops the update (ADR-0196): over a real tunnel, the agent's refresh of
// dependencies, with its own vetter, offers a location holding a version with a form feed - as an inventory reader with
// a fault would - and ends with precheck_vetting_unavailable, the hub's code in the detail, the connection kept. The
// hub logs the refusal without the request's text. The agent's own reader never offers such an entry (inventory tests).
func TestAVetRequestOutOfBoundsStopsTheUpdate(t *testing.T) {
	h, _ := vetHub(t, versions.Npm)
	dir := filepath.Join(t.TempDir(), "project")
	writeFiles(t, dir, map[string]string{"package.json": `{"dependencies":{"debug":"^4.4.0"}}`,
		"package-lock.json": `{"lockfileVersion":3,"packages":{}}`})
	loc := inventory.Location{ID: "project", Kind: inventory.KindNpmDir, Ecosystem: versions.Npm, Path: dir,
		Packages: []inventory.Package{{Name: "debug", Version: "4.4.3", Direct: true}, {Name: "ms", Version: "2.1.3"},
			{Name: "left-pad", Version: "1.3.0\f"}}}
	c := h.client(t, "echo", &fakeChecker{})
	c.Packages = &PackageUpdates{}
	ctx := runAgent(t, c)
	waitFor(t, "pending", func() bool { return state(h.reg, "echo") == wire.StatePendingApproval })
	h.srv.Admin(hub.AdminRequest{Cmd: "approve", Node: "echo", Fingerprint: c.ID.Fingerprint()})
	waitFor(t, "first report", func() bool { e, _ := h.reg.Get("echo"); return e.LastReport != nil })

	u := &inventory.Updater{Now: time.Now, Vet: c.vetter("0123456789abcdef01234567")}
	res := u.RefreshDeps(ctx, loc, func() error { t.Fatal("journal started"); return nil })
	if res.Code != protocol.CodePrecheckVettingUnavailable || !strings.Contains(res.Detail, protocol.VetOutOfBounds) {
		t.Fatalf("refresh: %s %q", res.Code, res.Detail)
	}
	if online, _ := h.srv.Online("echo"); !online {
		t.Fatal("the refused request ended the connection")
	}
	refused := vetEvents(t, h, "vet_refused")
	if len(refused) != 1 || !strings.Contains(refused[0], `"fields":{"packages[].version":"characters"}`) ||
		!strings.Contains(refused[0], c.ID.Fingerprint()) {
		t.Fatalf("vet_refused: %v", refused)
	}
	if vets := vetEvents(t, h, "vet"); len(vets) != 0 {
		t.Errorf("a refused request logged as vetted: %v", vets)
	}
	all, _, _ := h.events.Read(hub.EventFilter{Limit: hub.MaxEventLimit})
	for _, l := range all {
		if strings.Contains(string(l), "left-pad") || strings.Contains(string(l), "2.1.3") {
			t.Errorf("the refused request in the event log: %s", l)
		}
	}
}

// The real agent never asks the hub with a plan it would refuse (ADR-0196): npm's lock file writes a version with a
// form feed after it, which the reading of versions trims; the agent stops with its own failed_package_manager before
// it asks, and the hub logs no vet request at all.
func TestAnAgentDoesNotAskWithAPlanTheHubWouldRefuse(t *testing.T) {
	inventory.NpmCommand = func() (string, []string, bool) { return "npm", nil, true }
	t.Cleanup(func() { inventory.NpmCommand = inventory.DefaultNpmCommand }) // after the agent has stopped
	h, _ := vetHub(t, versions.Npm)
	reg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		old := time.Now().Add(-30 * 24 * time.Hour).Format(time.RFC3339)
		_ = json.NewEncoder(w).Encode(map[string]any{"dist-tags": map[string]string{"latest": "2.0.1"},
			"time":     map[string]string{"1.4.0": old, "2.0.1": old},
			"versions": map[string]any{"1.4.0": map[string]any{}, "2.0.1": map[string]any{}}})
	}))
	t.Cleanup(reg.Close)
	nm := filepath.Join(t.TempDir(), "lib", "node_modules")
	writeFile(t, filepath.Join(nm, "@example", "nodes", "package.json"), `{"name":"@example/nodes","version":"1.4.0"}`)
	npm := &fakeNpm{nm: nm, plan: map[string]string{"@example/nodes": "2.0.1", "debug": "4.4.3\f"}}
	c := h.client(t, "echo", &fakeChecker{})
	c.Packages = &PackageUpdates{Cfg: policy.Packages{Update: []string{"npm-global"}, MinAgeDays: 7}, Exec: npm,
		Reg: inventory.Registries{Npm: reg.URL, Client: reg.Client()},
		Collect: func() []inventory.Location {
			return inventory.Collect(inventory.Options{Configured: []inventory.Configured{{ID: "npm-global", Path: nm}}})
		}}
	ctx := runAgent(t, c)
	waitFor(t, "pending", func() bool { return state(h.reg, "echo") == wire.StatePendingApproval })
	h.srv.Admin(hub.AdminRequest{Cmd: "approve", Node: "echo", Fingerprint: c.ID.Fingerprint()})
	waitFor(t, "first report", func() bool { e, _ := h.reg.Get("echo"); return e.LastReport != nil })

	jctx, jcancel := context.WithTimeout(ctx, 20*time.Second)
	defer jcancel()
	st, code := h.srv.SendJob(jctx, "echo", protocol.VerbInstall, "pkg:npm-global/@example/nodes")
	if code != hub.AdminOK || st.Code != protocol.CodeFailedPackageManager ||
		!strings.Contains(st.Detail, "planned an entry that is no package") {
		t.Fatalf("update: %s %+v", code, st)
	}
	if v := c.Packages.Collect()[0].Packages[0].Version; v != "1.4.0" {
		t.Fatalf("installed: %s", v)
	}
	for _, event := range []string{"vet", "vet_refused"} {
		if got := vetEvents(t, h, event); len(got) != 0 {
			t.Errorf("the hub was asked: %s %v", event, got)
		}
	}
}

// fakePip resolves a given plan and "installs" it by writing the dist-info folders the inventory reads.
type fakePip struct {
	mu   sync.Mutex
	site string
	plan map[string]string
}

func (f *fakePip) Run(_ context.Context, c inventory.Command) runner.Result {
	f.mu.Lock()
	defer f.mu.Unlock()
	args := strings.Join(c.Args, " ")
	switch {
	case strings.Contains(args, "install --dry-run"):
		var rep struct {
			Install []map[string]map[string]string `json:"install"`
		}
		for n, v := range f.plan {
			rep.Install = append(rep.Install, map[string]map[string]string{"metadata": {"name": n, "version": v}})
		}
		raw, _ := json.Marshal(rep)
		for i, a := range c.Args {
			if a == "--report" {
				_ = os.WriteFile(c.Args[i+1], raw, 0o644)
			}
		}
	case strings.Contains(args, "install --no-deps"):
		for _, a := range c.Args {
			if n, v, ok := strings.Cut(a, "=="); ok {
				old, _ := filepath.Glob(filepath.Join(f.site, n+"-*.dist-info"))
				for _, o := range old {
					_ = os.RemoveAll(o)
				}
				_ = os.MkdirAll(filepath.Join(f.site, n+"-"+v+".dist-info"), 0o755)
				_ = os.WriteFile(filepath.Join(f.site, n+"-"+v+".dist-info", "METADATA"),
					[]byte("Metadata-Version: 2.1\nName: "+n+"\nVersion: "+v+"\n"), 0o644)
			}
		}
	}
	return runner.Result{}
}

// The real agent's pip plan - its own planner, through its own checks - reaches the hub over a real tunnel and is
// vetted, not refused (ADR-0196); its npm plans do in TestPackageUpdateThroughTheHub.
func TestTheRealAgentsPipPlanFitsTheHubsBounds(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the virtual environment is laid out as on Linux")
	}
	h, _ := vetHub(t, versions.PyPI)
	reg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		old := time.Now().Add(-30 * 24 * time.Hour).UTC().Format(time.RFC3339)
		file := []map[string]any{{"upload_time_iso_8601": old, "yanked": false}}
		_ = json.NewEncoder(w).Encode(map[string]any{"releases": map[string]any{"2.31.0": file, "2.32.4": file,
			"2.5.0": file}})
	}))
	t.Cleanup(reg.Close) // after the agent has stopped
	venv := filepath.Join(t.TempDir(), "app")
	site := filepath.Join(venv, "lib", "python3.12", "site-packages")
	writeFiles(t, venv, map[string]string{"pyvenv.cfg": "home = /usr/bin\n", "bin/python": "",
		"lib/python3.12/site-packages/requests-2.31.0.dist-info/METADATA": "Metadata-Version: 2.1\nName: requests\n" +
			"Version: 2.31.0\n"})
	pip := &fakePip{site: site, plan: map[string]string{"requests": "2.32.4", "urllib3": "2.5.0"}}
	c := h.client(t, "pip-host", &fakeChecker{})
	c.Packages = &PackageUpdates{Cfg: policy.Packages{Update: []string{"venv-app"}, MinAgeDays: 7}, Exec: pip,
		Reg: inventory.Registries{PyPI: reg.URL, Client: reg.Client()},
		Collect: func() []inventory.Location {
			return inventory.Collect(inventory.Options{Configured: []inventory.Configured{{ID: "venv-app", Path: venv}}})
		}}
	ctx := runAgent(t, c)
	waitFor(t, "pending", func() bool { return state(h.reg, "pip-host") == wire.StatePendingApproval })
	h.srv.Admin(hub.AdminRequest{Cmd: "approve", Node: "pip-host", Fingerprint: c.ID.Fingerprint()})
	waitFor(t, "first report", func() bool { e, _ := h.reg.Get("pip-host"); return e.LastReport != nil })

	jctx, jcancel := context.WithTimeout(ctx, 20*time.Second)
	defer jcancel()
	st, code := h.srv.SendJob(jctx, "pip-host", protocol.VerbInstall, "pkg:venv-app/requests")
	// Vetted, clean, installed.
	if code != hub.AdminOK || st.Code != protocol.CodeOK || st.Summary == nil ||
		st.Summary.Packages[0] != "requests 2.31.0 -> 2.32.4" {
		t.Fatalf("update: %s %+v", code, st)
	}
	if refused := vetEvents(t, h, "vet_refused"); len(refused) != 0 {
		t.Fatalf("the agent's pip plan refused: %v", refused)
	}
	vets := vetEvents(t, h, "vet")
	if len(vets) != 1 || !strings.Contains(vets[0], `"ecosystem":"PyPI"`) || !strings.Contains(vets[0], `"packages":2`) ||
		!strings.Contains(vets[0], `"unavailable":""`) {
		t.Fatalf("vet: %v (update %+v)", vets, st)
	}
}
