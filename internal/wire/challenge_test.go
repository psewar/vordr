package wire

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"
)

// signed is a challenge as a hub with key priv sends it to an agent that sent agentNonce.
func signed(t *testing.T, priv ed25519.PrivateKey, agentNonce []byte) Challenge {
	t.Helper()
	nonce := make([]byte, NonceSize)
	_, _ = rand.Read(nonce)
	return Challenge{T: TChallenge, Nonce: nonce, HubSig: ed25519.Sign(priv, ChallengeSigned(nonce, agentNonce))}
}

// The agent's nonce travels in the upgrade request and comes out at the hub as it went in.
func TestChallengeNonceHeader(t *testing.T) {
	n, h := NewChallengeNonce()
	got, ok := AgentNonce(h)
	if !ok || !bytes.Equal(got, n) || len(n) != NonceSize {
		t.Fatalf("nonce %x, at the hub %x %v", n, got, ok)
	}
	if again, _ := NewChallengeNonce(); bytes.Equal(again, n) {
		t.Fatal("the same nonce twice")
	}
}

// Without a usable nonce the hub does not sign: no header (an agent before ADR-0170), not base64, the wrong length.
func TestAgentNonceRefused(t *testing.T) {
	for name, v := range map[string]string{
		"missing":     "",
		"not base64":  "not base64!",
		"too short":   base64.StdEncoding.EncodeToString(make([]byte, NonceSize-1)),
		"too long":    base64.StdEncoding.EncodeToString(make([]byte, NonceSize+1)),
		"url base64":  base64.URLEncoding.EncodeToString(bytes.Repeat([]byte{0xfb}, NonceSize)),
		"only spaces": "   ",
	} {
		h := http.Header{}
		if v != "" {
			h.Set(ChallengeNonceHeader, v)
		}
		if n, ok := AgentNonce(h); ok || n != nil {
			t.Errorf("%s: taken as %x", name, n)
		}
	}
}

// The three findings, each as its own code: the pinned key, another key, no signature.
func TestCheckHubKey(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	agentNonce, _ := NewChallengeNonce()
	ch := signed(t, priv, agentNonce)
	if got := ch.CheckHubKey(agentNonce, pub); got != HubKeyMatches {
		t.Errorf("pinned key: %s", got)
	}
	if got := ch.CheckHubKey(agentNonce, other); got != HubKeyMismatch {
		t.Errorf("another key in agent.yaml: %s", got)
	}
	if got := (Challenge{T: TChallenge, Nonce: ch.Nonce}).CheckHubKey(agentNonce, pub); got != HubKeyUnsigned {
		t.Errorf("older hub: %s", got)
	}
	if got := ch.CheckHubKey(agentNonce, nil); got != HubKeyMismatch {
		t.Errorf("no usable key pinned: %s", got)
	}
}

// A signature that does not belong to this connection does not count: damaged, over another agent's nonce (recorded
// and replayed), over another hub nonce, or made for another purpose of the hub key.
func TestCheckHubKeyRefusesForeignSignatures(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	agentNonce, _ := NewChallengeNonce()
	otherNonce, _ := NewChallengeNonce()

	tampered := signed(t, priv, agentNonce)
	tampered.HubSig = bytes.Clone(tampered.HubSig)
	tampered.HubSig[10] ^= 0x01
	short := signed(t, priv, agentNonce)
	short.HubSig = short.HubSig[:20]
	replayed := signed(t, priv, otherNonce)
	movedNonce := signed(t, priv, agentNonce)
	movedNonce.Nonce = bytes.Repeat([]byte{7}, NonceSize)
	job := signed(t, priv, agentNonce)
	job.HubSig = ed25519.Sign(priv, append([]byte("vordr/job/v1\x00"), append(job.Nonce, agentNonce...)...))

	for name, ch := range map[string]Challenge{"tampered": tampered, "short": short, "replayed": replayed,
		"other hub nonce": movedNonce, "other context": job} {
		if got := ch.CheckHubKey(agentNonce, pub); got != HubKeyMismatch {
			t.Errorf("%s: %s", name, got)
		}
	}
	if got := signed(t, priv, agentNonce).CheckHubKey(nil, pub); got != HubKeyMismatch {
		t.Errorf("without the agent's nonce: %s", got)
	}
}

// An agent before ADR-0170 reads the challenge leniently and ignores hub_sig; a hub before it sends none.
func TestChallengeFieldIsOptional(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	agentNonce, _ := NewChallengeNonce()
	raw, _ := json.Marshal(signed(t, priv, agentNonce))
	var older struct {
		T     string `json:"t"`
		Nonce []byte `json:"nonce"`
	}
	if err := json.Unmarshal(raw, &older); err != nil || older.T != TChallenge || len(older.Nonce) != NonceSize {
		t.Fatalf("older agent: %v %+v", err, older)
	}
	unsigned, _ := json.Marshal(Challenge{T: TChallenge, Nonce: make([]byte, NonceSize)})
	if bytes.Contains(unsigned, []byte("hub_sig")) {
		t.Fatalf("unsigned challenge carries the field: %s", unsigned)
	}
}

// Refused are exactly the findings the agent sends no hello for; each has its own close code and back (ADR-0170).
func TestHubKeyRefusalAndCloseCode(t *testing.T) {
	for h, want := range map[HubKeyCheck]int{HubKeyMatches: 0, HubKeyUnsigned: 0, HubKeyMismatch: CloseHubKeyMismatch,
		HubKeyDowngrade: CloseHubKeyDowngrade} {
		if h.Refused() != (want != 0) || h.CloseCode() != want {
			t.Errorf("%s: refused %v, close code %d", h, h.Refused(), h.CloseCode())
		}
		if back, ok := HubKeyRefusal(want); want != 0 && (!ok || back != h) {
			t.Errorf("%s: close code %d reads back as %q %v", h, want, back, ok)
		}
	}
	for _, code := range []int{0, 1000, 1001, 1006, 4000, 4003} {
		if h, ok := HubKeyRefusal(code); ok {
			t.Errorf("close code %d read as the refusal %s", code, h)
		}
	}
}
