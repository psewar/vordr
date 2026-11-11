package agent

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/source"
)

// fakeSource is one Windows source: it journals like a real one (BeforeChange before it changes anything), reports
// progress from 0 to 100 and returns its preset result.
type fakeSource struct {
	name   string
	result source.InstallResult
	calls  *[]string
}

func (f fakeSource) Install(_ context.Context, req source.InstallRequest, _ time.Duration,
	progress func(source.Progress)) source.InstallResult {
	*f.calls = append(*f.calls, f.name+" "+req.Scope.String())
	progress(source.Progress{Phase: protocol.PhasePrechecking, Percent: 0})
	if f.result.Planned > 0 && req.BeforeChange != nil {
		if err := req.BeforeChange(); err != nil {
			return source.InstallResult{Code: protocol.CodeFailedPackageManager}
		}
	}
	progress(source.Progress{Phase: protocol.PhaseVerifying, Percent: 100})
	return f.result
}

func fakeWindows(update, winget source.InstallResult) (windowsInstaller, *[]string) {
	calls := &[]string{}
	return windowsInstaller{update: fakeSource{"winupdate", update, calls}, winget: fakeSource{"winget", winget, calls},
		pending: func() bool { return false }}, calls
}

func runWindows(i windowsInstaller, scope string) (source.InstallResult, []int, int) {
	sc, _ := protocol.ParseScope(scope)
	journals := 0
	var percents []int
	res := i.Install(context.Background(), source.InstallRequest{Scope: sc, BeforeChange: func() error { journals++; return nil }},
		time.Hour, func(p source.Progress) { percents = append(percents, p.Percent) })
	return res, percents, journals
}

var (
	updOK = source.InstallResult{Code: protocol.CodeOK, Planned: 2, Packages: []string{"KB5000001", "KB5000002"},
		Log: []byte("wu\n")}
	wingetOK = source.InstallResult{Code: protocol.CodeOK, Planned: 1, Packages: []string{"jqlang.jq"}, Log: []byte("wg\n")}
)

// Security updates come from Windows Update only: winget does not classify.
func TestWindowsSecurityIsWindowsUpdateOnly(t *testing.T) {
	i, calls := fakeWindows(updOK, wingetOK)
	res, percents, journals := runWindows(i, "security")
	if !slices.Equal(*calls, []string{"winupdate security"}) || res.Code != protocol.CodeOK || res.Planned != 2 ||
		journals != 1 || !slices.Equal(percents, []int{0, 100}) || string(res.Log) != "wu\n" {
		t.Fatalf("calls %v, result %+v, journals %d, progress %v", *calls, res, journals, percents)
	}
}

// All: Windows Update first, then winget; one journal, both summaries, the progress shared between them.
func TestWindowsAllRunsBoth(t *testing.T) {
	i, calls := fakeWindows(updOK, wingetOK)
	res, percents, journals := runWindows(i, "all")
	if !slices.Equal(*calls, []string{"winupdate all", "winget all"}) || res.Code != protocol.CodeOK {
		t.Fatalf("calls %v, result %+v", *calls, res)
	}
	if res.Planned != 3 || !slices.Equal(res.Packages, []string{"KB5000001", "KB5000002", "jqlang.jq"}) || journals != 1 {
		t.Fatalf("summary: %+v, journals %d", res, journals)
	}
	if !slices.Equal(percents, []int{0, 50, 50, 100}) {
		t.Fatalf("progress: %v", percents)
	}
	if string(res.Log) != "== winupdate ==\nwu\n== winget ==\nwg\n" {
		t.Fatalf("log: %q", res.Log)
	}
}

// app:<id> is winget's alone: Windows Update is never asked.
func TestWindowsAppIsWingetOnly(t *testing.T) {
	i, calls := fakeWindows(updOK, wingetOK)
	res, _, _ := runWindows(i, "app:jq")
	if !slices.Equal(*calls, []string{"winget app:jq"}) || res.Code != protocol.CodeOK || res.Planned != 1 {
		t.Fatalf("calls %v, result %+v", *calls, res)
	}
}

func TestWindowsResult(t *testing.T) {
	failedWU := source.InstallResult{Code: protocol.CodeFailedWindowsUpdate, Planned: 1, Packages: []string{"KB5000001"},
		Detail: "KB5000001: failed (4) at install, hresult 0x80240022"}
	failedWinget := source.InstallResult{Code: protocol.CodeFailedPackageManager, Planned: 1, Packages: []string{"jqlang.jq"},
		Detail: "jqlang.jq: InstallError"}
	reboot := updOK
	reboot.Code = protocol.CodeOKRebootRequired
	for name, tc := range map[string]struct {
		update, winget source.InstallResult
		want           protocol.Code
		detail         string
	}{
		"both ok":            {updOK, wingetOK, protocol.CodeOK, ""},
		"windows reboot":     {reboot, wingetOK, protocol.CodeOKRebootRequired, ""},
		"winget reboot":      {updOK, source.InstallResult{Code: protocol.CodeOKRebootRequired}, protocol.CodeOKRebootRequired, ""},
		"windows failed":     {failedWU, wingetOK, protocol.CodeFailedWindowsUpdate, "winupdate: KB5000001: failed (4)"},
		"winget failed":      {reboot, failedWinget, protocol.CodeFailedPackageManager, "winget: jqlang.jq: InstallError"},
		"first failure wins": {failedWU, failedWinget, protocol.CodeFailedWindowsUpdate, "0x80240022\nwinget: jqlang.jq"},
		"winget timed out": {updOK, source.InstallResult{Code: protocol.CodeTimeout, Detail: "install.timeout_min reached"},
			protocol.CodeTimeout, "winget: install.timeout_min"},
		"windows busy": {source.InstallResult{Code: protocol.CodeRejectedBusy}, wingetOK, protocol.CodeRejectedBusy, ""},
	} {
		i, calls := fakeWindows(tc.update, tc.winget)
		res, _, _ := runWindows(i, "all")
		if res.Code != tc.want || !strings.Contains(res.Detail, tc.detail) || len(*calls) != 2 {
			t.Errorf("%s: %s %q, calls %v", name, res.Code, res.Detail, *calls)
		}
	}
}

// A journal that cannot be written stops both parts the same way: nothing changes without it.
func TestWindowsJournalOnceAndItsFailure(t *testing.T) {
	i, _ := fakeWindows(updOK, wingetOK)
	sc, _ := protocol.ParseScope("all")
	tries := 0
	res := i.Install(context.Background(), source.InstallRequest{Scope: sc, BeforeChange: func() error {
		tries++
		return context.Canceled
	}}, time.Hour, func(source.Progress) {})
	if res.Code != protocol.CodeFailedPackageManager || tries != 1 {
		t.Fatalf("result %+v, journal tried %d times", res, tries)
	}
}

func TestWindowsWithoutWindowsUpdate(t *testing.T) {
	calls := &[]string{}
	i := windowsInstaller{winget: fakeSource{"winget", wingetOK, calls}, pending: func() bool { return true }}
	if res, _, _ := runWindows(i, "security"); res.Code != protocol.CodeRejectedUnsupported || len(*calls) != 0 {
		t.Fatalf("security without Windows Update: %+v", res)
	}
	if !i.RebootRequired(context.Background()) {
		t.Fatal("reboot: Windows' own answer")
	}
}
