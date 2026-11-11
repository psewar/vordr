package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Files with the registries' last answers, in the state directory: the agent asks at most every outdated_hours (design
// 11.3), and a restart - a self-update, a reboot - must not start the clock again.
const (
	packageAnswersFile = "outdated-packages.json"
	imageAnswersFile   = "outdated-images.json"
)

// answersFile keeps one cache's last answers on disk. It is only a cache: missing, unreadable, made with other
// settings (minimum age, registries) or dated after now (the clock went back), it is left aside and the registries are
// asked again, as without it.
type answersFile struct {
	path     string // "": not kept (tests, no state directory)
	settings string // what the answers depend on besides the packages themselves
}

type savedAnswers[T any] struct {
	Settings string    `json:"settings"`
	Asked    time.Time `json:"asked"`
	Answers  T         `json:"answers"`
}

// loadAnswers returns the answers kept on disk and when they were asked, if they fit now and the settings.
func loadAnswers[T any](f answersFile, now time.Time) (time.Time, T, bool) {
	var s savedAnswers[T]
	if f.path == "" {
		return time.Time{}, s.Answers, false
	}
	if _, err := os.Stat(f.path); err != nil {
		return time.Time{}, s.Answers, false // none kept yet
	}
	raw, err := os.ReadFile(f.path)
	if err != nil || json.Unmarshal(raw, &s) != nil || s.Settings != f.settings || s.Asked.IsZero() || s.Asked.After(now) {
		var zero T
		return time.Time{}, zero, false
	}
	return s.Asked, s.Answers, true
}

// saveAnswers writes the answers through a temporary file, so a crash leaves the old file or the new one.
func saveAnswers[T any](f answersFile, asked time.Time, answers T) error {
	if f.path == "" {
		return nil
	}
	raw, err := json.Marshal(savedAnswers[T]{Settings: f.settings, Asked: asked.UTC(), Answers: answers})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(f.path), filepath.Base(f.path)+".tmp*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), f.path); err != nil {
		// The target held open (a scanner, the indexer on Windows): the temporary file must not stay behind each run.
		os.Remove(tmp.Name())
		return err
	}
	return nil
}
