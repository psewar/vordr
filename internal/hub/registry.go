package hub

import (
	"sort"
	"sync"
	"time"

	"github.com/psewar/vordr/internal/facts"
	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/wire"
)

// Entry is one host in the register. Its key is fixed at the first hello: another key under the same name is
// refused (key_mismatch) until the operator forgets the host.
type Entry struct {
	Node         string         `json:"node"`
	PublicKey    []byte         `json:"public_key"`
	Fingerprint  string         `json:"fingerprint"`
	State        wire.NodeState `json:"state"`
	FirstSeen    time.Time      `json:"first_seen"`
	DecidedAt    *time.Time     `json:"decided_at,omitempty"` // approval or rejection
	AgentVersion string         `json:"agent_version"`
	// AgentSince: since when the host has run AgentVersion (for "the canary ran the release long enough").
	AgentSince *time.Time         `json:"agent_since,omitempty"`
	Facts      facts.Host         `json:"facts"`
	Verbs      []protocol.Verb    `json:"verbs"`
	Policy     wire.PolicySummary `json:"policy"`
	LastReport *wire.Report       `json:"last_report,omitempty"`
	// PendingSince: since when updates have been pending without interruption (for "stale").
	PendingSince *time.Time `json:"pending_since,omitempty"`
	// PendingUpdates: the pending updates of the last complete report, each with when it was first reported - the
	// ring delay of automatic installs counts per update (ADR-0107).
	PendingUpdates []PendingUpdate `json:"pending_updates,omitempty"`
	// CleanInstallAt: when the host's last clean system install started (verb install, scope security or all, result
	// ok or ok_reboot_required). A canary's ends the ring delay for what was pending before it (ADR-0107).
	CleanInstallAt *time.Time `json:"clean_install_at,omitempty"`
	// CleanInstallEnd: when that install ended. A canary's run counts only once a complete report received after it
	// says what it left behind (PendingAt; ADR-0118).
	CleanInstallEnd *time.Time `json:"clean_install_end,omitempty"`
	// PendingAt: when the complete report PendingUpdates come from was received.
	PendingAt *time.Time `json:"pending_at,omitempty"`
	// LastInstall: when the agent's last run that changed packages started, as the agent reports it (hello, final
	// status); its install.min_interval_h counts from it (ADR-0110).
	LastInstall *time.Time `json:"last_install,omitempty"`
	// RebootSince: since when the host has needed a reboot without interruption (for "reboot_pending").
	RebootSince *time.Time `json:"reboot_since,omitempty"`
	LastRun     *Run       `json:"last_run,omitempty"`
	// FailedInstall: the host's last install of the system packages (scope security or all) that ended with a problem,
	// kept until one of them goes through again with at least its scope (ADR-0124). Any other job - a check, an image,
	// package or agent update - leaves it: the failure stays the host's problem, and a canary's holds the later rings.
	FailedInstall *Run `json:"failed_install,omitempty"`
	// Power: the host said it goes to sleep or shuts down (wire.Power); cleared by its next hello or message.
	Power      string     `json:"power,omitempty"`
	PowerSince *time.Time `json:"power_since,omitempty"`
}

// Run is the last job the hub sent to a host, with its outcome once known.
type Run struct {
	JobID    string                 `json:"job_id"`
	Verb     protocol.Verb          `json:"verb"`
	Scope    string                 `json:"scope,omitempty"`
	By       string                 `json:"by,omitempty"`    // who started it: ha:<client>, admin, rollout
	Phase    protocol.Phase         `json:"phase,omitempty"` // while running
	Code     protocol.Code          `json:"code,omitempty"`  // once finished
	Reason   protocol.InvalidReason `json:"reason,omitempty"`
	Detail   string                 `json:"detail,omitempty"`
	Started  time.Time              `json:"started"`
	Finished *time.Time             `json:"finished,omitempty"`
	Progress *int                   `json:"progress,omitempty"` // while running, percent of the whole run
	Item     string                 `json:"item,omitempty"`     // while running, the package being worked on
	Summary  *wire.RunSummary       `json:"summary,omitempty"`  // once finished: what an install run did
	// OutsideWindow: why it was asked for outside the host's window (ADR-0087); empty for a run in the window.
	OutsideWindow string `json:"outside_window,omitempty"`
}

// PendingUpdate is one pending update as the register remembers it (ADR-0107). A new target version is a new update.
// Name is empty for a source that reports a count without a package list; the entry then stands for the source's
// whole pending set.
type PendingUpdate struct {
	Source   string    `json:"source"`
	Name     string    `json:"name,omitempty"`
	To       string    `json:"to,omitempty"`
	Security bool      `json:"security,omitempty"`
	Since    time.Time `json:"since"`
}

// AdminCode is the outcome of an administration request; the CLI prints it, scripts can rely on it.
type AdminCode string

const (
	AdminOK                  AdminCode = "ok"
	AdminUnknownNode         AdminCode = "unknown_node"
	AdminFingerprintMismatch AdminCode = "fingerprint_mismatch"
	AdminAlready             AdminCode = "already_in_state"
	AdminNotConnected        AdminCode = "not_connected"
	AdminNotApproved         AdminCode = "not_approved"
	AdminVerbUnsupported     AdminCode = "verb_unsupported_by_agent"
	AdminBadRequest          AdminCode = "bad_request"
	AdminTimeout             AdminCode = "timeout"
	AdminSendFailed          AdminCode = "send_failed"
	AdminStoreFailed         AdminCode = "store_failed"
	AdminNotAllowed          AdminCode = "not_allowed_by_host_policy"
	// AdminClientNotApproved: a Home Assistant client asked for something before (or after) its approval.
	AdminClientNotApproved AdminCode = "client_not_approved"
	// An install needs a state to go back to (backup check, hub.yaml backup), refused with the reason: the host's last
	// backup is older than backup.max_age_hours, PBS has none in its group, PBS does not answer, or hub.yaml names no
	// group for the host.
	AdminBackupStale       AdminCode = "backup_stale"
	AdminBackupMissing     AdminCode = "backup_missing"
	AdminBackupUnreachable AdminCode = "backup_unreachable"
	AdminBackupUnmapped    AdminCode = "backup_unmapped"
	// AdminHubStopping: the hub stopped while it waited for the job's result; the run stays open (ADR-0142).
	AdminHubStopping AdminCode = "hub_stopping"
)

// Registry is the register of hosts, kept in one JSON file that is replaced atomically on every change.
type Registry struct {
	mu    sync.Mutex
	path  string
	nodes map[string]*Entry
}

// OpenRegistry loads the register from path; a missing file is an empty register.
func OpenRegistry(path string) (*Registry, error) {
	list, err := loadList[*Entry](path)
	if err != nil {
		return nil, err
	}
	r := &Registry{path: path, nodes: map[string]*Entry{}}
	for _, e := range list {
		r.nodes[e.Node] = e
	}
	return r, nil
}

// save writes the register; the caller holds mu.
func (r *Registry) save() error {
	list := make([]*Entry, 0, len(r.nodes))
	for _, e := range r.nodes {
		list = append(list, e)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Node < list[j].Node })
	return saveList(r.path, list)
}

// Admit decides about a hello whose signature is already verified. Unknown hosts become pending_approval, as long
// as fewer than maxPending are waiting; known hosts must come with their registered key.
func (r *Registry) Admit(h wire.Hello, fingerprint string, now time.Time, maxPending int) (wire.NodeState, wire.StatusReason, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.nodes[h.Node]
	if e == nil {
		if r.countLocked(wire.StatePendingApproval) >= maxPending {
			return wire.StateRejected, wire.ReasonTooManyPending, nil
		}
		e = &Entry{Node: h.Node, PublicKey: h.PublicKey, Fingerprint: fingerprint, State: wire.StatePendingApproval,
			FirstSeen: now}
		r.nodes[h.Node] = e
	}
	if e.Fingerprint != fingerprint {
		return wire.StateRejected, wire.ReasonKeyMismatch, nil
	}
	if e.State == wire.StateRejected {
		return wire.StateRejected, wire.ReasonRejected, nil
	}
	if e.AgentVersion != h.AgentVersion || e.AgentSince == nil {
		since := now
		e.AgentSince = &since
	}
	e.AgentVersion, e.Facts, e.Verbs, e.Policy = h.AgentVersion, h.Facts, h.Verbs, h.Policy
	e.LastInstall = h.LastInstall   // the agent's own record; an agent before ADR-0110 sends none and announces no interval
	e.Power, e.PowerSince = "", nil // it is up again
	return e.State, wire.ReasonNone, r.save()
}

// SetLastInstall records the start of a run that wrote the agent's journal, from its final status (ADR-0110).
func (r *Registry) SetLastInstall(node string, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.nodes[node]
	if e == nil || (e.LastInstall != nil && e.LastInstall.Equal(at)) {
		return nil
	}
	e.LastInstall = &at
	return r.save()
}

// SetPower records what an approved host said about going away (wire.Power), or "" once it is heard from again.
// Unchanged: nothing written.
func (r *Registry) SetPower(node, state string, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.nodes[node]
	if e == nil || e.State != wire.StateApproved || e.Power == state {
		return nil
	}
	e.Power, e.PowerSince = state, nil
	if state != "" {
		e.PowerSince = &at
	}
	return r.save()
}

func (r *Registry) countLocked(s wire.NodeState) int {
	n := 0
	for _, e := range r.nodes {
		if e.State == s {
			n++
		}
	}
	return n
}

// Decide approves or rejects a host. Approval needs the fingerprint the operator compared with the host itself.
func (r *Registry) Decide(node string, to wire.NodeState, fingerprint string, now time.Time) AdminCode {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.nodes[node]
	switch {
	case e == nil:
		return AdminUnknownNode
	case to == wire.StateApproved && fingerprint != e.Fingerprint:
		return AdminFingerprintMismatch
	case e.State == to:
		return AdminAlready
	}
	old := e.State
	e.State = to
	e.DecidedAt = &now
	if err := r.save(); err != nil {
		e.State = old
		return AdminStoreFailed
	}
	return AdminOK
}

// Forget removes a host; its next hello starts over as pending (e.g. after a reinstall with a new key).
func (r *Registry) Forget(node string) AdminCode {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.nodes[node]
	if e == nil {
		return AdminUnknownNode
	}
	delete(r.nodes, node)
	if err := r.save(); err != nil {
		r.nodes[node] = e
		return AdminStoreFailed
	}
	return AdminOK
}

// SetReport stores a report as if the hub had received it at the agent's own check time; see SetReportAt.
func (r *Registry) SetReport(node string, rep wire.Report) error {
	return r.SetReportAt(node, rep, rep.CheckedAt)
}

// SetReportAt stores the last report of an approved host, received by the hub at received, and keeps track of how
// long updates have been pending. A report whose sources failed changes nothing about that: it says nothing about what
// is pending.
//
// Something newly pending counts from the later of the agent's check time and the hub's receive time. The ring delay
// (ADR-0107) compares these times with the hub's clock, and a host whose clock lags - a board without a real-time
// clock, checking before its time is synced - would otherwise date its new updates days back and skip the delay.
// A clock that runs ahead only makes the wait longer.
func (r *Registry) SetReportAt(node string, rep wire.Report, received time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.nodes[node]
	if e == nil || e.State != wire.StateApproved {
		return nil
	}
	e.LastReport = &rep
	seen := rep.CheckedAt
	if received.After(seen) {
		seen = received
	}
	switch pending, known := pendingCount(rep); {
	case !known:
	case pending == 0:
		e.PendingSince, e.PendingUpdates = nil, nil
		at := seen
		e.PendingAt = &at
	default:
		if e.PendingSince == nil {
			at := seen
			e.PendingSince = &at
		}
		e.PendingUpdates = pendingUpdates(rep, e.PendingUpdates, seen)
		at := seen
		e.PendingAt = &at
	}
	// The same for the reboot; a report without an answering signal says nothing about it.
	if rb := rep.Reboot; rb != nil && len(rb.Checked) > 0 {
		switch {
		case !rb.Required:
			e.RebootSince = nil
		case e.RebootSince == nil:
			at := seen
			e.RebootSince = &at
		}
	}
	return r.save()
}

// pendingCount sums the pending updates of all sources; known is false if a source could not be checked.
func pendingCount(rep wire.Report) (int, bool) {
	n := 0
	for _, s := range rep.Sources {
		if s.State == protocol.SourceCheckFailed || s.State == protocol.SourceUnknown {
			return 0, false
		}
		n += s.Pending
	}
	return n, len(rep.Sources) > 0
}

// pendingUpdates lists the pending updates of a complete report; an update the register knew keeps its time, a new one
// counts from seen.
func pendingUpdates(rep wire.Report, old []PendingUpdate, seen time.Time) []PendingUpdate {
	since := make(map[PendingUpdate]time.Time, len(old))
	for _, u := range old {
		at := u.Since
		u.Since = time.Time{}
		since[u] = at
	}
	var out []PendingUpdate
	add := func(u PendingUpdate) { // u without Since: the key
		at, known := since[u]
		if !known {
			at = seen
		}
		u.Since = at
		out = append(out, u)
	}
	for _, s := range rep.Sources {
		switch {
		case s.Pending == 0:
		case len(s.Packages) == 0:
			add(PendingUpdate{Source: s.Source, Security: s.Security > 0})
		default:
			for _, p := range s.Packages {
				add(PendingUpdate{Source: s.Source, Name: p.Name, To: p.To, Security: p.Security})
			}
		}
	}
	return out
}

// cleanSystemInstall: an install of the system packages (scope security or all) that went through and changed
// packages. A run that found nothing to install met no update, and its ok says nothing about them, so it ends no ring
// delay (ADR-0107: with nothing installed at the canaries, the delay holds). That happens when
// nothing of the scope is pending, or with install.mode upgrade, which leaves packages that need new dependencies
// pending. Such a run writes no journal and sends no summary.
func cleanSystemInstall(run Run) bool {
	return systemInstall(run) && wentThrough(run.Code) && run.Summary != nil && run.Summary.PackageCount > 0
}

// systemInstall: an install of the system packages, scope security or all.
func systemInstall(run Run) bool {
	scope := protocol.ScopeKind(run.Scope)
	return run.Verb == protocol.VerbInstall && (scope == protocol.ScopeSecurity || scope == protocol.ScopeAll)
}

func wentThrough(c protocol.Code) bool {
	return c == protocol.CodeOK || c == protocol.CodeOKRebootRequired
}

// SetRun stores the last job of a host, when its last clean system install started and ended, and its last failed
// system install (ADR-0124): a system install with a problem code is kept; one that goes through clears it when its
// scope covers the failed one's (all covers security). A refusal, a run still going, and every other job leave it.
func (r *Registry) SetRun(node string, run Run) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.nodes[node]
	if e == nil {
		return nil
	}
	e.LastRun = &run
	if cleanSystemInstall(run) {
		at := run.Started
		e.CleanInstallAt, e.CleanInstallEnd = &at, nil
		if run.Finished != nil {
			end := *run.Finished
			e.CleanInstallEnd = &end
		}
	}
	if systemInstall(run) {
		switch f := e.FailedInstall; {
		case run.Code != "" && run.Code.Problem():
			failed := run
			e.FailedInstall = &failed
		case f != nil && wentThrough(run.Code) && (protocol.ScopeKind(run.Scope) == protocol.ScopeAll ||
			protocol.ScopeKind(f.Scope) == protocol.ScopeSecurity):
			e.FailedInstall = nil
		}
	}
	return r.save()
}

// Get returns a copy of one entry.
func (r *Registry) Get(node string) (Entry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e := r.nodes[node]; e != nil {
		return *e, true
	}
	return Entry{}, false
}

// List returns copies of all entries, sorted by node.
func (r *Registry) List() []Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Entry, 0, len(r.nodes))
	for _, e := range r.nodes {
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Node < out[j].Node })
	return out
}
