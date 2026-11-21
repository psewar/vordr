package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"

	"github.com/psewar/vordr/internal/facts"
	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/source"
	"github.com/psewar/vordr/internal/target"
	"github.com/psewar/vordr/internal/wire"
)

// ConfigBackupSource is the name of the report entry that says how fresh the appliance's configuration backup is.
const ConfigBackupSource = source.ConfigBackup

// TargetChecker reports on an appliance through its driver (design/proxy-agent.md) and keeps its configuration
// backup fresh (ADR-0090): with every check it exports again once the newest good export is older than maxAge less a
// margin, so a daily check keeps it inside maxAge.
func TargetChecker(d target.Driver, store target.Store, maxAge time.Duration, now func() time.Time) Checker {
	return targetChecker{d: d, store: store, maxAge: maxAge, now: now}
}

type targetChecker struct {
	d      target.Driver
	store  target.Store
	maxAge time.Duration
	now    func() time.Time
}

// backupMargin: an export this much younger than max_age_h is still fresh enough; older ones are renewed.
const backupMargin = 2 * time.Hour

func (c targetChecker) Check(ctx context.Context) wire.Report {
	at := c.now().UTC()
	st, res := c.d.Check(ctx)
	fw := source.Report{Source: c.d.Name(), CheckedAt: at, Facts: map[string]string{}}
	for k, v := range st.Facts {
		fw.Facts[k] = v
	}
	if st.SelfUpdate != "" {
		fw.Facts["self_update"] = st.SelfUpdate
	}
	if st.Model != "" {
		fw.Facts["model"] = st.Model
	}
	if res.OK() && st.Installed != "" {
		// The hello names the system as the device answered at the instance's start; a release installed since
		// changes it without a new hello. The hub takes these for the host (ADR-0210).
		fw.Facts[source.FactOS], fw.Facts[source.FactOSVersion] = deviceSystem(st)
	}
	switch {
	case !res.OK():
		fw.State, fw.Code, fw.Detail = protocol.SourceCheckFailed, res.Code, res.Detail
	case len(st.Offers) == 0:
		fw.State = protocol.SourceUpToDate
	default:
		fw.State, fw.Pending = protocol.SourceUpdatesAvailable, len(st.Offers)
		for _, o := range st.Offers {
			if o.Security {
				fw.Security++
			}
			if o.KeptBack { // the hub leaves it out of what the install button takes (ADR-0204)
				fw.KeptBack++
			}
			// An offer for something else than the firmware (a package on the device) names what it replaces.
			from := o.From
			if from == "" {
				from = st.Installed
			}
			fw.Packages = append(fw.Packages, source.Package{Name: o.Name, From: from, To: o.Version,
				Security: o.Security, KeptBack: o.KeptBack})
		}
		if fw.KeptBack > 0 { // an offer is kept back only as a release in another branch (target.Offer, ADR-0207)
			fw.KeptBackWhy = protocol.KeptBackOtherBranch
		}
	}
	return wire.Report{T: wire.TReport, CheckedAt: at, Sources: []source.Report{fw, c.backup(ctx, at, res)}}
}

// backup renews the export when due and reports how fresh the newest good one is. When the device could not be
// checked (unreachable, login refused, certificate), an export would fail the same way: it is not tried then.
func (c targetChecker) backup(ctx context.Context, at time.Time, checked target.Result) source.Report {
	rep := source.Report{Source: ConfigBackupSource, CheckedAt: at, Facts: map[string]string{}}
	last, have := c.store.Last()
	why := ""
	renew := !have || at.Sub(last.At) > c.maxAge-backupMargin
	switch {
	case !renew:
	case !checked.OK():
		why = "device not reachable for an export: " + string(checked.Code)
	default:
		data, ext, res := c.d.Backup(ctx)
		if !res.OK() {
			why = res.Detail
			if res.Code != protocol.CodeFailedConfigBackup {
				why = string(res.Code) + ": " + res.Detail
			}
			break
		}
		saved, err := c.store.Save(c.d.Name(), data, ext, at)
		if err != nil {
			why = "storing the export: " + err.Error()
			break
		}
		last, have = saved, true
	}
	if have {
		rep.Facts[source.FactConfigBackupAt] = last.At.Format(time.RFC3339)
		rep.Facts[source.FactConfigBackupSH] = last.SHA256
	}
	switch {
	case have && why == "":
		// Fresh by construction: no renewal was due (younger than max_age less the margin), or one just worked.
		rep.State = protocol.SourceUpToDate
	case have && at.Sub(last.At) <= c.maxAge:
		// The renewal failed, but the last export is still within max_age: fresh enough, the failure shows in Detail.
		rep.State, rep.Detail = protocol.SourceUpToDate, "renewal failed: "+why
	default:
		if why == "" {
			why = "no export yet"
		}
		rep.State, rep.Code, rep.Detail = protocol.SourceCheckFailed, protocol.CodeFailedConfigBackup, why
	}
	return rep
}

// TargetInstaller installs a proxy instance's offers through its driver (ADR-0205), where the driver can; nil where it
// cannot. Whether the instance may install at all is its agent.yaml's (install.allowed_scopes, closed by default).
func TargetInstaller(d target.Driver, store target.Store, now func() time.Time) Installer {
	in, ok := d.(target.Installer)
	if !ok {
		return nil
	}
	return targetInstaller{d: in, store: store, now: now, poll: targetPoll, lost: targetLost}
}

// targetPoll: how often the device is asked while an install runs; targetLost: how long after queueing a request the
// device may still not know it before it counts as lost (the root job takes requests every minute).
const (
	targetPoll = 15 * time.Second
	targetLost = 3 * time.Minute
)

type targetInstaller struct {
	d     target.Installer
	store target.Store
	now   func() time.Time
	poll  time.Duration
	lost  time.Duration
}

// RebootRequired: never asked of the device - its reboot belongs to its update (design/proxy-agent.md 4.5).
func (targetInstaller) RebootRequired(context.Context) bool { return false }

// Install runs an install of scope all on the appliance (ADR-0205):
//  1. a fresh configuration backup, taken and stored right before - without it nothing starts (ADR-0090);
//  2. a check, and from its offers the plan (installPlan);
//  3. per offer: queue it on the device, wait for the device's end of it (await), and verify it (verify).
//
// The phases: prechecking, installing while the device runs it, rebooting while the device does not answer, verifying.
func (t targetInstaller) Install(ctx context.Context, req source.InstallRequest, timeout time.Duration,
	progress func(source.Progress)) source.InstallResult {
	if req.Scope.Kind != protocol.ScopeAll {
		return source.InstallResult{Code: protocol.CodeRejectedScopeNotAllowed,
			Detail: "an appliance installs what its updater offers: scope all"}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	progress(source.Progress{Phase: protocol.PhasePrechecking})
	data, ext, res := t.d.Backup(ctx)
	if !res.OK() {
		return source.InstallResult{Code: res.Code, Detail: "configuration backup before the install: " + res.Detail}
	}
	if _, err := t.store.Save(t.d.Name(), data, ext, t.now().UTC()); err != nil {
		return source.InstallResult{Code: protocol.CodeFailedConfigBackup, Detail: "storing the backup: " + err.Error()}
	}
	st, res := t.d.Check(ctx)
	if !res.OK() {
		return source.InstallResult{Code: res.Code, Detail: res.Detail}
	}
	plan := installPlan(st.Offers)
	out := source.InstallResult{Planned: len(plan)}
	for _, o := range plan {
		out.Packages = append(out.Packages, o.Name)
	}
	if len(plan) == 0 {
		out.Code = protocol.CodeOK
		return out
	}
	if req.BeforeChange != nil {
		if err := req.BeforeChange(); err != nil {
			// Without the journal an interrupted run would go unnoticed; better not start.
			out.Code, out.Detail = protocol.CodeFailedPackageManager, "run journal not written: "+err.Error()
			return out
		}
	}
	for i, o := range plan {
		percent := 5 + 85*i/len(plan)
		id, err := requestID()
		if err != nil {
			out.Code, out.Detail = protocol.CodeFailedPackageManager, "no request id: "+err.Error()
			return out
		}
		progress(source.Progress{Phase: protocol.PhaseInstalling, Percent: percent, Item: o.Name})
		if res := t.d.Queue(ctx, o, id); !res.OK() {
			out.Code, out.Detail = res.Code, res.Detail
			return out
		}
		done, res := t.await(ctx, id, o.Name, percent, progress)
		if res.OK() {
			res = t.verify(ctx, o, done, progress)
		}
		if !res.OK() {
			out.Code, out.Detail = res.Code, res.Detail
			return out
		}
	}
	progress(source.Progress{Phase: protocol.PhaseVerifying, Percent: 100})
	out.Code = protocol.CodeOK
	return out
}

// installPlan: what an install of scope all takes (ADR-0205). The release of the configured branch goes alone: the
// updater brings the add-ons along with it, and what it leaves the next install takes. Without a release, each add-on.
// An offer kept back, or one the driver names no way to install, never.
func installPlan(offers []target.Offer) []target.Offer {
	var addons []target.Offer
	for _, o := range offers {
		switch {
		case o.KeptBack || o.Install == "":
		case o.Install == "release":
			return []target.Offer{o}
		default:
			addons = append(addons, o)
		}
	}
	return addons
}

// unreachable: the device does not answer, or its SSH service refuses for now - what a rebooting device looks like.
func unreachable(c protocol.Code) bool {
	return c == protocol.CodePrecheckRepoUnreachable || c == protocol.CodeFailedTargetAuth
}

// await asks the device where the request stands until it ended. Queued and running are waited out; a device that
// does not answer is rebooting. The end it reports decides: ok and rebooted go on to verify, busy is rejected_busy,
// any other refusal or failure failed_package_manager. A request the device still does not know a while after it was
// queued is lost; the run's time ending is a timeout.
func (t targetInstaller) await(ctx context.Context, id, item string, percent int,
	progress func(source.Progress)) (target.InstallState, target.Result) {
	queued := t.now()
	for {
		st, res := t.d.InstallStatus(ctx, id)
		switch {
		case !res.OK() && unreachable(res.Code):
			progress(source.Progress{Phase: protocol.PhaseRebooting, Percent: percent, Item: item})
		case !res.OK():
			return st, res
		case st.State == target.InstallQueued || st.State == target.InstallRunning:
			progress(source.Progress{Phase: protocol.PhaseInstalling, Percent: percent, Item: item})
		case st.State == target.InstallUnknown:
			if t.now().Sub(queued) > t.lost {
				return st, target.Result{Code: protocol.CodeFailedPackageManager,
					Detail: "the device did not take the install request"}
			}
		case st.State == target.InstallOK || st.State == target.InstallRebooted:
			return st, target.Result{}
		case st.State == target.InstallRefused && st.Code == target.InstallBusy:
			return st, target.Result{Code: protocol.CodeRejectedBusy, Detail: "another update runs on the device"}
		case st.State == target.InstallRefused:
			return st, target.Result{Code: protocol.CodeFailedPackageManager,
				Detail: "the device did not start the install: " + st.Code}
		default:
			return st, target.Result{Code: protocol.CodeFailedPackageManager,
				Detail: fmt.Sprintf("the device's updater ended %s (exit %s)", st.Code, st.RC)}
		}
		select {
		case <-ctx.Done():
			return st, target.Result{Code: protocol.CodeTimeout, Detail: "the install did not end within the run's time"}
		case <-time.After(t.poll):
		}
	}
}

// verify waits for the device's update facts read after the install ended, and checks that the device no longer
// offers what was installed (the same offer from the same version). After an install that ended ok the first such facts
// decide; after a reboot it asks until the run's time ends, since a release upgrade may go on in later boot stages.
func (t targetInstaller) verify(ctx context.Context, o target.Offer, done target.InstallState,
	progress func(source.Progress)) target.Result {
	for {
		progress(source.Progress{Phase: protocol.PhaseVerifying, Percent: 95, Item: o.Name})
		st, res := t.d.Check(ctx)
		switch {
		case !res.OK() && !unreachable(res.Code):
			return res
		case res.OK() && readAfter(st, done.Finished):
			if !stillOffered(st.Offers, o) {
				return target.Result{}
			}
			if done.State == target.InstallOK {
				return target.Result{Code: protocol.CodeFailedVerification,
					Detail: fmt.Sprintf("the device still offers %s %s after the install", o.Name, o.Version)}
			}
		}
		select {
		case <-ctx.Done():
			return target.Result{Code: protocol.CodeFailedVerification,
				Detail: fmt.Sprintf("the device did not confirm %s %s within the run's time", o.Name, o.Version)}
		case <-time.After(t.poll):
		}
	}
}

// readAfter: the device's update facts were read at or after end (seconds since the epoch).
func readAfter(st target.Status, end int64) bool {
	at, err := strconv.ParseInt(st.Facts["updates_checked_at"], 10, 64)
	return err == nil && at >= end
}

func stillOffered(offers []target.Offer, o target.Offer) bool {
	for _, x := range offers {
		if x.Name == o.Name && x.From == o.From {
			return true
		}
	}
	return false
}

// requestID names one install request on the device: 24 hex digits.
func requestID() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// TargetFacts are the appliance's facts for the hello: product and firmware as the device names them, the driver as
// the system id. When the device does not answer at start, the driver's name stands in until the next start.
func TargetFacts(ctx context.Context, node string, d target.Driver) facts.Host {
	h := facts.Host{Hostname: node, OS: d.Name(), OSID: d.Name(), OSVersion: facts.Unknown, GOOS: facts.Unknown,
		Arch: facts.Unknown, Virtualization: "none", Container: "none"}
	if st, res := d.Check(ctx); res.OK() {
		h.OS, h.OSVersion = deviceSystem(st)
		if st.Model != "" {
			h.Arch = st.Model
		}
	}
	return h
}

// deviceSystem: the system as the device names it, for the hello (os, os_version) and for each check's report.
func deviceSystem(st target.Status) (system, version string) {
	return st.Product + " " + st.Installed, st.Installed
}
