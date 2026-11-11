package protocol

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"time"
)

// Vetting (design 11.4): before a pip/npm update changes anything, the agent has its whole plan - every name and
// version pip or npm would install, dependencies included - checked against the hub's OSV data. The hub answers
// with a statement it signs like a job, bound to the job and the node, so that nobody between the two can wave a
// flagged version through. The agent installs only a plan the hub vouched for, entry by entry.

// Vet is the hub's statement about one plan.
type Vet struct {
	JobID     string       `json:"job_id"`
	Node      string       `json:"node"`
	Ecosystem string       `json:"ecosystem"`
	Packages  []VetPackage `json:"packages"`
	IssuedAt  time.Time    `json:"issued_at"`
	// Unavailable: the hub cannot vouch (its OSV data for the ecosystem is missing or stale); Packages is then empty.
	Unavailable string `json:"unavailable,omitempty"`
}

// VetPackage is one entry of the plan with the OSV entries that apply to it (none: clean).
type VetPackage struct {
	Name      string   `json:"name"`
	Version   string   `json:"version"`
	Flagged   []string `json:"flagged,omitempty"`
	Malicious bool     `json:"malicious,omitempty"`
}

// SignedVet travels through the tunnel.
type SignedVet struct {
	Payload   []byte `json:"payload"`
	Signature []byte `json:"signature"`
}

const (
	vetContext = "vordr/vet/v1\x00"
	// MaxVetAge bounds how old a statement may be when the agent uses it.
	MaxVetAge = 5 * time.Minute
)

// SignVet signs a statement with the hub key; sign is the key's signing function, as with SignJob.
func SignVet(v Vet, sign func([]byte) []byte) (SignedVet, error) {
	payload, err := json.Marshal(v)
	if err != nil {
		return SignedVet{}, err
	}
	return SignedVet{Payload: payload, Signature: sign(append([]byte(vetContext), payload...))}, nil
}

// VerifyVet checks signature, job, node and age, and returns the statement. ok is false for anything else - a
// statement that does not hold up is treated like none.
func VerifyVet(sv SignedVet, hubKey ed25519.PublicKey, node, jobID string, now time.Time) (Vet, bool) {
	if len(hubKey) != ed25519.PublicKeySize ||
		!ed25519.Verify(hubKey, append([]byte(vetContext), sv.Payload...), sv.Signature) {
		return Vet{}, false
	}
	var v Vet
	dec := json.NewDecoder(bytes.NewReader(sv.Payload))
	dec.DisallowUnknownFields()
	if dec.Decode(&v) != nil || v.JobID != jobID || v.Node != node {
		return Vet{}, false
	}
	if age := now.Sub(v.IssuedAt); age > MaxVetAge || age < -MaxClockSkew {
		return Vet{}, false
	}
	return v, true
}

// Covers reports whether the statement names exactly every entry of the plan, as name and version - an entry it
// leaves out has not been vetted.
func (v Vet) Covers(plan []VetPackage) bool {
	have := map[[2]string]bool{}
	for _, p := range v.Packages {
		have[[2]string{p.Name, p.Version}] = true
	}
	for _, p := range plan {
		if !have[[2]string{p.Name, p.Version}] {
			return false
		}
	}
	return len(v.Packages) == len(plan)
}
