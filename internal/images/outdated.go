package images

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/psewar/vordr/internal/inventory"
	"github.com/psewar/vordr/internal/versions"
)

// Bounds of one registry run (ADR-0113: a bound on lookups per check, as for pip and npm).
const (
	// MaxRequests: requests to registries per run, tokens included. A host with a dozen images needs a few dozen.
	MaxRequests = 300
	// maxTagsWalked: newer tags whose age is read per image, newest first, until one is old enough.
	maxTagsWalked = 5
)

// plausibleSince: an image claiming to be older (reproducible builds write 1970, some write nothing) has an unknown
// age. It is reported, but it is never a candidate: the minimum age cannot be judged.
var plausibleSince = time.Date(2013, 1, 1, 0, 0, 0, 0, time.UTC)

func plausible(t time.Time) bool { return !t.Before(plausibleSince) }

// LocationID names the inventory location of the running images.
const LocationID = "containers"

// Location turns the running images into the inventory location "containers" (kind container, ecosystem oci): one
// package per image, Name the repository with its registry, Version the tag it is compared under (Running.Tag; the
// digest for a reference that pins one and has no tag on the host), Digest the repo digest. Two references to one
// image under one tag (postgres:16 and postgres@sha256:... of the same build) are one package with the containers of
// both. An image whose reference is no reference (an image id) is counted as skipped.
func Location(running []Running, code, socket string) inventory.Location {
	loc := inventory.Location{ID: LocationID, Kind: inventory.KindContainer, Ecosystem: versions.OCI, Path: socket,
		Code: code, Packages: []inventory.Package{}}
	at := map[string]int{} // Key -> index in loc.Packages
	for _, r := range running {
		ref, ok := ParseRef(r.Reference)
		if !ok {
			loc.Skipped++
			continue
		}
		p := inventory.Package{Name: ref.Name(), Version: version(ref, r), Digest: r.RepoDigest(ref),
			Containers: slices.Clone(r.Containers), Direct: true}
		k := Key(p.Name, p.Version, p.Digest)
		if i, seen := at[k]; seen {
			c := append(loc.Packages[i].Containers, p.Containers...)
			slices.Sort(c)
			loc.Packages[i].Containers = slices.Compact(c)
			continue
		}
		at[k] = len(loc.Packages)
		loc.Packages = append(loc.Packages, p)
	}
	slices.SortFunc(loc.Packages, func(a, b inventory.Package) int {
		return strings.Compare(Key(a.Name, a.Version, a.Digest), Key(b.Name, b.Version, b.Digest))
	})
	return loc
}

// KeyOf: the key of the inventory entry Location makes for a running image; false for a reference that is none.
func KeyOf(r Running) (string, bool) {
	ref, ok := ParseRef(r.Reference)
	if !ok {
		return "", false
	}
	return Key(ref.Name(), version(ref, r), r.RepoDigest(ref)), true
}

// version: the tag the image is compared under, else the pinned digest.
func version(ref Ref, run Running) string {
	if t := run.Tag(ref); t != "" {
		return t
	}
	return ref.Digest
}

// Key identifies an image of the location across runs.
func Key(name, version, digest string) string { return name + "\x00" + version + "\x00" + digest }

// Outdated asks the registries about every image of loc (ADR-0113) and fills in what they say:
//   - Rebuilt: the tag now points to another build (its digest differs, and the image for this platform is not the
//     one running); RebuiltAt: when that build was made; RebuiltCandidate: it is at least minAge old.
//   - Latest, Candidate, LatestAt, Major: the newest tag of the same form, the newest one at least minAge old, when
//     Latest was built while it is younger, and whether Latest changes the first number.
//
// A reference that pins a digest is compared under its tag, from the reference or from the image's tags on the host
// (ADR-0117): Rebuilt then says the tag has moved on from the pinned build. A pin without a tag is only listed.
//
// An image built locally (no repo digest) is never compared. One the registry does not give anonymous callers
// counts as not in the registry; one whose questions got no answer as outdated_failed. At most budget requests go
// out, and at most maxManifestGets manifest downloads per registry; the rest count as failed. Save keeps what was
// read for the next run.
func (r *Registry) Outdated(ctx context.Context, running []Running, loc *inventory.Location, now time.Time,
	minAge time.Duration, budget int) {
	s := r.session(budget, now)
	type tagAnswer struct {
		digest string
		ans    answer
	}
	type tagsAnswer struct {
		tags []string
		ans  answer
	}
	digests := map[string]tagAnswer{} // name:tag -> digest, once per run
	lists := map[string]tagsAnswer{}  // name -> tags, while the run holds at most maxTagsHeld tags in all
	held := 0
	digestOf := func(ref Ref, tag string) (string, answer) {
		k := ref.Name() + ":" + tag
		if a, ok := digests[k]; ok {
			return a.digest, a.ans
		}
		d, ans := s.digestOf(ctx, ref, tag)
		digests[k] = tagAnswer{d, ans}
		return d, ans
	}
	tagsOf := func(ref Ref) ([]string, answer) {
		if a, ok := lists[ref.Name()]; ok {
			return a.tags, a.ans
		}
		t, ans := s.tags(ctx, ref)
		if held+len(t) <= maxTagsHeld { // past it, a second image of the same repository asks again
			lists[ref.Name()] = tagsAnswer{t, ans}
			held += len(t)
		}
		return t, ans
	}
	byKey := map[string]*inventory.Package{}
	for i := range loc.Packages {
		p := &loc.Packages[i]
		p.Latest, p.Candidate, p.LatestAt, p.Major, p.SameMajor = "", "", nil, false, ""
		p.Rebuilt, p.RebuiltAt, p.RebuiltCandidate, p.CompareFailed = "", nil, false, ""
		byKey[Key(p.Name, p.Version, p.Digest)] = p
	}
	loc.OutdatedFailed, loc.NotInRegistry = 0, 0
	done := map[*inventory.Package]bool{}
	for _, run := range running {
		ref, ok := ParseRef(run.Reference)
		if !ok {
			continue
		}
		p := byKey[Key(ref.Name(), version(ref, run), run.RepoDigest(ref))]
		if p == nil || done[p] || p.Digest == "" { // built locally: nothing in a registry to compare with
			continue
		}
		done[p] = true
		// Compared under its tag, also when the reference pins a digest (ADR-0117): the pin keeps the container on its
		// build, and whether the tag has moved on since is what the operator wants to know. A pin without a tag on the
		// host has nothing to compare with.
		ref = Ref{Registry: ref.Registry, Repository: ref.Repository, Tag: run.Tag(ref)}
		if ref.Tag == "" {
			continue
		}
		notFound, tagGone := false, false
		fail := answerOK // the first question without an answer: why the comparison is incomplete (ADR-0126)
		failed := func(a answer) {
			if fail == answerOK && a.failed() {
				fail = a
			}
		}
		switch d, ans := digestOf(ref, ref.Tag); {
		case ans == answerNotFound:
			tagGone = true // the tag was deleted, or the repository is closed: the tag list tells which
		case ans.failed():
			failed(ans)
		case d != p.Digest:
			switch rebuilt, created, ans := s.rebuilt(ctx, ref, run, p.Digest, d); {
			case ans.failed():
				failed(ans)
			case rebuilt:
				p.Rebuilt = d
				if plausible(created) { // else reported, never a candidate: the age is unknown
					at := created.UTC()
					p.RebuiltAt, p.RebuiltCandidate = &at, now.Sub(created) >= minAge
				}
			}
		}
		cur, hasForm := parseForm(ref.Tag)
		switch {
		case hasForm && !cur.prerelease():
			tags, ans := tagsOf(ref)
			switch {
			case ans == answerNotFound:
				notFound = true
			case ans.failed():
				failed(ans)
			default: // also when the running tag was deleted: a newer one may be there
				failed(newerTag(ctx, s, digestOf, ref, run.Platform, cur, newerTags(ref.Tag, tags), p, now, minAge))
			}
		case tagGone: // nothing else to ask about
			notFound = true
		}
		switch {
		case notFound:
			loc.NotInRegistry++
		case fail.failed():
			loc.OutdatedFailed++
			p.CompareFailed = fail.compareFailed()
		}
	}
}

// rebuilt decides whether the tag's new digest is another image for the host's platform. A digest alone does not
// say it: an index also changes when only another platform or an attestation is added. The configuration does - a
// rebuild has another one even when it keeps the creation time (reproducible builds write a fixed one).
//
// The running image's configuration: with the engine's classic image store the image id is its digest. With the
// containerd image store the id is the digest of the index or manifest it was pulled by, the same as its repo
// digest; then the configuration is read from the registry for that digest (once, the cache keeps it). When the
// registry no longer has the old digest, nothing tells the two apart, and it counts as rebuilt.
func (s *session) rebuilt(ctx context.Context, ref Ref, run Running, oldDigest, newDigest string) (bool, time.Time,
	answer) {
	created, config, ans := s.imageOf(ctx, ref, newDigest, run.Platform)
	switch {
	case ans.failed():
		return false, time.Time{}, ans
	case ans == answerNotFound: // the new build has no image for this platform: nothing that would replace the running one
		return false, time.Time{}, answerOK
	}
	running := run.ImageID
	if running == oldDigest {
		_, old, ans := s.imageOf(ctx, ref, oldDigest, run.Platform)
		switch {
		case ans.failed():
			return false, time.Time{}, ans
		case ans == answerNotFound:
			return true, created, answerOK
		}
		running = old
	}
	return config != running, created, answerOK
}

// maxTagsHeld bounds the tag lists a run keeps for further images of the same repository (a variable for tests):
// two of the longest lists a repository may have (ADR-0121).
var maxTagsHeld = 2 * maxRepoTags

// newerTag walks the newer tags of the same form, newest first, and fills Latest, Candidate, LatestAt and Major. When
// the candidate is a new major version, or Latest is one and no candidate was found, it walks the newer tags of the
// running major version as well and fills SameMajor (ADR-0131). It returns the first question that got no answer,
// answerOK when every one did.
func newerTag(ctx context.Context, s *session, digestOf func(Ref, string) (string, answer), ref Ref,
	platform Platform, cur form, newer []string, p *inventory.Package, now time.Time, minAge time.Duration) answer {
	// built: when the tag's image for the host's platform was built; found is false for a tag without one.
	built := func(tag string) (time.Time, bool, answer) {
		d, ans := digestOf(ref, tag)
		if ans.failed() || ans == answerNotFound {
			return time.Time{}, false, ans
		}
		created, _, ans := s.imageOf(ctx, ref, d, platform)
		if ans.failed() || ans == answerNotFound { // not found: no image of this tag for the host's platform
			return time.Time{}, false, ans
		}
		return created, true, answerOK
	}
	oldEnough := func(created time.Time) bool { return plausible(created) && now.Sub(created) >= minAge }
	var latestCreated time.Time
	walked := 0
	for _, tag := range newer {
		if walked >= maxTagsWalked {
			break
		}
		walked++
		created, found, ans := built(tag)
		if ans.failed() {
			return ans
		}
		if !found {
			continue
		}
		if p.Latest == "" {
			f, _ := parseForm(tag)
			p.Latest, p.Major, latestCreated = tag, cur.major(f), created
		}
		if oldEnough(created) {
			p.Candidate = tag
			break
		}
	}
	if p.Latest != "" && p.Candidate != p.Latest && plausible(latestCreated) {
		at := latestCreated.UTC()
		p.LatestAt = &at
	}
	if !p.Major {
		return answerOK
	}
	if c, ok := parseForm(p.Candidate); ok && !cur.major(c) {
		return answerOK // the candidate is already the step within the running major version
	}
	// The smaller step (ADR-0131): the newest tag of the running major version that is old enough, under the same bound.
	walked = 0
	for _, tag := range newer {
		if f, _ := parseForm(tag); cur.major(f) {
			continue
		}
		if walked >= maxTagsWalked {
			break
		}
		walked++
		created, found, ans := built(tag)
		if ans.failed() {
			return ans
		}
		if found && oldEnough(created) {
			p.SameMajor = tag
			break
		}
	}
	return answerOK
}
