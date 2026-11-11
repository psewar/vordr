package protocol

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// Job is what the hub asks an agent to do. The hub signs the JSON bytes of the job; the agent verifies exactly those
// bytes before decoding them, so no canonical form is needed.
type Job struct {
	ID        string    `json:"job_id"`
	Node      string    `json:"node"`
	Verb      Verb      `json:"verb"`
	Scope     string    `json:"scope,omitempty"`  // install only: security | all | app:<id>
	Target    string    `json:"target,omitempty"` // cancel only: the job id to cancel
	IssuedAt  time.Time `json:"issued_at"`
	ExpiresAt time.Time `json:"expires_at"`
	// agent_update only: the release as the operator signed it (the agent checks it against its own operator key)
	// and the one-time token for downloading the binary from the hub.
	Manifest    []byte `json:"manifest,omitempty"`
	ManifestSig []byte `json:"manifest_sig,omitempty"`
	Token       string `json:"token,omitempty"`
	// install and reboot only: why this run may go outside the host's window (ADR-0087). Only a person's explicit
	// request carries it; without it the agent keeps to its window.
	OutsideWindow string `json:"outside_window,omitempty"`
	// backup only: the guest to back up, as the backup server groups it ("vm/301", "ct/302"; ADR-0096).
	Guest string `json:"guest,omitempty"`
	// install with a pkg or deps scope only: the packages whose release fixes an OSV advisory of the installed
	// version, as the hub knows it (ADR-0111). The agent takes their target at its shorter min_age_days_security; the
	// rest of the plan keeps min_age_days. The hub sends it only to agents that announce the shorter age.
	SecurityFix []string `json:"security_fix,omitempty"`
}

// MaxSecurityFix bounds Job.SecurityFix.
const MaxSecurityFix = 200

// guestPattern: a Proxmox guest id (100 and up) with its kind, nothing that could become an option.
var guestPattern = regexp.MustCompile(`^(vm|ct)/[1-9][0-9]{2,8}$`)

// ParseGuest splits "vm/301" or "ct/302" into its kind and id.
func ParseGuest(g string) (kind, id string, ok bool) {
	if !guestPattern.MatchString(g) {
		return "", "", false
	}
	kind, id, _ = strings.Cut(g, "/")
	return kind, id, true
}

// MaxReasonLen bounds the reason of a run outside the window, in bytes.
const MaxReasonLen = 200

// ValidReason reports whether s may be the reason of a run outside the window: not blank, at most MaxReasonLen bytes
// of valid UTF-8, no control characters (it ends up in logs and in Home Assistant).
func ValidReason(s string) bool {
	if strings.TrimSpace(s) == "" || len(s) > MaxReasonLen || !utf8.ValidString(s) {
		return false
	}
	return !strings.ContainsFunc(s, unicode.IsControl)
}

// SignedJob travels through the tunnel. []byte fields are base64 in JSON.
type SignedJob struct {
	Payload   []byte `json:"payload"`
	Signature []byte `json:"signature"`
}

const (
	// jobContext separates job signatures from anything else the hub key might ever sign.
	jobContext = "vordr/job/v1\x00"
	// MaxJobLifetime bounds expires_at - issued_at; a captured job is useless after this.
	MaxJobLifetime = 5 * time.Minute
	// MaxClockSkew is how far issued_at may lie in the agent's future.
	MaxClockSkew = time.Minute
)

// InvalidReason says why a job was rejected with rejected_invalid_job. It travels as an attribute; "" means valid.
type InvalidReason string

const (
	InvalidNone            InvalidReason = ""
	InvalidNoHubKey        InvalidReason = "no_hub_key"
	InvalidBadSignature    InvalidReason = "bad_signature"
	InvalidMalformed       InvalidReason = "malformed"
	InvalidUnknownVerb     InvalidReason = "unknown_verb"
	InvalidBadParameters   InvalidReason = "bad_parameters"
	InvalidWrongNode       InvalidReason = "wrong_node"
	InvalidNotYetValid     InvalidReason = "not_yet_valid"
	InvalidExpired         InvalidReason = "expired"
	InvalidLifetimeTooLong InvalidReason = "lifetime_too_long"
	InvalidReplayed        InvalidReason = "replayed"
	// InvalidBadManifest: an agent_update whose manifest the operator key in agent.yaml did not sign.
	InvalidBadManifest InvalidReason = "bad_manifest"
	// InvalidOlderRelease: an agent_update whose release is not newer than the agent's own (design 10.4).
	InvalidOlderRelease InvalidReason = "older_release"
)

var jobIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// SignJob marshals j and signs it with the hub key; sign is the key's signing function, so the private key never
// leaves its holder.
func SignJob(j Job, sign func([]byte) []byte) (SignedJob, error) {
	payload, err := json.Marshal(j)
	if err != nil {
		return SignedJob{}, err
	}
	return SignedJob{Payload: payload, Signature: sign(signedBytes(payload))}, nil
}

// UntrustedJobID reads the job id from a payload without checking anything. Only for echoing it in a rejection, so
// that the hub can match the answer to its request.
func UntrustedJobID(payload []byte) string {
	var j struct {
		ID string `json:"job_id"`
	}
	if json.Unmarshal(payload, &j) != nil || !jobIDPattern.MatchString(j.ID) {
		return ""
	}
	return j.ID
}

func signedBytes(payload []byte) []byte {
	return append([]byte(jobContext), payload...)
}

// VerifyJob checks a signed job in this order: hub key, signature, decoding, verb and parameters, node, time,
// replay. Only a job that passes everything is returned with InvalidNone; the replay guard remembers it only then.
func VerifyJob(sj SignedJob, hubKey ed25519.PublicKey, node string, now time.Time, guard *ReplayGuard) (Job, InvalidReason) {
	if len(hubKey) != ed25519.PublicKeySize {
		return Job{}, InvalidNoHubKey
	}
	if len(sj.Signature) != ed25519.SignatureSize || !ed25519.Verify(hubKey, signedBytes(sj.Payload), sj.Signature) {
		return Job{}, InvalidBadSignature
	}
	var j Job
	dec := json.NewDecoder(bytes.NewReader(sj.Payload))
	dec.DisallowUnknownFields()
	if dec.Decode(&j) != nil || dec.More() || !jobIDPattern.MatchString(j.ID) {
		return Job{}, InvalidMalformed
	}
	if !j.Verb.Valid() {
		return Job{}, InvalidUnknownVerb
	}
	if !validParameters(j) {
		return Job{}, InvalidBadParameters
	}
	if j.Node != node {
		return Job{}, InvalidWrongNode
	}
	switch {
	case !j.ExpiresAt.After(j.IssuedAt) || j.ExpiresAt.Sub(j.IssuedAt) > MaxJobLifetime:
		return Job{}, InvalidLifetimeTooLong
	case j.IssuedAt.After(now.Add(MaxClockSkew)):
		return Job{}, InvalidNotYetValid
	case !now.Before(j.ExpiresAt):
		return Job{}, InvalidExpired
	}
	if !guard.Admit(j.ID, j.ExpiresAt, now) {
		return Job{}, InvalidReplayed
	}
	return j, InvalidNone
}

// validParameters: install carries a scope, cancel a target job id, agent_update a manifest with its signature and a
// download token, backup a guest; nothing else carries any of these. Only install and reboot may carry a reason to
// run outside the window.
func validParameters(j Job) bool {
	if j.OutsideWindow != "" && (j.Verb != VerbInstall && j.Verb != VerbReboot || !ValidReason(j.OutsideWindow)) {
		return false
	}
	if _, _, ok := ParseGuest(j.Guest); j.Verb == VerbBackup && !ok || j.Verb != VerbBackup && j.Guest != "" {
		return false // backup needs a guest, every other verb has none
	}
	if len(j.SecurityFix) > 0 && !validSecurityFix(j) {
		return false
	}
	update := len(j.Manifest) > 0 || len(j.ManifestSig) > 0 || j.Token != ""
	switch j.Verb {
	case VerbInstall:
		_, ok := ParseScope(j.Scope)
		return ok && j.Target == "" && !update
	case VerbCancel:
		return j.Scope == "" && jobIDPattern.MatchString(j.Target) && !update
	case VerbAgentUpdate:
		// The manifest's own signature is checked by the agent with the operator key, not here.
		return j.Scope == "" && j.Target == "" && len(j.Manifest) > 0 && len(j.ManifestSig) == ed25519.SignatureSize &&
			ValidToken(j.Token)
	}
	return j.Scope == "" && j.Target == "" && !update
}

// validSecurityFix: only an install of pip/npm packages or of dependencies names packages for the shorter age
// (ADR-0111), each a package name, none twice, at most MaxSecurityFix; with one package in the scope, only that one.
func validSecurityFix(j Job) bool {
	s, ok := ParseScope(j.Scope)
	if j.Verb != VerbInstall || !ok || s.Kind != ScopePkg && s.Kind != ScopeDeps || len(j.SecurityFix) > MaxSecurityFix {
		return false
	}
	seen := map[string]bool{}
	for _, n := range j.SecurityFix {
		if !PackageNamePattern.MatchString(n) || seen[n] || s.Package != "" && n != s.Package {
			return false
		}
		seen[n] = true
	}
	return true
}

// ReplayGuard remembers admitted job ids until their expiry. A job id that comes back before it expires is a replay;
// after expiry the time check rejects it anyway.
type ReplayGuard struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

// NewReplayGuard returns an empty guard.
func NewReplayGuard() *ReplayGuard { return &ReplayGuard{seen: map[string]time.Time{}} }

// Admit records id and reports true, or reports false if id was already admitted and has not expired.
func (g *ReplayGuard) Admit(id string, expires, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for k, exp := range g.seen {
		if !now.Before(exp) {
			delete(g.seen, k)
		}
	}
	if _, dup := g.seen[id]; dup {
		return false
	}
	g.seen[id] = expires
	return true
}
