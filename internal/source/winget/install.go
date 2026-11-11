package winget

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/source"
)

// Progress of a run in percent of the whole: prechecks and plan up to pctPlan, the packages up to pctInstall, the
// verification the rest. winget downloads and installs one package after the other, so there is no download phase.
const (
	pctPlan       = 5
	pctInstall    = 95
	verifyTimeout = 10 * time.Minute

	// installerBusyCode: ERROR_INSTALL_ALREADY_RUNNING, the exit code of an MSI that found another installation going.
	installerBusyCode = 1618
)

type upgradeEvent struct {
	Event              string `json:"event"`
	ID                 string `json:"id"`
	Status             string `json:"status"`
	RebootRequired     bool   `json:"reboot_required"`
	InstallerErrorCode int64  `json:"installer_error_code"`
	ExtendedErrorCode  int64  `json:"extended_error_code"`
	ErrorType          string `json:"error_type"`
	Detail             string `json:"detail"`
}

// Install carries out one install run: prechecks, a plan from winget's list for the scope, the upgrades one after the
// other with winget's status per package, then the verification against a fresh list. Scope security finds nothing:
// winget does not classify updates.
//
// ctx bounds the run until the first package is being upgraded. An installer is never stopped half way, so the
// upgrades themselves only end at installTimeout.
func (w *Winget) Install(ctx context.Context, req source.InstallRequest, installTimeout time.Duration,
	progress func(source.Progress)) source.InstallResult {
	say := func(ph protocol.Phase, pct int, item string) {
		progress(source.Progress{Phase: ph, Percent: pct, Item: item})
	}
	timedOut := source.InstallResult{Code: protocol.CodeTimeout, Detail: "install.timeout_min reached before installing"}

	say(protocol.PhasePrechecking, 0, "")
	switch {
	case w.needsPowerShell7():
		return source.InstallResult{Code: protocol.CodeRejectedUnsupported, Detail: needsPowerShell7Detail}
	case !w.Elevated && req.Scope.Kind != protocol.ScopeApp:
		// Without elevation every machine-wide installer would ask through UAC, and nobody is there to answer.
		return source.InstallResult{Code: protocol.CodeRejectedUnsupported,
			Detail: "not elevated: only apps that agent.yaml names one by one can be upgraded (run the agent as a service)"}
	case w.Busy():
		return source.InstallResult{Code: protocol.CodeRejectedBusy, Detail: "Windows Installer is in another installation"}
	}
	if free, known := w.FreeBytes(); known && free < MinFree {
		return source.InstallResult{Code: protocol.CodePrecheckDiskSpace,
			Detail: "system drive: " + mib(free) + " free, a run needs " + mib(MinFree)}
	}
	l, res, code := w.list(ctx)
	switch {
	case ctx.Err() != nil:
		return timedOut
	case code != "":
		return source.InstallResult{Code: code, Detail: detail(l, res)}
	}
	var plan []string
	for _, p := range l.Packages {
		if !IDPattern.MatchString(p.ID) || unknownVersion(p.Installed) {
			continue
		}
		if req.Scope.Kind == protocol.ScopeAll || (req.Scope.Kind == protocol.ScopeApp && strings.EqualFold(p.ID, req.Package)) {
			plan = append(plan, p.ID)
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
	result := source.InstallResult{Planned: len(plan), Packages: plan}

	results := map[string]upgradeEvent{}
	var log strings.Builder
	started, clientMissing := 0, false
	onLine := func(line string) {
		log.WriteString(line + "\n")
		var ev upgradeEvent
		if json.Unmarshal([]byte(line), &ev) != nil {
			return
		}
		switch ev.Event {
		case "start":
			say(protocol.PhaseInstalling, pctPlan+(pctInstall-pctPlan)*started/len(plan), ev.ID)
			started++
		case "result":
			results[strings.ToLower(ev.ID)] = ev
		case "client_missing":
			clientMissing = true
		}
	}
	runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), installTimeout)
	up := w.Lines.Lines(runCtx, onLine, w.PowerShell, w.args(withIDs(plan))...)
	ranOut := runCtx.Err() == context.DeadlineExceeded
	cancel()
	if len(up.Stderr) > 0 {
		log.WriteString(string(up.Stderr))
	}
	result.Log = []byte(log.String())
	switch {
	case clientMissing:
		result.Code, result.Detail = protocol.CodeFailedPackageManager, detail(listed{Status: "client_missing", User: l.User}, up)
		return result
	case ranOut && len(results) < len(plan):
		result.Code, result.Detail = protocol.CodeTimeout, "install timeout reached while upgrading"
		return result
	case !up.Ran():
		result.Code, result.Detail = protocol.CodeFailedPackageManager, up.Err.Error()
		return result
	}

	reboot := false
	var failures []string
	upgraded := 0
	for _, id := range plan {
		ev, ok := results[strings.ToLower(id)]
		if !ok {
			failures = append(failures, id+": no result")
			result.Code = first(result.Code, protocol.CodeFailedPackageManager)
			continue
		}
		reboot = reboot || ev.RebootRequired
		if c := statusCode(ev); c != protocol.CodeOK {
			result.Code = first(result.Code, c)
			failures = append(failures, describe(ev))
		} else if ev.Status == "Ok" { // NoApplicableUpgrade changed nothing
			upgraded++
		}
	}
	// A precheck code says "nothing changed". Once other packages of the run went through, one package that could not
	// even be downloaded is a failure of the run (five upgraded, one CatalogError).
	if upgraded > 0 && strings.HasPrefix(string(result.Code), "precheck_") {
		result.Code = protocol.CodeFailedPackageManager
	}
	if result.Code != "" {
		result.Detail = clip(strings.Join(failures, "\n"))
		return result
	}

	say(protocol.PhaseVerifying, pctInstall, "")
	vctx, vcancel := context.WithTimeout(context.WithoutCancel(ctx), verifyTimeout)
	after, vres, code := w.list(vctx)
	vcancel()
	if code != "" {
		result.Code, result.Detail = protocol.CodeFailedVerification, "verification: "+detail(after, vres)
		return result
	}
	var still []string
	for _, p := range after.Packages {
		for _, id := range plan {
			if strings.EqualFold(p.ID, id) {
				still = append(still, p.ID+" "+p.Installed+" -> "+p.Available)
			}
		}
	}
	if len(still) > 0 {
		result.Code, result.Detail = protocol.CodeFailedVerification, "still pending after the run: "+strings.Join(still, ", ")
		return result
	}
	say(protocol.PhaseVerifying, 100, "")
	result.Code = protocol.CodeOK
	if reboot {
		result.Code = protocol.CodeOKRebootRequired
	}
	return result
}

// RebootRequired: what Windows records about a pending reboot.
func (w *Winget) RebootRequired() bool { return w.Pending().Required }

// statusCode maps winget's PSInstallResultStatus to a result code.
func statusCode(ev upgradeEvent) protocol.Code {
	switch ev.Status {
	case "Ok", "NoApplicableUpgrade":
		return protocol.CodeOK
	case "DownloadError", "CatalogError":
		return protocol.CodePrecheckRepoUnreachable // nothing of this package was installed
	case "InstallError":
		if ev.InstallerErrorCode == installerBusyCode {
			return protocol.CodeRejectedBusy
		}
	}
	return protocol.CodeFailedPackageManager
}

func describe(ev upgradeEvent) string {
	s := ev.ID + ": " + ev.Status
	if ev.InstallerErrorCode != 0 {
		s += ", installer exit code " + strconv.FormatInt(ev.InstallerErrorCode, 10)
	}
	if ev.ExtendedErrorCode != 0 {
		s += ", " + fmtCode(ev.ExtendedErrorCode)
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
