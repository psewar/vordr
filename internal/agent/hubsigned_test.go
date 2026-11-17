package agent

import (
	"os"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/policy"
	"github.com/psewar/vordr/internal/wire"
)

// The record of a hub that signed (ADR-0170) belongs to one hub.url and one hub.public_key: agent.yaml changed in
// either starts fresh. Without it nothing changes; an unreadable one counts as seen.
func TestHubSignedRecord(t *testing.T) {
	dir := t.TempDir()
	key, other := otherKey(t), otherKey(t)
	cfg := policy.Config{StateDir: dir, Hub: policy.Hub{URL: "wss://hub.example/v1/tunnel", PublicKey: key}}
	if got := readHubSigned(cfg); got != HubSignedNone {
		t.Fatalf("fresh install: %v", got)
	}
	if err := rememberHubSigned(cfg, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := readHubSigned(cfg); got != HubSignedSeen {
		t.Fatalf("after remembering: %v", got)
	}
	moved := cfg
	moved.Hub.URL = "wss://hub.invalid/v1/tunnel"
	rekeyed := cfg
	rekeyed.Hub.PublicKey = other
	for name, c := range map[string]policy.Config{"another hub.url": moved, "another hub.public_key": rekeyed} {
		if got := readHubSigned(c); got != HubSignedNone {
			t.Errorf("%s: %v", name, got)
		}
	}
	if err := os.WriteFile(hubSignedPath(dir), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readHubSigned(cfg); got != HubSignedUnreadable {
		t.Fatalf("damaged record: %v", got)
	}
}

// Only an unsigned challenge changes with a record: it becomes a downgrade. What the signature says stays.
func TestJudgeHubKey(t *testing.T) {
	for _, tc := range []struct {
		check wire.HubKeyCheck
		rec   HubSignedRecord
		want  wire.HubKeyCheck
	}{
		{wire.HubKeyUnsigned, HubSignedNone, wire.HubKeyUnsigned},
		{wire.HubKeyUnsigned, HubSignedSeen, wire.HubKeyDowngrade},
		{wire.HubKeyUnsigned, HubSignedUnreadable, wire.HubKeyDowngrade},
		{wire.HubKeyMatches, HubSignedNone, wire.HubKeyMatches},
		{wire.HubKeyMatches, HubSignedSeen, wire.HubKeyMatches},
		{wire.HubKeyMismatch, HubSignedNone, wire.HubKeyMismatch},
		{wire.HubKeyMismatch, HubSignedSeen, wire.HubKeyMismatch},
	} {
		if got := judgeHubKey(tc.check, tc.rec); got != tc.want {
			t.Errorf("%s with record %v: %s, want %s", tc.check, tc.rec, got, tc.want)
		}
	}
}
