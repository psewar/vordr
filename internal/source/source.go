// Package source defines what every update source reports (design/architecture.md, section 11).
package source

import (
	"strconv"
	"strings"
	"time"

	"github.com/psewar/vordr/internal/protocol"
)

// Presence: only present and absent decide whether entities are created or removed; unknown changes nothing.
type Presence string

const (
	Present Presence = "present"
	Absent  Presence = "absent"
	Unknown Presence = "unknown"
)

// An appliance's configuration backup (ADR-0090): the proxy agent reports it as a source of its own, with the time of
// the newest good export as a fact. The hub's backup check reads that fact for hosts with config_backup: true.
const (
	ConfigBackup       = "config-backup"    // Report.Source
	FactConfigBackupAt = "config_backup_at" // RFC 3339, the newest good export
	FactConfigBackupSH = "config_backup_sha256"
)

// Package is one pending update.
type Package struct {
	Name     string   `json:"name"`
	From     string   `json:"from,omitempty"` // empty: newly installed as a dependency
	To       string   `json:"to"`
	Origins  []string `json:"origins,omitempty"`
	Security bool     `json:"security"`
	// Major: the upgrade changes the major version (MajorJump). Set by sources whose versions are the vendor's own
	// (winget); a distribution's stable updates keep their major version anyway.
	Major bool `json:"major,omitempty"`
	// KeptBack: the host's install.mode for scope all (upgrade) leaves it pending, since it needs packages added or
	// removed; only full-upgrade would take it (ADR-0127). Still pending, but no install of scope all brings it.
	KeptBack bool `json:"kept_back,omitempty"`
}

// MajorJump: from and to begin with different numbers ("20.11.1" -> "22.2.0", "2.7.1000" -> "3.0.2000"). Otherwise
// "update all" would take a runtime or an agent across major versions without anyone seeing it. Not a jump: a new
// package (no from), a version that does not begin with a number, and calendar versions
// (a first number from 1000 on: "2025.10" -> "2026.1" is a year, not a breaking change).
func MajorJump(from, to string) bool {
	a, okA := leadingNumber(from)
	b, okB := leadingNumber(to)
	return okA && okB && a != b && a < 1000 && b < 1000
}

// leadingNumber: the number a version begins with, after an epoch ("1:2.3" -> 2).
func leadingNumber(v string) (int, bool) {
	if i := strings.IndexByte(v, ':'); i >= 0 {
		v = v[i+1:]
	}
	end := 0
	for end < len(v) && end < 9 && v[end] >= '0' && v[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, false
	}
	n, err := strconv.Atoi(v[:end])
	return n, err == nil
}

// Report is the result of one check of one source.
type Report struct {
	Source   string               `json:"source"`
	State    protocol.SourceState `json:"state"`
	Code     protocol.Code        `json:"code,omitempty"` // set with check_failed
	Pending  int                  `json:"pending"`
	Security int                  `json:"security"`
	// KeptBack: of Pending, the packages marked KeptBack (ADR-0127); 0 with any other mode.
	KeptBack  int       `json:"kept_back,omitempty"`
	Packages  []Package `json:"packages,omitempty"`
	Removals  []string  `json:"removals,omitempty"` // packages the upgrade would remove
	CheckedAt time.Time `json:"checked_at"`
	// Facts the source found out about the host, e.g. apt_version, install_mode, unattended_upgrades.
	Facts map[string]string `json:"facts,omitempty"`
	// Detail is an excerpt of the tool's output for humans. Nothing ever decides on it.
	Detail string `json:"detail,omitempty"`
}

// Reboot says whether the host needs a reboot and why.
type Reboot struct {
	Required bool                  `json:"required"`
	Reason   protocol.RebootReason `json:"reason,omitempty"`
	Packages []string              `json:"packages,omitempty"`
	Running  string                `json:"kernel_running,omitempty"`
	Newest   string                `json:"kernel_newest,omitempty"`
	// Checked lists the signals that gave an answer (kernel, reboot_required_file). Empty means the need for a
	// reboot is unknown on this host - Required=false is then no statement.
	Checked []string `json:"checked"`
}

// InstallRequest is one install run as the local policy allowed it.
type InstallRequest struct {
	Scope protocol.Scope
	// Package: with an app scope, the package the agent's own app list names for it. Never from the hub.
	Package string
	// Mode for scope all: "upgrade" or "full-upgrade" as the local policy says; "" = the default, full-upgrade
	// (ADR-0076).
	Mode string
	// BeforeChange runs after the prechecks, before any package is touched (the run journal). An error stops the
	// run there.
	BeforeChange func() error
}

// Progress is what an install run reports while it runs.
type Progress struct {
	Phase   protocol.Phase
	Percent int    // of the whole run, 0..100
	Item    string // the package being downloaded or installed, when known
}

// InstallResult: Code is ok or the reason the run failed; ok_reboot_required is for the caller to decide.
type InstallResult struct {
	Code    protocol.Code
	Planned int    // packages the run set out to change
	Detail  string // excerpt of the tool output, for humans only
	// Packages the run set out to change, by name.
	Packages []string
	// Log: the package manager's whole output of the run (download and installation), for the run log on the host.
	Log []byte
	// OutdatedServices still run with replaced libraries after the run (needrestart, where installed).
	OutdatedServices []string
	// Replaced: packages the run removes because a package of the run replaces them, "old -> new".
	Replaced []string
}
