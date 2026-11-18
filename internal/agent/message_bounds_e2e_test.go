package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/facts"
	"github.com/psewar/vordr/internal/hub"
	"github.com/psewar/vordr/internal/identity"
	"github.com/psewar/vordr/internal/policy"
	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/source"
	"github.com/psewar/vordr/internal/wire"
)

// writeFiles writes each file below dir, creating its folders.
func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// The compatibility check of ADR-0195: what the real agent sends after its hello reaches the hub over a real tunnel
// without a field dropped or a message refused - its inventory reader on an npm project with the longest name and
// version npm takes, a nested dependency and its hold, a virtual environment, and whatever the machine the test runs
// on has installed in the well-known places; the progress and the result of an install run with a multi-line excerpt,
// a full summary and its run log. The report here is a stand-in; the sources' own tests run what they report through
// the hub's bounds (hub.ReportNotTaken).
func TestTheRealAgentsMessagesFitTheHubsBounds(t *testing.T) {
	h := startHub(t, 5)
	pk, err := hub.NewPackages(t.TempDir(), nil, time.Now, quiet)
	if err != nil {
		t.Fatal(err)
	}
	h.srv.Packages = pk
	dir := t.TempDir()
	scoped := "@" + strings.Repeat("s", 100) + "/" + strings.Repeat("n", 112) // npm's longest: 214 characters
	version := "1.0.0-beta.1+build." + strings.Repeat("7", 45)                // the agent's longest: 64
	writeFiles(t, dir, map[string]string{
		"project/package.json": `{"dependencies": {"` + scoped + `": "^1.0.0-beta.1", "left-pad": "~1.3.0"}}`,
		"project/node_modules/" + scoped + "/package.json": `{"name": "` + scoped + `", "version": "` + version +
			`", "dependencies": {"lodash": ">=4.17.0 <4.18.0 || ^5.0.0-rc.1"}}`,
		"project/node_modules/" + scoped + "/node_modules/lodash/package.json": `{"name": "lodash", "version": "4.17.21"}`,
		"project/node_modules/left-pad/package.json":                           `{"name": "left-pad", "version": "1.3.0"}`,
		"venv/pyvenv.cfg": "home = /usr/bin\n",
		"venv/lib/python3.12/site-packages/zope.interface-7.2.dist-info/METADATA": "Metadata-Version: 2.1\n" +
			"Name: zope.interface\nVersion: 7.2\n",
	})
	stateDir := t.TempDir()
	cfg, issues := policy.Parse([]byte(fmt.Sprintf("node: dev-box\nstate_dir: '%s'\n"+
		"hub: {url: '%s', public_key: %s, ca_file: '%s'}\n"+
		"install: {allowed_scopes: [all], default_scope: all}\n"+
		"packages: {outdated: false, locations: [{id: project, path: '%s'}, {id: tool, path: '%s'}]}\n",
		stateDir, h.url, h.key.PublicB64(), h.caFile, filepath.Join(dir, "project"), filepath.Join(dir, "venv"))))
	if len(issues) > 0 {
		t.Fatalf("agent.yaml: %v", issues)
	}
	id, _, err := identity.LoadOrCreate(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	checker := &fakeChecker{}
	agent := NewClient(cfg, id, "0.48.0", facts.Host{OS: "Debian GNU/Linux 12 (bookworm)"}, checker, quiet)
	agent.Inventory = Inventory(cfg, stateDir, "0.48.0", quiet, time.Now)
	packages := make([]string, 150)
	for i := range packages {
		packages[i] = fmt.Sprintf("libreoffice-l10n-%d", i)
	}
	agent.Installer = &fakeInstaller{change: true, reboot: true,
		steps: []source.Progress{{Phase: protocol.PhaseDownloading, Percent: 30, Item: "libreoffice-l10n-0"},
			{Phase: protocol.PhaseInstalling, Percent: 70, Item: "libreoffice-l10n-1"}},
		result: source.InstallResult{Code: protocol.CodeOK, Planned: len(packages), Packages: packages,
			Detail:           strings.Repeat("Setting up libreoffice-l10n (4:7.4.7-1+deb12u8) ...\n", 15),
			Log:              []byte("Get:1 http://deb.example/debian bookworm/main amd64 libreoffice-l10n\n"),
			OutdatedServices: []string{"systemd-logind.service", "cron.service"}, Replaced: []string{"libssl3 -> libssl3t64"}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go agent.Run(ctx)
	waitFor(t, "pending", func() bool { return state(h.reg, "dev-box") == wire.StatePendingApproval })
	h.srv.Admin(hub.AdminRequest{Cmd: "approve", Node: "dev-box", Fingerprint: agent.ID.Fingerprint()})
	waitFor(t, "inventory at the hub", func() bool { return pk.View("dev-box") != nil })

	jctx, jcancel := context.WithTimeout(ctx, 10*time.Second)
	defer jcancel()
	st, code := h.srv.SendJob(jctx, "dev-box", protocol.VerbInstall, "all")
	if code != hub.AdminOK || st.Code != protocol.CodeOKRebootRequired || st.Summary == nil ||
		st.Summary.PackageCount != len(packages) || st.Summary.LogFile == "" {
		t.Fatalf("install: %s %+v", code, st)
	}
	e, _ := h.reg.Get("dev-box")
	if e.LastRun == nil || e.LastRun.Detail != st.Detail || e.LastRun.Summary == nil ||
		len(e.LastRun.Summary.Packages) != wire.MaxSummaryPackages {
		t.Fatalf("run: %+v", e.LastRun)
	}
	v := pk.View("dev-box")
	found := false
	for _, l := range v.Locations {
		found = found || l.ID == "project" && l.Packages == 3
	}
	if !found {
		t.Fatalf("the project did not reach the hub whole: %+v", v.Locations)
	}
	for _, event := range []string{"report_fields_dropped", "report_refused", "inventory_fields_dropped",
		"inventory_refused", "job_status_fields_dropped", "job_status_refused"} {
		raw, _, err := h.events.Read(hub.EventFilter{Event: event})
		if err != nil || len(raw) != 0 {
			t.Errorf("%s: %d %v", event, len(raw), err)
			for _, l := range raw {
				t.Log(string(l))
			}
		}
	}
}
