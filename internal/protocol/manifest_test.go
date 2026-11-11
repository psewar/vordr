package protocol

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func testManifest(t *testing.T, m Manifest) ([]byte, []byte, ed25519.PublicKey) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return raw, SignManifest(raw, func(b []byte) []byte { return ed25519.Sign(priv, b) }), pub
}

// Releases compare as numbers, X.Y.Z only; anything else never counts as newer.
func TestNewerRelease(t *testing.T) {
	cases := []struct {
		a, b  string
		newer bool
	}{
		{"0.7.3", "0.7.2", true}, {"0.7.10", "0.7.9", true}, {"1.0.0", "0.99.99", true}, {"0.8.0", "0.7.99", true},
		{"0.7.2", "0.7.2", false}, {"0.7.1", "0.7.2", false}, {"0.7.9", "0.7.10", false},
		{"0.8.0-dev", "0.7.2", false}, {"0.7.3", "2023.11.14-1200", false}, {"07.3.0", "0.7.2", false}, {"", "0.7.2", false},
	}
	for _, c := range cases {
		if got := NewerRelease(c.a, c.b); got != c.newer {
			t.Errorf("NewerRelease(%q, %q) = %v", c.a, c.b, got)
		}
	}
	if !IsRelease("0.7.2") || IsRelease("0.11.1-test") || IsRelease("2023.11.14-1200") {
		t.Error("IsRelease")
	}
	m := goodManifest()
	raw, sig, pub := testManifest(t, m)
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	if _, ok := VerifyManifestWith(raw, sig, []ed25519.PublicKey{other, pub}); !ok {
		t.Error("the second key signed it")
	}
	if _, ok := VerifyManifestWith(raw, sig, []ed25519.PublicKey{other}); ok {
		t.Error("no key signed it")
	}
}

func goodManifest() Manifest {
	sum := sha256.Sum256([]byte("binary"))
	return Manifest{Version: "0.7.0", Issued: time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC),
		Binaries: []ManifestBinary{{OS: "linux", Arch: "amd64", File: "vordr-agent-linux-amd64",
			SHA256: hex.EncodeToString(sum[:]), Size: 6}}}
}

func TestManifestSignature(t *testing.T) {
	raw, sig, key := testManifest(t, goodManifest())
	m, ok := VerifyManifest(raw, sig, key)
	if !ok || m.Version != "0.7.0" {
		t.Fatalf("good manifest: %+v %v", m, ok)
	}
	if b, ok := m.For("linux", "amd64"); !ok || b.File != "vordr-agent-linux-amd64" {
		t.Fatalf("for linux/amd64: %+v", b)
	}
	if _, ok := m.For("linux", "arm64"); ok {
		t.Fatal("found a build that is not there")
	}
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	tampered := []byte(strings.Replace(string(raw), "0.7.0", "0.7.1", 1))
	for name, c := range map[string]struct {
		raw, sig []byte
		key      ed25519.PublicKey
	}{
		"other key": {raw, sig, other},
		"tampered":  {tampered, sig, key},
		"no sig":    {raw, nil, key},
		"no key":    {raw, sig, nil},
	} {
		if _, ok := VerifyManifest(c.raw, c.sig, c.key); ok {
			t.Errorf("%s: accepted", name)
		}
	}
}

// Signed but malformed: still refused (a signed manifest naming "../" or no hash must not reach the agent's disk).
func TestManifestFields(t *testing.T) {
	for name, change := range map[string]func(*Manifest){
		"path in file": func(m *Manifest) { m.Binaries[0].File = "../../usr/bin/x" },
		"short hash":   func(m *Manifest) { m.Binaries[0].SHA256 = "abcd" },
		"no size":      func(m *Manifest) { m.Binaries[0].Size = 0 },
		"huge":         func(m *Manifest) { m.Binaries[0].Size = MaxBinarySize + 1 },
		"no version":   func(m *Manifest) { m.Version = "" },
		"no binaries":  func(m *Manifest) { m.Binaries = nil },
	} {
		m := goodManifest()
		change(&m)
		raw, sig, key := testManifest(t, m)
		if _, ok := VerifyManifest(raw, sig, key); ok {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestAgentUpdateJobParameters(t *testing.T) {
	sig := make([]byte, ed25519.SignatureSize)
	token := strings.Repeat("ab", 24)
	cases := map[string]struct {
		j  Job
		ok bool
	}{
		"complete":          {Job{Verb: VerbAgentUpdate, Manifest: []byte("{}"), ManifestSig: sig, Token: token}, true},
		"no token":          {Job{Verb: VerbAgentUpdate, Manifest: []byte("{}"), ManifestSig: sig}, false},
		"bad token":         {Job{Verb: VerbAgentUpdate, Manifest: []byte("{}"), ManifestSig: sig, Token: "x"}, false},
		"no manifest":       {Job{Verb: VerbAgentUpdate, ManifestSig: sig, Token: token}, false},
		"install with one":  {Job{Verb: VerbInstall, Scope: "all", Token: token}, false},
		"check with a file": {Job{Verb: VerbCheck, Manifest: []byte("{}")}, false},
	}
	for name, c := range cases {
		if got := validParameters(c.j); got != c.ok {
			t.Errorf("%s: %v, want %v", name, got, c.ok)
		}
	}
}
