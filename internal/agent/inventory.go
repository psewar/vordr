package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/psewar/vordr/internal/images"
	"github.com/psewar/vordr/internal/inventory"
	"github.com/psewar/vordr/internal/policy"
	"github.com/psewar/vordr/internal/wire"
)

// maxRegistryLookups bounds one outdated run (names asked); registryTimeout bounds the whole run.
const (
	maxRegistryLookups = 500
	registryTimeout    = 10 * time.Minute
)

// containerSocket is the engine socket packages.containers reads (tests point it at a fake engine).
var containerSocket = images.DefaultSocket

// imageCacheFile: the image registries' answers that never change (configuration and creation time per digest), in
// the state directory, so that a restarted agent does not download the same manifests again (ADR-0113).
const imageCacheFile = "image-cache.json"

// Inventory returns what reads the packages of pip, pipx and npm as agent.yaml says (design 11.2) and, at most every
// outdated_hours, asks the registries for newer releases (11.3); in between the last answers are reused. With
// containers (the hub announced FeatureContainers) and packages.containers, the images of the running containers
// come along as the location "containers" and are compared with their registries on a schedule of their own
// (ADR-0113). The client sends the result after every check to a hub that announced FeatureInventory. version is the
// agent's own: answers kept on disk by another version are left aside, as it may compute or fill them differently.
// Per image it names the Compose services that install.allowed_scopes lets Home Assistant update (ADR-0119).
func Inventory(full policy.Config, stateDir, version string, logger *log.Logger,
	now func() time.Time) func(ctx context.Context, w Wants) wire.Inventory {
	cfg := full.Packages
	allowed := func(s images.Service) bool { return full.Install.AllowsImage(s.Project, s.Service) }
	var configured []inventory.Configured
	for _, l := range cfg.Locations {
		configured = append(configured, inventory.Configured{ID: l.ID, Path: l.Path})
	}
	reg := inventory.Registries{PyPI: cfg.Registries.PyPI, Npm: cfg.Registries.Npm,
		Client: &http.Client{Timeout: 60 * time.Second}}
	pkgSettings, imgSettings := answerSettings(version, cfg)
	o := &outdatedCache{log: logger, file: answersFile{path: keptIn(stateDir, packageAnswersFile), settings: pkgSettings}}
	var engine images.Engine
	var imgs *imagesCache
	if cfg.Containers {
		engine = images.NewEngine(containerSocket)
		imgs = &imagesCache{reg: images.NewRegistry(filepath.Join(stateDir, imageCacheFile)), log: logger,
			file: answersFile{path: keptIn(stateDir, imageAnswersFile), settings: imgSettings}}
	}
	every := time.Duration(cfg.OutdatedHours) * time.Hour
	return func(ctx context.Context, w Wants) wire.Inventory {
		inv := wire.Inventory{T: wire.TInventory, CheckedAt: now().UTC(), Locations: []inventory.Location{}}
		if !cfg.InventoryOn() {
			return inv
		}
		inv.Locations = inventory.Collect(inventory.Options{Detect: cfg.DetectOn(), Configured: configured, Now: now})
		for i := range inv.Locations {
			l := &inv.Locations[i]
			l.Update = policy.Matches(cfg.Update, l.ID) && inventory.Updatable(l.Kind)
			l.Deps = l.Update && inventory.Refreshable(*l)
		}
		if cfg.OutdatedOn() {
			inv.MinAgeDays, inv.MinAgeDaysSecurity = cfg.MinAgeDays, cfg.MinAgeDaysSecurity
			inv.OutdatedAt = o.apply(ctx, reg, inv.Locations, now(), cfg.MinAge(), cfg.MinAgeSecurity(), every)
		}
		if w.Containers && cfg.Containers {
			running, code := engine.Running(ctx)
			loc := images.Location(running, code, engine.Socket)
			imgs.apply(ctx, &loc, running, now(), cfg.MinAge(), every, cfg.OutdatedOn(), contentsFor(w, engine))
			if len(full.Install.ImagePatterns()) > 0 { // which services the button can move (ADR-0119)
				images.MarkUpdatable(&loc, running, allowed, engine.Movable(ctx))
			}
			inv.Locations = append(inv.Locations, loc)
		}
		fit(&inv, wire.MaxInventory)
		return inv
	}
}

// Wants: what the hub announced it takes beyond pip and npm packages.
type Wants struct {
	Containers    bool // FeatureContainers: the running images (ADR-0113)
	ImageContents bool // FeatureImageContents: the packages inside them (ADR-0120)
}

// contentsFor: what reads the packages inside the images - only for a hub that asked for them (ADR-0120); nil, and
// nothing read, otherwise.
func contentsFor(w Wants, engine images.Engine) func(context.Context, string) inventory.ImageOS {
	if !w.ImageContents {
		return nil
	}
	return engine.Contents
}

// answerSettings: what the kept answers depend on besides the packages - the agent's version (another one may compute
// or fill them differently), the minimum ages and which registries were asked; for the images also whether they were
// compared at all (without outdated only their contents are kept). Kept answers of other settings are left aside.
func answerSettings(version string, cfg policy.Packages) (packages, images string) {
	return fmt.Sprintf("%s %s %s %s %s", version, cfg.MinAge(), cfg.MinAgeSecurity(), cfg.Registries.PyPI,
		cfg.Registries.Npm), fmt.Sprintf("%s %s %t", version, cfg.MinAge(), cfg.OutdatedOn())
}

// keptIn: where a file of the state directory lives; "" without one (nothing is kept).
func keptIn(stateDir, name string) string {
	if stateDir == "" {
		return ""
	}
	return filepath.Join(stateDir, name)
}

// outdatedCache keeps the last answers of the registries per location, name and installed version, also on disk.
type outdatedCache struct {
	mu     sync.Mutex
	file   answersFile
	log    *log.Logger
	loaded bool // the answers on disk were read (once, at the first run)
	asked  time.Time
	result map[[3]string]inventory.Package
	failed map[string][2]int // location -> lookups that failed, packages not in the registry
}

// packageAnswers: the outdated cache as it is kept on disk.
type packageAnswers struct {
	Failed  map[string][2]int `json:"failed"`
	Results []packageAnswer   `json:"results"`
}

type packageAnswer struct {
	Location string            `json:"location"`
	Package  inventory.Package `json:"package"`
}

// load takes the answers kept on disk, once: a restarted agent goes on with them until outdated_hours are up.
func (o *outdatedCache) load(now time.Time) {
	if o.loaded {
		return
	}
	o.loaded = true
	asked, a, ok := loadAnswers[packageAnswers](o.file, now)
	if !ok {
		return
	}
	o.asked, o.failed, o.result = asked, a.Failed, map[[3]string]inventory.Package{}
	if o.failed == nil {
		o.failed = map[string][2]int{}
	}
	for _, r := range a.Results {
		o.result[[3]string{r.Location, r.Package.Name, r.Package.Version}] = r.Package
	}
}

// apply fills Latest and Candidate from the registries when the last run is older than every, else from the cache;
// a package installed since the last run waits for the next one. It returns when the registries were last asked.
func (o *outdatedCache) apply(ctx context.Context, reg inventory.Registries, locs []inventory.Location, now time.Time,
	minAge, securityAge, every time.Duration) *time.Time {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.load(now)
	if o.result == nil || now.Sub(o.asked) >= every {
		rctx, cancel := context.WithTimeout(ctx, registryTimeout)
		inventory.Outdated(rctx, reg, locs, now, minAge, securityAge, maxRegistryLookups)
		cancel()
		o.asked, o.result, o.failed = now, map[[3]string]inventory.Package{}, map[string][2]int{}
		saved := packageAnswers{Failed: o.failed}
		for _, l := range locs {
			o.failed[l.ID] = [2]int{l.OutdatedFailed, l.NotInRegistry}
			for _, p := range l.Packages {
				if p.Direct {
					o.result[[3]string{l.ID, p.Name, p.Version}] = p
					saved.Results = append(saved.Results, packageAnswer{Location: l.ID, Package: p})
				}
			}
		}
		if err := saveAnswers(o.file, now, saved); err != nil && o.log != nil {
			o.log.Printf("registry answers not kept: %v", err)
		}
	} else {
		for i := range locs {
			l := &locs[i]
			l.OutdatedFailed, l.NotInRegistry = o.failed[l.ID][0], o.failed[l.ID][1]
			for j := range l.Packages {
				p := &l.Packages[j]
				if c, ok := o.result[[3]string{l.ID, p.Name, p.Version}]; ok {
					p.Latest, p.Candidate, p.LatestAt, p.Major = c.Latest, c.Candidate, c.LatestAt, c.Major
					p.SecurityCandidate = c.SecurityCandidate
				}
			}
		}
	}
	at := o.asked.UTC()
	return &at
}

// imageChecker compares the images of the container location with their registries (images.Registry).
type imageChecker interface {
	Outdated(ctx context.Context, running []images.Running, loc *inventory.Location, now time.Time,
		minAge time.Duration, budget int)
	Save() error
}

// imagesCache keeps the last comparison of the images, on its own schedule: a run that found no images to compare
// (the engine did not answer, or the hub did not keep images yet) does not hold the next one back for outdated_hours.
type imagesCache struct {
	mu     sync.Mutex
	reg    imageChecker
	log    *log.Logger
	file   answersFile
	loaded bool
	asked  time.Time
	result map[string]inventory.Package // images.Key -> the registries' answers
	failed [2]int                       // outdated_failed, not_in_registry of the last comparison
}

// imageAnswers: the last comparison as it is kept on disk.
type imageAnswers struct {
	Failed  [2]int                       `json:"failed"`
	Results map[string]inventory.Package `json:"results"`
}

// load takes the comparison kept on disk, once.
func (c *imagesCache) load(now time.Time) {
	if c.loaded {
		return
	}
	c.loaded = true
	if asked, a, ok := loadAnswers[imageAnswers](c.file, now); ok && a.Results != nil {
		c.asked, c.result, c.failed = asked, a.Results, a.Failed
	}
}

// apply compares the images (with compare: packages.outdated) when the last comparison is older than every (or there
// was none), else fills in the last answers; an image started since waits for the next one. It sets ComparedAt, and
// leaves it nil when the engine was not read or nothing is compared: then whether an image is outdated is unknown.
//
// contents, when the hub wants them (ADR-0120), reads what an image carries inside, from one of its containers: on the
// same schedule, with or without a comparison, and in between only for an image the cache has none for - one started
// since, or every image once when the hub has just begun to ask; such a read is kept too. nil: not
// read, and none sent.
func (c *imagesCache) apply(ctx context.Context, loc *inventory.Location, running []images.Running, now time.Time,
	minAge, every time.Duration, compare bool, contents func(context.Context, string) inventory.ImageOS) {
	if loc.Code != "" || (!compare && contents == nil) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.load(now)
	if c.result == nil || now.Sub(c.asked) >= every {
		if compare {
			rctx, cancel := context.WithTimeout(ctx, registryTimeout)
			c.reg.Outdated(rctx, running, loc, now, minAge, images.MaxRequests)
			cancel()
			if err := c.reg.Save(); err != nil && c.log != nil {
				c.log.Printf("image cache not saved: %v", err)
			}
		}
		readContents(ctx, loc, running, contents, func(*inventory.Package) bool { return true })
		c.asked, c.result, c.failed = now, map[string]inventory.Package{}, [2]int{loc.OutdatedFailed, loc.NotInRegistry}
		for _, p := range loc.Packages {
			c.result[images.Key(p.Name, p.Version, p.Digest)] = p
		}
		c.save(now)
	} else {
		loc.OutdatedFailed, loc.NotInRegistry = c.failed[0], c.failed[1]
		for j := range loc.Packages {
			p := &loc.Packages[j]
			if r, ok := c.result[images.Key(p.Name, p.Version, p.Digest)]; ok {
				p.Latest, p.Candidate, p.LatestAt, p.Major = r.Latest, r.Candidate, r.LatestAt, r.Major
				p.Rebuilt, p.RebuiltAt, p.RebuiltCandidate = r.Rebuilt, r.RebuiltAt, r.RebuiltCandidate
				p.CompareFailed = r.CompareFailed // why the last comparison got no answer (ADR-0126)
				p.SameMajor = r.SameMajor         // the smaller step beside a new major version (ADR-0131)
				if contents != nil {
					p.OS = r.OS
				}
			}
		}
		read := readContents(ctx, loc, running, contents, func(p *inventory.Package) bool { return p.OS == nil })
		for _, j := range read { // keep it, so the next check does not read it again
			p := loc.Packages[j]
			k := images.Key(p.Name, p.Version, p.Digest)
			r, ok := c.result[k]
			if !ok { // an image started since: not compared yet, only what was read
				r = inventory.Package{Name: p.Name, Version: p.Version, Digest: p.Digest}
			}
			r.OS = p.OS
			c.result[k] = r
		}
		if len(read) > 0 {
			c.save(c.asked)
		}
	}
	if compare {
		at := c.asked.UTC()
		loc.ComparedAt = &at
	}
}

func (c *imagesCache) save(asked time.Time) {
	if err := saveAnswers(c.file, asked, imageAnswers{Failed: c.failed, Results: c.result}); err != nil && c.log != nil {
		c.log.Printf("image answers not kept: %v", err)
	}
}

// readContents fills OS for the images of loc that want a read, from the first container of each; it returns the
// indexes it filled. Nothing without contents.
func readContents(ctx context.Context, loc *inventory.Location, running []images.Running,
	contents func(context.Context, string) inventory.ImageOS, want func(*inventory.Package) bool) []int {
	if contents == nil {
		return nil
	}
	ids := map[string]string{} // images.Key -> one container of the image
	for _, r := range running {
		if k, ok := images.KeyOf(r); ok && ids[k] == "" && r.ContainerID != "" {
			ids[k] = r.ContainerID
		}
	}
	var read []int
	for j := range loc.Packages {
		p := &loc.Packages[j]
		id := ids[images.Key(p.Name, p.Version, p.Digest)]
		if id == "" || !want(p) {
			continue
		}
		os := contents(ctx, id)
		p.OS = &os
		read = append(read, j)
	}
	return read
}

// fit shortens the largest locations until the message stays within limit; each shortened one says so in its code.
// The hub would otherwise drop the whole connection on an oversized message.
//
// Largest by size, not by count: a few images with what they carry inside outweigh hundreds of
// pip packages. In the container location the packages inside the image that carries the most go first; the image and
// its comparison stay.
func fit(inv *wire.Inventory, limit int) {
	for {
		raw, err := json.Marshal(inv)
		if err != nil || len(raw) <= limit {
			return
		}
		largest, size := -1, 0
		for i := range inv.Locations {
			if len(inv.Locations[i].Packages) == 0 {
				continue
			}
			if n := jsonSize(inv.Locations[i]); largest < 0 || n > size {
				largest, size = i, n
			}
		}
		if largest < 0 {
			return
		}
		l := &inv.Locations[largest]
		if dropLargestContents(l) {
			continue
		}
		l.Packages = l.Packages[:len(l.Packages)/2]
		l.Code = inventory.CodeTooManyPackages
	}
}

func jsonSize(v any) int {
	raw, _ := json.Marshal(v)
	return len(raw)
}

// dropLargestContents leaves out the packages inside the image of l that carries the most (ADR-0120), on a copy - the
// cache keeps what was read. Its code says so; the hub does not match it then. false: no image of l carries any.
func dropLargestContents(l *inventory.Location) bool {
	j := -1
	for i, p := range l.Packages {
		if p.OS != nil && len(p.OS.Packages) > 0 && (j < 0 || len(p.OS.Packages) > len(l.Packages[j].OS.Packages)) {
			j = i
		}
	}
	if j < 0 {
		return false
	}
	cut := *l.Packages[j].OS
	cut.Packages, cut.Code = nil, inventory.CodeTooManyPackages
	l.Packages[j].OS = &cut
	return true
}
