package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/psewar/vordr/internal/kev"
	"github.com/psewar/vordr/internal/osv"
	"github.com/psewar/vordr/internal/wire"
)

// Known exploitation (ADR-0168): with a kev section in hub.yaml the hub fetches the whole KEV catalog, keeps it on disk
// and marks every pip/npm finding with a CVE the catalog lists. It is a second data set beside the OSV dumps, fetched
// the same way (whole, conditional, kept when a fetch fails), and nothing of a host goes along.

// KEVConfig is the kev section of hub.yaml. Without it the hub fetches no catalog and marks nothing.
type KEVConfig struct {
	URL          string `yaml:"url"`           // the JSON feed; default CISA's, a mirror of it in the same format
	RefreshHours int    `yaml:"refresh_hours"` // default 24
	// StaleHours: a catalog not confirmed for this long (refresh keeps failing) shows as stale; default 72.
	StaleHours int `yaml:"stale_hours"`
}

const (
	defaultKEVURL       = "https://www.cisa.gov/sites/default/files/feeds/known_exploited_vulnerabilities.json"
	defaultKEVRefreshH  = 24
	defaultKEVStaleH    = 72
	kevDownloadDeadline = 5 * time.Minute
	kevMinShare         = osvMinShare // a new catalog needs this share of the entries in use (ADR-0085's rule)
)

func (k *KEVConfig) applyDefaults() {
	if k.URL == "" {
		k.URL = defaultKEVURL
	}
	if k.RefreshHours == 0 {
		k.RefreshHours = defaultKEVRefreshH
	}
	if k.StaleHours == 0 {
		k.StaleHours = defaultKEVStaleH
	}
}

// check: osvOn says whether hub.yaml has an osv section; the catalog marks OSV findings, so without it there is nothing
// to mark and the hub would fetch it for nothing.
func (k *KEVConfig) check(osvOn bool) []string {
	var out []string
	if !osvOn {
		out = append(out, "kev: needs the osv section - the catalog marks the findings of the OSV data, without it "+
			"there is nothing to mark; add osv or delete kev")
	}
	if u, err := url.Parse(k.URL); err != nil || u.Scheme != "https" || u.Host == "" {
		out = append(out, "kev.url: must be an https:// URL")
	}
	if k.RefreshHours < 1 || k.RefreshHours > 168 {
		out = append(out, "kev.refresh_hours: must be between 1 and 168")
	}
	if k.StaleHours < k.RefreshHours || k.StaleHours > 720 {
		out = append(out, "kev.stale_hours: must be at least refresh_hours and at most 720")
	}
	return out
}

// The codes of a failed catalog refresh (wire.KEVView.Code); the states are those of the OSV data (OSVOk, OSVStale,
// OSVMissing).
const (
	KEVDownloadFailed = OSVDownloadFailed  // no answer, not 200/304, or larger than kev.MaxFeedSize
	KEVBadFeed        = "bad_feed"         // not the catalog: no JSON, a field missing, count and entries differ
	KEVStoreFailed    = OSVStoreFailed     // the catalog could not be written
	KEVImplausible    = "implausible_feed" // no entries, or far fewer than the catalog in use
)

// kevMeta is what the hub remembers about its catalog, next to it.
type kevMeta struct {
	ETag         string    `json:"etag,omitempty"`
	LastModified string    `json:"last_modified,omitempty"`
	Checked      time.Time `json:"checked"` // last download, or last answer "not modified"
	Code         string    `json:"code,omitempty"`
	Failed       time.Time `json:"failed,omitempty"`
}

// KEV keeps the hub's copy of the catalog.
type KEV struct {
	dir    string
	cfg    KEVConfig
	client *http.Client
	now    func() time.Time
	log    *log.Logger

	mu   sync.Mutex
	cat  *kev.Catalog // nil: none yet
	meta kevMeta
}

// NewKEV loads the catalog kept in <stateDir>/kev; a missing or unreadable one is fetched on the first refresh.
func NewKEV(stateDir string, cfg KEVConfig, now func() time.Time, logger *log.Logger) (*KEV, error) {
	k := &KEV{dir: filepath.Join(stateDir, "kev"), cfg: cfg, client: &http.Client{Timeout: kevDownloadDeadline},
		now: now, log: logger}
	if err := os.MkdirAll(k.dir, 0o700); err != nil {
		return nil, err
	}
	if raw, err := os.ReadFile(k.metaPath()); err == nil {
		_ = json.Unmarshal(raw, &k.meta) // a broken file only costs one download
	}
	if _, err := os.Stat(k.catalogPath()); errors.Is(err, fs.ErrNotExist) {
		k.meta.Checked, k.meta.ETag, k.meta.LastModified = time.Time{}, "", "" // fetch at once, whatever the meta says
		return k, nil
	}
	raw, err := os.ReadFile(k.catalogPath())
	if err != nil {
		return nil, err
	}
	cat, err := kev.Parse(raw)
	if err != nil {
		logger.Printf("kev: kept catalog unreadable, fetching anew: %v", err)
		k.meta.Checked, k.meta.ETag, k.meta.LastModified = time.Time{}, "", ""
		return k, nil
	}
	k.cat = cat
	logger.Printf("kev: %s", cat)
	return k, nil
}

func (k *KEV) catalogPath() string { return filepath.Join(k.dir, "catalog.json") }
func (k *KEV) metaPath() string    { return filepath.Join(k.dir, "meta.json") }

// Catalog is the catalog in use; nil while there is none. Stale data is still used: an entry stays a fact, only newer
// entries may be missing, and the view says so.
func (k *KEV) Catalog() *kev.Catalog {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.cat
}

// Run refreshes the catalog when due, until ctx ends; changed is called whenever the catalog or its state code changes.
func (k *KEV) Run(ctx context.Context, changed func()) {
	for {
		if k.due() && k.Refresh(ctx) {
			changed()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(osvTick):
		}
	}
}

func (k *KEV) due() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	now := k.now()
	if k.meta.Code != "" && now.Sub(k.meta.Failed) < osvRetry {
		return false
	}
	return now.Sub(k.meta.Checked) >= time.Duration(k.cfg.RefreshHours)*time.Hour
}

// kevFetch is the outcome of one request: a code when it failed, nil cat when the catalog has not changed.
type kevFetch struct {
	code               string
	err                error
	cat                *kev.Catalog
	etag, lastModified string
}

// Refresh fetches the catalog unless it is unchanged and reports whether the catalog or the state code changed. A
// failure keeps the catalog in use and is remembered with a code; it shows in the hub view.
func (k *KEV) Refresh(ctx context.Context) bool {
	k.mu.Lock()
	old, codeBefore := k.cat, k.meta.Code
	// The validators exist only together with a catalog: Refresh sets them with one, NewKEV clears them without one.
	etag, lastModified := k.meta.ETag, k.meta.LastModified
	k.mu.Unlock()
	f := k.fetch(ctx, old, etag, lastModified)
	k.mu.Lock()
	m, now := &k.meta, k.now()
	switch {
	case f.code != "":
		m.Code, m.Failed = f.code, now
		k.log.Printf("kev: %s: %v", f.code, f.err)
	case f.cat == nil: // not modified
		m.Code, m.Checked = "", now
	default:
		m.Code, m.Checked, m.ETag, m.LastModified = "", now, f.etag, f.lastModified
		k.cat = f.cat
		k.log.Printf("kev: %s", f.cat)
	}
	changed := f.cat != nil || m.Code != codeBefore
	raw, _ := json.Marshal(m)
	k.mu.Unlock()
	if err := writeFileAtomic(k.metaPath(), raw); err != nil {
		k.log.Printf("kev: %v", err)
	}
	return changed
}

// fetch asks with both validators of the last answer: servers and mirrors honour one or the other.
func (k *KEV) fetch(ctx context.Context, old *kev.Catalog, etag, lastModified string) kevFetch {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, k.cfg.URL, nil)
	if err != nil {
		return kevFetch{code: KEVDownloadFailed, err: err}
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if lastModified != "" {
		req.Header.Set("If-Modified-Since", lastModified)
	}
	resp, err := k.client.Do(req)
	if err != nil {
		return kevFetch{code: KEVDownloadFailed, err: err}
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotModified && old != nil:
		return kevFetch{}
	case resp.StatusCode != http.StatusOK:
		return kevFetch{code: KEVDownloadFailed, err: fmt.Errorf("HTTP %d", resp.StatusCode)}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, kev.MaxFeedSize+1))
	switch {
	case err != nil:
		return kevFetch{code: KEVDownloadFailed, err: err}
	case len(raw) > kev.MaxFeedSize:
		return kevFetch{code: KEVDownloadFailed, err: fmt.Errorf("feed larger than %d bytes", kev.MaxFeedSize)}
	}
	cat, err := kev.Parse(raw)
	if err != nil {
		return kevFetch{code: KEVBadFeed, err: err}
	}
	if why := implausibleCatalog(cat, old); why != "" {
		return kevFetch{code: KEVImplausible, err: errors.New(why)}
	}
	if err := writeFileAtomic(k.catalogPath(), raw); err != nil {
		return kevFetch{code: KEVStoreFailed, err: err}
	}
	return kevFetch{cat: cat, etag: resp.Header.Get("ETag"), lastModified: resp.Header.Get("Last-Modified")}
}

// implausibleCatalog says why a new catalog must not replace the one in use: no entries (a changed format leaves every
// entry unreadable), or fewer than kevMinShare percent of the entries in use. CISA removes an entry only rarely, so a
// sharp drop is a broken or partial publication; the catalog in use keeps marking and goes stale instead.
func implausibleCatalog(cat, old *kev.Catalog) string {
	switch {
	case len(cat.Entries) == 0:
		return fmt.Sprintf("the catalog has no readable entries (%d unreadable)", cat.Skipped)
	case old != nil && len(cat.Entries)*100 < len(old.Entries)*kevMinShare:
		return fmt.Sprintf("%d entries, the catalog in use has %d", len(cat.Entries), len(old.Entries))
	}
	return ""
}

// View is the state of the catalog for Home Assistant.
func (k *KEV) View() *wire.KEVView {
	k.mu.Lock()
	defer k.mu.Unlock()
	v := &wire.KEVView{State: OSVMissing, Code: k.meta.Code}
	if k.cat != nil {
		v.State = OSVOk
		if k.now().Sub(k.meta.Checked) > time.Duration(k.cfg.StaleHours)*time.Hour {
			v.State = OSVStale
		}
		checked, released := k.meta.Checked, k.cat.Released
		v.Checked, v.Released = &checked, &released
		v.CatalogVersion, v.Entries = k.cat.Version, len(k.cat.Entries)
	}
	return v
}

// markKnownExploited marks a finding whose advisories name a CVE the catalog lists, from the CVE ids of each advisory
// (its id, aliases and upstream, osv.Finding.CVEs).
func markKnownExploited(fv *wire.FindingView, found []osv.Finding, cat *kev.Catalog) {
	var cves []string
	for _, f := range found {
		cves = append(cves, f.CVEs()...)
	}
	m := cat.Match(cves)
	if !m.Listed() {
		return
	}
	fv.KnownExploited, fv.KEVCVEs, fv.KEVAdded, fv.KEVRansomware = true, m.CVEs, m.Added, m.Ransomware
}
