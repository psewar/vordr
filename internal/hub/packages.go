package hub

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/psewar/vordr/internal/inventory"
	"github.com/psewar/vordr/internal/osv"
	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/versions"
	"github.com/psewar/vordr/internal/wire"
)

// Packages of pip, pipx and npm (design 11.2): agents report what is installed, the hub matches it against OSV data
// it keeps itself. The hub downloads each ecosystem's whole dump (osv.dev publishes them daily) and builds a small
// index; no package list of a host goes to api.osv.dev or anywhere else.

// OSVConfig is the osv section of hub.yaml. Without it the hub keeps inventories but has no data to match them:
// Home Assistant then shows the counts as unknown, not as zero.
type OSVConfig struct {
	URL          string   `yaml:"url"`           // where <ecosystem>/all.zip lies; default the osv.dev bucket
	Ecosystems   []string `yaml:"ecosystems"`    // default PyPI and npm
	RefreshHours int      `yaml:"refresh_hours"` // default 24
	// StaleHours: data older than this (refresh keeps failing) shows as stale; default 72.
	StaleHours int `yaml:"stale_hours"`
}

const (
	defaultOSVURL       = "https://osv-vulnerabilities.storage.googleapis.com"
	defaultOSVRefreshH  = 24
	defaultOSVStaleH    = 72
	osvRetry            = time.Hour       // after a failed download
	osvTick             = 5 * time.Minute // how often the hub looks whether a refresh is due
	maxOSVDump          = 2 << 30         // npm's dump has 220 MB (October 2026)
	osvDownloadDeadline = 30 * time.Minute
)

func (o *OSVConfig) applyDefaults() {
	if o.URL == "" {
		o.URL = defaultOSVURL
	}
	if len(o.Ecosystems) == 0 {
		o.Ecosystems = []string{string(versions.PyPI), string(versions.Npm)}
	}
	if o.RefreshHours == 0 {
		o.RefreshHours = defaultOSVRefreshH
	}
	if o.StaleHours == 0 {
		o.StaleHours = defaultOSVStaleH
	}
}

func (o *OSVConfig) check() []string {
	var out []string
	if u, err := url.Parse(o.URL); err != nil || u.Scheme != "https" || u.Host == "" {
		out = append(out, "osv.url: must be an https:// URL")
	}
	seen := map[string]bool{}
	for _, e := range o.Ecosystems {
		if !(versions.Ecosystem(e).Known() || isDistribution(versions.Ecosystem(e))) || seen[e] {
			out = append(out, fmt.Sprintf("osv.ecosystems: %q is unknown or twice (PyPI, npm; Debian, Alpine for the "+
				"packages inside container images)", e))
		}
		seen[e] = true
	}
	if o.RefreshHours < 1 || o.RefreshHours > 168 {
		out = append(out, "osv.refresh_hours: must be between 1 and 168")
	}
	if o.StaleHours < o.RefreshHours || o.StaleHours > 720 {
		out = append(out, "osv.stale_hours: must be at least refresh_hours and at most 720")
	}
	return out
}

// OSV data states (wire.OSVView.State) and the codes of a failed refresh.
const (
	OSVOk      = "ok"
	OSVStale   = "stale"   // the last good data is older than stale_hours
	OSVMissing = "missing" // no data yet

	OSVDownloadFailed = "download_failed"  // no answer, or not 200/304
	OSVBadArchive     = "bad_archive"      // the dump is no readable zip
	OSVStoreFailed    = "store_failed"     // the index could not be written
	OSVImplausible    = "implausible_dump" // no entries, or far fewer (malware) entries than the data in use (ADR-0085)
)

// osvMinShare: a new dump needs at least this share, in percent, of the entries and of the malware entries of the data
// in use, or it does not replace them (ADR-0085).
const osvMinShare = 90

// osvMeta is what the hub remembers about one ecosystem's data, next to its index.
type osvMeta struct {
	ETag    string    `json:"etag,omitempty"`
	Checked time.Time `json:"checked"` // last download, or last answer "not modified"
	Code    string    `json:"code,omitempty"`
	Failed  time.Time `json:"failed,omitempty"`
	// Releases: a distribution's releases with an index on disk (ADR-0120), by OSV ecosystem (Debian:13).
	Releases map[string]releaseMeta `json:"releases,omitempty"`
}

// Packages keeps the inventories of the hosts and the OSV data, and matches one against the other.
type Packages struct {
	invDir, osvDir string
	cfg            *OSVConfig // nil: no OSV data
	client         *http.Client
	now            func() time.Time
	log            *log.Logger

	mu    sync.Mutex
	inv   map[string]*wire.Inventory
	dbs   map[versions.Ecosystem]*osv.DB
	meta  map[versions.Ecosystem]*osvMeta
	views map[string]*wire.PackagesView // computed on demand, dropped when inventory or data change
	// images: per host, what the packages inside its images matched (ADR-0120), by imageKey. The distributions'
	// indexes stay on disk; matchMu lets one match at a time load one.
	images  map[string]map[string]imageMatch
	matchMu sync.Mutex
}

// NewPackages loads the inventories and OSV indexes kept in stateDir. cfg nil: no OSV data.
func NewPackages(stateDir string, cfg *OSVConfig, now func() time.Time, logger *log.Logger) (*Packages, error) {
	p := &Packages{invDir: filepath.Join(stateDir, "packages"), osvDir: filepath.Join(stateDir, "osv"), cfg: cfg,
		client: &http.Client{Timeout: osvDownloadDeadline}, now: now, log: logger,
		inv: map[string]*wire.Inventory{}, dbs: map[versions.Ecosystem]*osv.DB{},
		meta: map[versions.Ecosystem]*osvMeta{}, views: map[string]*wire.PackagesView{},
		images: map[string]map[string]imageMatch{}}
	for _, d := range []string{p.invDir, p.osvDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	files, err := filepath.Glob(filepath.Join(p.invDir, "*.json"))
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var inv wire.Inventory
		if err := json.Unmarshal(raw, &inv); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		p.inv[strings.TrimSuffix(filepath.Base(f), ".json")] = &inv
	}
	if cfg == nil {
		return p, nil
	}
	for _, e := range cfg.Ecosystems {
		eco := versions.Ecosystem(e)
		p.meta[eco] = &osvMeta{}
		if raw, err := os.ReadFile(p.metaPath(eco)); err == nil {
			_ = json.Unmarshal(raw, p.meta[eco]) // a broken file only costs one download
		}
		if isDistribution(eco) {
			continue // its releases' indexes stay on disk; matchAll uses them after the start (ADR-0120)
		}
		idx := p.indexPath(eco)
		if _, err := os.Stat(idx); errors.Is(err, fs.ErrNotExist) {
			p.meta[eco].Checked = time.Time{} // no index: download at once, whatever the meta file says
			continue
		}
		db, current, err := osv.Load(idx)
		switch {
		case err != nil:
			logger.Printf("osv %s: index unreadable, downloading anew: %v", eco, err)
			p.meta[eco].Checked, p.meta[eco].ETag = time.Time{}, ""
		case !current:
			logger.Printf("osv %s: index of an older version, downloading anew", eco)
			p.meta[eco].Checked, p.meta[eco].ETag = time.Time{}, ""
		default:
			p.dbs[eco] = db
			logger.Printf("osv %s", db)
		}
	}
	return p, nil
}

func (p *Packages) indexPath(eco versions.Ecosystem) string {
	return filepath.Join(p.osvDir, string(eco)+".idx")
}
func (p *Packages) metaPath(eco versions.Ecosystem) string {
	return filepath.Join(p.osvDir, string(eco)+".json")
}

// SetInventory keeps a host's newest inventory (also on disk, so that a restart keeps the findings).
func (p *Packages) SetInventory(node string, inv wire.Inventory) error {
	raw, err := json.Marshal(inv)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.inv[node] = &inv
	delete(p.views, node)
	p.mu.Unlock()
	if p.ImageContentsOn() {
		p.matchNode(node) // the packages inside its images, against the releases' indexes on disk (ADR-0120)
	}
	return writeFileAtomic(filepath.Join(p.invDir, node+".json"), raw)
}

// Forget drops a host's inventory (the host was forgotten at the hub).
func (p *Packages) Forget(node string) {
	p.mu.Lock()
	delete(p.inv, node)
	delete(p.views, node)
	delete(p.images, node)
	p.mu.Unlock()
	_ = os.Remove(filepath.Join(p.invDir, node+".json")) // absent already is just as good
}

func writeFileAtomic(path string, raw []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Run refreshes the OSV data when due, until ctx ends; changed is called whenever new data changes the views.
func (p *Packages) Run(ctx context.Context, changed func()) {
	if p.cfg == nil {
		return
	}
	if p.ImageContentsOn() {
		p.matchAll() // the images' findings after a restart, from the indexes on disk (ADR-0120)
		changed()
	}
	for {
		p.refreshAll(ctx, changed)
		select {
		case <-ctx.Done():
			return
		case <-time.After(osvTick):
		}
	}
}

// refreshAll refreshes every ecosystem whose data is due. A distribution's dump only while some image runs one of its
// releases, and then only for those releases (ADR-0120).
func (p *Packages) refreshAll(ctx context.Context, changed func()) {
	for _, e := range p.cfg.Ecosystems {
		if ctx.Err() != nil {
			return
		}
		eco := versions.Ecosystem(e)
		if isDistribution(eco) {
			if need := p.neededReleases(eco); len(need) > 0 {
				if due, missing := p.distributionDue(eco, need); due && p.refreshDistribution(ctx, eco, need, missing) {
					changed()
				}
			}
			continue
		}
		if p.due(eco) && p.Refresh(ctx, eco) {
			changed()
		}
	}
}

func (p *Packages) due(eco versions.Ecosystem) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	m := p.meta[eco]
	now := p.now()
	if m.Code != "" && now.Sub(m.Failed) < osvRetry {
		return false
	}
	return now.Sub(m.Checked) >= time.Duration(p.cfg.RefreshHours)*time.Hour
}

// Refresh downloads one ecosystem's dump unless it is unchanged (ETag) and rebuilds the index. It reports whether the
// data changed. A failure keeps the old data and is remembered with a code; it shows in the hub view.
func (p *Packages) Refresh(ctx context.Context, eco versions.Ecosystem) bool {
	code, etag, db, err := p.fetch(ctx, eco)
	p.mu.Lock()
	m := p.meta[eco]
	now := p.now()
	switch {
	case code != "":
		m.Code, m.Failed = code, now
		p.log.Printf("osv %s: %s: %v", eco, code, err)
	case db == nil: // not modified
		m.Code, m.Checked = "", now
	default:
		m.Code, m.Checked, m.ETag = "", now, etag
		p.dbs[eco] = db
		p.views = map[string]*wire.PackagesView{}
		p.log.Printf("osv %s", db)
	}
	raw, _ := json.Marshal(m)
	p.mu.Unlock()
	if err := writeFileAtomic(p.metaPath(eco), raw); err != nil {
		p.log.Printf("osv %s: %v", eco, err)
	}
	return db != nil
}

// fetch returns the new index, or nil with no code when the dump has not changed since the ETag.
func (p *Packages) fetch(ctx context.Context, eco versions.Ecosystem) (code, etag string, db *osv.DB, err error) {
	p.mu.Lock()
	known := ""
	if p.dbs[eco] != nil {
		known = p.meta[eco].ETag
	}
	p.mu.Unlock()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(p.cfg.URL, "/")+"/"+string(eco)+"/all.zip", nil)
	if err != nil {
		return OSVDownloadFailed, "", nil, err
	}
	if known != "" {
		req.Header.Set("If-None-Match", known)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return OSVDownloadFailed, "", nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotModified:
		return "", known, nil, nil
	case http.StatusOK:
	default:
		return OSVDownloadFailed, "", nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	part := filepath.Join(p.osvDir, string(eco)+"-all.zip.part")
	defer os.Remove(part) // the dump is only needed until the index is built
	f, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0o600)
	if err != nil {
		return OSVStoreFailed, "", nil, err
	}
	defer f.Close()
	n, err := io.Copy(f, io.LimitReader(resp.Body, maxOSVDump+1))
	switch {
	case err != nil:
		return OSVDownloadFailed, "", nil, err
	case n > maxOSVDump:
		return OSVDownloadFailed, "", nil, fmt.Errorf("dump larger than %d bytes", maxOSVDump)
	}
	db, skipped, err := osv.Build(f, n, eco, p.now())
	if err != nil {
		return OSVBadArchive, "", nil, err
	}
	if skipped > 0 {
		p.log.Printf("osv %s: %d files of the dump unreadable, left out", eco, skipped)
	}
	if why := p.implausible(eco, db); why != "" {
		return OSVImplausible, "", nil, errors.New(why)
	}
	if err := db.Save(p.indexPath(eco)); err != nil {
		return OSVStoreFailed, "", nil, err
	}
	return "", resp.Header.Get("ETag"), db, nil
}

// implausible says why a freshly built index must not replace the data in use: a dump without entries, or one with far
// fewer entries or malware entries. The dumps grow from day to day, and withdrawn advisories are few, so a sharp drop is
// a broken or truncated publication. Taking it would make the hub vouch for every update plan; keeping the old data
// lets it go stale instead, and stale data vouches for nothing (ADR-0085).
func (p *Packages) implausible(eco versions.Ecosystem, db *osv.DB) string {
	if db.Entries == 0 {
		return "the dump has no entries"
	}
	p.mu.Lock()
	old := p.dbs[eco]
	p.mu.Unlock()
	switch {
	case old == nil:
		return ""
	case db.Entries*100 < old.Entries*osvMinShare:
		return fmt.Sprintf("%d entries, the data in use has %d", db.Entries, old.Entries)
	case db.Malicious*100 < old.Malicious*osvMinShare:
		return fmt.Sprintf("%d malware entries, the data in use has %d", db.Malicious, old.Malicious)
	}
	return ""
}

// OSVOn reports whether the hub keeps OSV data (an osv section), so that it can vouch for update plans.
func (p *Packages) OSVOn() bool { return p.cfg != nil }

// Vet checks every entry of an update plan against the OSV data (design 11.4). unavailable says why the hub cannot
// vouch (data missing or stale); then there is no statement about the entries.
func (p *Packages) Vet(eco versions.Ecosystem, plan []protocol.VetPackage) ([]protocol.VetPackage, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	db, m := p.dbs[eco], p.meta[eco]
	switch {
	case db == nil || m == nil:
		return []protocol.VetPackage{}, OSVMissing
	case p.now().Sub(m.Checked) > time.Duration(p.cfg.StaleHours)*time.Hour:
		return []protocol.VetPackage{}, OSVStale
	}
	out := make([]protocol.VetPackage, 0, len(plan))
	for _, e := range plan {
		found, undecided := db.Lookup(e.Name, e.Version)
		v := protocol.VetPackage{Name: e.Name, Version: e.Version}
		for _, f := range found {
			v.Flagged = append(v.Flagged, f.ID)
			v.Malicious = v.Malicious || f.Malicious
		}
		if undecided > 0 { // a version OSV's order cannot place is not clean either
			v.Flagged = append(v.Flagged, "undecided")
		}
		out = append(out, v)
	}
	return out, ""
}

// UpdatableLocations: the locations of a host whose packages Home Assistant may update - the agent said so (its
// policy and kind) and the hub can vouch for plans.
func (p *Packages) UpdatableLocations(node string) []string {
	if p.cfg == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	if inv := p.inv[node]; inv != nil {
		for _, l := range inv.Locations {
			if l.Update {
				out = append(out, l.ID)
			}
		}
	}
	return out
}

// RefreshableLocations: the updatable locations whose dependencies with findings the agent can refresh (ADR-0104).
func (p *Packages) RefreshableLocations(node string) []string {
	if p.cfg == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	if inv := p.inv[node]; inv != nil {
		for _, l := range inv.Locations {
			if l.Update && l.Deps {
				out = append(out, l.ID)
			}
		}
	}
	return out
}

// Updatable reports whether a package update command fits what the host reported: an updatable location, and with
// a name a direct package there that has a candidate the OSV data does not flag. The agent checks all of it again.
func (p *Packages) Updatable(node, loc, name string) bool {
	if p.cfg == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	inv := p.inv[node]
	if inv == nil {
		return false
	}
	for _, l := range inv.Locations {
		if l.ID != loc || !l.Update {
			continue
		}
		if name == "" {
			return true
		}
		db := p.dbs[l.Ecosystem]
		for _, pkg := range l.Packages {
			if !pkg.Direct || pkg.Name != name || db == nil {
				continue
			}
			if inv.MinAgeDaysSecurity > 0 && fixOf(pkg, db) != "" {
				return true // a fix at the shorter age (ADR-0111)
			}
			if pkg.Candidate != "" {
				found, undecided := db.Lookup(pkg.Name, pkg.Candidate)
				return len(found) == 0 && undecided == 0
			}
		}
	}
	return false
}

// HubView: the state of the OSV data per ecosystem; nil without an osv section.
func (p *Packages) HubView() []wire.OSVView {
	if p.cfg == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	out := []wire.OSVView{}
	for _, e := range p.cfg.Ecosystems {
		eco := versions.Ecosystem(e)
		m := p.meta[eco]
		if isDistribution(eco) {
			out = append(out, p.distributionViews(eco, m, now)...)
			continue
		}
		v := wire.OSVView{Ecosystem: e, State: OSVMissing, Code: m.Code}
		if db := p.dbs[eco]; db != nil {
			v.State = OSVOk
			if now.Sub(m.Checked) > time.Duration(p.cfg.StaleHours)*time.Hour {
				v.State = OSVStale
			}
			checked, modified := m.Checked, db.Modified
			v.Checked, v.Modified = &checked, &modified
			v.Advisories, v.Malicious = db.Entries, db.Malicious
		}
		out = append(out, v)
	}
	return out
}

// distributionViews: a distribution's releases that some image runs, each with its index's state; nothing while no
// image runs one (the data is not needed then, and its absence is no problem). Called with p.mu held.
func (p *Packages) distributionViews(base versions.Ecosystem, m *osvMeta, now time.Time) []wire.OSVView {
	need := map[string]bool{}
	for _, inv := range p.inv {
		for _, img := range containerImages(containerLocation(inv.Locations)) {
			if r, code := releaseOf(img.OS); code == "" && versions.Base(r) == base {
				need[string(r)] = true
			}
		}
	}
	var out []wire.OSVView
	for _, r := range slices.Sorted(maps.Keys(need)) {
		v := wire.OSVView{Ecosystem: r, State: OSVMissing, Code: m.Code}
		rm, ok := m.Releases[r]
		switch {
		case !ok:
		case rm.Empty:
			v.Code = cmp.Or(m.Code, OSVNoEntries)
		case rm.Built.IsZero(): // never built: only its failure is known
			v.Code = cmp.Or(rm.Code, m.Code)
		default:
			// data of the dump in use counts from the last check; data of an older dump from when it was built
			checked, modified := rm.Built, rm.Modified
			if rm.ETag == m.ETag {
				checked = m.Checked
			}
			v.State = OSVOk
			if now.Sub(checked) > time.Duration(p.cfg.StaleHours)*time.Hour {
				v.State = OSVStale
			}
			v.Code = cmp.Or(rm.Code, m.Code)
			v.Checked, v.Modified, v.Advisories = &checked, &modified, rm.Entries
		}
		out = append(out, v)
	}
	return out
}

// severityRank orders the sources' ratings, most severe first; unknown last.
var severityRank = map[string]int{"CRITICAL": 0, "HIGH": 1, "MODERATE": 2, "MEDIUM": 2, "LOW": 3}

func rank(s string) int {
	if r, ok := severityRank[s]; ok {
		return r
	}
	return 4
}

// View is what the hub knows about a host's packages; nil when the host never sent an inventory.
func (p *Packages) View(node string) *wire.PackagesView {
	p.mu.Lock()
	defer p.mu.Unlock()
	if v, ok := p.views[node]; ok {
		return v
	}
	inv := p.inv[node]
	if inv == nil {
		return nil
	}
	v := &wire.PackagesView{CheckedAt: inv.CheckedAt, Locations: []wire.LocationView{}}
	vulnerable, malicious, outdated, waiting, known := 0, 0, 0, 0, true
	var findings []wire.FindingView
	var updates []wire.UpdateView
	for _, loc := range inv.Locations {
		if loc.Kind == inventory.KindContainer { // its own view (Containers): no OSV data, not a pip/npm package
			continue
		}
		lv := wire.LocationView{ID: loc.ID, Kind: loc.Kind, Ecosystem: string(loc.Ecosystem), Path: loc.Path,
			Packages: len(loc.Packages), Code: loc.Code, Skipped: loc.Skipped, OutdatedFailed: loc.OutdatedFailed,
			NotInRegistry: loc.NotInRegistry}
		v.Total += len(loc.Packages)
		db := p.dbs[loc.Ecosystem]
		if db == nil {
			known = known && len(loc.Packages) == 0
		}
		for _, pkg := range loc.Packages {
			if pkg.Latest != "" {
				outdated++
				lv.Outdated++
				updates = append(updates, updateView(loc.ID, pkg, db))
			}
			if db == nil {
				continue
			}
			found, undecided := db.Lookup(pkg.Name, pkg.Version)
			v.Undecided += undecided
			if len(found) == 0 {
				continue
			}
			fv := findingView(loc.ID, pkg.Name, pkg.Version, pkg.Via, found, loc.Ecosystem)
			if fv.Malicious {
				malicious++
				lv.Malicious++
			} else {
				vulnerable++
				lv.Vulnerable++
				fv.Reach, fv.HeldBy = reach(loc, pkg, fv.FixedIn, db)
				if !wire.Actionable(fv.Reach) {
					waiting++
				}
			}
			findings = append(findings, fv)
		}
		v.Locations = append(v.Locations, lv)
	}
	if known {
		v.Vulnerable, v.Malicious, v.Waiting = &vulnerable, &malicious, &waiting
	}
	slices.SortStableFunc(findings, func(a, b wire.FindingView) int {
		switch {
		case a.Malicious != b.Malicious:
			if a.Malicious {
				return -1
			}
			return 1
		case rank(a.Severity) != rank(b.Severity):
			return rank(a.Severity) - rank(b.Severity)
		}
		return strings.Compare(a.Location+"\x00"+a.Name, b.Location+"\x00"+b.Name)
	})
	if len(findings) > wire.MaxFindings {
		findings = findings[:wire.MaxFindings]
	}
	v.Findings = findings
	if inv.OutdatedAt != nil {
		v.Outdated, v.OutdatedAt, v.MinAgeDays = &outdated, inv.OutdatedAt, inv.MinAgeDays
		v.MinAgeDaysSecurity = inv.MinAgeDaysSecurity
		// Those an update can take now first; then those that wait (too new, or the candidate is flagged).
		ready := func(u wire.UpdateView) bool { return u.Fix != "" || u.Candidate != "" && len(u.CandidateFlagged) == 0 }
		slices.SortStableFunc(updates, func(a, b wire.UpdateView) int {
			ra, rb := ready(a), ready(b)
			switch {
			case ra && !rb:
				return -1
			case rb && !ra:
				return 1
			}
			return strings.Compare(a.Location+"\x00"+a.Name, b.Location+"\x00"+b.Name)
		})
		if len(updates) > wire.MaxUpdates {
			updates = updates[:wire.MaxUpdates]
		}
		v.Updates = updates
	}
	p.views[node] = v
	return v
}

// updateView: an outdated package, with what the OSV data says about its candidate and its security candidate (nil
// db: nothing known).
func updateView(loc string, pkg inventory.Package, db *osv.DB) wire.UpdateView {
	uv := wire.UpdateView{Location: loc, Name: pkg.Name, Installed: pkg.Version, Latest: pkg.Latest,
		Candidate: pkg.Candidate, LatestAt: pkg.LatestAt, Major: pkg.Major}
	if db != nil && pkg.Candidate != "" {
		found, _ := db.Lookup(pkg.Name, pkg.Candidate)
		for _, f := range found {
			uv.CandidateFlagged = append(uv.CandidateFlagged, f.ID)
		}
	}
	uv.Fix = fixOf(pkg, db)
	return uv
}

// fixOf: the security candidate, when it is a fix (ADR-0111) - the installed version has an advisory and the
// security candidate has none (and none OSV's order cannot place). "" otherwise, and without OSV data.
func fixOf(pkg inventory.Package, db *osv.DB) string {
	if db == nil || pkg.SecurityCandidate == "" {
		return ""
	}
	if installed, _ := db.Lookup(pkg.Name, pkg.Version); len(installed) == 0 {
		return "" // nothing to fix: the normal minimum age holds
	}
	if found, undecided := db.Lookup(pkg.Name, pkg.SecurityCandidate); len(found) > 0 || undecided > 0 {
		return ""
	}
	return pkg.SecurityCandidate
}

// SecurityFixes: the packages an install job with this scope may take at the agent's shorter minimum age
// (ADR-0111) - for pkg:<location>[/<name>] the direct packages whose update is a fix, for deps:<location> the
// dependencies there that have findings. nil when the agent announces no shorter age or the hub has no OSV data.
func (p *Packages) SecurityFixes(node, scope string) []string {
	sc, ok := protocol.ParseScope(scope)
	if !ok || p.cfg == nil || sc.Kind != protocol.ScopePkg && sc.Kind != protocol.ScopeDeps {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	inv := p.inv[node]
	if inv == nil || inv.MinAgeDaysSecurity <= 0 {
		return nil
	}
	var out []string
	for _, l := range inv.Locations {
		db := p.dbs[l.Ecosystem]
		if l.ID != sc.Location || db == nil {
			continue
		}
		for _, pkg := range l.Packages {
			name := pkg.Name
			switch {
			case slices.Contains(out, name) || len(out) >= protocol.MaxSecurityFix:
			case sc.Kind == protocol.ScopePkg && pkg.Direct && (sc.Package == "" || sc.Package == name):
				if fixOf(pkg, db) != "" {
					out = append(out, name)
				}
			case sc.Kind == protocol.ScopeDeps && !pkg.Direct:
				if found, _ := db.Lookup(name, pkg.Version); len(found) > 0 {
					out = append(out, name)
				}
			}
		}
	}
	slices.Sort(out)
	return out
}

// reach says where the fix of a vulnerable package is (ADR-0129). A hold keeps the package below fixedIn when it is a
// bundle or its range admits no version at or above fixedIn; a hold that does not read keeps nothing, so that a doubt
// leaves the finding actionable.
func reach(loc inventory.Location, pkg inventory.Package, fixedIn string, db *osv.DB) (string, []inventory.Hold) {
	if fixedIn == "" {
		return wire.ReachNoFix, nil
	}
	var held []inventory.Hold
	for _, h := range pkg.Holds {
		if h.Bundled {
			held = append(held, h)
			continue
		}
		b, ok := versions.UpperBound(loc.Ecosystem, h.Range)
		if !ok {
			continue
		}
		if admits, ok := b.Admits(loc.Ecosystem, fixedIn); ok && !admits {
			held = append(held, h)
		}
	}
	switch {
	case len(held) > 0 && holderReady(loc, pkg, held, db):
		if loc.Update {
			return wire.ReachUpdateVia, held
		}
		return wire.ReachByHand, held
	case len(held) > 0:
		return wire.ReachHeld, held
	case pkg.Direct && (fixOf(pkg, db) != "" || atLeast(loc.Ecosystem, pkg.Candidate, fixedIn)):
		if loc.Update {
			return wire.ReachUpdate, nil
		}
		return wire.ReachByHand, nil
	case pkg.Direct && atLeast(loc.Ecosystem, pkg.Latest, fixedIn):
		return wire.ReachTooNew, nil
	case !pkg.Direct && loc.Update && loc.Deps:
		return wire.ReachRefresh, nil
	}
	return wire.ReachByHand, nil
}

// holderReady: a direct package of the location that holds pkg, or that pkg came with (npm), has an update ready - it
// may bring the fix along.
func holderReady(loc inventory.Location, pkg inventory.Package, held []inventory.Hold, db *osv.DB) bool {
	for _, p := range loc.Packages {
		if !p.Direct || p.Candidate == "" && fixOf(p, db) == "" {
			continue
		}
		if p.Name == pkg.Via || slices.ContainsFunc(held, func(h inventory.Hold) bool { return h.By == p.Name+"@"+p.Version }) {
			return true
		}
	}
	return false
}

// atLeast: v is a version at or above min (false for "" or what does not order).
func atLeast(eco versions.Ecosystem, v, min string) bool {
	if v == "" {
		return false
	}
	c, ok := versions.Compare(eco, v, min)
	return ok && c >= 0
}

// findingView sums up the advisories of one installed package: malicious if any says so, the most severe rating,
// and the lowest version that fixes all of them (none if one of them names no fix above the installed version).
func findingView(loc, name, version, via string, found []osv.Finding, eco versions.Ecosystem) wire.FindingView {
	fv := wire.FindingView{Location: loc, Name: name, Version: version, Via: via}
	fixedAll := true
	for _, f := range found {
		fv.Advisories = append(fv.Advisories, f.ID)
		for _, a := range f.Aliases {
			if !slices.Contains(fv.Aliases, a) {
				fv.Aliases = append(fv.Aliases, a)
			}
		}
		fv.Malicious = fv.Malicious || f.Malicious
		if fv.Severity == "" || rank(f.Severity) < rank(fv.Severity) {
			fv.Severity = f.Severity
		}
		if len(f.Fixed) == 0 {
			fixedAll = false
			continue
		}
		if c, ok := versions.Compare(eco, f.Fixed[0], fv.FixedIn); fv.FixedIn == "" || ok && c > 0 {
			fv.FixedIn = f.Fixed[0]
		}
	}
	if !fixedAll || fv.Malicious {
		fv.FixedIn = "" // malware: remove it, an update is not the answer
	}
	return fv
}
