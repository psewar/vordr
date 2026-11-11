package protocol

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strconv"
	"time"
)

// Manifest is an agent release as the operator signs it, away from the hub (design 7.6 and 10.4). The hub only
// passes it on: an agent replaces itself only with a binary whose hash a manifest names that the operator key from
// its own agent.yaml signed. Whoever takes over the hub cannot push a binary of their own.
type Manifest struct {
	Version  string           `json:"version"`
	Issued   time.Time        `json:"issued"`
	Binaries []ManifestBinary `json:"binaries"`
}

// ManifestBinary is the build for one platform.
type ManifestBinary struct {
	OS     string `json:"os"`     // linux, windows
	Arch   string `json:"arch"`   // amd64, arm64, arm
	File   string `json:"file"`   // the name the hub serves it under
	SHA256 string `json:"sha256"` // hex
	Size   int64  `json:"size"`
}

const (
	// manifestContext separates manifest signatures from anything else the operator key might sign.
	manifestContext = "vordr/agent-manifest/v1\x00"
	// MaxBinarySize bounds what an agent downloads for an update.
	MaxBinarySize = 128 << 20
)

var (
	versionPattern  = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+-]{0,63}$`)
	fileNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	tokenPattern    = regexp.MustCompile(`^[0-9a-f]{32,128}$`)
	// releasePattern: the X.Y.Z of a published release; hand builds carry dates or suffixes and never compare.
	releasePattern = regexp.MustCompile(`^(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})$`)
)

// VerifyManifestWith verifies with each key in turn - the operator key and, where configured, its backup.
func VerifyManifestWith(raw, sig []byte, keys []ed25519.PublicKey) (Manifest, bool) {
	for _, k := range keys {
		if m, ok := VerifyManifest(raw, sig, k); ok {
			return m, true
		}
	}
	return Manifest{}, false
}

// IsRelease reports whether v is a release version X.Y.Z.
func IsRelease(v string) bool { return releasePattern.MatchString(v) }

// NewerRelease reports whether release a is newer than release b; false unless both are X.Y.Z. Every manifest the
// operator ever signed stays valid, so an agent on a release takes only newer ones: an old manifest sent again cannot
// bring an old agent back. Going back to old code means publishing it under a new version.
func NewerRelease(a, b string) bool {
	pa, pb := releasePattern.FindStringSubmatch(a), releasePattern.FindStringSubmatch(b)
	if pa == nil || pb == nil {
		return false
	}
	for i := 1; i <= 3; i++ {
		x, _ := strconv.Atoi(pa[i]) // the pattern allows at most nine digits
		y, _ := strconv.Atoi(pb[i])
		if x != y {
			return x > y
		}
	}
	return false
}

// SignManifest signs the manifest's bytes exactly as they are stored; sign keeps the private key with its holder.
func SignManifest(raw []byte, sign func([]byte) []byte) []byte {
	return sign(append([]byte(manifestContext), raw...))
}

// VerifyManifest checks the operator's signature over raw, then decodes it strictly. ok=false for any doubt.
func VerifyManifest(raw, sig []byte, key ed25519.PublicKey) (Manifest, bool) {
	if len(key) != ed25519.PublicKeySize || len(sig) != ed25519.SignatureSize ||
		!ed25519.Verify(key, append([]byte(manifestContext), raw...), sig) {
		return Manifest{}, false
	}
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&m) != nil || dec.More() || !versionPattern.MatchString(m.Version) || len(m.Binaries) == 0 {
		return Manifest{}, false
	}
	for _, b := range m.Binaries {
		sum, err := hex.DecodeString(b.SHA256)
		if err != nil || len(sum) != 32 || !fileNamePattern.MatchString(b.File) || b.Size <= 0 ||
			b.Size > MaxBinarySize || b.OS == "" || b.Arch == "" {
			return Manifest{}, false
		}
	}
	return m, true
}

// For finds the build for a platform.
func (m Manifest) For(goos, goarch string) (ManifestBinary, bool) {
	for _, b := range m.Binaries {
		if b.OS == goos && b.Arch == goarch {
			return b, true
		}
	}
	return ManifestBinary{}, false
}

// Has reports whether the manifest names this file (the hub serves nothing else).
func (m Manifest) Has(file string) (ManifestBinary, bool) {
	for _, b := range m.Binaries {
		if b.File == file {
			return b, true
		}
	}
	return ManifestBinary{}, false
}

// ValidToken: the one-time download token of an agent_update job.
func ValidToken(t string) bool { return tokenPattern.MatchString(t) }
