package identity

import (
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreateKeepsKey(t *testing.T) {
	dir := t.TempDir()
	a, created, err := LoadOrCreate(dir)
	if err != nil || !created {
		t.Fatalf("first: created=%v err=%v", created, err)
	}
	b, created, err := LoadOrCreate(dir)
	if err != nil || created {
		t.Fatalf("second: created=%v err=%v", created, err)
	}
	if a.Fingerprint() != b.Fingerprint() || len(a.Fingerprint()) != 64 {
		t.Fatalf("fingerprints differ: %s / %s", a.Fingerprint(), b.Fingerprint())
	}
	pub, _ := base64.StdEncoding.DecodeString(b.PublicB64())
	if !ed25519.Verify(pub, []byte("challenge"), a.Sign([]byte("challenge"))) {
		t.Fatal("signature of the first instance does not verify with the reloaded key")
	}
	info, err := os.Stat(filepath.Join(dir, keyFile))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 && os.PathSeparator == '/' {
		t.Fatalf("key file mode %v, want 0600", perm)
	}
}

func TestLoadAtNeverCreates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.key")
	if id, found, err := LoadAt(path); id != nil || found || err != nil {
		t.Fatalf("missing key: %v %v %v", id, found, err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("LoadAt created a key")
	}
	created, _, _ := LoadOrCreateAt(path)
	loaded, found, err := LoadAt(path)
	if !found || err != nil || loaded.Fingerprint() != created.Fingerprint() {
		t.Fatalf("existing key: %v %v", found, err)
	}
}

func TestLoadRejectsDamagedKey(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, keyFile), []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadOrCreate(dir); err == nil {
		t.Fatal("damaged key accepted")
	}
}
