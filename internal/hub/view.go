package hub

import (
	"slices"
	"strings"
	"time"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/wire"
)

// What the hub tells Home Assistant about hosts is decided here, once, as views (wire.HostView, wire.HubView) that
// /v1/ha sends as they are. They hold codes and numbers, never display text: the integration translates. The same
// goes for commands: Resolve is the one place that turns a command into a job, and Capabilities offers exactly what
// Resolve allows.

// waitingOf: the vulnerable packages whose fix waits for someone else (ADR-0129); 0 when the view has no count.
func waitingOf(pv *wire.PackagesView) int {
	if pv.Waiting == nil {
		return 0
	}
	return *pv.Waiting
}

// Problem reasons besides the result codes.
const (
	ReasonOffline       = "offline"
	ReasonStale         = "stale"
	ReasonCheckFailed   = "check_failed"
	ReasonRebootPending = "reboot_pending"
	ReasonNoReport      = "no_report"
	// An installed pip/npm package is malware according to OSV (a MAL- entry); comes first, it is the most urgent.
	ReasonMaliciousPackage = "malicious_package"
	// An installed pip/npm package has a known vulnerability whose fix something on the host can take now (design 11.2,
	// ADR-0129); one whose fix waits for someone else is only reported.
	ReasonVulnerablePackage = "vulnerable_package"
)

// Limits are the thresholds after which a host counts as having a problem.
type Limits struct {
	StaleAfter    time.Duration // updates pending longer than this without a run that installs them
	OfflineAfter  time.Duration // disconnected longer than this (a reboot is shorter)
	RebootAfter   time.Duration // a reboot needed longer than this (counted from the first report saying so)
	NoReportAfter time.Duration // connected, but no check reported for longer than this
}

// HostView is the state of one host as of now.
func (s *Server) HostView(e Entry, now time.Time) wire.HostView {
	online, _ := s.Online(e.Node)
	v := wire.HostView{Node: e.Node, Device: deviceView(e), Online: online, Capabilities: capabilities(e),
		Reboot: wire.RebootView{Checked: []string{}}, NextInstall: nextInstall(e, now)}

	var reasons []string
	var failedCode protocol.Code
	if rep := e.LastReport; rep != nil {
		at := rep.CheckedAt
		v.LastReport = &at
		if pending, known := pendingCount(*rep); known {
			security, keptBack := 0, 0
			for _, src := range rep.Sources {
				security += src.Security
				keptBack += src.KeptBack
			}
			// What install.mode upgrade leaves, an install of scope all does not take (ADR-0127).
			installable := installableCount(v.Capabilities, pending-keptBack, security)
			v.Pending, v.Security, v.Installable = &pending, &security, &installable
			if keptBack > 0 {
				v.KeptBack = &keptBack
			}
		}
		for _, src := range rep.Sources {
			if src.State == protocol.SourceCheckFailed && failedCode == "" {
				failedCode = src.Code
			}
		}
		if r := rep.Reboot; r != nil && len(r.Checked) > 0 {
			required := r.Required
			v.Reboot = wire.RebootView{Required: &required, Reason: r.Reason, Packages: r.Packages, Checked: r.Checked,
				KernelRunning: r.Running, KernelNewest: r.Newest}
			if required {
				v.Reboot.Since = e.RebootSince
			}
		}
		if len(rep.Sources) > 0 {
			v.System = systemView(e, v.Pending, rep)
		}
	}

	if !online {
		v.Power = e.Power
	}
	if !online && e.Power == "" && now.Sub(s.DisconnectedSince(e.Node)) > s.Limits.OfflineAfter {
		reasons = append(reasons, ReasonOffline)
	}
	if run := e.LastRun; run != nil {
		if run.Code != "" && run.Code.Problem() {
			reasons = append(reasons, string(run.Code))
		}
		v.LastRun = runView(*run)
	}
	// A failed system install stays a problem after later jobs of other kinds (ADR-0124), with its detail.
	if f := e.FailedInstall; f != nil {
		if (e.LastRun == nil || e.LastRun.JobID != f.JobID) && !slices.Contains(reasons, string(f.Code)) {
			reasons = append(reasons, string(f.Code))
		}
		v.FailedInstall = runView(*f)
	}
	if failedCode != "" {
		reasons = append(reasons, ReasonCheckFailed)
	}
	if e.PendingSince != nil && now.Sub(*e.PendingSince) > s.Limits.StaleAfter {
		reasons = append(reasons, ReasonStale)
	}
	if v.Reboot.Since != nil && now.Sub(*v.Reboot.Since) > s.Limits.RebootAfter {
		reasons = append(reasons, ReasonRebootPending)
	}
	if online && v.LastReport != nil && now.Sub(*v.LastReport) > s.Limits.NoReportAfter {
		reasons = append(reasons, ReasonNoReport)
	}
	if s.Packages != nil {
		if slices.Contains(e.Verbs, protocol.VerbInstall) {
			v.Capabilities.PackageUpdates = s.Packages.UpdatableLocations(e.Node)
			v.Capabilities.DependencyRefresh = s.Packages.RefreshableLocations(e.Node)
			v.Capabilities.ImageUpdates = s.Packages.UpdatableServices(e.Node)
		}
		if pv := s.Packages.View(e.Node); pv != nil {
			v.Packages = pv
			if pv.Malicious != nil && *pv.Malicious > 0 {
				reasons = append([]string{ReasonMaliciousPackage}, reasons...)
			}
			if pv.Vulnerable != nil && *pv.Vulnerable > waitingOf(pv) {
				reasons = append(reasons, ReasonVulnerablePackage)
			}
		}
		v.Containers = s.Packages.Containers(e.Node) // outdated images are no problem: only reported (ADR-0113)
	}
	if len(reasons) > 0 {
		v.Problem = &wire.ProblemView{Reason: reasons[0], Reasons: reasons, CheckFailedCode: failedCode}
	}
	if s.Backups != nil {
		v.Backup = s.Backups.View(e.Node, now, ReportedConfigBackup(e.LastReport))
	}
	if s.Releases != nil {
		if rel := s.Releases.Loaded(); rel != nil {
			v.AgentRelease, v.AgentReleaseNoBinary = rel.Manifest.Version, !rel.Fits(e.Facts)
		}
	}
	v.Queued = s.Queued(e.Node)
	return v
}

// runView: a job as Home Assistant sees it - its phase and progress while running, its result code and summary once
// done.
func runView(run Run) *wire.RunView {
	v := &wire.RunView{State: string(run.Phase), JobID: run.JobID, Verb: run.Verb, Scope: run.Scope, By: run.By,
		Started: run.Started, Finished: run.Finished, Reason: run.Reason, LogExcerpt: run.Detail,
		OutsideWindow: run.OutsideWindow}
	if run.Code == "" {
		v.Progress, v.Item = run.Progress, run.Item
	} else {
		v.State, v.Summary = string(run.Code), run.Summary
	}
	return v
}

// deviceView: "Vördr <node>" - the same host often exists in HA already (its own integration), the prefix keeps
// them apart.
func deviceView(e Entry) wire.DeviceView {
	model := e.Facts.OS
	if v := e.Facts.Virtualization; v != "" && v != "none" && v != "unknown" {
		model += " · " + v
	}
	return wire.DeviceView{Name: "Vördr " + e.Node, Model: model, AgentVersion: e.AgentVersion}
}

// systemView: a package set has no single version, so installed_version names the system (design 8.3); the pending
// set comes as counts and the first MaxPackages packages, which the integration words.
func systemView(e Entry, pending *int, rep *wire.Report) *wire.SystemView {
	installed := e.Facts.OS
	if installed == "" {
		installed = "System"
	}
	sv := &wire.SystemView{InstalledVersion: installed}
	if pending == nil {
		return sv // check failed: pending unknown
	}
	for _, s := range rep.Sources {
		for _, p := range s.Packages {
			if len(sv.Packages) == wire.MaxPackages {
				return sv
			}
			sv.Packages = append(sv.Packages, wire.PackageView{Name: p.Name, From: p.From, To: p.To, Security: p.Security,
				Major: p.Major})
		}
	}
	return sv
}

// capabilities offers what the agent announced and its policy allows - the same rules as Resolve.
func capabilities(e Entry) wire.Capabilities {
	has := func(v protocol.Verb) bool { return slices.Contains(e.Verbs, v) }
	p := e.Policy
	allowed := func(scope string) bool { return has(protocol.VerbInstall) && slices.Contains(p.AllowedScopes, scope) }
	c := wire.Capabilities{Check: has(protocol.VerbCheck), Reboot: has(protocol.VerbReboot) && p.RebootAllowed,
		AgentUpdate:   has(protocol.VerbAgentUpdate) && p.AgentUpdate,
		OutsideWindow: p.OutsideWindow && (has(protocol.VerbInstall) || has(protocol.VerbReboot))}
	if p.DefaultScope != "" && allowed(p.DefaultScope) {
		c.Install, c.DefaultScope = true, p.DefaultScope
	}
	c.InstallSecurity = allowed("security") && p.DefaultScope != "security"
	for _, a := range p.Apps {
		if protocol.ValidAppID(a.ID) && allowed("app:"+a.ID) {
			c.Apps = append(c.Apps, a)
		}
	}
	return c
}

// installableCount: what the install button takes under the host's policy (ADR-0106) - everything pending with the
// default scope all, the security updates with the default scope security or with only the button for security
// updates, nothing without either.
func installableCount(c wire.Capabilities, pending, security int) int {
	switch {
	case c.Install && c.DefaultScope == string(protocol.ScopeAll):
		return pending
	case c.Install && c.DefaultScope == string(protocol.ScopeSecurity), c.InstallSecurity:
		return security
	}
	return 0
}

// HubView is the hub's own state; entries as from Reg.List.
func (s *Server) HubView(entries []Entry) wire.HubView {
	lim := s.Limits
	v := wire.HubView{Version: s.Version, Protocol: wire.HAProtocol, Waiting: []wire.WaitingHost{},
		Limits: wire.LimitsView{OfflineAfterMinutes: int(lim.OfflineAfter.Minutes()), StaleAfterDays: int(lim.StaleAfter.Hours() / 24),
			RebootPendingAfterDays: int(lim.RebootAfter.Hours() / 24), NoReportAfterHours: int(lim.NoReportAfter.Hours())}}
	if s.Packages != nil {
		v.OSV = s.Packages.HubView()
	}
	for _, e := range entries {
		switch e.State {
		case wire.StatePendingApproval:
			v.Waiting = append(v.Waiting, wire.WaitingHost{Node: e.Node, Fingerprint: e.Fingerprint, OS: e.Facts.OS})
		case wire.StateApproved:
			if online, _ := s.Online(e.Node); online {
				v.HostsOnline++
			}
		}
	}
	return v
}

// Resolve turns a command from Home Assistant into a job, or refuses it with a code. The hub only asks for what the
// host announced it can do and allows; the agent checks again.
func (s *Server) Resolve(node, cmd string) (protocol.Verb, string, AdminCode) {
	e, ok := s.Reg.Get(node)
	switch {
	case !ok:
		return "", "", AdminUnknownNode
	case e.State != wire.StateApproved:
		return "", "", AdminNotApproved
	}
	var verb protocol.Verb
	var scope string
	switch {
	case cmd == "check":
		verb = protocol.VerbCheck
	case cmd == "install":
		verb, scope = protocol.VerbInstall, e.Policy.DefaultScope
	case cmd == "install_security":
		verb, scope = protocol.VerbInstall, "security"
	case strings.HasPrefix(cmd, "install_app:") && protocol.ValidAppID(strings.TrimPrefix(cmd, "install_app:")):
		verb, scope = protocol.VerbInstall, "app:"+strings.TrimPrefix(cmd, "install_app:")
	case cmd == "reboot":
		verb = protocol.VerbReboot
	case cmd == "agent_update":
		verb = protocol.VerbAgentUpdate
	case strings.HasPrefix(cmd, "install_pkg:"):
		// pip/npm (design 11.4): a location the host reported as updatable, optionally one package of it.
		sc, ok := protocol.ParseScope("pkg:" + strings.TrimPrefix(cmd, "install_pkg:"))
		if !ok {
			return "", "", AdminBadRequest
		}
		if !slices.Contains(e.Verbs, protocol.VerbInstall) {
			return "", "", AdminVerbUnsupported
		}
		if s.Packages == nil || !s.Packages.Updatable(node, sc.Location, sc.Package) {
			return "", "", AdminNotAllowed
		}
		return protocol.VerbInstall, sc.String(), AdminOK
	case strings.HasPrefix(cmd, "refresh_deps:"):
		// Dependencies with findings (ADR-0104): a location the host reported as refreshable.
		sc, ok := protocol.ParseScope("deps:" + strings.TrimPrefix(cmd, "refresh_deps:"))
		if !ok {
			return "", "", AdminBadRequest
		}
		if !slices.Contains(e.Verbs, protocol.VerbInstall) {
			return "", "", AdminVerbUnsupported
		}
		if s.Packages == nil || !slices.Contains(s.Packages.RefreshableLocations(node), sc.Location) {
			return "", "", AdminNotAllowed
		}
		return protocol.VerbInstall, sc.String(), AdminOK
	case cmd == "install_images" || strings.HasPrefix(cmd, "install_image:"):
		// Container images (ADR-0119): a Compose service the host reported as updatable, or all of them; the agent
		// picks those that have a rebuild old enough.
		sc, ok := protocol.ParseScope("images")
		if cmd != "install_images" {
			sc, ok = protocol.ParseScope("image:" + strings.TrimPrefix(cmd, "install_image:"))
		}
		if !ok {
			return "", "", AdminBadRequest
		}
		if !slices.Contains(e.Verbs, protocol.VerbInstall) {
			return "", "", AdminVerbUnsupported
		}
		var services []string
		if s.Packages != nil {
			services = s.Packages.UpdatableServices(node)
		}
		if len(services) == 0 || sc.Kind == protocol.ScopeImage && !slices.Contains(services, sc.Project+"/"+sc.Service) {
			return "", "", AdminNotAllowed
		}
		return protocol.VerbInstall, sc.String(), AdminOK
	default:
		return "", "", AdminBadRequest
	}
	switch {
	case !slices.Contains(e.Verbs, verb):
		return "", "", AdminVerbUnsupported
	case verb == protocol.VerbInstall && (scope == "" || !slices.Contains(e.Policy.AllowedScopes, scope)):
		return "", "", AdminNotAllowed
	case verb == protocol.VerbReboot && !e.Policy.RebootAllowed:
		return "", "", AdminNotAllowed
	case verb == protocol.VerbAgentUpdate && !e.Policy.AgentUpdate:
		return "", "", AdminNotAllowed
	}
	return verb, scope, AdminOK
}
