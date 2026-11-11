package apt

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/runner"
	"github.com/psewar/vordr/internal/source"
)

// Progress of a run in percent of the whole: prechecks and plan up to pctPrecheck, the download up to pctDownload,
// installing up to pctInstall, the verification the rest.
const (
	pctPrecheck = 5
	pctDownload = 35
	pctInstall  = 95

	downloadMargin = 100 << 20 // free space beyond the download itself in the package cache
	bootMinFree    = 100 << 20 // a kernel update writes a kernel and an initramfs to /boot
	verifyTimeout  = 10 * time.Minute
)

// Install carries out one install run (design/architecture.md, 10.2): prechecks, a plan from apt's simulation,
// download and installation with progress from APT::Status-Fd, then the verification. Like Check it decides on exit
// codes, simulation lines and the state it asks for after a failure; tool output only goes into Detail.
//
// ctx bounds the run until packages are being installed. dpkg is never stopped half way - that would leave the
// system half updated - so the installation itself only ends at installTimeout.
func (a *Apt) Install(ctx context.Context, req source.InstallRequest, installTimeout time.Duration,
	progress func(source.Progress)) source.InstallResult {
	say := func(ph protocol.Phase, pct int, item string) {
		progress(source.Progress{Phase: ph, Percent: pct, Item: item})
	}
	fail := func(code protocol.Code, res runner.Result) source.InstallResult {
		return source.InstallResult{Code: code, Detail: excerpt(res)}
	}
	timedOut := source.InstallResult{Code: protocol.CodeTimeout, Detail: "install.timeout_min reached before installing"}

	say(protocol.PhasePrechecking, 0, "")
	if a.busy() {
		return source.InstallResult{Code: protocol.CodeRejectedBusy, Detail: "another package manager holds the lock"}
	}
	if res, broken := a.audit(ctx); broken {
		return fail(protocol.CodePrecheckPackageDBBroken, res)
	}
	upd := a.Run.Run(ctx, env, "apt-get", updateArgs(a.aptVersion(ctx))...)
	switch {
	case ctx.Err() != nil:
		return timedOut
	case !upd.Ran():
		return fail(protocol.CodeFailedPackageManager, upd)
	case upd.ExitCode != 0 && a.busy():
		return fail(protocol.CodeRejectedBusy, upd)
	case upd.ExitCode != 0:
		return fail(protocol.CodePrecheckRepoUnreachable, upd)
	}

	t, res, code := a.target(ctx, req)
	switch {
	case code != "" && ctx.Err() != nil:
		return timedOut // a security run may simulate once per update: the time can run out while choosing
	case code != "":
		return fail(code, res)
	}
	held := heldDetail(t.held) // said on every ok: the updates stay pending, and why
	if t.args == nil {
		say(protocol.PhaseVerifying, 100, "")
		// nothing in this scope is pending, or a security run held back all of it
		return source.InstallResult{Code: protocol.CodeOK, Detail: held}
	}
	target := t.args
	var sim runner.Result
	if t.sim != nil {
		sim = *t.sim // choosing the target simulated exactly it
	} else {
		sim = a.Run.Run(ctx, env, "apt-get", append([]string{"-s", "-q"}, target...)...)
	}
	switch {
	case (!sim.Ran() || sim.ExitCode != 0) && ctx.Err() != nil:
		return timedOut
	case !sim.Ran() || sim.ExitCode != 0:
		return fail(a.classifyFailure(ctx), sim)
	}
	plan, removals := ParseSimulation(sim.Stdout)
	replaced, real := a.replacements(ctx, plan, removals)
	if len(real) > 0 {
		detail := "the upgrade would remove: " + strings.Join(real, ", ")
		if len(replaced) > 0 {
			detail += " (replaced by the run: " + strings.Join(replaced, ", ") + ")"
		}
		return source.InstallResult{Code: protocol.CodePrecheckWouldRemove, Detail: detail}
	}
	if len(plan) == 0 {
		say(protocol.PhaseVerifying, 100, "")
		return source.InstallResult{Code: protocol.CodeOK, Detail: held}
	}
	if detail := a.lacksSpace(ctx, target, plan); detail != "" {
		return source.InstallResult{Code: protocol.CodePrecheckDiskSpace, Detail: detail}
	}
	result := source.InstallResult{Planned: len(plan), Replaced: replaced}
	for _, p := range plan {
		result.Packages = append(result.Packages, p.Name)
	}

	say(protocol.PhaseDownloading, pctPrecheck, "")
	var pmErrors []string
	name, args := a.withIOPriority(append([]string{"-q", "-y", "-d", "-o", "APT::Status-Fd=3"}, target...))
	dl := a.Stream.Stream(ctx, env, statusLines(protocol.PhaseDownloading, pctPrecheck, pctDownload, say, &pmErrors),
		name, args...)
	result.Log = runLog(nil, dl)
	switch {
	case ctx.Err() != nil:
		timedOut.Planned = len(plan)
		return timedOut
	case !dl.Ran():
		result.Code, result.Detail = protocol.CodeFailedPackageManager, excerpt(dl)
		return result
	case dl.ExitCode != 0 && a.busy():
		result.Code, result.Detail = protocol.CodeRejectedBusy, excerpt(dl)
		return result
	case dl.ExitCode != 0:
		result.Code, result.Detail = protocol.CodePrecheckRepoUnreachable, excerpt(dl)
		return result
	}
	// The journal comes after the download: downloading only fills apt's cache and touches no package. A run that
	// fails there changed nothing, so it must not count for install.min_interval_h (design 10.2: the interval counts
	// from the journal, "only runs that touched packages"), and a mirror hiccup does not cost the night.
	if req.BeforeChange != nil {
		if err := req.BeforeChange(); err != nil {
			// Without the journal an interrupted run would go unnoticed; better not start.
			result.Code, result.Detail = protocol.CodeFailedPackageManager, "run journal not written: "+err.Error()
			return result
		}
	}

	ictx, cancel := context.WithTimeout(context.WithoutCancel(ctx), installTimeout)
	defer cancel()
	say(protocol.PhaseInstalling, pctDownload, "")
	name, args = a.withIOPriority(append([]string{"-q", "-y", "-o", "APT::Status-Fd=3",
		"-o", "Dpkg::Options::=--force-confdef", "-o", "Dpkg::Options::=--force-confold"}, target...))
	ins := a.Stream.Stream(ictx, env, statusLines(protocol.PhaseInstalling, pctDownload, pctInstall, say, &pmErrors),
		name, args...)
	result.Log = runLog(result.Log, ins)
	vctx, vcancel := context.WithTimeout(context.WithoutCancel(ctx), verifyTimeout)
	defer vcancel()
	switch {
	case ictx.Err() != nil:
		result.Code, result.Detail = protocol.CodeTimeout, withErrors(excerpt(ins), pmErrors)
		return result
	case !ins.Ran():
		result.Code, result.Detail = protocol.CodeFailedPackageManager, excerpt(ins)
		return result
	case ins.ExitCode != 0 && a.busy():
		result.Code, result.Detail = protocol.CodeRejectedBusy, excerpt(ins)
		return result
	case ins.ExitCode != 0:
		detail := withErrors(fmt.Sprintf("apt-get exit code %d\n%s", ins.ExitCode, excerpt(ins)), pmErrors)
		if _, broken := a.audit(vctx); broken {
			detail += "\ndpkg --audit: package database interrupted"
		}
		result.Code, result.Detail = protocol.CodeFailedPackageManager, detail
		return result
	}

	say(protocol.PhaseVerifying, pctInstall, "")
	if res, broken := a.audit(vctx); broken {
		result.Code, result.Detail = protocol.CodeFailedVerification, "dpkg --audit: "+excerpt(res)
		return result
	}
	again := a.Run.Run(vctx, env, "apt-get", append([]string{"-s", "-q"}, target...)...)
	if !again.Ran() || again.ExitCode != 0 {
		result.Code, result.Detail = protocol.CodeFailedVerification, excerpt(again)
		return result
	}
	if left, _ := ParseSimulation(again.Stdout); len(left) > 0 {
		names := make([]string, len(left))
		for i, p := range left {
			names[i] = p.Name
		}
		result.Code, result.Detail = protocol.CodeFailedVerification, "still pending: "+strings.Join(names, ", ")
		return result
	}
	result.OutdatedServices = a.outdatedServices(vctx)
	// The downloaded packages are installed now; keeping them would only fill the disk (apt keeps them by default).
	a.Run.Run(vctx, env, "apt-get", "clean")
	say(protocol.PhaseVerifying, 100, "")
	result.Code, result.Detail = protocol.CodeOK, held
	return result
}

// maxRunLog bounds the output kept for the run log on the host; the end is what matters after a failure.
const maxRunLog = 1 << 20

// runLog appends a command's whole output to the run log, keeping at most the last maxRunLog bytes.
func runLog(log []byte, res runner.Result) []byte {
	log = append(append(log, res.Stdout...), res.Stderr...)
	if len(log) > maxRunLog {
		log = log[len(log)-maxRunLog:]
	}
	return log
}

// outdatedServices asks needrestart, where installed, which services still run with replaced libraries; its batch
// mode (-b) is meant for programs: "NEEDRESTART-SVC: <unit>" per service.
func (a *Apt) outdatedServices(ctx context.Context) []string {
	if _, err := a.LookPath("needrestart"); err != nil {
		return nil
	}
	res := a.Run.Run(ctx, env, "needrestart", "-b")
	if !res.Ran() || res.ExitCode != 0 {
		return nil
	}
	var units []string
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		if unit, ok := strings.CutPrefix(strings.TrimSpace(line), "NEEDRESTART-SVC:"); ok {
			units = append(units, strings.TrimSpace(unit))
		}
	}
	return units
}

// aptTarget is what a run installs: the arguments after "apt-get [-s]" (nil: nothing to install), the simulation of
// exactly them when choosing them already ran it, and the security updates a security run holds back.
type aptTarget struct {
	args []string
	sim  *runner.Result
	held []heldBack
}

// heldBack: a security update the run leaves pending, and the packages from other sources its plan would change.
type heldBack struct {
	name    string
	outside []string
}

// target is what this scope installs. All: the mode the policy names, else full-upgrade (ADR-0076). Security and app:
// the packages from a full simulation, upgraded by name; for security only those whose plan stays within the security
// archives and the distribution's release (ADR-0116).
func (a *Apt) target(ctx context.Context, req source.InstallRequest) (aptTarget, runner.Result, protocol.Code) {
	if req.Scope.Kind == protocol.ScopeAll {
		return aptTarget{args: []string{runMode(req.Mode)}}, runner.Result{}, ""
	}
	sim := a.Run.Run(ctx, env, "apt-get", "-s", "-q", "full-upgrade")
	if !sim.Ran() || sim.ExitCode != 0 {
		return aptTarget{}, sim, a.classifyFailure(ctx)
	}
	pkgs, _ := ParseSimulation(sim.Stdout)
	var names []string
	for _, p := range pkgs {
		if (req.Scope.Kind == protocol.ScopeSecurity && p.Security) ||
			(req.Scope.Kind == protocol.ScopeApp && p.Name == req.Package) {
			names = append(names, p.Name)
		}
	}
	switch {
	case len(names) == 0:
		return aptTarget{}, sim, ""
	case req.Scope.Kind == protocol.ScopeSecurity:
		return a.securityTarget(ctx, names)
	}
	return aptTarget{args: upgradeArgs(names)}, sim, ""
}

func upgradeArgs(names []string) []string {
	return append([]string{"install", "--only-upgrade"}, names...)
}

// securityTarget keeps the security updates whose plan stays within what a security run may change (ADR-0116). First
// all of them together: the usual case, one simulation. When that plan reaches further, one at a time in the order of
// the full simulation, as unattended-upgrades does: an update is kept when the plan with the ones kept so far and it
// still stays within, else it is held back.
func (a *Apt) securityTarget(ctx context.Context, names []string) (aptTarget, runner.Result, protocol.Code) {
	sim, outside, code := a.simulateUpgrade(ctx, names)
	if code != "" {
		return aptTarget{}, sim, code
	}
	switch {
	case len(outside) == 0:
		return aptTarget{args: upgradeArgs(names), sim: &sim}, sim, ""
	case len(names) == 1: // one at a time would only simulate the same again
		return aptTarget{held: []heldBack{{name: names[0], outside: outside}}}, sim, ""
	}
	var t aptTarget
	var kept []string
	for _, name := range names {
		try := append(slices.Clone(kept), name)
		sim, outside, code := a.simulateUpgrade(ctx, try)
		if code != "" {
			return aptTarget{}, sim, code
		}
		if len(outside) > 0 {
			t.held = append(t.held, heldBack{name: name, outside: outside})
			continue
		}
		kept, t.sim = try, &sim
	}
	if len(kept) > 0 {
		t.args = upgradeArgs(kept)
	}
	return t, runner.Result{}, ""
}

// simulateUpgrade simulates upgrading names; outside describes the planned packages a security run may not change.
func (a *Apt) simulateUpgrade(ctx context.Context, names []string) (runner.Result, []string, protocol.Code) {
	sim := a.Run.Run(ctx, env, "apt-get", append([]string{"-s", "-q"}, upgradeArgs(names)...)...)
	if !sim.Ran() || sim.ExitCode != 0 {
		return sim, nil, a.classifyFailure(ctx)
	}
	plan, _ := ParseSimulation(sim.Stdout)
	var outside []string
	for _, p := range plan {
		if !SecurityRunAllows(p, a.Release) {
			from := strings.Join(p.Origins, ", ")
			if from == "" {
				from = "origin unknown"
			}
			outside = append(outside, fmt.Sprintf("%s %s (%s)", p.Name, p.To, from))
		}
	}
	return sim, outside, ""
}

// Bounds of the held-back detail: it is read in Home Assistant, not searched.
const (
	maxHeldNamed    = 10
	maxOutsideNamed = 3
)

// heldDetail says which security updates the run left pending and what they would have needed; "" when none.
func heldDetail(held []heldBack) string {
	if len(held) == 0 {
		return ""
	}
	parts := make([]string, 0, min(len(held), maxHeldNamed)+1)
	for i, h := range held {
		if i == maxHeldNamed {
			parts = append(parts, fmt.Sprintf("and %d more", len(held)-maxHeldNamed))
			break
		}
		need := strings.Join(h.outside[:min(len(h.outside), maxOutsideNamed)], ", ")
		if n := len(h.outside) - maxOutsideNamed; n > 0 {
			need += fmt.Sprintf(" and %d more", n)
		}
		parts = append(parts, h.name+" needs "+need)
	}
	return "security updates held back, they need packages from outside the security archives and the release: " +
		strings.Join(parts, "; ")
}

// withIOPriority runs apt-get with ionice in the lowest best-effort class. The agent's unit gives it the idle class,
// right for checks; but on a host that always writes (a video recorder) idle never gets the disk, and a
// package run would crawl while dpkg holds its lock. Without ionice apt-get runs as it is.
func (a *Apt) withIOPriority(args []string) (string, []string) {
	if _, err := a.LookPath("ionice"); err != nil {
		return "apt-get", args
	}
	return "ionice", append([]string{"-c", "2", "-n", "7", "apt-get"}, args...)
}

// busy: someone else holds dpkg's or apt's lock (unattended-upgrades, a person at the shell).
func (a *Apt) busy() bool {
	return lockState(a.Paths.DpkgLock) == lockBusy || lockState(a.Paths.ListsLock) == lockBusy
}

// audit: "dpkg --audit" lists half-configured or half-installed packages; empty output with exit 0 means clean.
func (a *Apt) audit(ctx context.Context) (runner.Result, bool) {
	res := a.Run.Run(ctx, env, "dpkg", "--audit")
	return res, res.Ran() && (res.ExitCode != 0 || len(strings.TrimSpace(string(res.Stdout))) > 0)
}

// lacksSpace says why the download or a kernel would not fit, or "" when it fits or the space is unknown (apt checks
// again itself).
func (a *Apt) lacksSpace(ctx context.Context, target []string, plan []source.Package) string {
	uris := a.Run.Run(ctx, env, "apt-get", append([]string{"-qq", "--print-uris", "-y", "-d"}, target...)...)
	if uris.Ran() && uris.ExitCode == 0 {
		var need uint64
		for _, line := range strings.Split(string(uris.Stdout), "\n") {
			// 'http://deb.debian.org/.../bash_5.2.15-2+b13_amd64.deb' bash_5.2.15-2+b13_amd64.deb 1491288 SHA256:...
			if f := strings.Fields(line); len(f) >= 3 && strings.HasPrefix(f[0], "'") {
				if n, err := strconv.ParseUint(f[2], 10, 64); err == nil {
					need += n
				}
			}
		}
		if free, ok := freeBytes(a.Paths.ArchivesDir); ok && free < need+downloadMargin {
			return fmt.Sprintf("%s: %d MiB free, the download needs %d MiB plus %d MiB", a.Paths.ArchivesDir,
				free>>20, need>>20, downloadMargin>>20)
		}
	}
	for _, p := range plan {
		if isKernelPackage(p.Name) {
			if free, ok := freeBytes(a.Paths.BootDir); ok && free < bootMinFree {
				return fmt.Sprintf("%s: %d MiB free, %s needs at least %d MiB", a.Paths.BootDir, free>>20, p.Name,
					bootMinFree>>20)
			}
			return ""
		}
	}
	return ""
}

// statusLines turns APT::Status-Fd lines into progress between from and to percent; dpkg errors are collected for
// the detail.
//
//	dlstatus:3:27.2727:Retrieving file 3 of 11
//	pmstatus:libc6:amd64:41.6667:Unpacking libc6:amd64 (2.36-9+deb12u10) over (2.36-9+deb12u9)
//	pmerror:foo:amd64:50:subprocess installed post-installation script returned error exit status 1
func statusLines(phase protocol.Phase, from, to int, say func(protocol.Phase, int, string), errs *[]string) func(string) {
	return func(line string) {
		kind, rest, _ := strings.Cut(line, ":")
		f := strings.Split(rest, ":")
		switch kind {
		case "dlstatus":
			if len(f) >= 2 {
				if pct, err := strconv.ParseFloat(f[1], 64); err == nil {
					say(phase, scale(from, to, pct), "")
				}
			}
		case "pmstatus", "pmerror":
			// The package may carry its architecture ("libc6:amd64"): the percentage is the first number after it.
			for i := 1; i < len(f); i++ {
				pct, err := strconv.ParseFloat(f[i], 64)
				if err != nil {
					continue
				}
				pkg, msg := strings.Join(f[:i], ":"), strings.Join(f[i+1:], ":")
				if kind == "pmerror" {
					*errs = append(*errs, pkg+": "+msg)
				} else {
					say(phase, scale(from, to, pct), pkg)
				}
				return
			}
		}
	}
}

func scale(from, to int, pct float64) int {
	pct = min(max(pct, 0), 100)
	return from + int(pct*float64(to-from)/100)
}

func withErrors(detail string, errs []string) string {
	if len(errs) == 0 {
		return detail
	}
	return detail + "\ndpkg errors:\n" + strings.Join(errs, "\n")
}

// updateArgs: without --error-on=any (apt >= 2.1.16) an unreachable source is only a warning and exit code 0.
func updateArgs(aptVersion string) []string {
	args := []string{"update", "-q"}
	if versionAtLeast(aptVersion, 2, 1, 16) {
		args = append(args, "--error-on=any")
	}
	return args
}
