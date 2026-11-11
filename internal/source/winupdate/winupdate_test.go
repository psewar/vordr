package winupdate

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

// fakeRun answers the search script with the next of its outputs and remembers which scripts ran.
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

// fakeLines plays the install script's lines.
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

// Update ids and lines in the shape search.ps1 prints them, plus the cases it must leave out.
const (
	idNet      = "00000000-0000-4000-8000-000000000001"
	idDefender = "00000000-0000-4000-8000-000000000002"
	idNoKB     = "0c2b9f7e-1111-4a2b-9c3d-123456789abc"
	idOther    = "44444444-5555-6666-7777-888888888888"

	upNet = `{"id":"` + idNet + `","revision":200,"title":"2023-11 Security Update for Example Runtime 1.0 for x64 Client (KB5000001)",` +
		`"kb":["5000001"],"categories":["00000000-0000-4000-8000-000000000011","0fa1201d-4330-4fa8-8ae9-b877473b6441"],` +
		`"browse_only":false,"reboot_required":false,"eula_accepted":true,"downloaded":false,"max_download_size":150000000}`
	upDefender = `{"id":"` + idDefender + `","revision":200,"title":"Security Intelligence Update for Microsoft Defender ` +
		`Antivirus - KB2267602 (Version 1.400.0.0) - Current Channel (Broad)","kb":["2267602"],` +
		`"categories":["e0789628-ce08-4437-be74-2495b842f43b","00000000-0000-4000-8000-000000000012"],` +
		`"browse_only":false,"reboot_required":false,"eula_accepted":true,"downloaded":false,"max_download_size":1500000000}`
	upNoKB = `{"id":"` + idNoKB + `","title":"Some update without a KB article","kb":[],` +
		`"categories":["cd5ffd1e-e932-4e3a-bf74-18bf0b1bbd83"]}`
	upOther = `{"id":"` + idOther + `","revision":1,"title":"2023-11 Update for Windows 11 for x64-based Systems (KB5000002)",` +
		`"kb":["5000002"],"categories":["cd5ffd1e-e932-4e3a-bf74-18bf0b1bbd83"],"browse_only":false,"reboot_required":false,` +
		`"eula_accepted":true,"downloaded":false,"max_download_size":52428800}`
	upFeature = `{"id":"11111111-2222-3333-4444-555555555555","title":"Windows 11, version 23H2","kb":["5000003"],` +
		`"categories":["3689BDC8-B205-4AF4-8D4A-A63924C5E9D5"]}`
	upPreview = `{"id":"22222222-3333-4444-5555-666666666666","title":"2023-11 Cumulative Update Preview","kb":["5000004"],` +
		`"categories":["cd5ffd1e-e932-4e3a-bf74-18bf0b1bbd83"],"browse_only":true}`
	upWaiting = `{"id":"33333333-4444-5555-6666-777777777777","title":"2023-11 Cumulative Update","kb":["5000005"],` +
		`"categories":["0fa1201d-4330-4fa8-8ae9-b877473b6441"],"reboot_required":true}`
	upBadID = `{"id":"x'); Remove-Item C:\\ -Recurse; ('","title":"bad","kb":["1"],"categories":[]}`
)

func searched(updates ...string) string {
	return `{"status":"ok","agent_version":"10.0.1000.0","user":"SYSTEM","installer_busy":false,"updates":[` +
		strings.Join(updates, ",") + `]}`
}

var (
	foundTwo  = searched(upNet, upOther)
	foundNone = searched()
)

func newWinUpdate(elevated bool, outputs ...runner.Result) (*WinUpdate, *fakeRun, *fakeLines) {
	r, l := &fakeRun{outputs: outputs}, &fakeLines{}
	return &WinUpdate{Run: r, Lines: l, PowerShell: `C:\Program Files\PowerShell\7\pwsh.exe`, Elevated: elevated,
		Now:       func() time.Time { return time.Unix(1700000000, 0).UTC() },
		FreeBytes: func() (uint64, bool) { return 50 << 30, true }, Busy: func() bool { return false },
		Pending: func() source.Reboot { return source.Reboot{Checked: []string{"windows_update"}} }}, r, l
}

func TestCheck(t *testing.T) {
	w, r, _ := newWinUpdate(true, out("WARNING: something\n"+
		searched(upNet, upDefender, upNoKB, upFeature, upPreview, upWaiting, upBadID)+"\n"))
	rep := w.Check(context.Background())
	if rep.Source != "winupdate" || rep.State != protocol.SourceUpdatesAvailable || rep.Pending != 2 || rep.Security != 1 {
		t.Fatalf("report: %+v", rep)
	}
	// Defender's definition update is left out: Defender fetches its security intelligence itself.
	want := []source.Package{
		{Name: "KB5000001", To: "2023-11 Security Update for Example Runtime 1.0 for x64 Client (KB5000001)", Security: true},
		{Name: idNoKB, To: "Some update without a KB article"}, // no KB number: the update id is the name
	}
	if len(rep.Packages) != len(want) {
		t.Fatalf("packages: %+v", rep.Packages)
	}
	for i, p := range rep.Packages {
		if p.Name != want[i].Name || p.To != want[i].To || p.Security != want[i].Security || p.From != "" || p.Major {
			t.Errorf("package %d: %+v, want %+v", i, p, want[i])
		}
	}
	for fact, value := range map[string]string{"install_context": "elevated", FactSearch: SearchOK,
		FactAgent: "10.0.1000.0", FactOptional: "1", FactUpgrades: "1", FactDefinitions: "1",
		FactAwaitingReboot: "1"} {
		if rep.Facts[fact] != value {
			t.Errorf("fact %s: %q, want %q (%v)", fact, rep.Facts[fact], value, rep.Facts)
		}
	}
	if _, set := rep.Facts[FactHResult]; set {
		t.Errorf("hresult fact without a failure: %v", rep.Facts)
	}
	if len(r.scripts) != 1 || !strings.Contains(r.scripts[0], "IsInstalled=0 and IsHidden=0 and Type='Software'") ||
		strings.Contains(r.scripts[0], ".Install(") || strings.Contains(r.scripts[0], "Download(") {
		t.Fatalf("the check must only search: %q", r.scripts)
	}
}

func TestCheckUpToDate(t *testing.T) {
	w, _, _ := newWinUpdate(false, out(foundNone))
	rep := w.Check(context.Background())
	if rep.State != protocol.SourceUpToDate || rep.Pending != 0 || rep.Facts["install_context"] != "user" ||
		rep.Facts[FactAwaitingReboot] != "0" {
		t.Fatalf("report: %+v", rep)
	}
}

// A search Windows Update refuses is check_failed with a fixed code and its HRESULT as a fact; the text only goes
// into the detail. A search that never got an answer says so in the same fact.
func TestCheckFailed(t *testing.T) {
	w, _, _ := newWinUpdate(true, out(`{"status":"failed","hresult":-2145107924,"error_type":`+
		`"System.Management.Automation.MethodInvocationException","detail":"Exception from HRESULT: 0x8024402C"}`))
	rep := w.Check(context.Background())
	if rep.State != protocol.SourceCheckFailed || rep.Code != protocol.CodeFailedWindowsUpdate ||
		rep.Facts[FactSearch] != SearchFailed || rep.Facts[FactHResult] != "0x8024402C" ||
		!strings.Contains(rep.Detail, "hresult 0x8024402C") || rep.Pending != 0 {
		t.Fatalf("report: %+v", rep)
	}
	for name, res := range map[string]runner.Result{
		"cannot run": {Err: errors.New("not found")},
		"exit code":  {ExitCode: 1, Stderr: []byte("boom")},
		"no json":    out("New-Object : something\n"),
		"odd status": out(`{"status":"what"}`),
	} {
		w, _, _ := newWinUpdate(true, res)
		rep := w.Check(context.Background())
		if rep.State != protocol.SourceCheckFailed || rep.Code != protocol.CodeFailedPackageManager ||
			rep.Facts[FactSearch] != SearchNoAnswer {
			t.Errorf("%s: %+v", name, rep)
		}
	}
}

func TestDetectWithoutPowerShell(t *testing.T) {
	w, _, _ := newWinUpdate(true)
	w.PowerShell = ""
	if w.Detect() != source.Absent {
		t.Fatal("present without PowerShell")
	}
}

func install(w *WinUpdate, scope protocol.Scope) (source.InstallResult, []source.Progress, bool) {
	var seen []source.Progress
	journaled := false
	res := w.Install(context.Background(), source.InstallRequest{Scope: scope,
		BeforeChange: func() error { journaled = true; return nil }}, time.Hour, func(p source.Progress) { seen = append(seen, p) })
	return res, seen, journaled
}

var (
	all      = protocol.Scope{Kind: protocol.ScopeAll}
	security = protocol.Scope{Kind: protocol.ScopeSecurity}
)

// lines plays a whole run: download and install of each update with the given result line fields.
func lines(results map[string]string) []string {
	var l []string
	for _, id := range []string{idNet, idOther} {
		if r, ok := results[id]; ok {
			l = append(l, `{"event":"download","id":"`+id+`"}`, `{"event":"start","id":"`+id+`"}`,
				`{"event":"result","id":"`+id+`",`+r+`}`)
		}
	}
	return append(l, `{"event":"done"}`)
}

const succeeded = `"stage":"install","result_code":2,"hresult":0`

// Scope security plans only the updates of the classification "Security Updates", with their progress, and the next
// search must no longer list them.
func TestInstallSecurity(t *testing.T) {
	w, r, l := newWinUpdate(true, out(foundTwo), out(searched(upOther)))
	l.lines = lines(map[string]string{idNet: succeeded})
	res, seen, journaled := install(w, security)
	if res.Code != protocol.CodeOK || res.Planned != 1 || !slices.Equal(res.Packages, []string{"KB5000001"}) || !journaled {
		t.Fatalf("result: %+v journaled %v", res, journaled)
	}
	if !strings.HasPrefix(l.script, "$VordrIds = @('"+idNet+"')\n") || !strings.Contains(l.script, "CreateUpdateInstaller") {
		t.Fatalf("install script: %q", l.script[:80])
	}
	for _, want := range []source.Progress{{Phase: protocol.PhaseDownloading, Percent: pctPlan, Item: "KB5000001"},
		{Phase: protocol.PhaseInstalling, Percent: pctDownload, Item: "KB5000001"},
		{Phase: protocol.PhaseVerifying, Percent: 100}} {
		if !slices.Contains(seen, want) {
			t.Errorf("progress %+v missing: %+v", want, seen)
		}
	}
	if len(r.scripts) != 2 || !strings.Contains(string(res.Log), `"event":"result"`) {
		t.Fatalf("searches %d, log %q", len(r.scripts), res.Log)
	}
}

func TestInstallAll(t *testing.T) {
	w, _, l := newWinUpdate(true, out(searched(upNet, upOther, upDefender, upFeature, upPreview, upWaiting)), out(foundNone))
	l.lines = lines(map[string]string{idNet: succeeded, idOther: succeeded})
	res, _, _ := install(w, all)
	if res.Code != protocol.CodeOK || !slices.Equal(res.Packages, []string{"KB5000001", "KB5000002"}) {
		t.Fatalf("result: %+v", res)
	}
	// feature upgrades, optional updates and updates waiting for a reboot are not part of the run
	if !strings.HasPrefix(l.script, "$VordrIds = @('"+idNet+"', '"+idOther+"')\n") {
		t.Fatalf("install script: %q", l.script[:100])
	}
}

// Apps belong to winget: this source refuses them without running anything.
func TestInstallRefusesApps(t *testing.T) {
	w, r, l := newWinUpdate(true, out(foundTwo))
	res, _, journaled := install(w, protocol.Scope{Kind: protocol.ScopeApp, App: "jq"})
	if res.Code != protocol.CodeRejectedUnsupported || journaled || len(r.scripts) != 0 || l.script != "" {
		t.Fatalf("app scope: %+v", res)
	}
}

func TestInstallPrechecks(t *testing.T) {
	w, r, _ := newWinUpdate(false, out(foundTwo))
	if res, _, journaled := install(w, all); res.Code != protocol.CodeRejectedUnsupported || journaled || len(r.scripts) != 0 {
		t.Fatalf("not elevated: %+v", res)
	}
	w, _, _ = newWinUpdate(true, out(foundTwo))
	w.Busy = func() bool { return true }
	if res, _, _ := install(w, all); res.Code != protocol.CodeRejectedBusy || !strings.Contains(res.Detail, "Windows Installer") {
		t.Fatalf("msi busy: %+v", res)
	}
	w, _, _ = newWinUpdate(true, out(foundTwo))
	w.FreeBytes = func() (uint64, bool) { return 1 << 30, true }
	if res, _, _ := install(w, all); res.Code != protocol.CodePrecheckDiskSpace || !strings.Contains(res.Detail, "1024 MiB") {
		t.Fatalf("disk: %+v", res)
	}
	w, _, l := newWinUpdate(true, out(strings.Replace(foundTwo, `"installer_busy":false`, `"installer_busy":true`, 1)))
	if res, _, journaled := install(w, all); res.Code != protocol.CodeRejectedBusy || journaled || l.script != "" ||
		!strings.Contains(res.Detail, "IsBusy") {
		t.Fatalf("Windows Update busy: %+v", res)
	}
	w, _, l = newWinUpdate(true, out(`{"status":"failed","hresult":-2145124322,"error_type":"x","detail":"y"}`))
	if res, _, journaled := install(w, all); res.Code != protocol.CodeFailedWindowsUpdate || journaled || l.script != "" ||
		!strings.Contains(res.Detail, "0x8024001E") {
		t.Fatalf("search failed: %+v", res)
	}
}

// Nothing applicable in the scope: ok with nothing planned, nothing journaled, nothing run.
func TestInstallNothingApplicable(t *testing.T) {
	w, _, l := newWinUpdate(true, out(searched(upDefender, upPreview, upFeature)))
	res, _, journaled := install(w, security)
	if res.Code != protocol.CodeOK || res.Planned != 0 || journaled || l.script != "" {
		t.Fatalf("security without security updates: %+v", res)
	}
}

func TestInstallResults(t *testing.T) {
	for name, tc := range map[string]struct {
		net, other string
		after      string
		want       protocol.Code
		detail     string
	}{
		"both succeeded": {succeeded, succeeded, foundNone, protocol.CodeOK, ""},
		"reboot": {`"stage":"install","result_code":2,"reboot_required":true`, succeeded, foundNone,
			protocol.CodeOKRebootRequired, ""},
		"succeeded with errors": {`"stage":"install","result_code":3,"hresult":-2145124320`, succeeded, foundNone, protocol.CodeOK,
			"KB5000001: succeeded with errors (3) at install, hresult 0x80240020"},
		"failed": {succeeded, `"stage":"install","result_code":4,"hresult":-2145124318`, foundNone, protocol.CodeFailedWindowsUpdate,
			"KB5000002: failed (4) at install, hresult 0x80240022"},
		"aborted": {`"stage":"install","result_code":5,"hresult":-2145124341`, succeeded, foundNone, protocol.CodeFailedWindowsUpdate,
			"KB5000001: aborted (5) at install, hresult 0x8024000B"},
		"both failed": {`"stage":"install","result_code":4,"hresult":-2145124318`, `"stage":"install","result_code":5,` +
			`"hresult":-2145124341`, foundNone, protocol.CodeFailedWindowsUpdate, "0x80240022\nKB5000002: aborted (5)"},
		"download failed": {`"stage":"download","result_code":4,"hresult":-2145107944`, succeeded, foundNone,
			protocol.CodeFailedWindowsUpdate, "KB5000001: failed (4) at download, hresult 0x80244018"},
		"thrown": {`"stage":"install","status":"exception","hresult":-2145124330,"error_type":"System.Runtime.InteropServices.COMException",` +
			`"detail":"x"`, succeeded, foundNone, protocol.CodeFailedWindowsUpdate,
			"KB5000001: exception at install, hresult 0x80240016 (System.Runtime.InteropServices.COMException: x)"},
		"never ended": {`"stage":"install","result_code":1`, succeeded, foundNone, protocol.CodeFailedWindowsUpdate,
			"KB5000001: in progress (1)"},
		"installed meanwhile": {`"status":"not_pending"`, succeeded, foundNone, protocol.CodeOK, ""},
		"waiting meanwhile":   {`"status":"not_pending","reboot_required":true`, succeeded, foundNone, protocol.CodeOKRebootRequired, ""},
		"still pending":       {succeeded, succeeded, searched(upOther), protocol.CodeFailedVerification, "still pending after the run: KB5000002 2023-11 Update"},
		"waits for reboot": {succeeded, succeeded, searched(strings.Replace(upOther, `"reboot_required":false`, `"reboot_required":true`, 1)),
			protocol.CodeOKRebootRequired, ""},
		"verification search failed": {succeeded, succeeded, `{"status":"failed","hresult":-2145107924}`, protocol.CodeFailedVerification,
			"verification: hresult 0x8024402C"},
	} {
		w, _, l := newWinUpdate(true, out(foundTwo), out(tc.after))
		l.lines = lines(map[string]string{idNet: tc.net, idOther: tc.other})
		res, _, _ := install(w, all)
		if res.Code != tc.want || !strings.Contains(res.Detail, tc.detail) || res.Planned != 2 {
			t.Errorf("%s: %s %q", name, res.Code, res.Detail)
		}
	}
}

// What the install script finds before it changes anything: Windows Update busy, or the search refused.
func TestInstallScriptRefuses(t *testing.T) {
	for name, tc := range map[string]struct {
		line   string
		want   protocol.Code
		detail string
	}{
		"busy":          {`{"event":"busy"}`, protocol.CodeRejectedBusy, "IsBusy"},
		"search failed": {`{"event":"search_failed","hresult":-2145107924,"error_type":"x","detail":"y"}`, protocol.CodeFailedWindowsUpdate, "0x8024402C"},
	} {
		w, r, l := newWinUpdate(true, out(foundTwo), out(foundNone))
		l.lines = []string{tc.line}
		res, _, _ := install(w, all)
		if res.Code != tc.want || !strings.Contains(res.Detail, tc.detail) || len(r.scripts) != 1 {
			t.Errorf("%s: %+v, %d searches", name, res, len(r.scripts))
		}
	}
}

// A run that ends without a result for an update (PowerShell died) is no success.
func TestInstallMissingResult(t *testing.T) {
	w, _, l := newWinUpdate(true, out(foundTwo), out(foundNone))
	l.lines = lines(map[string]string{idNet: succeeded})
	if res, _, _ := install(w, all); res.Code != protocol.CodeFailedPackageManager || !strings.Contains(res.Detail, "KB5000002: no result") {
		t.Fatalf("missing result: %+v", res)
	}
}

func TestJournalNotWritten(t *testing.T) {
	w, _, l := newWinUpdate(true, out(foundTwo))
	res := w.Install(context.Background(), source.InstallRequest{Scope: all,
		BeforeChange: func() error { return errors.New("disk full") }}, time.Hour, func(source.Progress) {})
	if res.Code != protocol.CodeFailedPackageManager || l.script != "" {
		t.Fatalf("no journal: %+v", res)
	}
}

func TestWithIDsQuotes(t *testing.T) {
	if got := withIDs([]string{"a", "c'd"}); !strings.HasPrefix(got, "$VordrIds = @('a', 'c''d')\n") {
		t.Fatalf("withIDs: %q", got[:40])
	}
}
