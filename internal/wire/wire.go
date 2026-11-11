// Package wire is the tunnel protocol between agent and hub: JSON text messages over a WebSocket over TLS, opened
// by the agent. Every message carries its type in "t".
//
//	hub   -> agent  challenge   {nonce}
//	agent -> hub    hello       {node, public_key, signature, agent_version, facts, verbs, policy}
//	hub   -> agent  status      {state, reason}      again whenever the state changes (e.g. approved)
//	agent -> hub    heartbeat   every HeartbeatInterval; the hub answers with a heartbeat
//	agent -> hub    report      {sources, reboot}    after every check
//	hub   -> agent  job         {job: SignedJob}     only to approved agents
//	agent -> hub    job_status  {job_id, phase | code, reason, detail}
//	agent -> hub    power       {state}              the host is about to sleep or shut down (hub 0.15.0 and later)
//	agent -> hub    inventory   {locations}          packages of pip, pipx, npm; only to a hub whose status
//	                                                 announced FeatureInventory (hub 0.18.0 and later)
//	hub   -> agent  ack         {job_id}             a final job_status is stored; only to an agent whose hello
//	                                                 named its runs (ADR-0114)
package wire

import (
	"crypto/ed25519"
	"time"

	"github.com/psewar/vordr/internal/facts"
	"github.com/psewar/vordr/internal/inventory"
	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/source"
)

const (
	TunnelPath        = "/v1/tunnel"
	HeartbeatInterval = 30 * time.Second
	// IdleTimeout: the hub drops a connection that sent nothing for this long and reports the host offline.
	IdleTimeout = 3 * HeartbeatInterval
	// HelloTimeout: an agent has this long after the challenge to prove its key.
	HelloTimeout = 10 * time.Second
	// MaxMessage bounds one message (a report lists every pending package).
	MaxMessage = 4 << 20
	NonceSize  = 32
)

const (
	TChallenge = "challenge"
	THello     = "hello"
	TStatus    = "status"
	THeartbeat = "heartbeat"
	TReport    = "report"
	TJob       = "job"
	TJobStatus = "job_status"
	TPower     = "power"
	TInventory = "inventory"
	TVet       = "vet"        // agent -> hub: the plan of a pip/npm update, to be checked against OSV (11.4)
	TVetResult = "vet_result" // hub -> agent: the signed answer, only to an agent that asked
	TAck       = "ack"        // hub -> agent: a final job status is stored (ADR-0114)
)

// Vet asks the hub to vouch for the plan of a running pip/npm update (job_id), dependencies included.
type Vet struct {
	T         string                `json:"t"`
	JobID     string                `json:"job_id"`
	Ecosystem string                `json:"ecosystem"`
	Packages  []protocol.VetPackage `json:"packages"`
}

// VetResult is the hub's signed answer (protocol.Vet).
type VetResult struct {
	T     string             `json:"t"`
	JobID string             `json:"job_id"`
	Vet   protocol.SignedVet `json:"vet"`
}

// MaxVetPackages bounds one plan; a global npm package with its whole tree has a few hundred.
const MaxVetPackages = 5000

// Features a hub announces in its status, beyond what every hub understands. An agent sends a message of a newer
// kind only to a hub that named it: an older hub drops the connection on a message it does not know, and the agent
// must not find that out by trying.
const (
	FeatureInventory = "inventory"
	// FeatureVet: the hub answers vet messages, so pip/npm updates can run (11.4).
	FeatureVet = "vet"
	// FeatureAck: the hub takes Runs in the hello and acknowledges every final job status (ADR-0114). It is announced
	// in the challenge already, because the hello comes before the status, and an older hub refuses a hello with
	// fields it does not know.
	FeatureAck = "ack"
	// FeatureContainers: the hub keeps an inventory location of kind container (ADR-0113). An older hub would count
	// the images among the pip/npm packages, so an agent sends them only to a hub that announced this.
	FeatureContainers = "containers"
	// FeatureImageContents: the hub matches the packages inside container images with its OSV data (ADR-0120). The
	// agent reads them only for a hub that announced this: reading costs the host a little, and an older hub would
	// only carry them around.
	FeatureImageContents = "image_contents"
)

// Runs is what the agent knows about its runs, in the hello to a hub that announced FeatureAck in its challenge
// (ADR-0114). The hub takes it as the fact and stops guessing from the disconnect. Absent: an agent before ADR-0114,
// or a hub that did not announce it; the hub then keeps its guess.
type Runs struct {
	// Running: the jobs the agent carries out now.
	Running []string `json:"running"`
	// Unacknowledged: final results the agent holds and sends on this session - its run journal until the hub
	// acknowledged it, a self-update or a reboot it reports after its restart.
	Unacknowledged []string `json:"unacknowledged"`
}

// Ack tells the agent that the hub has stored the final status of a job; the run journal counts a result as reported
// only then (ADR-0114). It carries no authority: a forged one could only make the agent stop resending a result, and
// whoever can forge it can also drop the result.
type Ack struct {
	T     string `json:"t"`
	JobID string `json:"job_id"`
}

// Inventory lists the packages of language package managers on the host (design 11.2). The hub matches them against
// its OSV data; they go nowhere else.
type Inventory struct {
	T         string               `json:"t"`
	CheckedAt time.Time            `json:"checked_at"`
	Locations []inventory.Location `json:"locations"`
	// OutdatedAt: when the agent last asked the registries for newer releases (11.3); nil: it does not ask.
	OutdatedAt *time.Time `json:"outdated_at,omitempty"`
	// MinAgeDays: how long a release must have been public to be a candidate, as the host's policy says.
	MinAgeDays int `json:"min_age_days,omitempty"`
	// MinAgeDaysSecurity: the shorter minimum age for fixes (ADR-0111). Its presence says that the agent reports
	// security candidates and takes security_fix in its jobs; agents before it: 0, and the hub sends no such job.
	MinAgeDaysSecurity int `json:"min_age_days_security,omitempty"`
}

// MaxInventory bounds an inventory message below MaxMessage; the agent shortens the largest locations to fit
// (their code says so).
const MaxInventory = 3 << 20

// Power: the host says it is about to sleep or shut down. Until its next hello or message, a missing connection is no
// problem: a PC that sleeps is expected to be away, one that drops off without notice is not.
type Power struct {
	T     string `json:"t"`
	State string `json:"state"` // PowerSleeping or PowerShutdown
}

const (
	PowerSleeping = "sleeping"
	PowerShutdown = "shutdown"
)

// ValidPower reports whether s is a power state a host may announce.
func ValidPower(s string) bool { return s == PowerSleeping || s == PowerShutdown }

// Type reads only the type of a message.
type Type struct {
	T string `json:"t"`
}

type Challenge struct {
	T     string `json:"t"`
	Nonce []byte `json:"nonce"`
	// Features: what the hub accepts in the hello (FeatureAck). Hubs before ADR-0114 send none.
	Features []string `json:"features,omitempty"`
}

// PolicySummary is what the local policy allows; the hub offers only matching buttons in Home Assistant.
type PolicySummary struct {
	AllowedScopes []string  `json:"allowed_scopes"`
	DefaultScope  string    `json:"default_scope,omitempty"`
	Auto          string    `json:"auto"`
	AutoScope     string    `json:"auto_scope,omitempty"` // what install.auto installs; empty: DefaultScope (agents before 0.8.2)
	RebootAllowed bool      `json:"reboot_allowed"`
	AgentUpdate   bool      `json:"agent_update"`
	Apps          []AppInfo `json:"apps,omitempty"`
	// Window and TZ: when the local policy lets installs run, in the host's own time zone (IANA name). The hub plans
	// install.auto: hub runs inside it instead of asking and being refused (design 10.3). Agents before 0.7.4: none.
	Window *WindowInfo `json:"window,omitempty"`
	TZ     string      `json:"tz,omitempty"`
	// OutsideWindow: install and reboot jobs that carry a reason run outside the windows too (ADR-0087). Agents before
	// it: false, and the hub sends them no such job (they would refuse the unknown field as an invalid job).
	OutsideWindow bool `json:"outside_window,omitempty"`
	// MinIntervalH: install.min_interval_h, the agent's minimum gap between install runs of the system packages or an
	// app (ADR-0110). Agents before it: 0, and the hub leaves the check to the agent.
	MinIntervalH int `json:"min_interval_h,omitempty"`
}

// WindowInfo is install.window of agent.yaml: days mon..sun, start and end HH:MM (end before start: over midnight).
type WindowInfo struct {
	Days  []string `json:"days"`
	Start string   `json:"start"`
	End   string   `json:"end"`
}

type AppInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Hello struct {
	T            string          `json:"t"`
	Node         string          `json:"node"`
	PublicKey    []byte          `json:"public_key"`
	Signature    []byte          `json:"signature"` // over HelloSigned(nonce, node, public_key)
	AgentVersion string          `json:"agent_version"`
	Facts        facts.Host      `json:"facts"`
	Verbs        []protocol.Verb `json:"verbs"` // what this agent version can do
	Policy       PolicySummary   `json:"policy"`
	// LastInstall: when the run in the agent's journal started - the last run that changed packages, from which
	// install.min_interval_h counts (ADR-0110). Absent when there is none, and from agents before it.
	LastInstall *time.Time `json:"last_install,omitempty"`
	// Runs: the jobs the agent runs and the results it holds, only to a hub that announced FeatureAck (ADR-0114).
	Runs *Runs `json:"runs,omitempty"`
}

// helloContext separates hello signatures from anything else the agent key signs.
const helloContext = "vordr/hello/v1\x00"

// HelloSigned is what the agent signs to prove its key for this connection. The nonce binds it to the challenge,
// node and key bind it to the claimed identity.
func HelloSigned(nonce []byte, node string, pub ed25519.PublicKey) []byte {
	b := append([]byte(helloContext), nonce...)
	b = append(b, 0)
	b = append(b, node...)
	b = append(b, 0)
	return append(b, pub...)
}

// NodeState is the registration state of a host at the hub.
type NodeState string

const (
	StatePendingApproval NodeState = "pending_approval"
	StateApproved        NodeState = "approved"
	StateRejected        NodeState = "rejected"
)

// StatusReason says why the hub refused a hello. "" with an approved or pending state.
type StatusReason string

const (
	ReasonNone           StatusReason = ""
	ReasonBadHello       StatusReason = "bad_hello"        // malformed, bad node name or key, signature wrong
	ReasonKeyMismatch    StatusReason = "key_mismatch"     // the node is registered with another key
	ReasonTooManyPending StatusReason = "too_many_pending" // the hub holds no more unapproved hosts
	ReasonRejected       StatusReason = "rejected"         // the operator rejected this host
)

type Status struct {
	T           string       `json:"t"`
	State       NodeState    `json:"state"`
	Reason      StatusReason `json:"reason,omitempty"`
	Fingerprint string       `json:"fingerprint,omitempty"` // as the hub saw the key, for comparing with "identity"
	// Features: what this hub accepts beyond the base protocol (FeatureInventory ...). Older hubs send none.
	Features []string `json:"features,omitempty"`
}

type Heartbeat struct {
	T string `json:"t"`
}

type Report struct {
	T         string          `json:"t"`
	CheckedAt time.Time       `json:"checked_at"`
	Sources   []source.Report `json:"sources"`
	Reboot    *source.Reboot  `json:"reboot,omitempty"`
}

type JobMsg struct {
	T   string             `json:"t"`
	Job protocol.SignedJob `json:"job"`
}

// JobStatus reports a phase while the job runs, and a code once it is done (then Phase is empty).
type JobStatus struct {
	T      string                 `json:"t"`
	JobID  string                 `json:"job_id"`
	Phase  protocol.Phase         `json:"phase,omitempty"`
	Code   protocol.Code          `json:"code,omitempty"`
	Reason protocol.InvalidReason `json:"reason,omitempty"` // with rejected_invalid_job
	Detail string                 `json:"detail,omitempty"` // for humans only
	// While the job runs: percent of the whole run, and the package being worked on when known.
	Progress *int   `json:"progress,omitempty"`
	Item     string `json:"item,omitempty"`
	// With the result of a run that changed packages: what it did.
	Summary *RunSummary `json:"summary,omitempty"`
	// InstallStarted: with the result of a run that wrote the agent's journal, when it started; install.min_interval_h
	// counts from it (ADR-0110).
	InstallStarted *time.Time `json:"install_started,omitempty"`
}

// MaxSummaryPackages caps RunSummary.Packages; PackageCount is always the whole count, the run log on the host has all.
const MaxSummaryPackages = 100

// RunSummary is what an install run did, sent with its result.
type RunSummary struct {
	PackageCount int      `json:"package_count"`
	Packages     []string `json:"packages,omitempty"`
	Seconds      int      `json:"seconds"`
	// LogFile: the package manager's whole output of the run, on the host.
	LogFile string `json:"log_file,omitempty"`
	// OutdatedServices still run with replaced libraries (needrestart); they pick up the new ones only on a restart.
	OutdatedServices []string `json:"outdated_services,omitempty"`
	// Replaced: packages the run removed because a package of the run replaces them, "old -> new".
	Replaced []string `json:"replaced,omitempty"`
	// Restarted: containers that ran before the run, did not come back by themselves and were started by the agent.
	Restarted []string `json:"restarted,omitempty"`
}

// Final reports whether the status ends the job.
func (s JobStatus) Final() bool { return s.Code != "" }
