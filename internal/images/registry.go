package images

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/psewar/vordr/internal/inventory"
)

// Registry asks image registries through the distribution API, anonymously (ADR-0113): when a registry answers 401
// with a Bearer challenge, the agent fetches a token for pull access from the realm it names, with no credentials.
// What a registry does not give anonymous callers counts as not in the registry, like a private npm package.
//
// Registries such as Docker Hub count every manifest download against an anonymous limit per address - the same
// limit the host's own pulls draw on, and often shared by every host behind one public address. HEAD requests do
// not count. So a Registry remembers, per manifest digest and platform, the configuration and creation time it has
// read (a digest never changes its content) in a file under the agent's state directory, and a run downloads at
// most maxManifestGets manifests per registry.
type Registry struct {
	Client *http.Client
	// Path keeps the cache across restarts; "" keeps it in memory only.
	Path   string
	mu     sync.Mutex
	cache  map[string]cachedImage // digest + " " + platform -> image
	loaded bool
	dirty  bool
}

// cachedImage is what a manifest digest gives for one platform.
type cachedImage struct {
	Config  string    `json:"config"`
	Created time.Time `json:"created"`
	Used    time.Time `json:"used"` // the last run that needed it; the oldest go first past maxCachedImages
	// Size: the image's download for the platform, layers and configuration (ADR-0119); 0: not known (an entry an
	// agent before it kept).
	Size int64 `json:"size,omitempty"`
}

// NewRegistry returns a registry client with sane timeouts that keeps its cache in path ("": memory only).
func NewRegistry(path string) *Registry {
	return &Registry{Client: &http.Client{Timeout: 60 * time.Second}, Path: path}
}

// maxCachedImages bounds the cache; past it, the entries used longest ago are dropped.
const maxCachedImages = 4096

// maxManifestGets: manifest downloads per registry and run - what registries count as pulls (Docker Hub: an index and
// a platform manifest are up to two GETs of one pull). What is left counts as failed and comes next run; the cache
// keeps what was read, so the runs move on. A variable for tests.
var maxManifestGets = 20

// load reads the cache file once. A missing file is an empty cache; an unreadable one too - it is only a cache.
func (r *Registry) load() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.loaded {
		return
	}
	r.loaded, r.cache = true, map[string]cachedImage{}
	if r.Path == "" {
		return
	}
	if _, err := os.Stat(r.Path); err != nil {
		return // not written yet
	}
	raw, err := os.ReadFile(r.Path)
	if err != nil {
		return
	}
	var c map[string]cachedImage
	if json.Unmarshal(raw, &c) == nil && c != nil {
		r.cache = c
	}
}

// Save writes the cache when a run changed it, at most maxCachedImages entries, the most recently used kept.
func (r *Registry) Save() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Path == "" || !r.dirty {
		return nil
	}
	if len(r.cache) > maxCachedImages {
		keys := make([]string, 0, len(r.cache))
		for k := range r.cache {
			keys = append(keys, k)
		}
		slices.SortFunc(keys, func(a, b string) int { return r.cache[b].Used.Compare(r.cache[a].Used) })
		for _, k := range keys[maxCachedImages:] {
			delete(r.cache, k)
		}
	}
	raw, err := json.Marshal(r.cache)
	if err != nil {
		return err
	}
	tmp := r.Path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, r.Path); err != nil {
		return err
	}
	r.dirty = false
	return nil
}

func (r *Registry) cached(key string, now time.Time) (cachedImage, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.cache[key]
	if ok && !c.Used.Equal(now) {
		c.Used = now
		r.cache[key], r.dirty = c, true
	}
	return c, ok
}

func (r *Registry) remember(key string, c cachedImage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cache[key], r.dirty = c, true
}

// answer is the state of one registry question. Every value from answerFailed on is a question without a usable
// answer, each with its own reason (ADR-0126); failed tells them from the others.
type answer int

const (
	answerOK            answer = iota
	answerNotFound             // the registry does not have it, or not for anonymous callers (404, 401/403 with a token)
	answerFailed               // no usable answer: network, server error, a malformed reply
	answerRateLimited          // the registry said 429
	answerBudget               // the run's request budget was used up before the question
	answerManifestShare        // the run's manifest downloads for this registry were used up (maxManifestGets)
	answerTooLong              // the tag list is longer than the agent reads (maxTagPages, maxTagsPage)
)

func (a answer) failed() bool { return a >= answerFailed }

// compareFailed: the reason of an image's comparison without an answer, as the inventory reports it (ADR-0126).
func (a answer) compareFailed() string {
	switch a {
	case answerRateLimited:
		return inventory.CompareRateLimited
	case answerBudget:
		return inventory.CompareRequestBudget
	case answerManifestShare:
		return inventory.CompareManifestShare
	case answerTooLong:
		return inventory.CompareTagListTooLong
	}
	return inventory.CompareNoAnswer
}

const (
	acceptManifests = "application/vnd.oci.image.index.v1+json, " +
		"application/vnd.docker.distribution.manifest.list.v2+json, " +
		"application/vnd.oci.image.manifest.v1+json, " +
		"application/vnd.docker.distribution.manifest.v2+json"
	maxManifest = 4 << 20  // an index of many platforms stays far below
	maxConfig   = 16 << 20 // image configuration with its history
	maxTagsPage = 1 << 20  // a page of 1000 tags of at most 128 characters stays far below
	tagsPerPage = 1000
	// maxTagPages: pages read of one tag list. Repositories that publish a build per commit pass 20 pages
	// (ADR-0121).
	maxTagPages = 60
	// maxRepoTags: tags kept of one repository; a list with more counts as no answer.
	maxRepoTags = maxTagPages * tagsPerPage
)

// session is one run against the registries: it counts requests against the run's budget and keeps the tokens.
type session struct {
	reg       *Registry
	now       time.Time
	left      int
	manifests map[string]int    // registry host -> manifest downloads left in this run
	tokens    map[string]string // host + repository -> bearer token
}

func (r *Registry) session(budget int, now time.Time) *session {
	r.load()
	return &session{reg: r, now: now, left: budget, manifests: map[string]int{}, tokens: map[string]string{}}
}

// takeManifest counts one manifest download against the registry's share of this run.
func (s *session) takeManifest(ref Ref) bool {
	host := ref.apiHost()
	left, seen := s.manifests[host]
	if !seen {
		left = maxManifestGets
	}
	if left <= 0 {
		return false
	}
	s.manifests[host] = left - 1
	return true
}

// do sends one request to the repository's API. When the registry asks for a token, or no longer takes the one from
// an earlier request (Docker Hub's expire after five minutes), it fetches a fresh anonymous one; a 401 with that
// fresh token means the repository is closed to anonymous callers. The caller closes the body of a non-nil response.
func (s *session) do(ctx context.Context, method string, ref Ref, path, accept string) (*http.Response, answer) {
	u := "https://" + ref.apiHost() + "/v2/" + ref.Repository + "/" + path
	return s.doURL(ctx, method, ref, u, accept)
}

func (s *session) doURL(ctx context.Context, method string, ref Ref, u, accept string) (*http.Response, answer) {
	key := ref.apiHost() + "/" + ref.Repository
	fresh := false // a token fetched for this request
	for {
		switch {
		case ctx.Err() != nil:
			return nil, answerFailed
		case s.left <= 0:
			return nil, answerBudget
		}
		s.left--
		req, err := http.NewRequestWithContext(ctx, method, u, nil)
		if err != nil {
			return nil, answerFailed
		}
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		if tok := s.tokens[key]; tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, err := s.reg.Client.Do(req)
		if err != nil {
			return nil, answerFailed
		}
		switch resp.StatusCode {
		case http.StatusOK:
			return resp, answerOK
		case http.StatusUnauthorized:
			challenge := resp.Header.Get("Www-Authenticate")
			drain(resp)
			if fresh {
				return nil, answerNotFound // a fresh token for pull access was not enough: private, or not there
			}
			tok, ans := s.token(ctx, challenge, ref.Repository)
			if ans != answerOK {
				return nil, ans
			}
			s.tokens[key], fresh = tok, true
			continue
		case http.StatusNotFound, http.StatusForbidden:
			drain(resp)
			return nil, answerNotFound
		case http.StatusTooManyRequests:
			drain(resp)
			return nil, answerRateLimited
		default: // 5xx, anything else: no usable answer this time
			drain(resp)
			return nil, answerFailed
		}
	}
}

// token fetches an anonymous token as the challenge describes. Only a Bearer challenge with an https realm is
// followed; a registry that wants Basic auth has nothing for anonymous callers.
func (s *session) token(ctx context.Context, challenge, repository string) (string, answer) {
	scheme, params := parseChallenge(challenge)
	realm, err := url.Parse(params["realm"])
	if !strings.EqualFold(scheme, "bearer") || err != nil || realm.Scheme != "https" || realm.Host == "" {
		return "", answerNotFound
	}
	q := realm.Query()
	if svc := params["service"]; svc != "" {
		q.Set("service", svc)
	}
	scope := params["scope"]
	if scope == "" {
		scope = "repository:" + repository + ":pull"
	}
	q.Set("scope", scope)
	realm.RawQuery = q.Encode()
	if s.left <= 0 {
		return "", answerBudget
	}
	s.left--
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, realm.String(), nil)
	if err != nil {
		return "", answerFailed
	}
	resp, err := s.reg.Client.Do(req)
	if err != nil {
		return "", answerFailed
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return "", answerNotFound
	case resp.StatusCode != http.StatusOK:
		return "", answerFailed
	}
	var t struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&t) != nil {
		return "", answerFailed
	}
	if t.Token == "" {
		t.Token = t.AccessToken
	}
	if t.Token == "" {
		return "", answerFailed
	}
	return t.Token, answerOK
}

// parseChallenge reads a WWW-Authenticate header: Bearer realm="https://…",service="…",scope="…".
func parseChallenge(h string) (string, map[string]string) {
	scheme, rest, _ := strings.Cut(strings.TrimSpace(h), " ")
	params := map[string]string{}
	for rest = strings.TrimSpace(rest); rest != ""; {
		eq := strings.IndexByte(rest, '=')
		if eq <= 0 {
			break
		}
		key := strings.ToLower(strings.TrimSpace(rest[:eq]))
		rest = strings.TrimSpace(rest[eq+1:])
		var val string
		if strings.HasPrefix(rest, `"`) {
			end := 1
			for end < len(rest) && rest[end] != '"' {
				if rest[end] == '\\' {
					end++
				}
				end++
			}
			if end >= len(rest) {
				break
			}
			val = strings.ReplaceAll(rest[1:end], `\"`, `"`)
			rest = rest[end+1:]
		} else {
			val, rest, _ = strings.Cut(rest, ",")
			val = strings.TrimSpace(val)
		}
		params[key] = val
		rest = strings.TrimLeft(strings.TrimSpace(rest), ",")
		rest = strings.TrimSpace(rest)
	}
	return scheme, params
}

func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
}

// digestOf asks for the digest a tag points to now - a HEAD request, which registries do not count as a download.
// A registry that leaves out Docker-Content-Digest is asked for the manifest itself and its bytes are hashed.
func (s *session) digestOf(ctx context.Context, ref Ref, tag string) (string, answer) {
	resp, ans := s.do(ctx, http.MethodHead, ref, "manifests/"+tag, acceptManifests)
	if ans != answerOK {
		return "", ans
	}
	d := resp.Header.Get("Docker-Content-Digest")
	drain(resp)
	if digestPattern.MatchString(d) {
		return d, answerOK
	}
	if !s.takeManifest(ref) {
		return "", answerManifestShare
	}
	body, ans := s.read(ctx, ref, "manifests/"+tag, acceptManifests, maxManifest)
	if ans != answerOK {
		return "", ans
	}
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:]), answerOK
}

// read GETs a document of the repository, at most limit bytes.
func (s *session) read(ctx context.Context, ref Ref, path, accept string, limit int64) ([]byte, answer) {
	resp, ans := s.do(ctx, http.MethodGet, ref, path, accept)
	if ans != answerOK {
		return nil, ans
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(body)) > limit {
		return nil, answerFailed
	}
	return body, answerOK
}

// readByDigest reads a manifest or blob by its digest and checks that its bytes have that digest. A manifest counts
// against the run's manifest downloads (takeManifest).
func (s *session) readByDigest(ctx context.Context, ref Ref, kind, digest, accept string, limit int64) ([]byte, answer) {
	if kind == "manifests" && !s.takeManifest(ref) {
		return nil, answerManifestShare
	}
	body, ans := s.read(ctx, ref, kind+"/"+digest, accept, limit)
	if ans != answerOK {
		return nil, ans
	}
	sum := sha256.Sum256(body)
	if "sha256:"+hex.EncodeToString(sum[:]) != digest {
		return nil, answerFailed
	}
	return body, answerOK
}

// imageOf returns the creation time and the config digest of the platform's image behind a manifest digest: an
// index is resolved to the platform's manifest, the manifest to its configuration. An index without an image for the
// platform answers answerNotFound.
func (s *session) imageOf(ctx context.Context, ref Ref, digest string, p Platform) (created time.Time, config string,
	ans answer) {
	key := digest + " " + p.String()
	if c, seen := s.reg.cached(key, s.now); seen {
		return c.Created, c.Config, answerOK
	}
	m, ans := s.platformManifest(ctx, ref, digest, p)
	if ans != answerOK {
		return time.Time{}, "", ans
	}
	body, ans := s.readByDigest(ctx, ref, "blobs", m.Config.Digest, "", maxConfig)
	if ans != answerOK {
		return time.Time{}, "", ans
	}
	var cfg struct {
		Created time.Time `json:"created"`
	}
	if json.NewDecoder(bytes.NewReader(body)).Decode(&cfg) != nil {
		return time.Time{}, "", answerFailed
	}
	s.reg.remember(key, cachedImage{Config: m.Config.Digest, Created: cfg.Created, Used: s.now, Size: m.size()})
	return cfg.Created, m.Config.Digest, answerOK
}

// maxImageSize bounds a size the registry claims; past it the size counts as unknown.
const maxImageSize = 1 << 40

// size: the download of a platform's image, its layers and configuration; 0 when the manifest's sizes are not
// plausible.
func (m manifestDoc) size() int64 {
	n := m.Config.Size
	for _, l := range m.Layers {
		if l.Size < 0 || l.Size > maxImageSize {
			return 0
		}
		n += l.Size
	}
	if n <= 0 || n > maxImageSize {
		return 0
	}
	return n
}

// manifestDoc is an image manifest or an index, as far as the agent reads them.
type manifestDoc struct {
	Manifests []struct {
		Digest   string   `json:"digest"`
		Platform Platform `json:"platform"`
	} `json:"manifests"`
	Config struct {
		Digest string `json:"digest"`
		Size   int64  `json:"size"`
	} `json:"config"`
	Layers []struct {
		Size int64 `json:"size"`
	} `json:"layers"`
}

// platformManifest reads the manifest of the platform's image behind a manifest digest: an index is resolved to the
// platform's entry. An index without an image for the platform answers answerNotFound.
func (s *session) platformManifest(ctx context.Context, ref Ref, digest string, p Platform) (manifestDoc, answer) {
	body, ans := s.readByDigest(ctx, ref, "manifests", digest, acceptManifests, maxManifest)
	if ans != answerOK {
		return manifestDoc{}, ans
	}
	var m manifestDoc
	if json.Unmarshal(body, &m) != nil {
		return manifestDoc{}, answerFailed
	}
	if m.Config.Digest == "" { // an index: the platform's manifest
		pick := ""
		for _, e := range m.Manifests {
			if e.Platform.OS != p.OS || e.Platform.Architecture != p.Architecture || !digestPattern.MatchString(e.Digest) {
				continue
			}
			if e.Platform.Variant == p.Variant {
				pick = e.Digest
				break
			}
			if pick == "" && (p.Variant == "" || e.Platform.Variant == "") {
				pick = e.Digest
			}
		}
		if pick == "" {
			return manifestDoc{}, answerNotFound
		}
		body, ans = s.readByDigest(ctx, ref, "manifests", pick, acceptManifests, maxManifest)
		if ans != answerOK {
			return manifestDoc{}, ans
		}
		m = manifestDoc{}
		if json.Unmarshal(body, &m) != nil || m.Config.Digest == "" {
			return manifestDoc{}, answerFailed
		}
	}
	if !digestPattern.MatchString(m.Config.Digest) {
		return manifestDoc{}, answerFailed
	}
	return m, answerOK
}

// tags lists the repository's tags, following the registry's pages (Link: <…>; rel="next") up to maxTagPages of at
// most maxTagsPage bytes. Only valid tags are kept, at most maxRepoTags; a longer list counts as no answer, since
// the newest tags could be among those not read: answerTooLong, as for more pages or a larger page (ADR-0126).
func (s *session) tags(ctx context.Context, ref Ref) ([]string, answer) {
	u := "https://" + ref.apiHost() + "/v2/" + ref.Repository + "/tags/list?n=" + strconv.Itoa(tagsPerPage)
	var all []string
	for page := 0; page < maxTagPages; page++ {
		resp, ans := s.doURL(ctx, http.MethodGet, ref, u, "application/json")
		if ans != answerOK {
			return nil, ans
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxTagsPage+1))
		link := resp.Header.Get("Link")
		resp.Body.Close()
		switch {
		case err != nil:
			return nil, answerFailed
		case len(body) > maxTagsPage:
			return nil, answerTooLong
		}
		var list struct {
			Tags []string `json:"tags"`
		}
		if json.Unmarshal(body, &list) != nil {
			return nil, answerFailed
		}
		for _, t := range list.Tags {
			if tagPattern.MatchString(t) {
				all = append(all, t)
			}
		}
		if len(all) > maxRepoTags {
			return nil, answerTooLong
		}
		next, ok := nextPage(u, link)
		if !ok {
			return all, answerOK
		}
		u = next
	}
	return nil, answerTooLong
}

// nextPage reads the next page's address from a Link header; only one on the same host is followed (the token
// belongs to it).
func nextPage(current, link string) (string, bool) {
	if link == "" {
		return "", false
	}
	for _, part := range strings.Split(link, ",") {
		target, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if !strings.Contains(strings.ReplaceAll(params, " ", ""), `rel="next"`) {
			continue
		}
		target = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(target), "<"), ">")
		base, err := url.Parse(current)
		if err != nil {
			return "", false
		}
		next, err := base.Parse(target)
		if err != nil || next.Host != base.Host || next.Scheme != base.Scheme {
			return "", false
		}
		return next.String(), true
	}
	return "", false
}
