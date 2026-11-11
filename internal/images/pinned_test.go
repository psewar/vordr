package images

import (
	"slices"
	"strings"
	"testing"
)

// A reference that pins only a digest is compared under the tag its image carries on the host (ADR-0117): the tag has
// moved on to a newer build, and a newer tag of the same form exists. A pin whose image has no tag is listed, never
// asked about. A reference with tag and digest is checked for a rebuild too.
func TestPinnedReferenceIsComparedUnderItsTag(t *testing.T) {
	manifestBound(t, 100)
	f := newFakeRegistry(t)
	host := f.host()

	old, cfg := f.push("org/app", "1.0", []build{{amd64, days(40), ""}}, false)
	f.push("org/app", "1.0", []build{{amd64, days(20), ""}}, false) // the tag moved on
	f.push("org/app", "1.1", []build{{amd64, days(15), ""}}, false)
	pinned := Running{Reference: host + "/org/app@" + old, ImageID: cfg[amd64.String()], Platform: amd64,
		RepoDigests: []string{host + "/org/app@" + old}, RepoTags: []string{host + "/org/app:1.0"},
		Created: days(40), Containers: []string{"app"}}

	nd, ncfg := f.push("org/notag", "2.0", []build{{amd64, days(40), ""}}, false)
	f.push("org/notag", "2.0", []build{{amd64, days(20), ""}}, false)
	untagged := Running{Reference: host + "/org/notag@" + nd, ImageID: ncfg[amd64.String()], Platform: amd64,
		RepoDigests: []string{host + "/org/notag@" + nd}, Created: days(40), Containers: []string{"notag"}}

	bd, bcfg := f.push("org/both", "3.0", []build{{amd64, days(40), ""}}, false)
	f.push("org/both", "3.0", []build{{amd64, days(20), ""}}, false)
	both := Running{Reference: host + "/org/both:3.0@" + bd, ImageID: bcfg[amd64.String()], Platform: amd64,
		RepoDigests: []string{host + "/org/both@" + bd}, Created: days(40), Containers: []string{"both"}}

	run := []Running{pinned, untagged, both}
	loc := check(t, &Registry{Client: f.srv.Client()}, fixture{f, run})

	app := pkg(t, loc, "app")
	if app.Version != "1.0" || app.Digest != old || app.Rebuilt == "" || !app.RebuiltCandidate ||
		app.Latest != "1.1" || app.Candidate != "1.1" {
		t.Errorf("pinned, compared under its tag: %+v", app)
	}
	if n := pkg(t, loc, "notag"); n.Version != nd || n.Rebuilt != "" || n.Latest != "" {
		t.Errorf("pinned without a tag: %+v", n)
	}
	if n := f.requests("GET /v2/org/notag") + f.requests("HEAD /v2/org/notag"); n != 0 {
		t.Errorf("a pin without a tag was asked about: %d requests", n)
	}
	if b := pkg(t, loc, "both"); b.Version != "3.0" || b.Rebuilt == "" {
		t.Errorf("tag and digest: %+v", b)
	}
	if loc.NotInRegistry != 0 || loc.OutdatedFailed != 0 {
		t.Errorf("not in registry %d, failed %d", loc.NotInRegistry, loc.OutdatedFailed)
	}
}

// The tag a pin is compared under: the reference's own; else the image's tags of the same repository, one with a
// version form before a word, then the first in alphabetical order; tags of other repositories
// do not count.
func TestRunningTag(t *testing.T) {
	ref, _ := ParseRef("postgres@sha256:" + strings.Repeat("a", 64))
	r := Running{RepoTags: []string{"redis:7", "postgres:latest", "postgres:17.6-alpine", "postgres:17-alpine"}}
	if got := r.Tag(ref); got != "17-alpine" {
		t.Errorf("from the image's tags: %q", got)
	}
	traefik, _ := ParseRef("traefik@sha256:" + strings.Repeat("a", 64))
	if got := (Running{RepoTags: []string{"traefik:latest", "traefik:v3.1"}}).Tag(traefik); got != "v3.1" {
		t.Errorf("a version before a word that sorts first: %q", got)
	}
	words := Running{RepoTags: []string{"postgres:stable", "postgres:latest"}}
	if got := words.Tag(ref); got != "latest" {
		t.Errorf("words only: %q", got)
	}
	if got := (Running{RepoTags: []string{"redis:7"}}).Tag(ref); got != "" {
		t.Errorf("another repository's tag: %q", got)
	}
	own, _ := ParseRef("postgres:16@sha256:" + strings.Repeat("a", 64))
	if got := r.Tag(own); got != "16" {
		t.Errorf("the reference's own tag: %q", got)
	}
}

// Two references to one image under one tag are one package with the containers of both.
func TestLocationMergesReferencesToOneImage(t *testing.T) {
	d := "sha256:" + strings.Repeat("d", 64)
	run := []Running{
		{Reference: "postgres:16", ImageID: "sha256:" + strings.Repeat("1", 64), RepoDigests: []string{"postgres@" + d},
			Containers: []string{"b"}},
		{Reference: "postgres@" + d, ImageID: "sha256:" + strings.Repeat("1", 64), RepoDigests: []string{"postgres@" + d},
			RepoTags: []string{"postgres:16"}, Containers: []string{"a", "b"}},
	}
	loc := Location(run, "", "/run/test.sock")
	if len(loc.Packages) != 1 || loc.Packages[0].Version != "16" ||
		!slices.Equal(loc.Packages[0].Containers, []string{"a", "b"}) {
		t.Fatalf("%+v", loc.Packages)
	}
}
