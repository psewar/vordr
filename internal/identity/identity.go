// Package identity holds the agent's Ed25519 key pair (taken over from an earlier agent, ADR-0012). The public key is the
// agent's identity towards the hub; the hub admits a host by its fingerprint. The private key is DPAPI-protected on
// Windows and a 0600 file elsewhere.
package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

const keyFile = "agent-identity.key"

type Identity struct {
	priv ed25519.PrivateKey
	pub  ed25519.PublicKey
}

// LoadOrCreate reads the agent key pair from dir, or creates one if dir holds none. created tells which happened.
func LoadOrCreate(dir string) (id *Identity, created bool, err error) {
	return LoadOrCreateAt(filepath.Join(dir, keyFile))
}

// LoadOrCreateAt is LoadOrCreate for an explicit key file (the hub keeps its signing key this way).
func LoadOrCreateAt(path string) (id *Identity, created bool, err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, false, err
	}
	if _, statErr := os.Stat(path); errors.Is(statErr, fs.ErrNotExist) {
		id, err := create(path)
		return id, err == nil, err
	}
	id, err = load(path)
	return id, false, err
}

// LoadAt reads an existing key pair and never creates one; found=false if path holds none. For tools that run as
// another user than the service owning the key: a key they created would belong to the wrong user.
func LoadAt(path string) (id *Identity, found bool, err error) {
	if _, statErr := os.Stat(path); errors.Is(statErr, fs.ErrNotExist) {
		return nil, false, nil
	}
	id, err = load(path)
	return id, err == nil, err
}

func load(path string) (*Identity, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	seed, err := unprotect(raw)
	if err != nil {
		return nil, fmt.Errorf("identity %s: %w", path, err)
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("identity %s: invalid length %d", path, len(seed))
	}
	priv := ed25519.NewKeyFromSeed(seed)
	return &Identity{priv: priv, pub: priv.Public().(ed25519.PublicKey)}, nil
}

func create(path string) (*Identity, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	blob, err := protect(priv.Seed())
	if err != nil {
		return nil, err
	}
	// O_EXCL: if another process created the key in the meantime, keep that one instead of overwriting it.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	if _, err := f.Write(blob); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	return &Identity{priv: priv, pub: pub}, nil
}

// Fingerprint is the SHA-256 of the raw public key, hex - what the hub shows for approval.
func (i *Identity) Fingerprint() string { return Fingerprint(i.pub) }

// Fingerprint of any raw public key, the same way.
func Fingerprint(pub []byte) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:])
}

func (i *Identity) Public() ed25519.PublicKey { return i.pub }

func (i *Identity) PublicB64() string { return base64.StdEncoding.EncodeToString(i.pub) }

func (i *Identity) Sign(msg []byte) []byte { return ed25519.Sign(i.priv, msg) }
