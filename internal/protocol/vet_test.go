package protocol

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"testing"
	"time"
)

func TestParsePkgScope(t *testing.T) {
	for s, want := range map[string]Scope{
		"pkg:node-red":                  {Kind: ScopePkg, Location: "node-red"},
		"pkg:npm-global/@example/nodes": {Kind: ScopePkg, Location: "npm-global", Package: "@example/nodes"},
		"pkg:services-web-app/pip":      {Kind: ScopePkg, Location: "services-web-app", Package: "pip"},
		"pkg:venv/zope.interface":       {Kind: ScopePkg, Location: "venv", Package: "zope.interface"},
		"deps:node-red":                 {Kind: ScopeDeps, Location: "node-red"}, // ADR-0104
	} {
		got, ok := ParseScope(s)
		if !ok || got != want || got.String() != s {
			t.Errorf("%q: %+v %v -> %q", s, got, ok, got.String())
		}
	}
	for _, s := range []string{"pkg:", "pkg:Node-Red", "pkg:x/", "pkg:x/a b", "pkg:x/a;rm", "pkg:x/--index-url=evil",
		"pkg:x/-e", "pkg:../etc", "deps:", "deps:node-red/lib-a", "deps:../etc"} {
		if _, ok := ParseScope(s); ok {
			t.Errorf("%q parsed", s)
		}
	}
}

func TestVetSignature(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	sign := func(b []byte) []byte { return ed25519.Sign(priv, b) }
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	plan := []VetPackage{{Name: "debug", Version: "4.4.2"}, {Name: "ms", Version: "2.1.3"}}
	v := Vet{JobID: "j1", Node: "echo", Ecosystem: "npm", IssuedAt: now,
		Packages: []VetPackage{{Name: "debug", Version: "4.4.2", Flagged: []string{"MAL-2025-46974"}, Malicious: true},
			{Name: "ms", Version: "2.1.3"}}}
	sv, err := SignVet(v, sign)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := VerifyVet(sv, pub, "echo", "j1", now.Add(time.Minute))
	if !ok || !got.Covers(plan) || !got.Packages[0].Malicious {
		t.Fatalf("valid statement: %v %+v", ok, got)
	}
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	tampered := sv
	tampered.Payload = []byte(string(sv.Payload[:len(sv.Payload)-1]) + " }")
	jobSigned := SignedVet{Payload: sv.Payload, Signature: ed25519.Sign(priv, append([]byte(jobContext), sv.Payload...))}
	for name, c := range map[string]struct {
		sv        SignedVet
		key       ed25519.PublicKey
		node, job string
		at        time.Time
	}{
		"other key":         {sv, otherPub, "echo", "j1", now},
		"other node":        {sv, pub, "golf", "j1", now},
		"other job":         {sv, pub, "echo", "j2", now},
		"too old":           {sv, pub, "echo", "j1", now.Add(MaxVetAge + time.Second)},
		"from the future":   {sv, pub, "echo", "j1", now.Add(-2 * MaxClockSkew)},
		"changed payload":   {tampered, pub, "echo", "j1", now},
		"a job's signature": {jobSigned, pub, "echo", "j1", now},
		"no key":            {sv, nil, "echo", "j1", now},
	} {
		if _, ok := VerifyVet(c.sv, c.key, c.node, c.job, c.at); ok {
			t.Errorf("%s: accepted", name)
		}
	}
	// Unknown fields are refused: a statement must be exactly what this version understands.
	var m map[string]any
	_ = json.Unmarshal(sv.Payload, &m)
	m["trust_me"] = true
	extra, _ := json.Marshal(m)
	if _, ok := VerifyVet(SignedVet{Payload: extra, Signature: sign(append([]byte(vetContext), extra...))}, pub,
		"echo", "j1", now); ok {
		t.Error("unknown field accepted")
	}
	// Covers: every planned entry, and nothing else.
	if got.Covers(plan[:1]) || got.Covers(append(plan, VetPackage{Name: "x", Version: "1.0.0"})) {
		t.Error("Covers accepts a different plan")
	}
}
