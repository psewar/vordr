package images

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/inventory"
)

// fakeRegistry speaks the distribution API as Docker Hub, GHCR and Quay do: every repository wants an anonymous
// Bearer token first (token flow), HEAD answers with Docker-Content-Digest, tag lists come in pages with a Link
// header, multi-platform images are indexes. Tokens can be made to expire after a number of uses.
type fakeRegistry struct {
	srv       *httptest.Server
	mu        sync.Mutex
	repos     map[string]*fakeRepo
	private   map[string]bool // a token is issued, but the repository stays closed (private, or gone)
	limited   map[string]bool // 429
	pageSize  int
	tokenUses int            // 0: tokens never expire; else each one is taken this many times
	issued    map[string]int // token -> uses left (-1: unlimited)
	tokens    map[string]int // scope -> tokens issued for it
	rawTags   map[string]string
	log       []string           // "METHOD /path" of every request
	layers    map[string][]int64 // repo -> layer sizes of every manifest pushed from now on (ADR-0119: free space)
}

type fakeRepo struct {
	tags  []string          // in the registry's (lexical) order
	refs  map[string]string // tag -> manifest or index digest
	blobs map[string][]byte // digest -> manifest, index or configuration
}

func newFakeRegistry(t *testing.T) *fakeRegistry {
	f := &fakeRegistry{repos: map[string]*fakeRepo{}, private: map[string]bool{}, limited: map[string]bool{},
		pageSize: 2, issued: map[string]int{}, tokens: map[string]int{}, rawTags: map[string]string{},
		layers: map[string][]int64{}}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeRegistry) host() string { return f.srv.Listener.Addr().String() }

func digestOfBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

type build struct {
	platform Platform
	created  time.Time
	rev      string // another build with the same time and platform: another configuration
}

// push stores an image under a tag: one manifest per platform, an index when there is more than one. extra adds an
// index entry that is no image (an attestation). It returns the tag's digest and the config digest per platform.
func (f *fakeRegistry) push(repo, tag string, builds []build, extra bool) (string, map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.repos[repo]
	if r == nil {
		r = &fakeRepo{refs: map[string]string{}, blobs: map[string][]byte{}}
		f.repos[repo] = r
	}
	configs := map[string]string{}
	type entry struct {
		MediaType string   `json:"mediaType"`
		Digest    string   `json:"digest"`
		Platform  Platform `json:"platform"`
	}
	var entries []entry
	var single string
	for _, b := range builds {
		cfg, _ := json.Marshal(map[string]any{"created": b.created, "architecture": b.platform.Architecture,
			"os": b.platform.OS, "config": map[string]any{"Labels": map[string]string{"rev": b.rev}}})
		cd := digestOfBytes(cfg)
		r.blobs[cd] = cfg
		configs[b.platform.String()] = cd
		doc := map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json",
			"config": map[string]any{"digest": cd, "mediaType": "application/vnd.oci.image.config.v1+json",
				"size": len(cfg)}}
		if sizes := f.layers[repo]; len(sizes) > 0 {
			var ls []map[string]any
			for i, n := range sizes {
				ls = append(ls, map[string]any{"digest": digestOfBytes([]byte(fmt.Sprint(repo, i))), "size": n,
					"mediaType": "application/vnd.oci.image.layer.v1.tar+gzip"})
			}
			doc["layers"] = ls
		}
		man, _ := json.Marshal(doc)
		md := digestOfBytes(man)
		r.blobs[md] = man
		single = md
		entries = append(entries, entry{"application/vnd.oci.image.manifest.v1+json", md, b.platform})
	}
	d := single
	if len(builds) > 1 || extra {
		if extra {
			entries = append(entries, entry{"application/vnd.oci.image.manifest.v1+json", single,
				Platform{OS: "unknown", Architecture: "unknown"}})
		}
		idx, _ := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.index.v1+json",
			"manifests": entries})
		d = digestOfBytes(idx)
		r.blobs[d] = idx
	}
	if _, ok := r.refs[tag]; !ok {
		r.tags = append(r.tags, tag)
		slices.Sort(r.tags)
	}
	r.refs[tag] = d
	return d, configs
}

// untag removes a tag, as a registry's retention does; the images stay reachable by digest.
func (f *fakeRegistry) untag(repo, tag string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.repos[repo]
	delete(r.refs, tag)
	r.tags = slices.DeleteFunc(r.tags, func(t string) bool { return t == tag })
}

func (f *fakeRegistry) authorized(req *http.Request, scope string) bool {
	tok, ok := strings.CutPrefix(req.Header.Get("Authorization"), "Bearer ")
	if !ok || !strings.HasPrefix(tok, "anon "+scope+" #") {
		return false
	}
	left, issued := f.issued[tok]
	if !issued || left == 0 {
		return false // expired
	}
	if left > 0 {
		f.issued[tok] = left - 1
	}
	return true
}

func (f *fakeRegistry) serve(w http.ResponseWriter, req *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = append(f.log, req.Method+" "+req.URL.Path)
	if req.URL.Path == "/token" {
		scope := req.URL.Query().Get("scope")
		f.tokens[scope]++
		tok := fmt.Sprintf("anon %s #%d", scope, f.tokens[scope])
		f.issued[tok] = -1
		if f.tokenUses > 0 {
			f.issued[tok] = f.tokenUses
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"token": tok})
		return
	}
	rest, ok := strings.CutPrefix(req.URL.Path, "/v2/")
	if !ok {
		http.NotFound(w, req)
		return
	}
	var repo, kind, ref string
	for _, k := range []string{"/manifests/", "/blobs/", "/tags/list"} {
		if i := strings.Index(rest, k); i > 0 {
			repo, kind, ref = rest[:i], strings.Trim(k, "/"), rest[i+len(k):]
			break
		}
	}
	scope := "repository:" + repo + ":pull"
	if !f.authorized(req, scope) || f.private[repo] { // like Docker Hub: every 401 carries the challenge
		w.Header().Set("Www-Authenticate",
			fmt.Sprintf(`Bearer realm="%s/token",service="fake.test",scope="%s"`, f.srv.URL, scope))
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if f.limited[repo] {
		w.WriteHeader(http.StatusTooManyRequests)
		return
	}
	if raw, ok := f.rawTags[repo]; ok && kind == "tags/list" {
		_, _ = w.Write([]byte(raw))
		return
	}
	r := f.repos[repo]
	if r == nil {
		http.NotFound(w, req)
		return
	}
	switch kind {
	case "tags/list":
		from := 0
		if last := req.URL.Query().Get("last"); last != "" {
			from = slices.Index(r.tags, last) + 1
		}
		to := min(from+f.pageSize, len(r.tags))
		if to < len(r.tags) {
			w.Header().Set("Link", fmt.Sprintf(`</v2/%s/tags/list?n=%d&last=%s>; rel="next"`, repo, f.pageSize,
				url.QueryEscape(r.tags[to-1])))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"name": repo, "tags": r.tags[from:to]})
	default:
		d := ref
		if !strings.HasPrefix(ref, "sha256:") {
			d = r.refs[ref]
		}
		body, ok := r.blobs[d]
		if !ok {
			http.NotFound(w, req)
			return
		}
		w.Header().Set("Docker-Content-Digest", d)
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		if req.Method == http.MethodGet {
			_, _ = w.Write(body)
		}
	}
}

func (f *fakeRegistry) requests(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, l := range f.log {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

var (
	amd64 = Platform{OS: "linux", Architecture: "amd64"}
	arm64 = Platform{OS: "linux", Architecture: "arm64", Variant: "v8"}
	now   = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	epoch = time.Unix(0, 0).UTC() // what reproducible builds write
)

func days(n int) time.Time { return now.Add(-time.Duration(n) * 24 * time.Hour) }

// running is what the engine reports for an image pulled by tag: the tag's digest as repo digest, and as image id
// the platform's config digest (the classic image store) or, with the containerd image store, the pulled digest.
func running(f *fakeRegistry, repo, tag, digest, id string, p Platform, created time.Time, ctr string) Running {
	r := Running{Reference: f.host() + "/" + repo + ":" + tag, ImageID: id, Created: created, Platform: p,
		Containers: []string{ctr}}
	if digest != "" {
		r.RepoDigests = []string{f.host() + "/" + repo + "@" + digest}
	}
	return r
}

type fixture struct {
	f       *fakeRegistry
	running []Running
}

func newFixture(t *testing.T) fixture {
	f := newFakeRegistry(t)
	var run []Running
	// postgres:16 was rebuilt 11 days ago; 17 is 16 days old, 18 one day: candidate 17, latest 18.
	oldDigest, oldCfg := f.push("library/postgres", "16", []build{{amd64, days(40), ""}, {arm64, days(40), ""}}, false)
	f.push("library/postgres", "16", []build{{amd64, days(11), ""}, {arm64, days(11), ""}}, false)
	f.push("library/postgres", "15", []build{{amd64, days(60), ""}}, false)
	f.push("library/postgres", "17", []build{{amd64, days(16), ""}}, false)
	f.push("library/postgres", "18", []build{{amd64, days(1), ""}}, false)
	f.push("library/postgres", "18-rc1", []build{{amd64, days(30), ""}}, false)
	run = append(run, running(f, "library/postgres", "16", oldDigest, oldCfg[amd64.String()], amd64, days(40), "db"))
	// app on arm64: its tag is unchanged; 1.4.3-gpu is old for amd64 but only 2 days for arm64 - the
	// host's platform decides. The tag list needs several pages.
	d, cfg := f.push("org/app", "1.4.2-gpu", []build{{amd64, days(30), ""}, {arm64, days(30), ""}}, true)
	f.push("org/app", "1.4.1-gpu", []build{{amd64, days(50), ""}, {arm64, days(50), ""}}, false)
	f.push("org/app", "1.4.3", []build{{amd64, days(20), ""}, {arm64, days(20), ""}}, false)
	f.push("org/app", "1.4.3-gpu", []build{{amd64, days(20), ""}, {arm64, days(2), ""}}, false)
	f.push("org/app", "1.5.0-beta1-gpu", []build{{amd64, days(20), ""}, {arm64, days(20), ""}}, false)
	run = append(run, running(f, "org/app", "1.4.2-gpu", d, cfg[arm64.String()], arm64, days(30), "app"))
	// an amd64 host and an index that lists arm64 first: 2.1 is old for arm64, too new for amd64.
	d, cfg = f.push("org/multi", "2.0", []build{{arm64, days(40), ""}, {amd64, days(40), ""}}, false)
	f.push("org/multi", "2.1", []build{{arm64, days(30), ""}, {amd64, days(2), ""}}, false)
	run = append(run, running(f, "org/multi", "2.0", d, cfg[amd64.String()], amd64, days(40), "multi"))
	// the index of this tag changed (an attestation added), the image for amd64 did not: no rebuild.
	d, cfg = f.push("org/indexonly", "1.0", []build{{amd64, days(30), ""}}, false)
	f.push("org/indexonly", "1.0", []build{{amd64, days(30), ""}}, true)
	run = append(run, running(f, "org/indexonly", "1.0", d, cfg[amd64.String()], amd64, days(30), "svc"))
	// a reproducible build (fixed creation time) rebuilt with a patched base: another configuration, the same time.
	d, cfg = f.push("org/repro", "1.0", []build{{amd64, epoch, "a"}}, false)
	f.push("org/repro", "1.0", []build{{amd64, epoch, "b"}}, false)
	run = append(run, running(f, "org/repro", "1.0", d, cfg[amd64.String()], amd64, epoch, "repro"))
	// the containerd image store: the image id is the pulled index digest. An index change alone is no rebuild there
	// either; a real one is.
	d, _ = f.push("org/ctrd", "1.0", []build{{amd64, days(30), ""}, {arm64, days(30), ""}}, false)
	f.push("org/ctrd", "1.0", []build{{amd64, days(30), ""}, {arm64, days(30), ""}}, true)
	run = append(run, running(f, "org/ctrd", "1.0", d, d, amd64, days(30), "ctrd"))
	d, _ = f.push("org/ctrd-rebuilt", "1.0", []build{{amd64, days(30), ""}, {arm64, days(30), ""}}, false)
	f.push("org/ctrd-rebuilt", "1.0", []build{{amd64, days(10), ""}, {arm64, days(10), ""}}, false)
	run = append(run, running(f, "org/ctrd-rebuilt", "1.0", d, d, amd64, days(30), "ctrd2"))
	// retention deleted the running tag; a newer one is there.
	d, cfg = f.push("org/gone", "1.4", []build{{amd64, days(40), ""}}, false)
	f.push("org/gone", "1.5", []build{{amd64, days(20), ""}}, false)
	f.untag("org/gone", "1.4")
	run = append(run, running(f, "org/gone", "1.4", d, cfg[amd64.String()], amd64, days(40), "gone"))
	f.push("org/private", "1.0", []build{{amd64, days(30), ""}}, false)
	f.private["org/private"] = true
	run = append(run, running(f, "org/private", "1.0", "sha256:"+strings.Repeat("1", 64), "x", amd64, days(30), "p"))
	f.push("org/limited", "1.0", []build{{amd64, days(30), ""}}, false)
	f.limited["org/limited"] = true
	run = append(run, running(f, "org/limited", "1.0", "sha256:"+strings.Repeat("2", 64), "y", amd64, days(30), "l"))
	run = append(run, running(f, "org/local", "1.0", "", "z", amd64, days(30), "mine")) // built on the host
	return fixture{f, run}
}

func pkg(t *testing.T, loc inventory.Location, name string) inventory.Package {
	t.Helper()
	for _, p := range loc.Packages {
		if strings.HasSuffix(p.Name, "/"+name) {
			return p
		}
	}
	t.Fatalf("no image %s in %+v", name, loc.Packages)
	return inventory.Package{}
}

// manifestBound sets maxManifestGets for one test: the fixture needs more than one run's share of a real registry.
func manifestBound(t *testing.T, n int) {
	was := maxManifestGets
	maxManifestGets = n
	t.Cleanup(func() { maxManifestGets = was })
}

func check(t *testing.T, reg *Registry, fx fixture) inventory.Location {
	t.Helper()
	loc := Location(fx.running, "", "/run/test.sock")
	reg.Outdated(context.Background(), fx.running, &loc, now, 7*24*time.Hour, MaxRequests)
	return loc
}

func TestOutdatedAgainstAFakeRegistry(t *testing.T) {
	manifestBound(t, 100)
	fx := newFixture(t)
	loc := check(t, &Registry{Client: fx.f.srv.Client()}, fx)

	pg := pkg(t, loc, "postgres")
	if pg.Version != "16" || pg.Rebuilt == "" || pg.Rebuilt == pg.Digest || !pg.RebuiltCandidate ||
		pg.RebuiltAt == nil || !pg.RebuiltAt.Equal(days(11)) {
		t.Errorf("postgres rebuild: %+v", pg)
	}
	if pg.Latest != "18" || pg.Candidate != "17" || !pg.Major || pg.LatestAt == nil || !pg.LatestAt.Equal(days(1)) {
		t.Errorf("postgres tags: latest %q candidate %q major %v at %v", pg.Latest, pg.Candidate, pg.Major, pg.LatestAt)
	}
	fr := pkg(t, loc, "app")
	if fr.Rebuilt != "" || fr.Latest != "1.4.3-gpu" || fr.Candidate != "" || fr.Major ||
		fr.LatestAt == nil || !fr.LatestAt.Equal(days(2)) {
		t.Errorf("app: %+v", fr)
	}
	if m := pkg(t, loc, "multi"); m.Latest != "2.1" || m.Candidate != "" || m.LatestAt == nil || !m.LatestAt.Equal(days(2)) {
		t.Errorf("the host's platform decides the age: %+v", m)
	}
	if ix := pkg(t, loc, "indexonly"); ix.Rebuilt != "" {
		t.Errorf("a changed index with the same image is no rebuild: %+v", ix)
	}
	if r := pkg(t, loc, "repro"); r.Rebuilt == "" || r.RebuiltAt != nil || r.RebuiltCandidate {
		t.Errorf("a reproducible rebuild is reported, with an unknown age and never a candidate: %+v", r)
	}
	if c := pkg(t, loc, "ctrd"); c.Rebuilt != "" {
		t.Errorf("containerd store, index change only: %+v", c)
	}
	if c := pkg(t, loc, "ctrd-rebuilt"); c.Rebuilt == "" || !c.RebuiltCandidate {
		t.Errorf("containerd store, rebuilt: %+v", c)
	}
	if g := pkg(t, loc, "gone"); g.Latest != "1.5" || g.Candidate != "1.5" {
		t.Errorf("a deleted running tag: the newer one is still found: %+v", g)
	}
	if loc.NotInRegistry != 1 || loc.OutdatedFailed != 1 {
		t.Errorf("not in registry %d (want 1, private), failed %d (want 1, rate limited)", loc.NotInRegistry,
			loc.OutdatedFailed)
	}
	// The failed one says why (ADR-0126); every other image has its answer.
	for _, p := range loc.Packages {
		want := ""
		if strings.HasSuffix(p.Name, "/limited") {
			want = inventory.CompareRateLimited
		}
		if p.CompareFailed != want {
			t.Errorf("%s: compare_failed %q, want %q", p.Name, p.CompareFailed, want)
		}
	}
	if n := fx.f.requests("GET /v2/org/local") + fx.f.requests("HEAD /v2/org/local"); n != 0 {
		t.Errorf("a locally built image was asked about: %d requests", n)
	}
	// The digest of a running tag is asked by HEAD (no download counted); only changed builds are fetched.
	if n := fx.f.requests("GET /v2/org/app/manifests/1.4.2-gpu"); n != 0 {
		t.Errorf("GET of an unchanged tag: %d", n)
	}
	for scope, n := range fx.f.tokens {
		// a closed repository rejects every token: each question tries one fresh token
		if n != 1 && scope != "repository:org/private:pull" {
			t.Errorf("%s: %d tokens, want one per repository", scope, n)
		}
	}
}

// ADR-0131: beside a new major version, the newest old enough tag of the running major version. Only when the
// candidate is a new major version, or the newest is one and none was old enough; never when the candidate already
// stays in the running major version, nor when none of its tags is old enough.
func TestSameMajorCandidate(t *testing.T) {
	manifestBound(t, 100)
	f := newFakeRegistry(t)
	var run []Running
	img := func(repo, tag string, age int) (string, string) {
		d, cfg := f.push(repo, tag, []build{{amd64, days(age), ""}}, false)
		return d, cfg[amd64.String()]
	}
	start := func(repo, tag string, age int) {
		d, cfg := img(repo, tag, age)
		run = append(run, running(f, repo, tag, d, cfg, amd64, days(age), filepath.Base(repo)))
	}
	// a candidate in the new major version; 4.4.1 is too new, so the step within 4 is 4.4.0
	start("org/app", "4.2.0", 400)
	img("org/app", "4.2.1", 300)
	img("org/app", "4.4.0", 200)
	img("org/app", "4.4.1", 3)
	img("org/app", "5.0.0", 100)
	img("org/app", "5.3.0", 11)
	// the newest of the new major version too new, an older one the candidate; the newest of 7 too new as well
	start("org/db", "7.5", 300)
	img("org/db", "7.10", 40)
	img("org/db", "7.11", 2)
	img("org/db", "8.4", 20)
	img("org/db", "8.6", 2)
	// the candidate stays in the running major version: nothing more to say
	start("org/svc", "2.0", 300)
	img("org/svc", "2.1", 30)
	img("org/svc", "3.0", 1)
	// all five tags read in the first walk are too new; the running major version has an old enough one
	start("org/many", "1.0", 300)
	img("org/many", "1.1", 30)
	for _, tag := range []string{"2.0", "2.1", "2.2", "2.3", "2.4", "2.5"} {
		img("org/many", tag, 1)
	}
	// the running major version has only a tag that is too new: none
	start("org/fresh", "5.0", 300)
	img("org/fresh", "5.1", 1)
	img("org/fresh", "6.0", 30)

	loc := Location(run, "", "/run/test.sock")
	(&Registry{Client: f.srv.Client()}).Outdated(context.Background(), run, &loc, now, 7*24*time.Hour, MaxRequests)
	for _, c := range []struct{ name, latest, candidate, same string }{
		{"app", "5.3.0", "5.3.0", "4.4.0"},
		{"db", "8.6", "8.4", "7.10"},
		{"svc", "3.0", "2.1", ""},
		{"many", "2.5", "", "1.1"},
		{"fresh", "6.0", "6.0", ""},
	} {
		p := pkg(t, loc, c.name)
		if p.Latest != c.latest || p.Candidate != c.candidate || p.SameMajor != c.same || !p.Major || p.CompareFailed != "" {
			t.Errorf("%s: latest %q candidate %q same_major %q major %v failed %q, want %q %q %q", c.name, p.Latest,
				p.Candidate, p.SameMajor, p.Major, p.CompareFailed, c.latest, c.candidate, c.same)
		}
	}
	// A later run without the step clears it, like the other answers.
	f.untag("org/app", "4.4.0")
	f.untag("org/app", "4.2.1")
	(&Registry{Client: f.srv.Client()}).Outdated(context.Background(), run, &loc, now, 7*24*time.Hour, MaxRequests)
	if p := pkg(t, loc, "app"); p.SameMajor != "" || p.Candidate != "5.3.0" {
		t.Errorf("after the tags are gone: %+v", p)
	}
}

// A Compose build under the containerd image store has a repo digest like a pulled image (ADR-0115). It is never asked
// about all the same - not even when a public repository of that name exists, was rebuilt and has a newer tag: its
// name stays on the host, and it is not compared with somebody else's image.
func TestLocalBuildWithARepoDigestIsNeverAsked(t *testing.T) {
	manifestBound(t, 100)
	f := newFakeRegistry(t)
	d, _ := f.push("org/composed", "1.0", []build{{amd64, days(30), ""}}, false)
	f.push("org/composed", "1.0", []build{{amd64, days(10), ""}}, false)
	f.push("org/composed", "1.1", []build{{amd64, days(10), ""}}, false)
	r := running(f, "org/composed", "1.0", d, d, amd64, days(30), "web")
	r.BuiltLocally = true
	loc := check(t, &Registry{Client: f.srv.Client()}, fixture{f, []Running{r}})
	if p := pkg(t, loc, "composed"); p.Digest != "" || p.Rebuilt != "" || p.Latest != "" || p.Candidate != "" {
		t.Errorf("a local build was compared: %+v", p)
	}
	if n := f.requests("GET /v2/org/composed") + f.requests("HEAD /v2/org/composed"); n != 0 || len(f.tokens) != 0 {
		t.Errorf("a local build was asked about: %d requests, tokens %v", n, f.tokens)
	}
	if loc.NotInRegistry != 0 || loc.OutdatedFailed != 0 {
		t.Errorf("a local build is neither missing from a registry nor failed: %+v", loc)
	}
}

// Every manifest download counts against the anonymous pull limit (Docker Hub), which the host's own pulls share.
// What a run has read is kept on disk, so a restarted agent downloads none of it again, and a run downloads at most
// maxManifestGets per registry.
func TestManifestDownloadsAreKeptAndBounded(t *testing.T) {
	manifestBound(t, 100)
	fx := newFixture(t)
	path := filepath.Join(t.TempDir(), "image-cache.json")
	reg := &Registry{Client: fx.f.srv.Client(), Path: path}
	check(t, reg, fx)
	if err := reg.Save(); err != nil {
		t.Fatal(err)
	}
	manifests := countManifestGets(fx.f)
	if manifests == 0 {
		t.Fatal("the first run downloaded nothing")
	}
	restarted := &Registry{Client: fx.f.srv.Client(), Path: path} // a new agent process
	loc := check(t, restarted, fx)
	if again := countManifestGets(fx.f) - manifests; again != 0 {
		t.Errorf("after a restart %d manifests were downloaded again", again)
	}
	if p := pkg(t, loc, "postgres"); p.Candidate != "17" || !p.RebuiltCandidate {
		t.Errorf("from the cache: %+v", p)
	}

	manifestBound(t, 3)
	cold := newFixture(t)
	loc = check(t, &Registry{Client: cold.f.srv.Client()}, cold)
	if n := countManifestGets(cold.f); n > 3 {
		t.Errorf("%d manifest downloads in one run, bound 3", n)
	}
	if loc.OutdatedFailed <= 1 {
		t.Errorf("what the bound left out must count as failed: %d", loc.OutdatedFailed)
	}
}

func countManifestGets(f *fakeRegistry) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, l := range f.log {
		if strings.HasPrefix(l, "GET /v2/") && strings.Contains(l, "/manifests/") {
			n++
		}
	}
	return n
}

// Docker Hub's anonymous tokens expire after five minutes; a 401 to an older token gets a fresh one, and only a
// 401 to a fresh token means "closed to anonymous callers".
func TestExpiredTokenIsRenewed(t *testing.T) {
	manifestBound(t, 100)
	fx := newFixture(t)
	fx.f.tokenUses = 2
	loc := check(t, &Registry{Client: fx.f.srv.Client()}, fx)
	if pg := pkg(t, loc, "postgres"); pg.Candidate != "17" || !pg.RebuiltCandidate {
		t.Errorf("postgres with expiring tokens: %+v", pg)
	}
	if loc.NotInRegistry != 1 {
		t.Errorf("not in registry %d, want only the private one", loc.NotInRegistry)
	}
}

// What a run holds of tag lists is bounded: a page by its size, a repository by its number of tags, the run by the
// tags it keeps for further images of the same repository.
func TestTagListsAreBounded(t *testing.T) {
	f := newFakeRegistry(t)
	ref, _ := ParseRef(f.host() + "/org/big:1.0")
	long := strings.Repeat("a", 120)
	var big, many []string
	for i := range 9000 { // about 1.2 MB of valid tags: past the page size, far below the number of tags
		big = append(big, fmt.Sprintf("%s%05d", long, i))
	}
	for i := range maxRepoTags + 100 {
		many = append(many, "t"+strconv.Itoa(i))
	}
	for name, tags := range map[string][]string{"bytes": big, "count": many} {
		raw, _ := json.Marshal(map[string]any{"tags": tags})
		f.rawTags["org/big"] = string(raw)
		s := (&Registry{Client: f.srv.Client()}).session(MaxRequests, now)
		if _, ans := s.tags(context.Background(), ref); ans != answerTooLong {
			t.Errorf("%s: %v, want no answer", name, ans)
		}
	}

	defer func(was int) { maxTagsHeld = was }(maxTagsHeld)
	maxTagsHeld = 1
	d, cfg := f.push("org/twice", "1.0", []build{{amd64, days(30), ""}}, false)
	d2, cfg2 := f.push("org/twice", "1.1", []build{{amd64, days(20), ""}}, false)
	run := []Running{running(f, "org/twice", "1.0", d, cfg[amd64.String()], amd64, days(30), "a"),
		running(f, "org/twice", "1.1", d2, cfg2[amd64.String()], amd64, days(20), "b")}
	loc := Location(run, "", "/run/test.sock")
	(&Registry{Client: f.srv.Client()}).Outdated(context.Background(), run, &loc, now, 7*24*time.Hour, MaxRequests)
	if n := f.requests("GET /v2/org/twice/tags/list"); n != 2 {
		t.Errorf("tag list read %d times; past maxTagsHeld it is not kept, so twice", n)
	}
}

// Repositories that publish a build per commit have tens of thousands of tags (ADR-0121; 28282 for one such
// repository in 2026). A list of up to 60000 tags in pages of 1000 is read whole, one tag more is no answer, and a
// run keeps a list of that length for a further image of the same repository.
func TestLongTagListsAreRead(t *testing.T) {
	f := newFakeRegistry(t)
	f.pageSize = tagsPerPage
	ref, _ := ParseRef(f.host() + "/org/busy:1.0")
	commits := func(n int) []string {
		tags := make([]string, n)
		for i := range tags {
			tags[i] = fmt.Sprintf("main-%07d", i) // in the registry's lexical order
		}
		return tags
	}
	// The numbers ADR-0121 decided, not the constants: lowering them must fail here.
	for n, want := range map[int]answer{28282: answerOK, 60000: answerOK, 60001: answerTooLong} {
		f.mu.Lock()
		f.repos["org/busy"] = &fakeRepo{tags: commits(n), refs: map[string]string{}, blobs: map[string][]byte{}}
		f.mu.Unlock()
		s := (&Registry{Client: f.srv.Client()}).session(MaxRequests, now)
		got, ans := s.tags(context.Background(), ref)
		wantLen := n
		if want != answerOK {
			wantLen = 0
		}
		if ans != want || len(got) != wantLen {
			t.Errorf("%d tags: %v with %d tags, want %v with %d", n, ans, len(got), want, wantLen)
		}
	}

	f.mu.Lock()
	delete(f.repos, "org/busy")
	f.mu.Unlock()
	d, cfg := f.push("org/busy", "1.0", []build{{amd64, days(30), ""}}, false)
	d2, cfg2 := f.push("org/busy", "1.1", []build{{amd64, days(20), ""}}, false)
	f.mu.Lock()
	f.repos["org/busy"].tags = append(f.repos["org/busy"].tags, commits(maxRepoTags-2)...)
	f.mu.Unlock()
	run := []Running{running(f, "org/busy", "1.0", d, cfg[amd64.String()], amd64, days(30), "a"),
		running(f, "org/busy", "1.1", d2, cfg2[amd64.String()], amd64, days(20), "b")}
	loc := Location(run, "", "/run/test.sock")
	before := f.requests("GET /v2/org/busy/tags/list")
	(&Registry{Client: f.srv.Client()}).Outdated(context.Background(), run, &loc, now, 7*24*time.Hour, MaxRequests)
	if n := f.requests("GET /v2/org/busy/tags/list") - before; n != maxTagPages {
		t.Errorf("tag list pages read %d times, want %d: the run keeps the list for the second image", n, maxTagPages)
	}
	if loc.OutdatedFailed != 0 {
		t.Errorf("failed %d, want 0", loc.OutdatedFailed)
	}
}

// A run stays within its request budget; what it could not ask counts as failed, not as up to date.
func TestOutdatedBudget(t *testing.T) {
	fx := newFixture(t)
	reg := &Registry{Client: fx.f.srv.Client()}
	loc := Location(fx.running[:2], "", "/run/test.sock")
	reg.Outdated(context.Background(), fx.running[:2], &loc, now, 7*24*time.Hour, 3)
	if loc.OutdatedFailed == 0 || len(fx.f.log) > 3 {
		t.Errorf("budget 3: %d requests, failed %d", len(fx.f.log), loc.OutdatedFailed)
	}
	// Each one says why (ADR-0126).
	n := 0
	for _, p := range loc.Packages {
		if p.CompareFailed != "" {
			n++
			if p.CompareFailed != inventory.CompareRequestBudget {
				t.Errorf("%s: compare_failed %q, want %q", p.Name, p.CompareFailed, inventory.CompareRequestBudget)
			}
		}
	}
	if n != loc.OutdatedFailed {
		t.Errorf("%d images say why, %d failed", n, loc.OutdatedFailed)
	}
}

// Why a comparison got no answer is told per image (ADR-0126): the run's manifest downloads for the registry used up,
// a tag list too long, a reply that does not parse. A later run that gets its answer clears it.
func TestCompareFailedReasons(t *testing.T) {
	f := newFakeRegistry(t)
	run := func(repo, tag string) Running {
		d, cfg := f.push(repo, tag, []build{{amd64, days(40), ""}}, false)
		return running(f, repo, tag, d, cfg[amd64.String()], amd64, days(40), strings.ReplaceAll(repo, "/", "-"))
	}
	rebuiltRun := run("org/rebuilt", "1.0")
	f.push("org/rebuilt", "1.0", []build{{amd64, days(10), ""}}, false) // its check reads the new build's manifest
	huge := run("org/huge", "1.0")
	broken := run("org/broken", "1.0")
	newer := run("org/newer", "1.0")
	f.push("org/newer", "1.1", []build{{amd64, days(20), ""}}, false) // reading it needs a manifest download
	long := strings.Repeat("a", 120)
	var big []string
	for i := range 9000 { // a page past maxTagsPage
		big = append(big, fmt.Sprintf("%s%05d", long, i))
	}
	raw, _ := json.Marshal(map[string]any{"tags": big})
	f.mu.Lock()
	f.rawTags["org/huge"], f.rawTags["org/broken"] = string(raw), "{not json"
	f.mu.Unlock()
	running := []Running{rebuiltRun, huge, broken, newer}

	manifestBound(t, 0)
	loc := Location(running, "", "/run/test.sock")
	reg := &Registry{Client: f.srv.Client()}
	reg.Outdated(context.Background(), running, &loc, now, 7*24*time.Hour, MaxRequests)
	for name, want := range map[string]string{"rebuilt": inventory.CompareManifestShare,
		"huge": inventory.CompareTagListTooLong, "broken": inventory.CompareNoAnswer,
		"newer": inventory.CompareManifestShare} { // the newer tag's image could not be read
		if got := pkg(t, loc, name).CompareFailed; got != want {
			t.Errorf("%s: compare_failed %q, want %q", name, got, want)
		}
	}
	if loc.OutdatedFailed != 4 {
		t.Errorf("failed %d, want 4", loc.OutdatedFailed)
	}

	// With downloads left, the rebuilt one gets its answer and says nothing anymore.
	manifestBound(t, 100)
	(&Registry{Client: f.srv.Client()}).Outdated(context.Background(), running, &loc, now, 7*24*time.Hour, MaxRequests)
	if p := pkg(t, loc, "rebuilt"); p.CompareFailed != "" || p.Rebuilt == "" {
		t.Errorf("answered later: %+v", p)
	}
}

func TestParseChallenge(t *testing.T) {
	scheme, p := parseChallenge(`Bearer realm="https://auth.example/token",service="registry.example",` +
		`scope="repository:a/b:pull,push"`)
	if scheme != "Bearer" || p["realm"] != "https://auth.example/token" || p["service"] != "registry.example" ||
		p["scope"] != "repository:a/b:pull,push" {
		t.Fatalf("%s %v", scheme, p)
	}
	// Only an https realm is followed: no token over plain http, no Basic auth.
	s := (&Registry{Client: http.DefaultClient}).session(10, now)
	for _, c := range []string{`Bearer realm="http://auth.example/token"`, `Basic realm="x"`, ``} {
		if _, ans := s.token(context.Background(), c, "a/b"); ans != answerNotFound {
			t.Errorf("%q: %v", c, ans)
		}
	}
}

func TestNextPageStaysOnTheHost(t *testing.T) {
	cur := "https://registry.example/v2/a/tags/list?n=2"
	if u, ok := nextPage(cur, `</v2/a/tags/list?n=2&last=b>; rel="next"`); !ok || u != "https://registry.example/v2/a/tags/list?n=2&last=b" {
		t.Errorf("relative: %q %v", u, ok)
	}
	if _, ok := nextPage(cur, `<https://other.example/v2/a/tags/list?last=b>; rel="next"`); ok {
		t.Error("followed a link to another host")
	}
}
