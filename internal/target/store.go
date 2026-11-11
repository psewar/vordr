package target

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Store keeps an instance's configuration backups (ADR-0090): one file per export, readable by the instance only,
// the newest Keep of them, and last.json naming the newest good one. The container is backed up like every guest,
// which puts the exports off the device.
type Store struct {
	Dir  string
	Keep int
}

// Saved describes one stored export.
type Saved struct {
	At     time.Time `json:"at"`
	File   string    `json:"file"`
	SHA256 string    `json:"sha256"`
	Bytes  int       `json:"bytes"`
}

const lastFile = "last.json"

// Save stores an export that passed its driver's form check, then drops the oldest beyond Keep.
func (s Store) Save(driver string, data []byte, ext string, now time.Time) (Saved, error) {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return Saved{}, err
	}
	sum := sha256.Sum256(data)
	saved := Saved{At: now.UTC(), File: driver + "-" + now.UTC().Format("20060102T150405Z") + ext,
		SHA256: hex.EncodeToString(sum[:]), Bytes: len(data)}
	if err := writeAtomic(filepath.Join(s.Dir, saved.File), data); err != nil {
		return Saved{}, err
	}
	meta, _ := json.Marshal(saved)
	if err := writeAtomic(filepath.Join(s.Dir, lastFile), meta); err != nil {
		return Saved{}, err
	}
	return saved, s.prune(driver, ext)
}

// Last is the newest good export; ok false when there is none yet.
func (s Store) Last() (Saved, bool) {
	raw, err := os.ReadFile(filepath.Join(s.Dir, lastFile))
	if errors.Is(err, fs.ErrNotExist) {
		return Saved{}, false
	}
	var saved Saved
	if err != nil || json.Unmarshal(raw, &saved) != nil {
		return Saved{}, false // unreadable counts as none: the next check takes a fresh one
	}
	return saved, true
}

func (s Store) prune(driver, ext string) error {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), driver+"-") && strings.HasSuffix(e.Name(), ext) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names) // the time stamp in the name sorts oldest first
	for len(names) > s.Keep {
		if err := os.Remove(filepath.Join(s.Dir, names[0])); err != nil {
			return err
		}
		names = names[1:]
	}
	return nil
}

// writeAtomic writes through a temporary file, so a crash leaves the old file or the new one, never half of one.
func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
