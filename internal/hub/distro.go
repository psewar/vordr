package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"github.com/psewar/vordr/internal/inventory"
	"github.com/psewar/vordr/internal/osv"
	"github.com/psewar/vordr/internal/versions"
)

// Packages inside container images (ADR-0120). The agent reads each image's distribution and installed packages; the
// hub matches them against the OSV dump of that distribution, release by release. An index of a whole distribution
// release is large in memory (Debian 13: about 60 MB), so the hub keeps these indexes on disk only: it loads one,
// matches every image of that release, keeps the results and drops the index - one at a time.

// distributions: the dumps of whole distributions the hub can match image contents with. Ubuntu is not among them
// yet: OSV names its releases differently (Ubuntu:24.04:LTS), and its dump is ten times Debian's.
var distributions = map[versions.Ecosystem]bool{versions.Debian: true, versions.Alpine: true}

func isDistribution(e versions.Ecosystem) bool { return distributions[e] }

// Codes of an image that was not matched (wire.ImageFindingsView.Code), besides the agent's own (no_package_db,
// unreadable, unknown_release).
const (
	ImageNotRead                 = "not_read"                 // the agent sent no contents (yet)
	ImageUnsupportedDistribution = "unsupported_distribution" // no dump the hub can match it with (Ubuntu, others)
	ImageUnsupportedRelease      = "unsupported_release"      // a release without a number (Debian testing)
	ImageNoData                  = "no_data"                  // the distribution is not in osv.ecosystems, or its data is not there yet
)

// OSVNoSpace: the hub's disk has no room for the dump (ADR-0120); the download is not started. OSVNoEntries: the dump
// has no entries for a release an image runs (one just released, or one OSV does not cover) - a state, not a failure;
// its images show ImageNoData.
const (
	OSVNoSpace   = "no_space"
	OSVNoEntries = "no_entries"
)

// spaceMargin: room left beside a dump, for the indexes built from it and everything else on the disk.
const spaceMargin = 256 << 20

// freeSpace tells the room on the disk (freeBytes; a variable for tests).
var freeSpace = freeBytes

// releaseOf: the OSV ecosystem of an image's distribution release - Debian:13, Alpine:v3.23 - or why there is none.
func releaseOf(img *inventory.ImageOS) (versions.Ecosystem, string) {
	if img == nil {
		return "", ImageNotRead
	}
	// too_many_packages with packages: the agent cut the list, the rest is matched; without any it left them out to fit
	// the message. Any other code: nothing to match.
	if img.Code != "" && (img.Code != inventory.CodeTooManyPackages || len(img.Packages) == 0) {
		return "", img.Code
	}
	parts := strings.Split(img.Version, ".")
	switch img.ID {
	case "debian":
		if !digits(parts[0]) {
			return "", ImageUnsupportedRelease
		}
		return versions.Ecosystem(string(versions.Debian) + ":" + parts[0]), ""
	case "alpine":
		if len(parts) < 2 || !digits(parts[0]) || !digits(parts[1]) {
			return "", ImageUnsupportedRelease
		}
		return versions.Ecosystem(string(versions.Alpine) + ":v" + parts[0] + "." + parts[1]), ""
	}
	return "", ImageUnsupportedDistribution
}

func digits(s string) bool {
	return s != "" && strings.Trim(s, "0123456789") == ""
}

// imageMatch is what one image's packages matched in its release's index.
type imageMatch struct {
	Release versions.Ecosystem
	Fixable []string // CVE (or advisory) ids a newer package version fixes, sorted
	Unfixed []string // ids without a fix yet, sorted
	// Kernel: ids only the kernel's own source packages have, left out of Fixable and Unfixed (ADR-0122).
	Kernel int
}

// kernelSource tells whether a distribution's source package is the kernel itself (ADR-0122). A container runs the
// host's kernel, never one from its image; the image only carries its headers (Debian's linux-libc-dev) or an unused
// kernel. Names as the OSV dumps write them: Debian's linux and its versioned sources (linux-6.12), Alpine's kernel
// aports. Packages that only have linux in their name (util-linux, linux-pam, linux-base) are not the kernel.
func kernelSource(base versions.Ecosystem, name string) bool {
	switch base {
	case versions.Debian:
		if name == "linux" {
			return true
		}
		v, ok := strings.CutPrefix(name, "linux-")
		parts := strings.Split(v, ".")
		if !ok || len(parts) < 2 {
			return false
		}
		for _, d := range parts {
			if !digits(d) {
				return false
			}
		}
		return true
	case versions.Alpine:
		return name == "linux-lts" || name == "linux-grsec"
	}
	return false
}

// matchImage looks up the image's packages in the release's index. Debian and Alpine write their advisories for source
// packages; the version is the source's when the package names one. Findings count by CVE: one CVE published several
// times (DEBIAN-CVE and a DSA) counts once, as fixable when any of its advisories names a fix; an advisory for several
// CVEs (a DSA) counts each; one without a CVE counts under its own id. A CVE the distribution rates "unimportant" for
// a package (not a security problem in its build) does not count for that package, whichever advisory names it.
//
// The kernel's source packages (kernelSource) are matched but not counted: their ids go to Kernel, unless another
// package of the image has the same id (ADR-0122).
func matchImage(img *inventory.ImageOS, db *osv.DB) imageMatch {
	m := imageMatch{Release: db.Ecosystem}
	type pkg struct{ name, version string }
	seen := map[pkg]bool{}
	fixable, unfixed, kernel := map[string]bool{}, map[string]bool{}, map[string]bool{}
	base := versions.Base(db.Ecosystem)
	for _, p := range img.Packages {
		k := pkg{p.Name, p.Version}
		if p.Source != "" {
			k.name = p.Source
		}
		if p.SourceVersion != "" {
			k.version = p.SourceVersion
		}
		if seen[k] {
			continue
		}
		seen[k] = true
		found, _ := db.Lookup(k.name, k.version)
		fix, open, unimportant := map[string]bool{}, map[string]bool{}, map[string]bool{}
		for _, f := range found {
			ids := f.CVEs()
			if len(ids) == 0 {
				ids = []string{f.ID}
			}
			for _, id := range ids {
				switch {
				case f.Urgency == "unimportant":
					unimportant[id] = true
				case len(f.Fixed) > 0:
					fix[id] = true
				default:
					open[id] = true
				}
			}
		}
		into, intoOpen := fixable, unfixed
		if kernelSource(base, k.name) {
			into, intoOpen = kernel, kernel
		}
		for id := range fix {
			if !unimportant[id] {
				into[id] = true
			}
		}
		for id := range open {
			if !unimportant[id] {
				intoOpen[id] = true
			}
		}
	}
	for id := range kernel {
		if !fixable[id] && !unfixed[id] { // another package has it too: it counts there
			m.Kernel++
		}
	}
	for id := range fixable {
		m.Fixable = append(m.Fixable, id)
		delete(unfixed, id) // fixed by one advisory of the CVE: fixable
	}
	for id := range unfixed {
		m.Unfixed = append(m.Unfixed, id)
	}
	slices.Sort(m.Fixable)
	slices.Sort(m.Unfixed)
	return m
}

// imageKey identifies an image of a host's container location, as the agent's inventory does.
func imageKey(p inventory.Package) string { return p.Name + "\x00" + p.Version + "\x00" + p.Digest }

// containerImages: the images of a host's container location in its last inventory; nil without one.
func containerImages(inv *inventory.Location) []inventory.Package {
	if inv == nil {
		return nil
	}
	return inv.Packages
}

func containerLocation(locs []inventory.Location) *inventory.Location {
	if i := slices.IndexFunc(locs, func(l inventory.Location) bool { return l.Kind == inventory.KindContainer }); i >= 0 {
		return &locs[i]
	}
	return nil
}

// distributionOn: the distribution's dump is in osv.ecosystems.
func (p *Packages) distributionOn(base versions.Ecosystem) bool {
	return p.cfg != nil && slices.Contains(p.cfg.Ecosystems, string(base))
}

// ImageContentsOn: the hub matches image contents with some distribution's dump, so it asks agents for them
// (wire.FeatureImageContents).
func (p *Packages) ImageContentsOn() bool {
	if p.cfg == nil {
		return false
	}
	for _, e := range p.cfg.Ecosystems {
		if isDistribution(versions.Ecosystem(e)) {
			return true
		}
	}
	return false
}

// neededReleases: the releases of the distribution that the hosts' images run, as their last inventories say.
func (p *Packages) neededReleases(base versions.Ecosystem) []versions.Ecosystem {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []versions.Ecosystem
	for _, inv := range p.inv {
		for _, img := range containerImages(containerLocation(inv.Locations)) {
			if r, code := releaseOf(img.OS); code == "" && versions.Base(r) == base && !slices.Contains(out, r) {
				out = append(out, r)
			}
		}
	}
	slices.Sort(out)
	return out
}

// hasIndex: the release's index is on disk.
func (p *Packages) hasIndex(release versions.Ecosystem) bool {
	_, err := os.Stat(p.indexPath(release))
	return err == nil
}

// releaseCurrent: the release has data from the dump with this ETag - an index of this hub's index version, or the
// fact that the dump has no entries for it. Not current: an index of an older dump (the release was not needed for a
// while), of an older index version, or none at all. Called with p.mu held.
func (p *Packages) releaseCurrent(m *osvMeta, r versions.Ecosystem, etag string) bool {
	rm, ok := m.Releases[string(r)]
	return ok && rm.ETag == etag && rm.Index == osv.IndexVersion && (rm.Empty || p.hasIndex(r))
}

// distributionDue: a release some image runs has no current data, or the dump's refresh time has come. A failed
// attempt - of the dump, or of a release built from it - waits osvRetry either way.
func (p *Packages) distributionDue(base versions.Ecosystem, need []versions.Ecosystem) (due, missing bool) {
	p.mu.Lock()
	m := p.meta[base]
	failed := m.Code != ""
	for _, r := range need {
		if !p.releaseCurrent(m, r, m.ETag) {
			missing = true
		}
		if m.Releases[string(r)].Code != "" {
			failed = true
		}
	}
	failedLately := failed && p.now().Sub(m.Failed) < osvRetry
	p.mu.Unlock()
	if failedLately {
		return false, missing
	}
	return missing || p.due(base), missing
}

// refreshDistribution downloads the distribution's dump (unless unchanged and every needed release is current),
// builds one index per needed release and matches every image of that release with it, before the next. Each release
// stands on its own: one without entries in the dump is noted as empty, one whose data would shrink sharply keeps its
// old index (ADR-0085) - neither stops the others. It reports whether results changed.
func (p *Packages) refreshDistribution(ctx context.Context, base versions.Ecosystem, need []versions.Ecosystem,
	missing bool) bool {
	dump, size, etag, code, err := p.fetchDump(ctx, base, missing)
	if dump != "" {
		defer os.Remove(dump)
	}
	now := p.now()
	changed, releaseFailed := false, false
	if code == "" && dump != "" {
		f, ferr := os.Open(dump)
		if ferr != nil {
			code, err = OSVStoreFailed, ferr
		} else {
			for _, r := range need {
				p.mu.Lock()
				current := p.releaseCurrent(p.meta[base], r, etag)
				p.mu.Unlock()
				if current {
					continue // built from this very dump already (a retry for another release)
				}
				built, why := p.buildRelease(f, size, base, r, etag)
				switch {
				case why == OSVBadArchive:
					code, err = why, errors.New("the dump is no readable zip")
				case why != "":
					releaseFailed = true // that release keeps its old data and its code; the others go on
				}
				if code != "" {
					break // the dump itself is unreadable: no release will do better
				}
				changed = changed || built
			}
			f.Close()
		}
	}
	p.mu.Lock()
	m := p.meta[base]
	switch {
	case code != "":
		m.Code, m.Failed = code, now
		p.log.Printf("osv %s: %s: %v", base, code, err)
	case dump == "": // not modified
		m.Code, m.Checked = "", now
	default:
		m.Code, m.Checked, m.ETag = "", now, etag
		if releaseFailed {
			m.Failed = now // the failed release is not current: it is tried again after osvRetry
		}
	}
	raw, _ := json.Marshal(m)
	p.mu.Unlock()
	if err := writeFileAtomic(p.metaPath(base), raw); err != nil {
		p.log.Printf("osv %s: %v", base, err)
	}
	return changed
}

// buildRelease builds one release's index from the dump, keeps it on disk and matches every image of the release with
// it - all under matchMu, so no other index is loaded meanwhile (ADR-0120: one in memory at a time). built: images
// were matched anew. why: the release keeps its old data, with this code (OSVBadArchive: the dump is unreadable).
func (p *Packages) buildRelease(dump io.ReaderAt, size int64, base, r versions.Ecosystem, etag string) (built bool,
	why string) {
	p.matchMu.Lock()
	defer p.matchMu.Unlock()
	defer debug.FreeOSMemory() // the next release's index must not stack on this one
	db, skipped, err := osv.Build(dump, size, r, p.now())
	if err != nil {
		p.log.Printf("osv %s: %s: %v", r, OSVBadArchive, err)
		return false, OSVBadArchive
	}
	if skipped > 0 {
		p.log.Printf("osv %s: %d files of the dump unreadable, left out", r, skipped)
	}
	p.mu.Lock()
	old, had := p.meta[base].Releases[string(r)]
	p.mu.Unlock()
	switch {
	case had && old.Entries > 0 && db.Entries*100 < old.Entries*osvMinShare: // none at all, or far fewer (ADR-0085)
		p.log.Printf("osv %s: %s: %d entries, the data in use has %d", r, OSVImplausible, db.Entries, old.Entries)
		old.Code = OSVImplausible
		p.setRelease(base, r, old)
		return false, OSVImplausible
	case db.Entries == 0: // the dump has nothing for this release (yet): a state, not a failure
		_ = os.Remove(p.indexPath(r))
		p.setRelease(base, r, releaseMeta{ETag: etag, Index: osv.IndexVersion, Empty: true, Built: p.now()})
		p.log.Printf("osv %s: %s", r, OSVNoEntries)
		return false, ""
	}
	if err := db.Save(p.indexPath(r)); err != nil {
		p.log.Printf("osv %s: %s: %v", r, OSVStoreFailed, err)
		old.Code = OSVStoreFailed // without old data a release of its code alone: never built (Built zero)
		p.setRelease(base, r, old)
		return false, OSVStoreFailed
	}
	p.setRelease(base, r, releaseMeta{Entries: db.Entries, Modified: db.Modified, ETag: etag, Index: osv.IndexVersion,
		Built: p.now()})
	p.log.Printf("osv %s", db)
	p.matchReleaseLocked(r, db)
	return true, ""
}

func (p *Packages) setRelease(base, r versions.Ecosystem, rm releaseMeta) {
	p.mu.Lock()
	defer p.mu.Unlock()
	m := p.meta[base]
	if m.Releases == nil {
		m.Releases = map[string]releaseMeta{}
	}
	m.Releases[string(r)] = rm
}

// forgetRelease drops a release whose index cannot be read (damaged, or of another index version): the next refresh
// builds it anew from the dump.
func (p *Packages) forgetRelease(r versions.Ecosystem) {
	p.mu.Lock()
	if m := p.meta[versions.Base(r)]; m != nil {
		delete(m.Releases, string(r))
	}
	p.mu.Unlock()
	_ = os.Remove(p.indexPath(r))
	p.log.Printf("osv %s: index unreadable, built anew from the next download", r)
}

// loadRelease reads a release's index from disk; a damaged one is forgotten. Called with matchMu held.
func (p *Packages) loadRelease(r versions.Ecosystem) *osv.DB {
	if !p.hasIndex(r) {
		return nil
	}
	db, current, err := osv.Load(p.indexPath(r))
	if err != nil || !current {
		p.forgetRelease(r)
		return nil
	}
	return db
}

// fetchDump downloads the distribution's dump into a file next to the indexes. It sends the ETag of the data in use
// unless a release is missing (then the dump is needed whatever changed). Before writing it checks the disk: a dump
// that does not fit beside spaceMargin is not started (OSVNoSpace). dump is "" when the dump has not changed.
func (p *Packages) fetchDump(ctx context.Context, base versions.Ecosystem, missing bool) (dump string, size int64,
	etag, code string, err error) {
	p.mu.Lock()
	known := p.meta[base].ETag
	p.mu.Unlock()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(p.cfg.URL, "/")+"/"+string(base)+"/all.zip", nil)
	if err != nil {
		return "", 0, "", OSVDownloadFailed, err
	}
	if known != "" && !missing {
		req.Header.Set("If-None-Match", known)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return "", 0, "", OSVDownloadFailed, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotModified:
		return "", 0, known, "", nil
	case http.StatusOK:
	default:
		return "", 0, "", OSVDownloadFailed, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	want := resp.ContentLength
	if want < 0 {
		want = maxOSVDump // unknown: the largest dump the hub takes
	}
	if free, known := freeSpace(p.osvDir); known && free < uint64(want)+spaceMargin {
		return "", 0, "", OSVNoSpace, fmt.Errorf("%d bytes free, the dump needs %d and %d beside it", free, want, spaceMargin)
	}
	part := filepath.Join(p.osvDir, string(base)+"-all.zip.part")
	f, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0o600)
	if err != nil {
		return "", 0, "", OSVStoreFailed, err
	}
	n, err := io.Copy(f, io.LimitReader(resp.Body, maxOSVDump+1))
	f.Close()
	switch {
	case err != nil:
		os.Remove(part)
		return "", 0, "", OSVDownloadFailed, err
	case n > maxOSVDump:
		os.Remove(part)
		return "", 0, "", OSVDownloadFailed, errors.New("dump larger than allowed")
	}
	return part, n, resp.Header.Get("ETag"), "", nil
}

// matchReleaseLocked matches every image of the release on every host with its index, and keeps the results. Called
// with matchMu held.
func (p *Packages) matchReleaseLocked(r versions.Ecosystem, db *osv.DB) {
	p.mu.Lock()
	type job struct {
		node, key string
		img       *inventory.ImageOS
	}
	var jobs []job
	for node, inv := range p.inv {
		for _, img := range containerImages(containerLocation(inv.Locations)) {
			if rel, code := releaseOf(img.OS); code == "" && rel == r {
				jobs = append(jobs, job{node, imageKey(img), img.OS})
			}
		}
	}
	p.mu.Unlock()
	results := make([]imageMatch, len(jobs))
	for i, j := range jobs {
		results[i] = matchImage(j.img, db)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, j := range jobs {
		if p.images[j.node] == nil {
			p.images[j.node] = map[string]imageMatch{}
		}
		p.images[j.node][j.key] = results[i]
		delete(p.views, j.node)
	}
}

// matchNode matches the images of one host anew (its inventory changed): each release its images run is loaded from
// disk once, used and dropped.
func (p *Packages) matchNode(node string) {
	p.matchMu.Lock()
	defer p.matchMu.Unlock()
	p.mu.Lock()
	inv := p.inv[node]
	var imgs []inventory.Package
	if inv != nil {
		imgs = containerImages(containerLocation(inv.Locations))
	}
	p.mu.Unlock()
	byRelease := map[versions.Ecosystem][]inventory.Package{}
	for _, img := range imgs {
		if r, code := releaseOf(img.OS); code == "" && p.distributionOn(versions.Base(r)) {
			byRelease[r] = append(byRelease[r], img)
		}
	}
	results := map[string]imageMatch{}
	for r, list := range byRelease {
		db := p.loadRelease(r)
		if db == nil {
			continue // the next refresh downloads it and matches
		}
		for _, img := range list {
			results[imageKey(img)] = matchImage(img.OS, db)
		}
		db = nil
		debug.FreeOSMemory()
	}
	p.mu.Lock()
	p.images[node] = results
	delete(p.views, node)
	p.mu.Unlock()
}

// matchAll matches every host's images, one release at a time: after a restart, before the views can show findings.
func (p *Packages) matchAll() {
	if !p.ImageContentsOn() {
		return
	}
	for e := range distributions {
		if !p.distributionOn(e) {
			continue
		}
		for _, r := range p.neededReleases(e) {
			p.matchStored(r)
		}
	}
}

// matchStored matches every image of the release with its index on disk, if there is one.
func (p *Packages) matchStored(r versions.Ecosystem) {
	p.matchMu.Lock()
	defer p.matchMu.Unlock()
	if db := p.loadRelease(r); db != nil {
		p.matchReleaseLocked(r, db)
		db = nil
		debug.FreeOSMemory()
	}
}

// releaseMeta: what the hub remembers about one release's index (the indexes themselves stay on disk): from which
// dump (ETag), when and in which index version it was built, or that the dump had no entries for the release (Empty).
// Code: the last build from a newer dump failed, and the release keeps this data (ADR-0085).
type releaseMeta struct {
	Entries  int       `json:"entries"`
	Modified time.Time `json:"modified"`
	ETag     string    `json:"etag,omitempty"`
	Index    int       `json:"index"`
	Empty    bool      `json:"empty,omitempty"`
	Built    time.Time `json:"built"`
	Code     string    `json:"code,omitempty"`
}
