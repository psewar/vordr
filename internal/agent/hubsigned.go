package agent

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/psewar/vordr/internal/policy"
	"github.com/psewar/vordr/internal/wire"
)

// Downgrade protection (ADR-0170): once this agent has seen the hub at hub.url sign its challenge with
// hub.public_key, it keeps that fact in its state_dir. A later unsigned challenge from the same hub.url, with the same
// hub.public_key, is then no older hub but a downgrade, refused like another key. The very first contact cannot be
// protected this way: until a hub has signed once, unsigned passes, so that older hubs keep working.

// hubSignedFile: the record in state_dir.
const hubSignedFile = "hub-signed.json"

// hubSigned is the record: the hub at URL signed its challenge with PublicKey (standard base64 of the key's bytes).
// Both as agent.yaml names them; agent.yaml changed in either names another hub, which may be older, and starts fresh.
type hubSigned struct {
	URL       string    `json:"hub_url"`
	PublicKey string    `json:"hub_public_key"`
	Since     time.Time `json:"since"` // when it was first seen, for the operator
}

func hubSignedPath(stateDir string) string { return filepath.Join(stateDir, hubSignedFile) }

// HubSignedRecord is what this agent remembers about the hub that agent.yaml names now.
type HubSignedRecord int

const (
	// HubSignedNone: no record for this hub.url and hub.public_key - a fresh install, or agent.yaml names another hub.
	HubSignedNone HubSignedRecord = iota
	// HubSignedSeen: the hub at this hub.url signed with this hub.public_key before.
	HubSignedSeen
	// HubSignedUnreadable: the record exists but cannot be read. It counts as seen: the file exists only because a
	// hub signed, and refusing an unsigned challenge is the safe side. A hub that signs replaces it.
	HubSignedUnreadable
)

// readHubSigned reports what state_dir says about the hub of cfg.
func readHubSigned(cfg policy.Config) HubSignedRecord {
	path := hubSignedPath(cfg.StateDir)
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return HubSignedNone
	}
	raw, err := os.ReadFile(path)
	var rec hubSigned
	if err != nil || json.Unmarshal(raw, &rec) != nil {
		return HubSignedUnreadable
	}
	if key := cfg.HubKey(); key == nil || rec.URL != cfg.Hub.URL ||
		rec.PublicKey != base64.StdEncoding.EncodeToString(key) {
		return HubSignedNone
	}
	return HubSignedSeen
}

// rememberHubSigned writes the record for the hub of cfg, atomically.
func rememberHubSigned(cfg policy.Config, now time.Time) error {
	return writeJSON(hubSignedPath(cfg.StateDir), hubSigned{URL: cfg.Hub.URL,
		PublicKey: base64.StdEncoding.EncodeToString(cfg.HubKey()), Since: now.UTC()})
}

// hubSignedNote says, for humans, where a downgrade comes from and how a hub put back on purpose is let in again.
func hubSignedNote(cfg policy.Config, rec HubSignedRecord) string {
	path := hubSignedPath(cfg.StateDir)
	if rec == HubSignedUnreadable {
		return path + " exists but cannot be read here, so it counts as signed before; as the agent's own user, " +
			"or with that file removed, it is read again"
	}
	return path + " remembers that this hub signed its challenge; if the hub was put back to a version before " +
		"ADR-0170 on purpose, remove that file"
}

// judgeHubKey applies the record to what one challenge says: unsigned from a hub that signed before is a downgrade.
func judgeHubKey(check wire.HubKeyCheck, rec HubSignedRecord) wire.HubKeyCheck {
	if check == wire.HubKeyUnsigned && rec != HubSignedNone {
		return wire.HubKeyDowngrade
	}
	return check
}
