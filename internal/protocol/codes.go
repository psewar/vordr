// Package protocol holds what agent, hub and Home Assistant agree on: verbs, scopes, phases, result codes and the
// signed job format. Every name here is part of the interface (design/architecture.md, sections 6 and 7) and stays
// English; display texts belong to Home Assistant, in the reader's language.
package protocol

// Verb is one of the closed set of job verbs. There is no other way to make an agent act.
type Verb string

const (
	VerbCheck       Verb = "check"
	VerbInstall     Verb = "install"
	VerbReboot      Verb = "reboot"
	VerbAgentUpdate Verb = "agent_update"
	VerbCancel      Verb = "cancel"
	// VerbBackup: the hypervisor's agent backs up one of its guests to the backup server, right before that guest
	// installs (ADR-0096). It only ever adds a backup; it never prunes, restores or deletes.
	VerbBackup Verb = "backup"
)

// Valid reports whether v belongs to the closed verb set.
func (v Verb) Valid() bool {
	switch v {
	case VerbCheck, VerbInstall, VerbReboot, VerbAgentUpdate, VerbCancel, VerbBackup:
		return true
	}
	return false
}

// Phase is the step a running job is in.
type Phase string

const (
	PhaseQueued      Phase = "queued"
	PhasePrechecking Phase = "prechecking"
	PhaseDownloading Phase = "downloading"
	PhaseInstalling  Phase = "installing"
	PhaseVerifying   Phase = "verifying"
	PhaseRebooting   Phase = "rebooting"
	// PhaseBackingUp: an install waits for the fresh backup of its guest (ADR-0096). The hub sets it; the guest's own
	// agent has not received the job yet.
	PhaseBackingUp Phase = "backing_up"
)

// Code is the final result of a job. Every outcome a caller has to tell apart has its own code; error texts only
// travel along as an excerpt for humans and never decide anything.
type Code string

const (
	CodeOK                      Code = "ok"
	CodeOKRebootRequired        Code = "ok_reboot_required"
	CodeRejectedInvalidJob      Code = "rejected_invalid_job"
	CodeRejectedScopeNotAllowed Code = "rejected_scope_not_allowed"
	CodeRejectedOutsideWindow   Code = "rejected_outside_window"
	CodeRejectedRateLimited     Code = "rejected_rate_limited"
	CodeRejectedBusy            Code = "rejected_busy"
	CodeRejectedNotAllowed      Code = "rejected_not_allowed" // the local policy forbids this verb
	CodeRejectedUnsupported     Code = "rejected_unsupported" // this agent version or platform cannot do it
	CodePrecheckDiskSpace       Code = "precheck_disk_space"
	CodePrecheckPackageDBBroken Code = "precheck_package_db_broken"
	CodePrecheckRepoUnreachable Code = "precheck_repo_unreachable"
	CodePrecheckWouldRemove     Code = "precheck_would_remove_packages"
	// pip/npm updates (design 11.4): a version of the plan is flagged by OSV; one is younger than min_age_days; the
	// hub could not vouch for the plan (no or stale OSV data, no answer); an npm plan has an entry from somewhere other
	// than the registry - another host, git, a file (ADR-0128).
	CodePrecheckVersionFlagged     Code = "precheck_version_flagged"
	CodePrecheckVersionTooNew      Code = "precheck_version_too_new"
	CodePrecheckVettingUnavailable Code = "precheck_vetting_unavailable"
	CodePrecheckNotFromRegistry    Code = "precheck_not_from_registry"
	CodeFailedPackageManager       Code = "failed_package_manager"
	CodeFailedVerification         Code = "failed_verification"
	CodeFailedHealth               Code = "failed_health" // the run went through, but a check from agent.yaml health: fails
	CodeFailedWindowsUpdate        Code = "failed_windows_update"
	// A proxy instance's appliance (design/stellvertreter.md, ADR-0089): the device refused the service account; the
	// device's certificate does not match the pinned fingerprint or name (someone may be in between); taking or
	// checking the configuration backup failed (ADR-0090). Over SSH (ADR-0094): the device does not offer the pinned
	// host key.
	CodeFailedTargetAuth    Code = "failed_target_auth"
	CodeFailedTargetTLS     Code = "failed_target_tls"
	CodeFailedTargetHostKey Code = "failed_target_hostkey"
	CodeFailedConfigBackup  Code = "failed_config_backup"
	// CodeFailedBackup: the backup right before an install did not come about (ADR-0096); the install never started.
	// The hypervisor's agent ends a failed backup job with it as well.
	CodeFailedBackup Code = "failed_backup"
	CodeTimeout      Code = "timeout"
	CodeAborted      Code = "aborted"
	CodeInterrupted  Code = "interrupted"
)

// codes lists every result code in the order of the design table, with whether it sets the host's problem sensor.
// Expected refusals (outside window, rate limit, busy, scope) are no problem by themselves; if they keep updates
// from landing, the hub's stale check catches it.
var codes = []struct {
	code    Code
	problem bool
}{
	{CodeOK, false},
	{CodeOKRebootRequired, false},
	{CodeRejectedInvalidJob, true},
	{CodeRejectedScopeNotAllowed, false},
	{CodeRejectedOutsideWindow, false},
	{CodeRejectedRateLimited, false},
	{CodeRejectedBusy, false},
	{CodeRejectedNotAllowed, false},
	// The hub knows the agent's verbs from its hello and should never send another one; if it does, that is worth seeing.
	{CodeRejectedUnsupported, true},
	{CodePrecheckDiskSpace, true},
	{CodePrecheckPackageDBBroken, true},
	{CodePrecheckRepoUnreachable, true},
	{CodePrecheckWouldRemove, true},
	// An update that would bring malware or a known hole: someone has to look. A release too new waits by design.
	{CodePrecheckVersionFlagged, true},
	{CodePrecheckVersionTooNew, false},
	{CodePrecheckVettingUnavailable, true},
	// Code from outside the registry: someone has to look at where the dependency points.
	{CodePrecheckNotFromRegistry, true},
	{CodeFailedPackageManager, true},
	{CodeFailedVerification, true},
	{CodeFailedHealth, true},
	{CodeFailedWindowsUpdate, true},
	{CodeFailedTargetAuth, true},
	{CodeFailedTargetTLS, true},
	{CodeFailedTargetHostKey, true},
	{CodeFailedConfigBackup, true},
	{CodeFailedBackup, true},
	{CodeTimeout, true},
	{CodeAborted, false},
	{CodeInterrupted, true},
}

// AllCodes returns every result code in table order (the options of the Home Assistant enum sensor).
func AllCodes() []Code {
	out := make([]Code, len(codes))
	for i, c := range codes {
		out[i] = c.code
	}
	return out
}

// Valid reports whether c is a known result code.
func (c Code) Valid() bool {
	for _, k := range codes {
		if k.code == c {
			return true
		}
	}
	return false
}

// Problem reports whether c sets binary_sensor.<node>_problem. Unknown codes count as a problem.
func (c Code) Problem() bool {
	for _, k := range codes {
		if k.code == c {
			return k.problem
		}
	}
	return true
}

// SourceState is the state of one update source or app (design 7.2).
type SourceState string

const (
	SourceUpToDate         SourceState = "up_to_date"
	SourceUpdatesAvailable SourceState = "updates_available"
	SourceCheckFailed      SourceState = "check_failed"
	SourceUnknown          SourceState = "unknown"
)

// RebootReason says why a host needs a reboot (design 7.4).
type RebootReason string

const (
	RebootNone          RebootReason = ""
	RebootKernel        RebootReason = "kernel"
	RebootLibraries     RebootReason = "libraries"
	RebootWindowsUpdate RebootReason = "windows_update"
	RebootOther         RebootReason = "other"
)
