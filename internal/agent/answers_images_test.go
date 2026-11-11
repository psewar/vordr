//go:build !windows

package agent

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/inventory"
)

// The images too: a restart within the day compares nothing again and keeps compared_at.
func TestImageAnswersSurviveARestart(t *testing.T) {
	stub := &stubImages{}
	file := answersFile{path: filepath.Join(t.TempDir(), imageAnswersFile), settings: "a"}
	t0 := time.Date(2026, 11, 5, 8, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	img := []inventory.Package{{Name: "docker.io/library/postgres", Version: "16", Digest: "sha256:a", Direct: true}}
	first := inventory.Location{Kind: inventory.KindContainer, Packages: slices.Clone(img)}
	(&imagesCache{reg: stub, file: file}).apply(context.Background(), &first, nil, t0, 7*day, day, true, nil)
	if stub.runs != 1 || first.ComparedAt == nil {
		t.Fatalf("first: %d runs, %+v", stub.runs, first)
	}
	again := inventory.Location{Kind: inventory.KindContainer, Packages: slices.Clone(img)}
	(&imagesCache{reg: stub, file: file}).apply(context.Background(), &again, nil, t0.Add(time.Hour), 7*day, day, true, nil)
	if stub.runs != 1 || again.Packages[0].Rebuilt != "sha256:new" || again.ComparedAt == nil || !again.ComparedAt.Equal(t0) {
		t.Errorf("after a restart: %d runs, %+v", stub.runs, again)
	}
	later := inventory.Location{Kind: inventory.KindContainer, Packages: slices.Clone(img)}
	(&imagesCache{reg: stub, file: file}).apply(context.Background(), &later, nil, t0.Add(25*time.Hour), 7*day, day, true, nil)
	if stub.runs != 2 {
		t.Errorf("after the day: %d runs", stub.runs)
	}
}
