package protocol

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 28, 2, 30, 0, 0, time.UTC)

func keys(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func job(verb Verb) Job {
	return Job{ID: "j-1", Node: "example-host", Verb: verb, IssuedAt: t0, ExpiresAt: t0.Add(2 * time.Minute)}
}

func sign(t *testing.T, j Job, priv ed25519.PrivateKey) SignedJob {
	t.Helper()
	sj, err := SignJob(j, func(m []byte) []byte { return ed25519.Sign(priv, m) })
	if err != nil {
		t.Fatal(err)
	}
	return sj
}

func TestVerifyJobAcceptsValidJob(t *testing.T) {
	pub, priv := keys(t)
	j := job(VerbInstall)
	j.Scope = "security"
	got, reason := VerifyJob(sign(t, j, priv), pub, "example-host", t0.Add(time.Minute), NewReplayGuard())
	if reason != InvalidNone || got.ID != "j-1" || got.Scope != "security" {
		t.Fatalf("got %+v, %q", got, reason)
	}
}

func TestVerifyJobRejects(t *testing.T) {
	pub, priv := keys(t)
	_, otherPriv := keys(t)
	now := t0.Add(time.Minute)

	tampered := sign(t, job(VerbCheck), priv)
	tampered.Payload = []byte(string(tampered.Payload[:len(tampered.Payload)-1]) + " }")

	installNoScope := job(VerbInstall)
	installPackageName := job(VerbInstall)
	installPackageName.Scope = "openssl" // package names never come from the hub
	checkWithScope := job(VerbCheck)
	checkWithScope.Scope = "all"
	cancelNoTarget := job(VerbCancel)
	wrongNode := job(VerbCheck)
	wrongNode.Node = "other-host"
	future := job(VerbCheck)
	future.IssuedAt, future.ExpiresAt = now.Add(2*time.Minute), now.Add(3*time.Minute)
	expired := job(VerbCheck)
	expired.ExpiresAt = now
	tooLong := job(VerbCheck)
	tooLong.ExpiresAt = t0.Add(MaxJobLifetime + time.Second)
	badID := job(VerbCheck)
	badID.ID = "../etc"

	unknownField, _ := json.Marshal(map[string]any{"job_id": "j-1", "node": "example-host", "verb": "check",
		"issued_at": t0, "expires_at": t0.Add(time.Minute), "command": "rm -rf /"})
	unknownVerb, _ := json.Marshal(map[string]any{"job_id": "j-1", "node": "example-host", "verb": "shell",
		"issued_at": t0, "expires_at": t0.Add(time.Minute)})

	cases := []struct {
		name string
		sj   SignedJob
		key  ed25519.PublicKey
		want InvalidReason
	}{
		{"no hub key", sign(t, job(VerbCheck), priv), nil, InvalidNoHubKey},
		{"foreign key", sign(t, job(VerbCheck), otherPriv), pub, InvalidBadSignature},
		{"tampered payload", tampered, pub, InvalidBadSignature},
		{"unknown field", SignedJob{unknownField, ed25519.Sign(priv, signedBytes(unknownField))}, pub, InvalidMalformed},
		{"bad job id", sign(t, badID, priv), pub, InvalidMalformed},
		{"unknown verb", SignedJob{unknownVerb, ed25519.Sign(priv, signedBytes(unknownVerb))}, pub, InvalidUnknownVerb},
		{"install without scope", sign(t, installNoScope, priv), pub, InvalidBadParameters},
		{"install with package name", sign(t, installPackageName, priv), pub, InvalidBadParameters},
		{"check with scope", sign(t, checkWithScope, priv), pub, InvalidBadParameters},
		{"cancel without target", sign(t, cancelNoTarget, priv), pub, InvalidBadParameters},
		{"wrong node", sign(t, wrongNode, priv), pub, InvalidWrongNode},
		{"issued in the future", sign(t, future, priv), pub, InvalidNotYetValid},
		{"expired", sign(t, expired, priv), pub, InvalidExpired},
		{"lifetime too long", sign(t, tooLong, priv), pub, InvalidLifetimeTooLong},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, got := VerifyJob(c.sj, c.key, "example-host", now, NewReplayGuard()); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

// A reason to run outside the window (ADR-0087): only on install and reboot, and only a readable one.
// security_fix (ADR-0111): only with an install of pip/npm packages or dependencies, package names, none twice, and
// with one package in the scope only that one.
func TestSecurityFixParameters(t *testing.T) {
	pub, priv := keys(t)
	for name, tc := range map[string]struct {
		verb  Verb
		scope string
		names []string
		want  InvalidReason
	}{
		"location":          {VerbInstall, "pkg:npm-global", []string{"undici", "@scope/pkg"}, InvalidNone},
		"one package":       {VerbInstall, "pkg:npm-global/undici", []string{"undici"}, InvalidNone},
		"dependencies":      {VerbInstall, "deps:node-red", []string{"lib-a", "lib-b"}, InvalidNone},
		"another package":   {VerbInstall, "pkg:npm-global/undici", []string{"zod"}, InvalidBadParameters},
		"system packages":   {VerbInstall, "all", []string{"openssl"}, InvalidBadParameters},
		"reboot":            {VerbReboot, "", []string{"undici"}, InvalidBadParameters},
		"twice":             {VerbInstall, "pkg:npm-global", []string{"undici", "undici"}, InvalidBadParameters},
		"no package name":   {VerbInstall, "pkg:npm-global", []string{"--registry=x"}, InvalidBadParameters},
		"too many":          {VerbInstall, "pkg:npm-global", manyNames(MaxSecurityFix + 1), InvalidBadParameters},
		"as many as may be": {VerbInstall, "pkg:npm-global", manyNames(MaxSecurityFix), InvalidNone},
	} {
		t.Run(name, func(t *testing.T) {
			j := job(tc.verb)
			j.Scope, j.SecurityFix = tc.scope, tc.names
			got, reason := VerifyJob(sign(t, j, priv), pub, "example-host", t0.Add(time.Minute), NewReplayGuard())
			if reason != tc.want || (reason == InvalidNone && len(got.SecurityFix) != len(tc.names)) {
				t.Fatalf("got %+v, %q", got, reason)
			}
		})
	}
}

func manyNames(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "pkg-" + strings.Repeat("a", i%5) + string(rune('a'+i%26)) + strings.Repeat("b", i/26)
	}
	return out
}

func TestOutsideWindowReason(t *testing.T) {
	pub, priv := keys(t)
	cancel := job(VerbCancel)
	cancel.Target = "j-0"
	for name, tc := range map[string]struct {
		base   Job
		scope  string
		reason string
		want   InvalidReason
	}{
		"install":           {job(VerbInstall), "all", "ops: tomorrow is no good", InvalidNone},
		"reboot":            {job(VerbReboot), "", "kernel update, approved now", InvalidNone},
		"longest":           {job(VerbInstall), "pkg:npm-global", strings.Repeat("ä", MaxReasonLen/2), InvalidNone},
		"check":             {job(VerbCheck), "", "ops", InvalidBadParameters},
		"cancel":            {cancel, "", "ops", InvalidBadParameters},
		"too long":          {job(VerbInstall), "all", strings.Repeat("x", MaxReasonLen+1), InvalidBadParameters},
		"blank":             {job(VerbInstall), "all", "   ", InvalidBadParameters}, // spaces only: not a control character
		"control character": {job(VerbReboot), "", "ops\nrm -rf /", InvalidBadParameters},
	} {
		t.Run(name, func(t *testing.T) {
			j := tc.base
			j.Scope, j.OutsideWindow = tc.scope, tc.reason
			got, reason := VerifyJob(sign(t, j, priv), pub, "example-host", t0.Add(time.Minute), NewReplayGuard())
			if reason != tc.want || (reason == InvalidNone && got.OutsideWindow != tc.reason) {
				t.Fatalf("got %+v, %q", got, reason)
			}
		})
	}
	for s, want := range map[string]bool{"ops": true, "a\x00b": false, "a b": true, "a\xffb": false, "": false,
		"  ": false, " ops ": true} {
		if ValidReason(s) != want {
			t.Errorf("ValidReason(%q) = %v", s, !want)
		}
	}
}

// A backup job names exactly one guest of the hypervisor (ADR-0096); no other verb carries one.
func TestBackupGuest(t *testing.T) {
	pub, priv := keys(t)
	for name, tc := range map[string]struct {
		verb  Verb
		guest string
		scope string
		want  InvalidReason
	}{
		"container":          {VerbBackup, "ct/302", "", InvalidNone},
		"vm":                 {VerbBackup, "vm/301", "", InvalidNone},
		"no guest":           {VerbBackup, "", "", InvalidBadParameters},
		"host group":         {VerbBackup, "host/hv", "", InvalidBadParameters}, // vzdump backs up guests only
		"id below 100":       {VerbBackup, "ct/99", "", InvalidBadParameters},
		"option":             {VerbBackup, "ct/--all", "", InvalidBadParameters},
		"two guests":         {VerbBackup, "ct/302 ct/303", "", InvalidBadParameters},
		"with scope":         {VerbBackup, "ct/302", "all", InvalidBadParameters},
		"check with guest":   {VerbCheck, "ct/302", "", InvalidBadParameters},
		"install with guest": {VerbInstall, "ct/302", "all", InvalidBadParameters},
		"check with garbage": {VerbCheck, "x", "", InvalidBadParameters},
	} {
		t.Run(name, func(t *testing.T) {
			j := job(tc.verb)
			j.Guest, j.Scope = tc.guest, tc.scope
			got, reason := VerifyJob(sign(t, j, priv), pub, "example-host", t0.Add(time.Minute), NewReplayGuard())
			if reason != tc.want || (reason == InvalidNone && got.Guest != tc.guest) {
				t.Fatalf("got %+v, %q, want %q", got, reason, tc.want)
			}
		})
	}
	if kind, id, ok := ParseGuest("vm/301"); !ok || kind != "vm" || id != "301" {
		t.Errorf("ParseGuest(vm/301) = %q %q %v", kind, id, ok)
	}
}

func TestVerifyJobRejectsReplayUntilExpiry(t *testing.T) {
	pub, priv := keys(t)
	sj := sign(t, job(VerbCheck), priv)
	g := NewReplayGuard()
	if _, r := VerifyJob(sj, pub, "example-host", t0.Add(time.Minute), g); r != InvalidNone {
		t.Fatalf("first: %q", r)
	}
	if _, r := VerifyJob(sj, pub, "example-host", t0.Add(90*time.Second), g); r != InvalidReplayed {
		t.Fatalf("second: %q, want replayed", r)
	}
	// After expiry the time check rejects it; the guard has forgotten the id by then.
	if _, r := VerifyJob(sj, pub, "example-host", t0.Add(3*time.Minute), g); r != InvalidExpired {
		t.Fatalf("after expiry: %q, want expired", r)
	}
}

func TestRejectedJobDoesNotConsumeID(t *testing.T) {
	pub, priv := keys(t)
	wrong := job(VerbCheck)
	wrong.Node = "other-host"
	g := NewReplayGuard()
	VerifyJob(sign(t, wrong, priv), pub, "example-host", t0.Add(time.Minute), g)
	if _, r := VerifyJob(sign(t, job(VerbCheck), priv), pub, "example-host", t0.Add(time.Minute), g); r != InvalidNone {
		t.Fatalf("valid job after a rejected one with the same id: %q", r)
	}
}

func TestParseScope(t *testing.T) {
	for in, ok := range map[string]bool{"security": true, "all": true, "app:gitlab": true, "app:": false,
		"app:GitLab": false, "app:../x": false, "openssl": false, "": false} {
		if _, got := ParseScope(in); got != ok {
			t.Errorf("ParseScope(%q) = %v, want %v", in, got, ok)
		}
	}
}

func TestCodes(t *testing.T) {
	seen := map[Code]bool{}
	for _, c := range AllCodes() {
		if seen[c] {
			t.Errorf("duplicate code %q", c)
		}
		seen[c] = true
	}
	if len(seen) != 29 {
		t.Errorf("%d codes, design table has 29", len(seen))
	}
	for c, want := range map[Code]bool{CodeOK: false, CodeRejectedOutsideWindow: false, CodeRejectedInvalidJob: true,
		CodeInterrupted: true, Code("made_up"): true, CodePrecheckVersionFlagged: true, CodePrecheckVersionTooNew: false,
		CodePrecheckVettingUnavailable: true, CodePrecheckNotFromRegistry: true, CodeFailedTargetAuth: true, CodeFailedTargetTLS: true,
		CodeFailedTargetHostKey: true, CodeFailedConfigBackup: true, CodeFailedBackup: true} {
		if c.Problem() != want {
			t.Errorf("%q.Problem() = %v, want %v", c, c.Problem(), want)
		}
	}
}
