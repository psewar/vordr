// Package apt is the update source for Debian, Ubuntu, Raspberry Pi OS, Proxmox VE and Proxmox Backup Server.
//
// It only reads exit codes and apt's structured simulation lines ("Inst", "Remv"); when a command fails, it asks
// the system for the state (dpkg --audit) and derives the code from that. Error texts only go into Detail.
package apt

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/runner"
	"github.com/psewar/vordr/internal/source"
)

// Paths the source reads; tests point them into a temporary directory.
type Paths struct {
	ListsDir       string // /var/lib/apt/lists
	ListsLock      string // /var/lib/apt/lists/lock
	DpkgLock       string // /var/lib/dpkg/lock-frontend
	ArchivesDir    string // /var/cache/apt/archives
	RebootRequired string // /var/run/reboot-required (Ubuntu, update-notifier)
	RebootNotifier string // /usr/share/update-notifier/notify-reboot-required: the file above is maintained
	BootDir        string // /boot
	OSRelease      string // /proc/sys/kernel/osrelease
}

type Apt struct {
	Run      runner.Runner
	Stream   runner.Streamer // install runs, which report progress while apt works
	LookPath func(string) (string, error)
	IsRoot   bool
	Now      func() time.Time
	Paths    Paths
	// PolicyMode: install.mode of the local policy. Check reports the mode scope all runs with it as the fact
	// install_mode (ADR-0095); Install takes the mode from its request, which the same policy fills.
	PolicyMode string
	// Release: the host's VERSION_ID (/etc/os-release). A security run takes dependencies only from the security
	// archives and this release (ADR-0116); "" (unknown) leaves only the security archives.
	Release string
}

// New returns the source for this host.
func New() *Apt {
	return &Apt{
		Run:      runner.Exec{},
		Stream:   runner.Exec{},
		LookPath: exec.LookPath,
		IsRoot:   os.Geteuid() == 0,
		Now:      time.Now,
		Paths: Paths{
			ListsDir:       "/var/lib/apt/lists",
			ListsLock:      "/var/lib/apt/lists/lock",
			DpkgLock:       "/var/lib/dpkg/lock-frontend",
			ArchivesDir:    "/var/cache/apt/archives",
			RebootRequired: "/var/run/reboot-required",
			RebootNotifier: "/usr/share/update-notifier/notify-reboot-required",
			BootDir:        "/boot",
			OSRelease:      "/proc/sys/kernel/osrelease",
		},
	}
}

// Stable, untranslated output and no questions from maintainer scripts.
var env = []string{"LC_ALL=C", "DEBIAN_FRONTEND=noninteractive"}

// Lists refresh outcomes, reported as the fact lists_refresh.
const (
	RefreshDone        = "refreshed"
	RefreshFailed      = "failed"
	RefreshNotWanted   = "skipped_by_request"
	RefreshNotRoot     = "skipped_not_root"
	RefreshBusy        = "skipped_busy"
	RefreshLockUnknown = "skipped_lock_unknown"
)

// Detect: apt-get on the PATH means apt is this host's package manager.
func (a *Apt) Detect() source.Presence {
	_, err := a.LookPath("apt-get")
	switch {
	case err == nil:
		return source.Present
	case errors.Is(err, exec.ErrNotFound):
		return source.Absent
	}
	return source.Unknown
}

// Check refreshes the package lists if wanted and possible, then simulates a full upgrade and reports what is
// pending. The simulation always uses full-upgrade so that held-back packages count as pending too; the fact
// install_mode is the mode scope all runs on this host (ADR-0095).
func (a *Apt) Check(ctx context.Context, refresh bool) source.Report {
	rep := source.Report{Source: "apt", State: protocol.SourceUnknown, CheckedAt: a.Now(), Facts: map[string]string{}}
	ver := a.aptVersion(ctx)
	rep.Facts["apt_version"] = ver
	rep.Facts["install_mode"] = runMode(a.PolicyMode)
	rep.Facts["unattended_upgrades"] = a.unattendedUpgrades(ctx)

	switch {
	case !refresh:
		rep.Facts["lists_refresh"] = RefreshNotWanted
	case !a.IsRoot:
		rep.Facts["lists_refresh"] = RefreshNotRoot
	default:
		switch lockState(a.Paths.ListsLock) {
		case lockBusy:
			rep.Facts["lists_refresh"] = RefreshBusy
		case lockUnknown:
			rep.Facts["lists_refresh"] = RefreshLockUnknown
		default:
			res := a.Run.Run(ctx, env, "apt-get", updateArgs(ver)...)
			if !res.Ran() {
				return failed(rep, protocol.CodeFailedPackageManager, res)
			}
			if res.ExitCode != 0 {
				rep.Facts["lists_refresh"] = RefreshFailed
				return failed(rep, protocol.CodePrecheckRepoUnreachable, res)
			}
			rep.Facts["lists_refresh"] = RefreshDone
		}
	}
	if t, ok := newestModTime(a.Paths.ListsDir); ok {
		rep.Facts["lists_updated_at"] = t.UTC().Format(time.RFC3339)
	}

	sim := a.Run.Run(ctx, env, "apt-get", "-s", "-q", "full-upgrade")
	if !sim.Ran() || sim.ExitCode != 0 {
		return failed(rep, a.classifyFailure(ctx), sim)
	}
	rep.Packages, rep.Removals = ParseSimulation(sim.Stdout)
	rep.Pending = len(rep.Packages)
	for _, p := range rep.Packages {
		if p.Security {
			rep.Security++
		}
	}
	if runMode(a.PolicyMode) == "upgrade" && rep.Pending > 0 {
		a.markKeptBack(ctx, &rep)
	}
	rep.State = protocol.SourceUpToDate
	if rep.Pending > 0 {
		rep.State = protocol.SourceUpdatesAvailable
	}
	return rep
}

// markKeptBack: with install.mode upgrade, an install of scope all runs "apt-get upgrade", which leaves packages that
// need others added or removed (a new kernel, a changed dependency). It simulates that too and marks what only the
// full simulation has (ADR-0127). When that simulation fails, nothing is marked: the counts stay as without the mode.
func (a *Apt) markKeptBack(ctx context.Context, rep *source.Report) {
	sim := a.Run.Run(ctx, env, "apt-get", "-s", "-q", "upgrade")
	if !sim.Ran() || sim.ExitCode != 0 {
		return
	}
	taken, _ := ParseSimulation(sim.Stdout)
	names := make(map[string]bool, len(taken))
	for _, p := range taken {
		names[p.Name] = true
	}
	for i := range rep.Packages {
		if !names[rep.Packages[i].Name] {
			rep.Packages[i].KeptBack = true
			rep.KeptBack++
		}
	}
}

func failed(rep source.Report, code protocol.Code, res runner.Result) source.Report {
	rep.State, rep.Code = protocol.SourceCheckFailed, code
	rep.Detail = excerpt(res)
	return rep
}

// classifyFailure asks the package database for its state after a failed apt run.
func (a *Apt) classifyFailure(ctx context.Context) protocol.Code {
	audit := a.Run.Run(ctx, env, "dpkg", "--audit")
	if audit.Ran() && (audit.ExitCode != 0 || len(strings.TrimSpace(string(audit.Stdout))) > 0) {
		return protocol.CodePrecheckPackageDBBroken
	}
	return protocol.CodeFailedPackageManager
}

// aptVersion: first line of "apt-get --version" is "apt 3.2.0 (amd64)".
func (a *Apt) aptVersion(ctx context.Context) string {
	res := a.Run.Run(ctx, env, "apt-get", "--version")
	if !res.Ran() || res.ExitCode != 0 {
		return ""
	}
	line, _, _ := strings.Cut(string(res.Stdout), "\n")
	f := strings.Fields(line)
	if len(f) < 2 || f[0] != "apt" {
		return ""
	}
	return f[1]
}

// defaultMode for scope all when the policy names none (ADR-0076): full-upgrade also installs packages that need new
// dependencies (a new kernel, firmware); a run that would remove packages is still refused.
const defaultMode = "full-upgrade"

// runMode is the command scope all runs: the mode install.mode names, else defaultMode. Install runs it and Check
// reports it, so the fact cannot drift from the run.
func runMode(policy string) string {
	if policy != "" {
		return policy
	}
	return defaultMode
}

// unattendedUpgrades reads APT::Periodic::Unattended-Upgrade through apt-config: enabled, disabled or unknown.
func (a *Apt) unattendedUpgrades(ctx context.Context) string {
	res := a.Run.Run(ctx, env, "apt-config", "shell", "UU", "APT::Periodic::Unattended-Upgrade")
	if !res.Ran() || res.ExitCode != 0 {
		return "unknown"
	}
	v := strings.Trim(strings.TrimPrefix(strings.TrimSpace(string(res.Stdout)), "UU="), "'")
	if v == "" || v == "0" {
		return "disabled"
	}
	return "enabled"
}

// versionAtLeast compares the leading numbers of a dotted version ("2.6.1", "2.7.14build2").
func versionAtLeast(v string, want ...int) bool {
	parts := strings.Split(v, ".")
	for i, w := range want {
		if i >= len(parts) {
			return false
		}
		n, ok := leadingInt(parts[i])
		if !ok {
			return false
		}
		if n != w {
			return n > w
		}
	}
	return true
}

func leadingInt(s string) (int, bool) {
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	n, err := strconv.Atoi(s[:end])
	return n, err == nil
}

// newestModTime of the package list files: when the lists were last refreshed successfully.
func newestModTime(dir string) (time.Time, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return time.Time{}, false
	}
	var newest time.Time
	for _, e := range entries {
		if e.IsDir() || e.Name() == "lock" {
			continue
		}
		if info, err := e.Info(); err == nil && info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	return newest, !newest.IsZero()
}

// excerpt keeps the last lines of the tool output for humans.
func excerpt(res runner.Result) string {
	if !res.Ran() {
		return res.Err.Error()
	}
	text := strings.TrimSpace(string(res.Stderr))
	if text == "" {
		text = strings.TrimSpace(string(res.Stdout))
	}
	lines := strings.Split(text, "\n")
	if len(lines) > 10 {
		lines = lines[len(lines)-10:]
	}
	text = strings.Join(lines, "\n")
	if len(text) > 800 {
		text = text[len(text)-800:]
	}
	return text
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func readTrim(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
