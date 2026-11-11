package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/psewar/vordr/internal/protocol"
)

func isRefused(err error) bool {
	var r refused
	return errors.As(err, &r)
}

func newKey(t *testing.T) (string, ed25519.PublicKey) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "operator.key")
	var out bytes.Buffer
	if err := keygen(path, &out); err != nil {
		t.Fatal(err)
	}
	priv, err := readKey(path)
	if err != nil {
		t.Fatal(err)
	}
	pub := priv.Public().(ed25519.PublicKey)
	if !strings.Contains(out.String(), base64.StdEncoding.EncodeToString(pub)) {
		t.Fatalf("keygen did not print the public key: %q", out.String())
	}
	return path, pub
}

func distWith(t *testing.T, files map[string]string) string {
	t.Helper()
	d := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(d, name), []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func TestKeygenNeverReplacesAKey(t *testing.T) {
	path, _ := newKey(t)
	before, _ := os.ReadFile(path)
	if err := keygen(path, &bytes.Buffer{}); !isRefused(err) {
		t.Fatalf("second keygen: %v, want refused", err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("the existing key changed")
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
			t.Fatalf("mode %o, want 600", st.Mode().Perm())
		}
	}
}

func TestPublicKeyMatchesKeygen(t *testing.T) {
	path, pub := newKey(t)
	var out bytes.Buffer
	if err := printPublic(path, &out); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != base64.StdEncoding.EncodeToString(pub) {
		t.Fatalf("public-key %q differs from keygen", out.String())
	}
}

func TestReadKeyRefusesOpenFilesAndBadContent(t *testing.T) {
	d := t.TempDir()
	if _, err := readKey(filepath.Join(d, "missing.key")); !isRefused(err) {
		t.Fatalf("missing key: %v, want refused", err)
	}
	bad := filepath.Join(d, "bad.key")
	os.WriteFile(bad, []byte("not base64 of a seed\n"), 0o600)
	if _, err := readKey(bad); !isRefused(err) {
		t.Fatalf("bad key: %v, want refused", err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	path, _ := newKey(t)
	os.Chmod(path, 0o644)
	if _, err := readKey(path); !isRefused(err) {
		t.Fatalf("key readable by others: %v, want refused", err)
	}
}

func TestSignWritesAManifestTheAgentAccepts(t *testing.T) {
	key, pub := newKey(t)
	dist := distWith(t, map[string]string{
		"vordr-agent-linux-arm64":       "arm64 build",
		"vordr-agent-windows-amd64.exe": "windows build",
		"vordr-hub-linux-amd64":         "not an agent",
	})
	out := filepath.Join(t.TempDir(), "release")
	var log bytes.Buffer
	// Neither build is for a test machine that could run it; the reason makes that explicit.
	if runtime.GOOS == "linux" && runtime.GOARCH == "arm64" || runtime.GOOS == "windows" && runtime.GOARCH == "amd64" {
		t.Skip("a fake build for this platform would be executed")
	}
	if err := sign(key, "1.2.3", dist, out, "test builds", &log); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(out, "manifest.json"))
	sig, _ := os.ReadFile(filepath.Join(out, "manifest.sig"))
	m, ok := protocol.VerifyManifest(raw, sig, pub)
	if !ok {
		t.Fatal("manifest does not verify with the operator key")
	}
	if m.Version != "1.2.3" || len(m.Binaries) != 2 {
		t.Fatalf("manifest %+v, want version 1.2.3 with two agent builds", m)
	}
	for _, b := range m.Binaries {
		if _, err := os.Stat(filepath.Join(out, b.File)); err != nil {
			t.Fatalf("%s not copied: %v", b.File, err)
		}
	}
	if _, err := os.Stat(filepath.Join(out, "vordr-hub-linux-amd64")); err == nil {
		t.Fatal("the hub binary was copied into the agent release")
	}
	if !strings.Contains(log.String(), "version check skipped: test builds") {
		t.Fatalf("skip reason not reported: %q", log.String())
	}
}

func TestSignRefusesBeforeWriting(t *testing.T) {
	key, _ := newKey(t)
	dist := distWith(t, map[string]string{"vordr-agent-linux-arm": "arm build"})
	full := t.TempDir()
	os.WriteFile(filepath.Join(full, "old"), []byte("x"), 0o644)
	cases := map[string]struct {
		ver, dist, out, reason string
	}{
		"no release version":      {"1.2.3-dev", dist, filepath.Join(t.TempDir(), "r"), "x"},
		"no binaries":             {"1.2.3", t.TempDir(), filepath.Join(t.TempDir(), "r"), "x"},
		"missing dist":            {"1.2.3", filepath.Join(t.TempDir(), "none"), filepath.Join(t.TempDir(), "r"), "x"},
		"out dir not empty":       {"1.2.3", dist, full, "x"},
		"no build for the host":   {"1.2.3", dist, filepath.Join(t.TempDir(), "r"), ""},
		"windows build lacks exe": {"1.2.3", distWith(t, map[string]string{"vordr-agent-windows-amd64": "w"}), filepath.Join(t.TempDir(), "r"), "x"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if name == "no build for the host" && runtime.GOOS == "linux" && runtime.GOARCH == "arm" {
				t.Skip("the fake build would be for this platform")
			}
			err := sign(key, c.ver, c.dist, c.out, c.reason, &bytes.Buffer{})
			if !isRefused(err) {
				t.Fatalf("got %v, want refused", err)
			}
			if _, err := os.Stat(filepath.Join(c.out, "manifest.json")); err == nil {
				t.Fatal("manifest written despite the refusal")
			}
		})
	}
}

func TestSignChecksTheVersionOfTheHostBuild(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the stand-in build is a shell script")
	}
	key, _ := newKey(t)
	host := "vordr-agent-linux-" + runtime.GOARCH
	dist := distWith(t, map[string]string{host: "#!/bin/sh\necho 1.2.2\n"})
	err := sign(key, "1.2.3", dist, filepath.Join(t.TempDir(), "r"), "", &bytes.Buffer{})
	if !isRefused(err) || !strings.Contains(err.Error(), `"1.2.2"`) {
		t.Fatalf("wrong version: %v, want refused naming 1.2.2", err)
	}
	dist = distWith(t, map[string]string{host: "#!/bin/sh\necho 1.2.3\n"})
	if err := sign(key, "1.2.3", dist, filepath.Join(t.TempDir(), "r"), "", &bytes.Buffer{}); err != nil {
		t.Fatalf("matching version: %v", err)
	}
}
