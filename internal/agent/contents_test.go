//go:build !windows

package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/images"
	"github.com/psewar/vordr/internal/inventory"
)

// With the hub asking for them (ADR-0120), what an image carries inside is read with each comparison and kept with the
// answers. In between it is read only for an image the cache has none for; without the hub asking, nothing is read or
// sent.
func TestImageContentsReadWithTheComparison(t *testing.T) {
	a, b := strings.Repeat("a", 64), strings.Repeat("b", 64)
	digest := "sha256:" + strings.Repeat("9", 64)
	pg := images.Running{Reference: "postgres:16", ContainerID: a, RepoDigests: []string{"postgres@" + digest}}
	redis := images.Running{Reference: "redis:7", ContainerID: b, RepoDigests: []string{"redis@" + digest}}
	reads := map[string]int{}
	contents := func(_ context.Context, id string) inventory.ImageOS {
		reads[id]++
		return inventory.ImageOS{ID: "debian", Version: "13", Packages: []inventory.OSPackage{{Name: "zlib1g", Version: "1"}}}
	}
	t0 := time.Date(2026, 11, 6, 2, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	check := func(c *imagesCache, run []images.Running, at time.Time,
		read func(context.Context, string) inventory.ImageOS) inventory.Location {
		loc := images.Location(run, "", "/run/test.sock")
		c.apply(context.Background(), &loc, run, at, 7*day, day, true, read)
		return loc
	}

	c := &imagesCache{reg: &stubImages{}}
	if l := check(c, []images.Running{pg}, t0, contents); reads[a] != 1 || l.Packages[0].OS == nil ||
		l.Packages[0].OS.Packages[0].Name != "zlib1g" {
		t.Fatalf("with the comparison: %d reads, %+v", reads[a], l.Packages[0])
	}
	if l := check(c, []images.Running{pg}, t0.Add(time.Hour), contents); reads[a] != 1 || l.Packages[0].OS == nil {
		t.Errorf("from the cache: %d reads, %+v", reads[a], l.Packages[0])
	}
	if l := check(c, []images.Running{pg, redis}, t0.Add(2*time.Hour), contents); reads[b] != 1 || reads[a] != 1 ||
		l.Packages[1].OS == nil {
		t.Errorf("an image started since: reads %v, %+v", reads, l.Packages)
	}
	if l := check(c, []images.Running{pg, redis}, t0.Add(150*time.Minute), contents); reads[b] != 1 ||
		l.Packages[1].OS == nil {
		t.Errorf("an image started since, read again: reads %v, %+v", reads, l.Packages)
	}
	if l := check(c, []images.Running{pg}, t0.Add(3*time.Hour), nil); l.Packages[0].OS != nil {
		t.Errorf("sent to a hub that did not ask: %+v", l.Packages[0])
	}
	if check(c, []images.Running{pg}, t0.Add(25*time.Hour), contents); reads[a] != 2 {
		t.Errorf("the next comparison: %d reads", reads[a])
	}

	// The hub begins to ask between two comparisons: every image is read once, then kept.
	reads = map[string]int{}
	c = &imagesCache{reg: &stubImages{}}
	check(c, []images.Running{pg}, t0, nil)
	check(c, []images.Running{pg}, t0.Add(time.Hour), contents)
	if l := check(c, []images.Running{pg}, t0.Add(2*time.Hour), contents); reads[a] != 1 || l.Packages[0].OS == nil {
		t.Errorf("once the hub asks: %d reads, %+v", reads[a], l.Packages[0])
	}
}

// Only a hub that announced image_contents gets them read (ADR-0120): for any other the agent reads nothing inside.
func TestContentsOnlyWhenTheHubAsks(t *testing.T) {
	e := images.NewEngine("/run/none.sock")
	if contentsFor(Wants{Containers: true}, e) != nil {
		t.Error("read inside images for a hub that did not ask")
	}
	if contentsFor(Wants{Containers: true, ImageContents: true}, e) == nil {
		t.Error("not read for a hub that asked")
	}
}

// Without packages.outdated the images are not compared, but their contents are still read for a hub that asks
// for them: on the same schedule, kept in between, and without a compared_at.
func TestImageContentsWithoutOutdated(t *testing.T) {
	a := strings.Repeat("a", 64)
	pg := images.Running{Reference: "postgres:16", ContainerID: a,
		RepoDigests: []string{"postgres@sha256:" + strings.Repeat("9", 64)}}
	reads := 0
	contents := func(context.Context, string) inventory.ImageOS {
		reads++
		return inventory.ImageOS{ID: "debian", Version: "13"}
	}
	stub := &stubImages{}
	c := &imagesCache{reg: stub}
	t0 := time.Date(2026, 11, 6, 2, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	for i, at := range []time.Time{t0, t0.Add(time.Hour), t0.Add(25 * time.Hour)} {
		loc := images.Location([]images.Running{pg}, "", "/run/test.sock")
		c.apply(context.Background(), &loc, []images.Running{pg}, at, 7*day, day, false, contents)
		if loc.Packages[0].OS == nil || loc.ComparedAt != nil || stub.runs != 0 || reads != []int{1, 1, 2}[i] {
			t.Errorf("check %d: %d reads, %d comparisons, %+v", i, reads, stub.runs, loc)
		}
	}
}
