package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/inventory"
	"github.com/psewar/vordr/internal/policy"
)

// A restarted agent goes on with the registries' last answers until outdated_hours are up (design 11.3), instead of
// asking every registry again after each self-update or reboot. Answers made with other settings, from the future
// (the clock went back) or unreadable are left aside.
func TestOutdatedAnswersSurviveARestart(t *testing.T) {
	asked := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked++
		_, _ = w.Write([]byte(`{"releases":{"1.0":[{"upload_time_iso_8601":"2026-01-01T00:00:00Z"}],` +
			`"1.1":[{"upload_time_iso_8601":"2026-09-01T00:00:00Z"}]}}`))
	}))
	defer ts.Close()
	reg := inventory.Registries{PyPI: ts.URL, Client: ts.Client()}
	locs := func() []inventory.Location {
		return []inventory.Location{{ID: "v", Ecosystem: "PyPI", Packages: []inventory.Package{{Name: "x", Version: "1.0", Direct: true}}}}
	}
	path := filepath.Join(t.TempDir(), packageAnswersFile)
	file := answersFile{path: path, settings: "a"}
	t0 := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	week, day := 7*24*time.Hour, 24*time.Hour
	run := func(f answersFile, at time.Time) (inventory.Package, *time.Time) {
		l := locs()
		got := (&outdatedCache{file: f}).apply(context.Background(), reg, l, at, week, 0, day)
		return l[0].Packages[0], got
	}

	if p, _ := run(file, t0); asked != 1 || p.Candidate != "1.1" {
		t.Fatalf("first run: asked %d, %+v", asked, p)
	}
	// restarted: the answers of the file, and when they were asked
	if p, at := run(file, t0.Add(6*time.Hour)); asked != 1 || p.Candidate != "1.1" || !at.Equal(t0) {
		t.Errorf("after a restart: asked %d, %+v, asked at %v", asked, p, at)
	}
	if run(file, t0.Add(25*time.Hour)); asked != 2 {
		t.Errorf("after the day: asked %d", asked)
	}
	if run(answersFile{path: path, settings: "b"}, t0.Add(26*time.Hour)); asked != 3 {
		t.Errorf("other settings: asked %d", asked)
	}
	if run(answersFile{path: path, settings: "b"}, t0); asked != 4 { // the file says 26 h, the clock says 0 h
		t.Errorf("answers from the future: asked %d", asked)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if run(file, t0.Add(time.Hour)); asked != 5 {
		t.Errorf("unreadable file: asked %d", asked)
	}
}

// Without a state directory nothing is kept, and nothing is read.
func TestAnswersWithoutAStateDirectory(t *testing.T) {
	if keptIn("", packageAnswersFile) != "" {
		t.Fatal("a path without a state directory")
	}
	if err := saveAnswers(answersFile{}, time.Now(), 1); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := loadAnswers[int](answersFile{}, time.Now()); ok {
		t.Fatal("answers without a file")
	}
}

// A self-update leaves the old version's answers aside: it may compute or fill them differently. The minimum ages and
// the registries count too.
func TestAnswerSettingsNameTheVersion(t *testing.T) {
	var cfg policy.Packages
	p1, i1 := answerSettings("0.16.0", cfg)
	p2, i2 := answerSettings("0.16.1", cfg)
	if p1 == p2 || i1 == i2 {
		t.Errorf("same settings for two versions: %q %q", p1, i1)
	}
	cfg.Registries.PyPI = "https://mirror.example/pypi"
	if p3, _ := answerSettings("0.16.0", cfg); p3 == p1 {
		t.Errorf("another registry, same settings: %q", p3)
	}
}

// When the file cannot be replaced (held open on Windows), its temporary copy does not stay behind.
func TestSavingLeavesNoTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, packageAnswersFile)
	if err := os.Mkdir(target, 0o700); err != nil { // a directory in its place: the rename fails
		t.Fatal(err)
	}
	if err := saveAnswers(answersFile{path: target, settings: "a"}, time.Now(), 1); err == nil {
		t.Fatal("saved over a directory")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("left behind: %v", entries)
	}
}
