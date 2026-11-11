package wire

import (
	"time"

	"github.com/psewar/vordr/internal/inventory"
	"github.com/psewar/vordr/internal/protocol"
)

// HostView is the transport-neutral state of one approved host, as the hub sends it to Home Assistant
// (design/ha-integration.md, section 5). Whatever the hub does not know is nil - never a guess.
type HostView struct {
	Node   string     `json:"node"`
	Device DeviceView `json:"device"`
	Online bool       `json:"online"`
	// Power: "sleeping" or "shutdown" while the host is away after saying so; "" otherwise.
	Power   string       `json:"power,omitempty"`
	Problem *ProblemView `json:"problem"` // nil: no problem
	Pending *int         `json:"pending"` // nil: unknown (no report, or the check failed)
	// Security is nil exactly when Pending is.
	Security *int `json:"security"`
	// KeptBack: of Pending, the updates the host's install.mode upgrade leaves for an install of scope all (ADR-0127);
	// nil when there are none.
	KeptBack *int `json:"kept_back,omitempty"`
	// Installable: the pending updates the install button would take under the host's policy (ADR-0106); nil exactly
	// when Pending is. Home Assistant shows the system entity as an update only when it is above zero.
	Installable *int `json:"installable"`
	// NextInstall: while the agent's minimum interval since its last run that changed packages still runs, when it
	// takes an install of the system packages or an app again (ADR-0110); nil when now, or when the hub cannot tell.
	NextInstall *time.Time `json:"next_install,omitempty"`
	// System is nil when the host has no package source - or no report yet, see LastReport.
	System *SystemView `json:"system"`
	Reboot RebootView  `json:"reboot"`
	// LastReport nil: the host never reported, so whether it has package entities is not decided yet; neither create
	// nor remove them.
	LastReport *time.Time `json:"last_report"`
	LastRun    *RunView   `json:"last_run"`
	// FailedInstall: the host's kept failed install of the system packages (ADR-0124), with its detail, as long as it
	// is a reason of the problem; LastRun may show a later job. nil without one.
	FailedInstall *RunView     `json:"failed_install,omitempty"`
	Capabilities  Capabilities `json:"capabilities"`
	// Backup: nil when the hub has no backup check (no backup section in hub.yaml).
	Backup *BackupView `json:"backup,omitempty"`
	// AgentRelease: the agent version the hub hands out (signed by the operator); "" when it has none.
	AgentRelease string `json:"agent_release,omitempty"`
	// AgentReleaseNoBinary: the release has no binary for this host's platform, so the hub does not offer it here.
	AgentReleaseNoBinary bool `json:"agent_release_no_binary,omitempty"`
	// Queued: commands waiting for the host's running run, as verb or verb:scope (e.g. "install:all").
	Queued []string `json:"queued,omitempty"`
	// Packages: packages of pip, pipx and npm and what the OSV data says about them; nil when the host never sent an
	// inventory (an agent before 0.9.0, or packages.inventory: false).
	Packages *PackagesView `json:"packages,omitempty"`
	// Containers: the images of the running containers and what their registries say (ADR-0113); nil when the host
	// does not report them (packages.containers off, or an agent before it).
	Containers *ContainersView `json:"containers,omitempty"`
}

// ContainersView is the host's container location: only reported, Vördr does not update images (ADR-0113).
type ContainersView struct {
	CheckedAt time.Time `json:"checked_at"`
	// Code: why the images could not be read (no_engine, engine_unreachable); empty: read.
	Code   string `json:"code,omitempty"`
	Images int    `json:"images"` // images of running containers
	// Local: images without a repo digest (built on the host), never compared.
	Local int `json:"local,omitempty"`
	// Outdated: images rebuilt under their tag or with a newer tag of the same form; Ready: of those, the ones with a
	// candidate (at least min_age_days old). Both nil while the images were never compared with their registries
	// (packages.outdated off, or the engine did not answer yet).
	Outdated *int `json:"outdated"`
	Ready    *int `json:"ready"`
	// Installable: of those, the images whose rebuild the install button would take - a rebuild old enough, and a
	// service the host lets Home Assistant update (ADR-0119). nil like Ready.
	Installable *int `json:"installable,omitempty"`
	// ComparedAt: when the agent last compared the images; MinAgeDays: its policy's minimum age of a candidate.
	ComparedAt *time.Time `json:"compared_at,omitempty"`
	MinAgeDays int        `json:"min_age_days,omitempty"`
	// OutdatedFailed: images whose registry gave no answer; NotInRegistry: images the registry does not give
	// anonymous callers (private, or gone).
	OutdatedFailed int `json:"outdated_failed,omitempty"`
	NotInRegistry  int `json:"not_in_registry,omitempty"`
	// CompareFailed: the images behind OutdatedFailed, each with why (ADR-0126), at most MaxUpdates, in the inventory's
	// order. Empty from an agent before ADR-0126, which reports only the count.
	CompareFailed []ImageCompareFailedView `json:"compare_failed,omitempty"`
	// Updates: at most MaxUpdates outdated images, those with a candidate first.
	Updates []ImageUpdateView `json:"updates,omitempty"`
	// Known vulnerabilities inside the images (ADR-0120). Fixable: images with an advisory a newer package version
	// fixes - fixed in the distribution, which a newer image can bring; Unfixed: images whose advisories have no fix
	// yet. Both nil while no image was matched (the hub has no data for their distribution, or the agent did not read
	// them yet).
	Fixable *int `json:"fixable,omitempty"`
	Unfixed *int `json:"unfixed,omitempty"`
	// FixableNewer (ADR-0130): of Fixable, the images that have a candidate - a newer image old enough to install. The
	// others wait for their publisher to rebuild. nil while Fixable is, or while the images were never compared with
	// their registries (whether a newer image exists is unknown then).
	FixableNewer *int `json:"fixable_newer,omitempty"`
	// Vulnerable: at most MaxUpdates images with advisories or a reason they could not be matched, fixable ones first.
	Vulnerable []ImageFindingsView `json:"vulnerable,omitempty"`
}

// ImageCompareFailedView is one image whose comparison with its registry got no answer, and why (ADR-0126): no_answer,
// rate_limited, request_budget, manifest_share, tag_list_too_long.
type ImageCompareFailedView struct {
	Image      string   `json:"image"`
	Tag        string   `json:"tag"`
	Containers []string `json:"containers,omitempty"`
	Code       string   `json:"code"`
}

// ImageFindingsView is what the hub's OSV data says about the packages inside one image (ADR-0120). One CVE published
// several times by the distribution counts once; advisories the distribution rates "unimportant" do not count.
type ImageFindingsView struct {
	Image      string   `json:"image"`
	Tag        string   `json:"tag"`
	Containers []string `json:"containers,omitempty"`
	Release    string   `json:"release,omitempty"` // the OSV ecosystem matched: Debian:13, Alpine:v3.23
	Fixable    int      `json:"fixable"`           // advisories a newer package version fixes
	Unfixed    int      `json:"unfixed"`           // advisories without a fix yet
	IDs        []string `json:"ids,omitempty"`     // at most MaxFindingIDs, fixable ones first
	// Kernel: advisories of the kernel's own packages in the image, left out of Fixable and Unfixed: a container runs
	// the host's kernel (ADR-0122). Shown, so nothing is dropped silently.
	Kernel int `json:"kernel,omitempty"`
	// Newer (ADR-0130): the image has a candidate, a rebuild or a newer tag at least min_age_days old. Without it, its
	// fixable advisories are fixed in the distribution, but no newer image is there to install yet.
	Newer bool `json:"newer,omitempty"`
	// Code: why the image was not matched - not_read, no_package_db, unreadable, unknown_release,
	// unsupported_distribution, unsupported_release, no_data. Empty: matched.
	Code string `json:"code,omitempty"`
}

// MaxFindingIDs bounds the advisory ids per image in a view.
const MaxFindingIDs = 20

// ImageUpdateView is one outdated image: rebuilt under its tag, a newer tag of the same form, or both.
type ImageUpdateView struct {
	Image      string   `json:"image"` // repository with its registry
	Tag        string   `json:"tag"`
	Containers []string `json:"containers,omitempty"`
	// Rebuilt: the tag points to another build; RebuiltAt: when it was built; RebuiltReady: it is at least
	// min_age_days old.
	Rebuilt      bool       `json:"rebuilt,omitempty"`
	RebuiltAt    *time.Time `json:"rebuilt_at,omitempty"`
	RebuiltReady bool       `json:"rebuilt_ready,omitempty"`
	// Latest: the newest tag of the same form; Candidate: the newest one at least min_age_days old; LatestAt: when
	// Latest was built, while it is younger; Major: Latest changes the first number.
	Latest    string     `json:"latest,omitempty"`
	Candidate string     `json:"candidate,omitempty"`
	LatestAt  *time.Time `json:"latest_at,omitempty"`
	Major     bool       `json:"major,omitempty"`
	// SameMajor (ADR-0131): beside a candidate that is a new major version (or a Latest that is one, without a
	// candidate), the newest tag of the running major version at least min_age_days old - the smaller step.
	SameMajor string `json:"same_major,omitempty"`
	// Services: the Compose services of the image that may be moved to a rebuild by button ("<project>/<service>",
	// ADR-0119). Only a rebuild is installed; a newer tag needs the Compose file changed and stays the operator's.
	Services []string `json:"services,omitempty"`
}

// PackagesView is the host's last inventory, matched against the hub's OSV data (design 11.2).
type PackagesView struct {
	CheckedAt time.Time      `json:"checked_at"`
	Total     int            `json:"total"` // packages in all locations
	Locations []LocationView `json:"locations"`
	// Malicious: installed packages (name and version in one location) that an OSV entry calls malware.
	// Vulnerable: those with an advisory, malware not counted again. Both nil when the hub has no OSV data for an
	// ecosystem the host has packages of: unknown, not none.
	Vulnerable *int `json:"vulnerable"`
	Malicious  *int `json:"malicious"`
	// Waiting (ADR-0129): of the vulnerable ones, those whose fix nothing on the host can take now (reach held, no_fix,
	// too_new); only the others make the problem. Set together with Vulnerable.
	Waiting *int `json:"waiting,omitempty"`
	// Undecided: advisories that may apply but whose versions do not parse; neither counted nor dropped silently.
	Undecided int `json:"undecided,omitempty"`
	// Findings: at most MaxFindings, malware first, then by rating.
	Findings []FindingView `json:"findings,omitempty"`
	// Outdated: direct packages with a newer release (11.3); nil when the agent does not ask the registries.
	Outdated *int `json:"outdated"`
	// OutdatedAt: when the agent last asked the registries; MinAgeDays: its policy's minimum age of a candidate.
	OutdatedAt *time.Time `json:"outdated_at,omitempty"`
	MinAgeDays int        `json:"min_age_days,omitempty"`
	// MinAgeDaysSecurity: its policy's shorter minimum age for fixes (ADR-0111); 0 from agents before it.
	MinAgeDaysSecurity int `json:"min_age_days_security,omitempty"`
	// Updates: at most MaxUpdates outdated packages, those with a candidate first.
	Updates []UpdateView `json:"updates,omitempty"`
}

// MaxFindings caps PackagesView.Findings; the counts are always complete.
const MaxFindings = 50

// MaxUpdates caps PackagesView.Updates.
const MaxUpdates = 100

// UpdateView is one package with a newer release.
type UpdateView struct {
	Location  string `json:"location"`
	Name      string `json:"name"`
	Installed string `json:"installed"`
	Latest    string `json:"latest"`
	// Candidate: what an update would install (public for at least min_age_days); empty while every newer release
	// is younger - LatestAt says since when the latest one is public.
	Candidate string     `json:"candidate,omitempty"`
	LatestAt  *time.Time `json:"latest_at,omitempty"`
	Major     bool       `json:"major,omitempty"`
	// CandidateFlagged: OSV entries that apply to the candidate itself (malware or vulnerable); an update to it is
	// refused (11.4).
	CandidateFlagged []string `json:"candidate_flagged,omitempty"`
	// Fix: a release younger than min_age_days but public for min_age_days_security that fixes the installed
	// version's advisories and has no OSV entry itself (ADR-0111). An update takes it instead of Candidate.
	Fix string `json:"fix,omitempty"`
}

type LocationView struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Ecosystem  string `json:"ecosystem"`
	Path       string `json:"path"`
	Packages   int    `json:"packages"`
	Code       string `json:"code,omitempty"` // unreadable, too_many_packages
	Skipped    int    `json:"skipped,omitempty"`
	Vulnerable int    `json:"vulnerable,omitempty"`
	Malicious  int    `json:"malicious,omitempty"`
	Outdated   int    `json:"outdated,omitempty"`
	// OutdatedFailed: direct packages whose registry did not answer in the last outdated run; NotInRegistry: those
	// the registry does not have (from a file, a git URL, a private index) - nothing to compare against.
	OutdatedFailed int `json:"outdated_failed,omitempty"`
	NotInRegistry  int `json:"not_in_registry,omitempty"`
}

// FindingView is one installed package with the advisories that apply to it.
type FindingView struct {
	Location   string   `json:"location"`
	Name       string   `json:"name"`
	Version    string   `json:"version"`
	Via        string   `json:"via,omitempty"` // npm: the top-level package it came with
	Malicious  bool     `json:"malicious,omitempty"`
	Advisories []string `json:"advisories"`        // OSV ids (https://osv.dev/vulnerability/<id>)
	Aliases    []string `json:"aliases,omitempty"` // CVE and GHSA ids
	Severity   string   `json:"severity,omitempty"`
	// FixedIn: the lowest version that fixes every advisory; empty when one names no fix, and always for malware.
	FixedIn string `json:"fixed_in,omitempty"`
	// Reach (ADR-0129, vulnerable only): where the fix is - one of the Reach values. HeldBy: what keeps the package
	// below FixedIn (reach held, update_via).
	Reach  string           `json:"reach,omitempty"`
	HeldBy []inventory.Hold `json:"held_by,omitempty"`
}

// Where the fix of a vulnerable finding is (ADR-0129). The first four make the problem vulnerable_package.
const (
	ReachUpdate    = "update"     // a Vördr update of the package takes the fix
	ReachUpdateVia = "update_via" // held, but what holds it, or the package it came with, has an update ready
	ReachRefresh   = "refresh"    // nothing holds it; the location's dependencies can be refreshed
	ReachByHand    = "by_hand"    // nothing holds it, but Vördr does not or may not update it there
	ReachHeld      = "held"       // something on the host keeps it below the fix (HeldBy)
	ReachNoFix     = "no_fix"     // no release fixes every advisory
	ReachTooNew    = "too_new"    // the fix is younger than the minimum age
)

// Actionable reports whether a reach is something the operator can do now.
func Actionable(reach string) bool {
	return reach != ReachHeld && reach != ReachNoFix && reach != ReachTooNew
}

// OSVView is the state of the hub's OSV data for one ecosystem. State: ok, stale (refresh failing for longer than
// stale_hours), missing (none yet); Code: why the last refresh failed (download_failed, bad_archive, store_failed,
// implausible_dump).
type OSVView struct {
	Ecosystem  string     `json:"ecosystem"`
	State      string     `json:"state"`
	Code       string     `json:"code,omitempty"`
	Checked    *time.Time `json:"checked,omitempty"`  // last download, or answer "not modified"
	Modified   *time.Time `json:"modified,omitempty"` // newest entry of the data
	Advisories int        `json:"advisories,omitempty"`
	Malicious  int        `json:"malicious,omitempty"`
}

// BackupView is the host's last backup on Proxmox Backup Server as the hub's backup check sees it. An install only
// starts with state ok or exempt. State: ok, stale (older than max_age_hours), missing (none in the group),
// unreachable (PBS did not answer), unmapped (hub.yaml names no group for the host), exempt (no backup by decision;
// Exempt is the operator's reason). BeforeInstall: the host is a guest that gets a fresh backup right before each
// install, made by this node's agent (ADR-0096); then its age does not stop an install. ConfigBackup: the host is an
// appliance whose proxy agent backs up its configuration (ADR-0090); Last is then that backup as the agent reported
// it, missing means it reported none, and PBS plays no part (no group, never unreachable).
type BackupView struct {
	Group         string     `json:"group,omitempty"`
	Last          *time.Time `json:"last,omitempty"`
	MaxAgeHours   int        `json:"max_age_hours"`
	State         string     `json:"state"`
	Exempt        string     `json:"exempt,omitempty"`
	BeforeInstall string     `json:"before_install,omitempty"`
	ConfigBackup  bool       `json:"config_backup,omitempty"`
}

type DeviceView struct {
	Name         string `json:"name"` // "Vördr <node>"
	Model        string `json:"model"`
	AgentVersion string `json:"agent_version"`
}

// ProblemView carries codes only (reasons, result codes) and no text: Home Assistant writes the texts, in the
// language of whoever reads them. HubView.Limits holds the thresholds the texts mention.
type ProblemView struct {
	Reason          string        `json:"reason"` // the first of Reasons
	Reasons         []string      `json:"reasons"`
	CheckFailedCode protocol.Code `json:"check_failed_code,omitempty"`
}

// SystemView feeds the update entity "Systempakete": installed_version names the system. Whether something is
// pending comes from HostView.Pending/Security (nil: unknown, the check failed); the integration writes
// latest_version and the release summary from those and Packages in its own language.
type SystemView struct {
	InstalledVersion string        `json:"installed_version"`
	Packages         []PackageView `json:"packages,omitempty"` // at most MaxPackages pending ones, in report order
}

// MaxPackages caps SystemView.Packages: HA shows at most 255 characters of release summary anyway.
const MaxPackages = 30

type PackageView struct {
	Name     string `json:"name"`
	From     string `json:"from,omitempty"` // empty: newly installed as a dependency
	To       string `json:"to"`
	Security bool   `json:"security,omitempty"`
	Major    bool   `json:"major,omitempty"` // the upgrade changes the major version (source.MajorJump)
}

// RebootView: Required is nil when no signal answered on this host (e.g. a container) - unknown, not "no".
type RebootView struct {
	Required *bool                 `json:"required"`
	Reason   protocol.RebootReason `json:"reason,omitempty"`
	Packages []string              `json:"packages,omitempty"`
	Checked  []string              `json:"checked"`
	// Since: when the hub first saw the host needing a reboot (only while it does).
	Since *time.Time `json:"since,omitempty"`
	// The kernel the host runs and the newest one installed, where the kernel signal answered (not in containers).
	KernelRunning string `json:"kernel_running,omitempty"`
	KernelNewest  string `json:"kernel_newest,omitempty"`
}

type RunView struct {
	State      string                 `json:"state"` // phase while running, result code when done
	JobID      string                 `json:"job_id"`
	Verb       protocol.Verb          `json:"verb"`
	Scope      string                 `json:"scope,omitempty"`
	By         string                 `json:"by,omitempty"` // who started it: ha:<client>, admin, rollout
	Started    time.Time              `json:"started"`
	Finished   *time.Time             `json:"finished,omitempty"`
	Reason     protocol.InvalidReason `json:"reason,omitempty"`
	LogExcerpt string                 `json:"log_excerpt,omitempty"`
	// OutsideWindow: why it was asked for outside the host's window (ADR-0087); empty for a run in the window.
	OutsideWindow string `json:"outside_window,omitempty"`
	// While running: percent of the whole run and the package being worked on, as far as the agent knows them.
	Progress *int   `json:"progress,omitempty"`
	Item     string `json:"item,omitempty"`
	// Once finished: what an install run did (packages, duration, run log on the host, services to restart).
	Summary *RunSummary `json:"summary,omitempty"`
}

// Capabilities says which actions Home Assistant may offer: the agent announced the verb AND its local policy
// allows it. A policy alone offers nothing an agent version cannot do. The hub refuses every command not offered
// here (with install_security also allowed when it is the default scope).
type Capabilities struct {
	Check           bool      `json:"check"`
	Install         bool      `json:"install"`          // install with DefaultScope (install button of the update entity)
	InstallSecurity bool      `json:"install_security"` // a button of its own: security allowed and not the default
	Reboot          bool      `json:"reboot"`
	AgentUpdate     bool      `json:"agent_update"`
	DefaultScope    string    `json:"default_scope,omitempty"`
	Apps            []AppInfo `json:"apps,omitempty"` // apps that may be installed one by one
	// PackageUpdates: locations whose pip/npm packages may be updated (commands install_pkg:<location>[/<name>]).
	PackageUpdates []string `json:"package_updates,omitempty"`
	// DependencyRefresh: of those, the locations whose dependencies with findings may be refreshed (commands
	// refresh_deps:<location>, ADR-0104).
	DependencyRefresh []string `json:"dependency_refresh,omitempty"`
	// ImageUpdates: the Compose services whose image may be moved to the rebuild of its tag ("<project>/<service>",
	// ADR-0119): command install_image:<project>/<service> for one, install_images for all of them that have one.
	ImageUpdates []string `json:"image_updates,omitempty"`
	// OutsideWindow: install and reboot commands may carry a reason and then run outside the host's window (ADR-0087).
	OutsideWindow bool `json:"outside_window"`
}

// HubView is the hub's own state for Home Assistant.
type HubView struct {
	Version     string        `json:"version"`
	Protocol    int           `json:"protocol"`
	HostsOnline int           `json:"hosts_online"`
	Waiting     []WaitingHost `json:"waiting"` // hosts waiting for approval at the hub
	Limits      LimitsView    `json:"limits"`
	// OSV: the hub's vulnerability data per ecosystem; absent without an osv section in hub.yaml.
	OSV []OSVView `json:"osv,omitempty"`
}

// LimitsView: the thresholds after which a host counts as having a problem, in the units of hub.yaml.
type LimitsView struct {
	OfflineAfterMinutes    int `json:"offline_after_minutes"`
	StaleAfterDays         int `json:"stale_after_days"`
	RebootPendingAfterDays int `json:"reboot_pending_after_days"`
	NoReportAfterHours     int `json:"no_report_after_hours"`
}

type WaitingHost struct {
	Node        string `json:"node"`
	Fingerprint string `json:"fingerprint"`
	OS          string `json:"os"`
}
