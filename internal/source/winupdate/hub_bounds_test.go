package winupdate

import (
	"context"
	"errors"
	"testing"

	"github.com/psewar/vordr/internal/hub"
	"github.com/psewar/vordr/internal/runner"
	"github.com/psewar/vordr/internal/source"
	"github.com/psewar/vordr/internal/wire"
)

// What Windows Update reports reaches the hub whole (ADR-0195): the search with every kind of update these tests
// know, nothing found, a search Windows refused with its HRESULT and text, and one that never answered.
func TestTheHubTakesWhatWindowsUpdateReports(t *testing.T) {
	for name, res := range map[string]runner.Result{
		"updates": out("WARNING: something\n" +
			searched(upNet, upDefender, upNoKB, upFeature, upPreview, upWaiting, upBadID, upOther) + "\n"),
		"nothing found": out(foundNone),
		"refused": out(`{"status":"failed","hresult":-2145107924,"error_type":` +
			`"System.Management.Automation.MethodInvocationException","detail":"Exception from HRESULT: 0x8024402C\r\n` +
			`\tat System.Management.Automation.MethodInvoker"}`),
		"cannot run": {Err: errors.New("not found")},
		"exit code":  {ExitCode: 1, Stderr: []byte("boom\r\nat line 1")},
		"no json":    out("New-Object : something\n"),
	} {
		w, _, _ := newWinUpdate(true, res)
		reboot := w.Pending()
		rep := wire.Report{T: wire.TReport, CheckedAt: w.Now(), Sources: []source.Report{w.Check(context.Background())},
			Reboot: &reboot}
		if miss := hub.ReportNotTaken(rep); miss != nil {
			t.Errorf("%s: the hub does not take %v", name, miss)
		}
	}
}
