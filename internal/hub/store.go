package hub

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// loadList reads a JSON list written by saveList; a missing file is an empty list.
func loadList[T any](path string) ([]T, error) {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var list []T
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// syncFile puts a written file on disk; a variable so that a test can make a save take as long as on a slow disk.
var syncFile = (*os.File).Sync

// saveList replaces path atomically with list as JSON (0600 in a 0700 directory), and durably: the new file is on
// disk before it takes the old one's name, and the rename is on disk before saveList returns. Without the syncs a
// power loss right after a save could leave registry.json empty, and the hub would not start.
func saveList(path string, list any) error {
	raw, err := json.MarshalIndent(list, "", " ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(raw)
	serr := syncFile(f)
	cerr := f.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return syncDir(dir)
}
