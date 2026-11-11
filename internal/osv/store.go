package osv

import (
	"compress/gzip"
	"encoding/gob"
	"os"
	"path/filepath"
)

// IndexVersion changes whenever DB changes shape; an index of another version is rebuilt from the next download.
const IndexVersion = 2 // 2: Affected.Upstream and Urgency (ADR-0120)

type stored struct {
	Version int
	DB      *DB
}

// Save writes the index next to path and renames it into place, so a reader never sees half a file.
func (db *DB) Save(path string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // gone after the rename; cleans up after a failure
	zw := gzip.NewWriter(tmp)
	if err := gob.NewEncoder(zw).Encode(stored{Version: IndexVersion, DB: db}); err != nil {
		tmp.Close()
		return err
	}
	if err := zw.Close(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Load reads an index Save wrote. current is false when the file is of another index version: rebuild it.
func Load(path string) (db *DB, current bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, false, err
	}
	var s stored
	if err := gob.NewDecoder(zr).Decode(&s); err != nil {
		return nil, false, err
	}
	if s.Version != IndexVersion || s.DB == nil {
		return nil, false, nil
	}
	return s.DB, true, nil
}
