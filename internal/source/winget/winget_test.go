package winget

import (
	"context"
	"encoding/base64"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/runner"
	"github.com/psewar/vordr/internal/source"
)

// fakeRun answers the list script with the next of its outputs and remembers which scripts ran.
type fakeRun struct {
	outputs []runner.Result
	scripts []string
}

func (f *fakeRun) Run(_ context.Context, _ []string, _ string, args ...string) runner.Result {
	f.scripts = append(f.scripts, decode(args[len(args)-1]))
	if len(f.outputs) == 0 {
		return runner.Result{Err: errors.New("no more answers")}
	}
	r := f.outputs[0]
	f.outputs = f.outputs[1:]
	return r
}

// fakeLines plays the upgrade script's lines.
type fakeLines struct {
	lines  []string
	script string
}

func (f *fakeLines) Lines(_ context.Context, onLine func(string), _ string, args ...string) runner.Result {
	f.script = decode(args[len(args)-1])
	for _, l := range f.lines {
		onLine(l)
	}
	return runner.Result{Stdout: []byte(strings.Join(f.lines, "\n"))}
}

func decode(b64 string) string {
	raw, _ := base64.StdEncoding.DecodeString(b64)
	units := make([]uint16, len(raw)/2)
	for i := range units {
		units[i] = uint16(raw[2*i]) | uint16(raw[2*i+1])<<8
	}
	return string(utf16.Decode(units))
}

func out(s string) runner.Result { return runner.Result{Stdout: []byte(s)} }

const listTwo = `{"status":"ok","winget_version":"v1.29.380","client_version":"1.29.380","user":"bob","packages":[` +
	`{"id":"jqlang.jq","name":"jq","installed":"1.7.0","available":"1.7.1","source":"winget"},` +
	`{"id":"OpenJS.NodeJS.LTS","name":"Node.js (LTS)","installed":"20.11.1","available":"22.2.0","source":"winget"},` +
	`{"id":"bad id; rm","name":"x","installed":"1","available":"2","source":"winget"}]}`
const listNone = `{"status":"ok","winget_version":"v1.29.380","client_version":"1.29.380","user":"bob","packages":[]}`

func newWinget(elevated bool, outputs ...runner.Result) (*Winget, *fakeRun, *fakeLines) {
	r, l := &fakeRun{outputs: outputs}, &fakeLines{}
	return &Winget{Run: r, Lines: l, PowerShell: `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`,
		Elevated: elevated, Now: func() time.Time { return time.Date(2026, 10, 30, 2, 0, 0, 0, time.UTC) },
		FreeBytes: func() (uint64, bool) { return 50 << 30, true }, Busy: func() bool { return false },
		Pending: func() source.Reboot { return source.Reboot{Checked: []string{"windows_update"}} }}, r, l
}

func TestCheck(t *testing.T) {
	w, r, _ := newWinget(true, out("WARNING: something\n"+listTwo+"\n"))
	rep := w.Check(context.Background())
	if rep.Source != "winget" || rep.State != protocol.SourceUpdatesAvailable || rep.Pending != 2 || rep.Security != 0 {
		t.Fatalf("report: %+v", rep)
	}
	if p := rep.Packages[0]; p.Name != "jqlang.jq" || p.From != "1.7.0" || p.To != "1.7.1" || !slices.Equal(p.Origins, []string{"winget"}) || p.Major {
		t.Fatalf("first package: %+v", p)
	}
	if p := rep.Packages[1]; p.Name != "OpenJS.NodeJS.LTS" || !p.Major {
		t.Fatalf("Node.js 20 -> 22 is a major jump: %+v", p)
	}
	if rep.Facts["winget_version"] != "v1.29.380" || rep.Facts["winget_client"] != "1.29.380" ||
		rep.Facts["install_context"] != ContextElevated || rep.Facts["winget_user"] != "bob" {
		t.Fatalf("facts: %v", rep.Facts)
	}
	if !strings.Contains(r.scripts[0], "Get-WinGetPackage") {
		t.Fatalf("ran something else: %q", r.scripts[0])
	}
}

// A missing PowerShell module is a fact and a failed check with the next step, not an absent source.
func TestCheckClientMissing(t *testing.T) {
	w, _, _ := newWinget(false, out(`{"status":"client_missing","user":"SYSTEM"}`))
	rep := w.Check(context.Background())
	if rep.State != protocol.SourceCheckFailed || rep.Code != protocol.CodeFailedPackageManager ||
		rep.Facts["winget_client"] != ClientMissing || rep.Facts["install_context"] != ContextUser ||
		!strings.Contains(rep.Detail, "Install-Module Microsoft.WinGet.Client") {
		t.Fatalf("report: %+v", rep)
	}
}

// As the service (LocalSystem) the module refuses Windows PowerShell: a fact and a next step before anything runs.
func TestServiceNeedsPowerShell7(t *testing.T) {
	w, r, _ := newWinget(true)
	w.System = true
	rep := w.Check(context.Background())
	if rep.State != protocol.SourceCheckFailed || rep.Code != protocol.CodeFailedPackageManager ||
		rep.Facts["winget_client"] != ClientNeedsPowerShell7 || !strings.Contains(rep.Detail, "Microsoft.PowerShell") {
		t.Fatalf("report: %+v", rep)
	}
	if res, _, journaled := install(w, all, ""); res.Code != protocol.CodeRejectedUnsupported || journaled || len(r.scripts) != 0 {
		t.Fatalf("install: %+v, ran %d scripts", res, len(r.scripts))
	}

	// with PowerShell 7 for all users the service checks as usual
	w, _, _ = newWinget(true, out(listNone))
	w.System, w.PowerShell = true, `C:\Program Files\PowerShell\7\pwsh.exe`
	if rep := w.Check(context.Background()); rep.State != protocol.SourceUpToDate {
		t.Fatalf("pwsh as LocalSystem: %+v", rep)
	}
}

// A package whose installed version winget does not know ("Unknown", as some launchers report) is neither pending
// nor upgraded, as with "winget upgrade" without --include-unknown; the fact names it.
func TestUnknownVersionIsLeftOut(t *testing.T) {
	withUnknown := strings.Replace(listTwo, `{"id":"bad id; rm"`,
		`{"id":"Example.Launcher","name":"Example Launcher","installed":"Unknown","available":"2.0.0.1000","source":"winget"},{"id":"bad id; rm"`, 1)
	w, _, _ := newWinget(true, out(withUnknown))
	rep := w.Check(context.Background())
	if rep.Pending != 2 || rep.Facts["winget_unknown_version"] != "Example.Launcher" {
		t.Fatalf("report: pending %d, facts %v", rep.Pending, rep.Facts)
	}
	w, _, l := newWinget(true, out(withUnknown), out(listNone))
	l.lines = []string{`{"event":"result","id":"jqlang.jq","status":"Ok"}`,
		`{"event":"result","id":"OpenJS.NodeJS.LTS","status":"Ok"}`, `{"event":"done"}`}
	if res, _, _ := install(w, all, ""); res.Code != protocol.CodeOK || res.Planned != 2 {
		t.Fatalf("install: %s planned %d %q", res.Code, res.Planned, res.Detail)
	}
}

func TestCheckFailed(t *testing.T) {
	for name, res := range map[string]runner.Result{
		"cannot run": {Err: errors.New("not found")},
		"exit code":  {ExitCode: 1, Stderr: []byte("boom")},
		"no json":    out("Get-WinGetPackage : something\n"),
		"failed":     out(`{"status":"failed","error_type":"System.Runtime.InteropServices.COMException","detail":"x"}`),
	} {
		w, _, _ := newWinget(true, res)
		if rep := w.Check(context.Background()); rep.State != protocol.SourceCheckFailed || rep.Code != protocol.CodeFailedPackageManager {
			t.Errorf("%s: %+v", name, rep)
		}
	}
}

func TestDetectWithoutPowerShell(t *testing.T) {
	w, _, _ := newWinget(true)
	w.PowerShell = ""
	if w.Detect() != source.Absent {
		t.Fatal("present without PowerShell")
	}
}

func install(w *Winget, scope protocol.Scope, pkg string) (source.InstallResult, []source.Progress, bool) {
	var seen []source.Progress
	journaled := false
	res := w.Install(context.Background(), source.InstallRequest{Scope: scope, Package: pkg,
		BeforeChange: func() error { journaled = true; return nil }}, time.Hour, func(p source.Progress) { seen = append(seen, p) })
	return res, seen, journaled
}

var (
	all = protocol.Scope{Kind: protocol.ScopeAll}
	jq  = protocol.Scope{Kind: protocol.ScopeApp, App: "jq"}
)

// One named app: only it is upgraded, with its progress, and the fresh list must no longer show it.
func TestInstallApp(t *testing.T) {
	w, _, l := newWinget(false, out(listTwo), out(listNone))
	l.lines = []string{`{"event":"start","id":"jqlang.jq"}`,
		`{"event":"result","id":"jqlang.jq","status":"Ok","reboot_required":false,"installer_error_code":0,"extended_error_code":0}`,
		`{"event":"done"}`}
	res, seen, journaled := install(w, jq, "JQlang.JQ")
	if res.Code != protocol.CodeOK || res.Planned != 1 || !slices.Equal(res.Packages, []string{"jqlang.jq"}) || !journaled {
		t.Fatalf("result: %+v journaled %v", res, journaled)
	}
	if !strings.HasPrefix(l.script, "$VordrIds = @('jqlang.jq')\n") || !strings.Contains(l.script, "Update-WinGetPackage") {
		t.Fatalf("upgrade script: %q", l.script[:60])
	}
	if !slices.ContainsFunc(seen, func(p source.Progress) bool { return p.Phase == protocol.PhaseInstalling && p.Item == "jqlang.jq" }) {
		t.Fatalf("progress: %+v", seen)
	}
	if !strings.Contains(string(res.Log), `"event":"result"`) {
		t.Fatalf("log: %q", res.Log)
	}
}

func TestInstallAllNeedsElevation(t *testing.T) {
	w, r, _ := newWinget(false)
	if res, _, journaled := install(w, all, ""); res.Code != protocol.CodeRejectedUnsupported || journaled || len(r.scripts) != 0 {
		t.Fatalf("unelevated all: %+v", res)
	}
}

func TestInstallPrechecks(t *testing.T) {
	w, _, _ := newWinget(true)
	w.Busy = func() bool { return true }
	if res, _, _ := install(w, all, ""); res.Code != protocol.CodeRejectedBusy {
		t.Fatalf("busy: %+v", res)
	}
	w, _, _ = newWinget(true)
	w.FreeBytes = func() (uint64, bool) { return 1 << 30, true }
	if res, _, _ := install(w, all, ""); res.Code != protocol.CodePrecheckDiskSpace || !strings.Contains(res.Detail, "1024 MiB") {
		t.Fatalf("disk: %+v", res)
	}
	w, _, _ = newWinget(true, out(`{"status":"client_missing","user":"SYSTEM"}`))
	if res, _, journaled := install(w, all, ""); res.Code != protocol.CodeFailedPackageManager || journaled {
		t.Fatalf("client missing: %+v", res)
	}
}

// winget does not classify updates: the security scope finds nothing and changes nothing.
func TestInstallSecurityFindsNothing(t *testing.T) {
	w, _, l := newWinget(true, out(listTwo))
	res, _, journaled := install(w, protocol.Scope{Kind: protocol.ScopeSecurity}, "")
	if res.Code != protocol.CodeOK || journaled || l.script != "" {
		t.Fatalf("security: %+v", res)
	}
}

func TestInstallResults(t *testing.T) {
	for name, tc := range map[string]struct {
		jq, node string
		after    string
		want     protocol.Code
		detail   string
	}{
		"both ok": {`"status":"Ok"`, `"status":"Ok"`, listNone, protocol.CodeOK, ""},
		"reboot":  {`"status":"Ok","reboot_required":true`, `"status":"Ok"`, listNone, protocol.CodeOKRebootRequired, ""},
		"msi busy": {`"status":"Ok"`, `"status":"InstallError","installer_error_code":1618`, listNone,
			protocol.CodeRejectedBusy, "OpenJS.NodeJS.LTS: InstallError, installer exit code 1618"},
		"installer failed": {`"status":"InstallError","installer_error_code":1603,"extended_error_code":-1978335216`,
			`"status":"Ok"`, listNone, protocol.CodeFailedPackageManager, "jqlang.jq: InstallError, installer exit code 1603, 0x8A150010"},
		// One package not even downloaded while the other went through: the run changed the machine, so not precheck_.
		"download, other ok": {`"status":"DownloadError"`, `"status":"Ok"`, listNone, protocol.CodeFailedPackageManager, "DownloadError"},
		"download, nothing else": {`"status":"CatalogError","extended_error_code":-1978335137`, `"status":"NoApplicableUpgrade"`,
			listNone, protocol.CodePrecheckRepoUnreachable, "jqlang.jq: CatalogError, 0x8A15005F"},
		"thrown": {`"status":"exception","error_type":"System.Exception","detail":"No package found"`, `"status":"Ok"`, listNone,
			protocol.CodeFailedPackageManager, "jqlang.jq: exception (System.Exception: No package found)"},
		"still pending": {`"status":"Ok"`, `"status":"NoApplicableUpgrade"`, strings.Replace(listTwo, `{"id":"jqlang.jq","name":"jq","installed":"1.7.0","available":"1.7.1","source":"winget"},`, "", 1),
			protocol.CodeFailedVerification, "still pending after the run: OpenJS.NodeJS.LTS 20.11.1 -> 22.2.0"},
	} {
		w, _, l := newWinget(true, out(listTwo), out(tc.after))
		l.lines = []string{`{"event":"start","id":"jqlang.jq"}`, `{"event":"result","id":"jqlang.jq",` + tc.jq + `}`,
			`{"event":"start","id":"OpenJS.NodeJS.LTS"}`, `{"event":"result","id":"OpenJS.NodeJS.LTS",` + tc.node + `}`,
			`{"event":"done"}`}
		res, _, _ := install(w, all, "")
		if res.Code != tc.want || !strings.Contains(res.Detail, tc.detail) || res.Planned != 2 {
			t.Errorf("%s: %s %q", name, res.Code, res.Detail)
		}
	}
}

// A run that ends without a result for a package (PowerShell died) is no success.
func TestInstallMissingResult(t *testing.T) {
	w, _, l := newWinget(true, out(listTwo), out(listNone))
	l.lines = []string{`{"event":"start","id":"jqlang.jq"}`}
	if res, _, _ := install(w, all, ""); res.Code != protocol.CodeFailedPackageManager || !strings.Contains(res.Detail, "no result") {
		t.Fatalf("missing result: %+v", res)
	}
}

func TestWithIDsQuotes(t *testing.T) {
	if got := withIDs([]string{"a.b", "c'd"}); !strings.HasPrefix(got, "$VordrIds = @('a.b', 'c''d')\n") {
		t.Fatalf("withIDs: %q", got[:40])
	}
}
