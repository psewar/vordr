package wire

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"net/http"
)

// The hub signs its challenge (ADR-0170): an agent learns at its first connection, and hub-check before an install,
// whether hub.public_key in its agent.yaml is the key of the hub at hub.url, instead of at the first job.

// ChallengeNonceHeader carries the agent's own nonce, base64, in the tunnel's upgrade request. The hub signs it with
// its challenge, so a signature recorded on another connection does not fit. A header, because the challenge is the
// hub's first message, and an older hub ignores a header it does not know.
const ChallengeNonceHeader = "Vordr-Challenge-Nonce"

// challengeContext separates the hub's challenge signatures from its jobs and vet answers.
const challengeContext = "vordr/challenge/v1\x00"

// ChallengeSigned is what the hub signs: its own nonce and the agent's, both NonceSize long.
func ChallengeSigned(hubNonce, agentNonce []byte) []byte {
	b := append([]byte(challengeContext), hubNonce...)
	return append(b, agentNonce...)
}

// NewChallengeNonce makes a fresh agent nonce and the request header that carries it to the hub.
func NewChallengeNonce() ([]byte, http.Header) {
	n := make([]byte, NonceSize)
	_, _ = rand.Read(n) // since Go 1.24 it never returns an error: it ends the program instead
	return n, http.Header{ChallengeNonceHeader: {base64.StdEncoding.EncodeToString(n)}}
}

// AgentNonce reads the agent's nonce from the upgrade request. ok is false when there is none, or it is not NonceSize
// bytes in base64: an agent before ADR-0170, which the hub then does not sign for.
func AgentNonce(h http.Header) (nonce []byte, ok bool) {
	v := h.Get(ChallengeNonceHeader)
	if v == "" {
		return nil, false
	}
	n, err := base64.StdEncoding.DecodeString(v)
	if err != nil || len(n) != NonceSize {
		return nil, false
	}
	return n, true
}

// HubKeyCheck is what the hub's challenge says about the hub key the agent pinned (ADR-0170), as a fixed code.
type HubKeyCheck string

const (
	// HubKeyMatches: signed with hub.public_key, over this connection's nonces.
	HubKeyMatches HubKeyCheck = "matches"
	// HubKeyMismatch: signed, but the signature does not verify with hub.public_key over this connection's nonces -
	// another key, a damaged signature, or one recorded on another connection.
	HubKeyMismatch HubKeyCheck = "mismatch"
	// HubKeyUnsigned: no signature - a hub before ADR-0170. The key is not checked at login; jobs are, as always.
	HubKeyUnsigned HubKeyCheck = "unsigned"
	// HubKeyDowngrade: no signature, from a hub.url whose hub this agent has seen sign with hub.public_key before (its
	// state_dir says so). Whoever answers may simply have left the signature out, so it is refused like a mismatch.
	// The agent decides it from its own record; CheckHubKey, which sees one challenge, never returns it.
	HubKeyDowngrade HubKeyCheck = "downgrade"
)

// Refused reports whether the agent refuses the hub for this finding: it sends no hello, and hub-check fails at stage
// hub_key.
func (h HubKeyCheck) Refused() bool { return h == HubKeyMismatch || h == HubKeyDowngrade }

// Close codes an agent ends a tunnel connection with when it refuses the hub's key, before any hello (ADR-0170), from
// the private range of RFC 6455. The hub logs them as the fact they are, instead of seeing a connection that just ends.
const (
	CloseHubKeyMismatch  = 4001
	CloseHubKeyDowngrade = 4002
)

// CloseCode is the close code an agent sends for a refusal; 0 for a finding it does not refuse.
func (h HubKeyCheck) CloseCode() int {
	switch h {
	case HubKeyMismatch:
		return CloseHubKeyMismatch
	case HubKeyDowngrade:
		return CloseHubKeyDowngrade
	}
	return 0
}

// HubKeyRefusal reads a close code from an agent: the finding it refused the hub for, ok false for any other code.
func HubKeyRefusal(code int) (h HubKeyCheck, ok bool) {
	switch code {
	case CloseHubKeyMismatch:
		return HubKeyMismatch, true
	case CloseHubKeyDowngrade:
		return HubKeyDowngrade, true
	}
	return "", false
}

// CheckHubKey checks the challenge's signature against the pinned hub key; agentNonce is the nonce this agent sent.
func (ch Challenge) CheckHubKey(agentNonce []byte, key ed25519.PublicKey) HubKeyCheck {
	switch {
	case len(ch.HubSig) == 0:
		return HubKeyUnsigned
	case len(key) == ed25519.PublicKeySize && len(agentNonce) == NonceSize &&
		ed25519.Verify(key, ChallengeSigned(ch.Nonce, agentNonce), ch.HubSig):
		return HubKeyMatches
	}
	return HubKeyMismatch
}
