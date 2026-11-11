package winupdate

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/source"
	"github.com/psewar/vordr/internal/source/winget"
)

// Progress of a run in percent of the whole, as for apt (design 10.2): prechecks and plan up to pctPlan, the
// downloads up to pctDownload, the installations up to pctInstall, the verification the rest.
const (
	pctPlan     = 5
	pctDownload = 35
	pctInstall  = 95
)

// OperationResultCode of the Windows Update Agent (orc*), per update.
const (
	orcNotStarted         = 0
	orcInProgress         = 1
	orcSucceeded          = 2
	orcSucceededWithError = 3
	orcFailed             = 4
	orcAborted            = 5
)

// Status values of a result line besides Windows Update's own result code.
const (
	statusException  = "exception"   // a COM call threw; hresult and the text come with it
	statusNotPending = "not_pending" // Windows Update no longer offers it: installed meanwhile or waiting for a reboot
)

type installEvent struct {
	Event          string `json:"event"`
	ID             string `json:"id"`
	Stage          string `json:"stage"` // download or install
	Status         string `json:"status"`
	ResultCode     int    `json:"result_code"`
	HResult        int64  `json:"hresult"`
	RebootRequired bool   `json:"reboot_required"`
	ErrorType      string `json:"error_type"`
	Detail         string `json:"detail"`
}

// Install carries out one install run: prechecks, a plan from a fresh search for the scope (security: the
// classification "Security Updates"; all: every update the check counts), download and installation one update after
// the other with Windows Update's result per update, then the verification against another search. Apps belong to
// winget; this source refuses them.
//
// ctx bounds the run until the install script starts. An installation is never stopped half way, so the script
// itself only ends at installTimeout.
func (u *WinUpdate) Install(ctx context.Context, req source.InstallRequest, installTimeout time.Duration,
	progress func(source.Progress)) source.InstallResult {
	say := func(ph protocol.Phase, pct int, item string) {
		progress(source.Progress{Phase: ph, Percent: pct, Item: item})
	}
	timedOut := source.InstallResult{Code: protocol.CodeTimeout, Detail: "install.timeout_min reached before installing"}

	say(protocol.PhasePrechecking, 0, "")
	switch {
	case req.Scope.Kind != protocol.ScopeSecurity && req.Scope.Kind != protocol.ScopeAll:
		return source.InstallResult{Code: protocol.CodeRejectedUnsupported,
			Detail: "Windows Update installs the scopes security and all; apps are upgraded by winget"}
	case !u.Elevated:
		return source.InstallResult{Code: protocol.CodeRejectedUnsupported,
			Detail: "not elevated: Windows Update installs only as administrator or as the service (LocalSystem)"}
	case u.Busy():
		return source.InstallResult{Code: protocol.CodeRejectedBusy, Detail: "Windows Installer is in another installation"}
	}
	if free, known := u.FreeBytes(); known && free < winget.MinFree {
		return source.InstallResult{Code: protocol.CodePrecheckDiskSpace,
			Detail: "system drive: " + mib(free) + " free, a run needs " + mib(winget.MinFree)}
	}
	f, res, code := u.search(ctx)
	switch {
	case ctx.Err() != nil:
		return timedOut
	case code != "":
		return source.InstallResult{Code: code, Detail: detail(f, res)}
	case f.InstallerBusy:
		return source.InstallResult{Code: protocol.CodeRejectedBusy, Detail: "Windows Update is installing (IUpdateInstaller.IsBusy)"}
	}
	var plan []update
	for _, up := range classify(f).pending {
		if req.Scope.Kind == protocol.ScopeAll || up.in(ClassSecurity) {
			plan = append(plan, up)
		}
	}
	if len(plan) == 0 {
		say(protocol.PhaseVerifying, 100, "")
		return source.InstallResult{Code: protocol.CodeOK} // nothing in this scope is pending
	}
	if req.BeforeChange != nil {
		if err := req.BeforeChange(); err != nil {
			// Without the journal an interrupted run would go unnoticed; better not start.
			return source.InstallResult{Code: protocol.CodeFailedPackageManager, Detail: "run journal not written: " + err.Error()}
		}
	}
	ids := make([]string, len(plan))
	names := map[string]string{}
	result := source.InstallResult{Planned: len(plan)}
	for i, up := range plan {
		ids[i] = up.ID
		names[strings.ToLower(up.ID)] = up.name()
		result.Packages = append(result.Packages, up.name())
	}

	results := map[string]installEvent{}
	var searchFailed *installEvent
	var log strings.Builder
	downloads, started, busy := 0, 0, false
	onLine := func(line string) {
		log.WriteString(line + "\n")
		var ev installEvent
		if json.Unmarshal([]byte(line), &ev) != nil {
			return
		}
		switch ev.Event {
		case "download":
			say(protocol.PhaseDownloading, pctPlan+(pctDownload-pctPlan)*downloads/len(plan), names[strings.ToLower(ev.ID)])
			downloads++
		case "start":
			say(protocol.PhaseInstalling, pctDownload+(pctInstall-pctDownload)*started/len(plan), names[strings.ToLower(ev.ID)])
			started++
		case "result":
			results[strings.ToLower(ev.ID)] = ev
		case "busy":
			busy = true
		case "search_failed":
			searchFailed = &ev
		}
	}
	runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), installTimeout)
	run := u.Lines.Lines(runCtx, onLine, u.PowerShell, winget.ScriptArgs(withIDs(ids))...)
	ranOut := runCtx.Err() == context.DeadlineExceeded
	cancel()
	if len(run.Stderr) > 0 {
		log.WriteString(string(run.Stderr))
	}
	result.Log = []byte(log.String())
	switch {
	case busy:
		result.Code, result.Detail = protocol.CodeRejectedBusy, "Windows Update is installing (IUpdateInstaller.IsBusy)"
		return result
	case searchFailed != nil:
		result.Code = protocol.CodeFailedWindowsUpdate
		result.Detail = clip("search before installing: hresult " + fmtCode(searchFailed.HResult) + " (" +
			searchFailed.ErrorType + ": " + searchFailed.Detail + ")")
		return result
	case ranOut && len(results) < len(plan):
		result.Code, result.Detail = protocol.CodeTimeout, "install timeout reached while installing"
		return result
	case !run.Ran():
		result.Code, result.Detail = protocol.CodeFailedPackageManager, run.Err.Error()
		return result
	}

	reboot := false
	var failures, notes []string
	for _, up := range plan {
		ev, ok := results[strings.ToLower(up.ID)]
		if !ok {
			failures = append(failures, up.name()+": no result")
			result.Code = first(result.Code, protocol.CodeFailedPackageManager)
			continue
		}
		reboot = reboot || ev.RebootRequired
		switch c := resultCode(ev); {
		case c != protocol.CodeOK:
			result.Code = first(result.Code, c)
			failures = append(failures, describe(up.name(), ev))
		case ev.ResultCode == orcSucceededWithError:
			notes = append(notes, describe(up.name(), ev))
		}
	}
	if result.Code != "" {
		result.Detail = clip(strings.Join(failures, "\n"))
		return result
	}

	say(protocol.PhaseVerifying, pctInstall, "")
	vctx, vcancel := context.WithTimeout(context.WithoutCancel(ctx), searchTimeout)
	after, vres, code := u.search(vctx)
	vcancel()
	if code != "" {
		result.Code, result.Detail = protocol.CodeFailedVerification, "verification: "+detail(after, vres)
		return result
	}
	var still []string
	for _, up := range after.Updates {
		for _, p := range plan {
			switch {
			case !strings.EqualFold(up.ID, p.ID):
			case up.RebootRequired:
				reboot = true // installed, waits for the reboot
			default:
				still = append(still, p.name()+" "+p.Title)
			}
		}
	}
	if len(still) > 0 {
		result.Code, result.Detail = protocol.CodeFailedVerification, "still pending after the run: "+strings.Join(still, ", ")
		return result
	}
	say(protocol.PhaseVerifying, 100, "")
	result.Code, result.Detail = protocol.CodeOK, clip(strings.Join(notes, "\n"))
	if reboot {
		result.Code = protocol.CodeOKRebootRequired
	}
	return result
}

// RebootRequired: what Windows records about a pending reboot.
func (u *WinUpdate) RebootRequired() bool { return u.Pending().Required }

// resultCode maps Windows Update's result for one update to a result code. "Succeeded with errors" counts as
// succeeded; the verification decides whether the update is still pending.
func resultCode(ev installEvent) protocol.Code {
	switch {
	case ev.Status == statusNotPending:
		return protocol.CodeOK
	case ev.Status == statusException:
		return protocol.CodeFailedWindowsUpdate
	case ev.ResultCode == orcSucceeded, ev.ResultCode == orcSucceededWithError:
		return protocol.CodeOK
	}
	return protocol.CodeFailedWindowsUpdate // failed, aborted, or never came to an end
}

var resultNames = map[int]string{orcNotStarted: "not started", orcInProgress: "in progress", orcSucceeded: "succeeded",
	orcSucceededWithError: "succeeded with errors", orcFailed: "failed", orcAborted: "aborted"}

func describe(name string, ev installEvent) string {
	s := name + ": "
	switch n, known := resultNames[ev.ResultCode]; {
	case ev.Status == statusException:
		s += "exception"
	case known:
		s += n + " (" + strconv.Itoa(ev.ResultCode) + ")"
	default:
		s += "result code " + strconv.Itoa(ev.ResultCode)
	}
	if ev.Stage != "" {
		s += " at " + ev.Stage
	}
	if ev.HResult != 0 {
		s += ", hresult " + fmtCode(ev.HResult)
	}
	if ev.Detail != "" {
		s += " (" + ev.ErrorType + ": " + ev.Detail + ")"
	}
	return s
}

// first keeps the first failure of a run: it says best where the run went wrong.
func first(have, next protocol.Code) protocol.Code {
	if have != "" {
		return have
	}
	return next
}

func mib(b uint64) string { return strconv.FormatUint(b>>20, 10) + " MiB" }
