// An external test: the hub reads winget's ID pattern through the policy, so a test inside the package could not
// import the hub.
package winget_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/hub"
	"github.com/psewar/vordr/internal/runner"
	"github.com/psewar/vordr/internal/source"
	"github.com/psewar/vordr/internal/source/winget"
	"github.com/psewar/vordr/internal/wire"
)

// answers plays the list script's outputs in turn.
type answers struct{ outputs []runner.Result }

func (a *answers) Run(context.Context, []string, string, ...string) runner.Result {
	if len(a.outputs) == 0 {
		return runner.Result{Err: errors.New("no more answers")}
	}
	r := a.outputs[0]
	a.outputs = a.outputs[1:]
	return r
}

func out(s string) runner.Result { return runner.Result{Stdout: []byte(s)} }

// What winget reports reaches the hub whole (ADR-0195), in the cases its own tests play: upgrades with one the source
// leaves out and one of an unknown installed version, none, the client missing, a failure with PowerShell's text, the
// service without PowerShell 7, a script that did not run or answer.
func TestTheHubTakesWhatWingetReports(t *testing.T) {
	const list = `{"status":"ok","winget_version":"v1.29.380","client_version":"1.29.380","user":"bob","packages":[` +
		`{"id":"jqlang.jq","name":"jq","installed":"1.7.0","available":"1.7.1","source":"winget"},` +
		`{"id":"OpenJS.NodeJS.LTS","name":"Node.js (LTS)","installed":"20.11.1","available":"22.2.0","source":"winget"},` +
		`{"id":"Example.Launcher","name":"Example Launcher","installed":"Unknown","available":"2.0.0.1000","source":"winget"},` +
		`{"id":"bad id; rm","name":"x","installed":"1","available":"2","source":"winget"}]}`
	for name, c := range map[string]struct {
		res    runner.Result
		system bool
	}{
		"upgrades": {out("WARNING: something\n" + list + "\n"), false},
		"none": {out(`{"status":"ok","winget_version":"v1.29.380","client_version":"1.29.380","user":"bob",` +
			`"packages":[]}`), false},
		"client missing": {out(`{"status":"client_missing","user":"SYSTEM"}`), false},
		"failed": {out(`{"status":"failed","error_type":"System.Runtime.InteropServices.COMException",` +
			`"detail":"No connection could be made\r\n\tat Microsoft.WinGet"}`), false},
		"the service without PowerShell 7": {runner.Result{}, true},
		"cannot run":                       {runner.Result{Err: errors.New("not found")}, false},
		"exit code":                        {runner.Result{ExitCode: 1, Stderr: []byte("boom")}, false},
		"no json":                          {out("Get-WinGetPackage : something\n"), false},
	} {
		w := &winget.Winget{Run: &answers{outputs: []runner.Result{c.res}},
			PowerShell: `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`, Elevated: true, System: c.system,
			Now:       func() time.Time { return time.Date(2026, 10, 30, 2, 0, 0, 0, time.UTC) },
			FreeBytes: func() (uint64, bool) { return 50 << 30, true }, Busy: func() bool { return false },
			Pending: func() source.Reboot { return source.Reboot{Required: true, Checked: []string{"windows_update"}} }}
		reboot := w.Reboot()
		rep := wire.Report{T: wire.TReport, CheckedAt: w.Now(), Sources: []source.Report{w.Check(context.Background())},
			Reboot: &reboot}
		if miss := hub.ReportNotTaken(rep); miss != nil {
			t.Errorf("%s: the hub does not take %v", name, miss)
		}
	}
}
