package agent

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/psewar/vordr/internal/inventory"
	"github.com/psewar/vordr/internal/policy"
	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/source"
	"github.com/psewar/vordr/internal/versions"
	"github.com/psewar/vordr/internal/wire"
)

// PackageUpdates carries out pip/npm updates by button (design 11.4) for the locations packages.update names.
type PackageUpdates struct {
	Cfg     policy.Packages
	Exec    inventory.CommandRunner
	Reg     inventory.Registries
	Collect func() []inventory.Location // the locations as the inventory reads them now
	TempDir string

	mu      sync.Mutex
	waiting map[string]chan protocol.SignedVet // job id -> where its vet result goes
}

// vetTimeout: how long a run waits for the hub's answer about its plan.
const vetTimeout = time.Minute

// NewPackageUpdates returns nil when agent.yaml allows no package updates (packages.update empty or inventory off).
func NewPackageUpdates(cfg policy.Packages) *PackageUpdates {
	if len(cfg.Update) == 0 || !cfg.InventoryOn() {
		return nil
	}
	var configured []inventory.Configured
	for _, l := range cfg.Locations {
		configured = append(configured, inventory.Configured{ID: l.ID, Path: l.Path})
	}
	return &PackageUpdates{Cfg: cfg, Exec: inventory.Exec{},
		Reg: inventory.Registries{PyPI: cfg.Registries.PyPI, Npm: cfg.Registries.Npm, Client: &http.Client{Timeout: time.Minute}},
		Collect: func() []inventory.Location {
			return inventory.Collect(inventory.Options{Detect: cfg.DetectOn(), Configured: configured})
		}}
}

// PlanUpdate shows what an update of a location (or one package of it) would install, without the policy's
// packages.update, without the hub and without changing anything (vordr-agent packages --plan).
func PlanUpdate(ctx context.Context, cfg policy.Packages, location, name string) inventory.UpdateResult {
	p := NewPackageUpdates(policy.Packages{Locations: cfg.Locations, Detect: cfg.Detect, Registries: cfg.Registries,
		Update: []string{location}})
	for _, l := range p.Collect() {
		if l.ID == location {
			u := &inventory.Updater{Exec: p.Exec, Reg: p.Reg, Now: time.Now, MinAge: cfg.MinAge(),
				Scripts: policy.Matches(cfg.AllowScripts, l.ID), Sdist: policy.Matches(cfg.AllowSdist, l.ID)}
			return u.Plan(ctx, l, name)
		}
	}
	return inventory.UpdateResult{Code: protocol.CodeRejectedScopeNotAllowed, Detail: "no location " + location}
}

// PlanDeps shows what refreshing the named dependencies of a location would install (ADR-0104), like PlanUpdate
// without the hub, which would name them (vordr-agent packages --plan-deps).
func PlanDeps(ctx context.Context, cfg policy.Packages, location string, names []string) inventory.UpdateResult {
	p := NewPackageUpdates(policy.Packages{Locations: cfg.Locations, Detect: cfg.Detect, Registries: cfg.Registries,
		Update: []string{location}})
	for _, l := range p.Collect() {
		if l.ID == location {
			u := &inventory.Updater{Exec: p.Exec, Reg: p.Reg, Now: time.Now, MinAge: cfg.MinAge(),
				Scripts: policy.Matches(cfg.AllowScripts, l.ID)}
			return u.PlanDeps(ctx, l, names)
		}
	}
	return inventory.UpdateResult{Code: protocol.CodeRejectedScopeNotAllowed, Detail: "no location " + location}
}

// allows reports whether the policy lets Home Assistant update this location, and the agent can.
func (p *PackageUpdates) allows(loc inventory.Location) bool {
	return p != nil && policy.Matches(p.Cfg.Update, loc.ID) && inventory.Updatable(loc.Kind)
}

// deliver hands a vet result to the run that waits for it; one nobody waits for is dropped.
func (p *PackageUpdates) deliver(r wire.VetResult) {
	p.mu.Lock()
	ch := p.waiting[r.JobID]
	p.mu.Unlock()
	if ch != nil {
		select {
		case ch <- r.Vet:
		default:
		}
	}
}

// vetter asks the hub about a plan over the current session and checks the signed answer: right job, right node,
// fresh, from the pinned hub key.
func (c *Client) vetter(jobID string) inventory.Vetter {
	return func(ctx context.Context, eco versions.Ecosystem, plan []protocol.VetPackage) (protocol.Vet, bool) {
		p := c.Packages
		ch := make(chan protocol.SignedVet, 1)
		p.mu.Lock()
		if p.waiting == nil {
			p.waiting = map[string]chan protocol.SignedVet{}
		}
		p.waiting[jobID] = ch
		p.mu.Unlock()
		defer func() {
			p.mu.Lock()
			delete(p.waiting, jobID)
			p.mu.Unlock()
		}()
		if len(plan) > wire.MaxVetPackages || c.sendNow(wire.Vet{T: wire.TVet, JobID: jobID, Ecosystem: string(eco),
			Packages: plan}) != nil {
			return protocol.Vet{}, false
		}
		select {
		case sv := <-ch:
			return protocol.VerifyVet(sv, c.Cfg.HubKey(), c.Cfg.Node, jobID, c.Now())
		case <-ctx.Done():
		case <-time.After(vetTimeout):
		}
		return protocol.Vet{}, false
	}
}

// packageRun is install with a pkg scope (design 11.4) or a deps scope (ADR-0104): policy, window, a hub that vets,
// then the updater.
func (c *Client) packageRun(j protocol.Job, scope protocol.Scope) (code protocol.Code, detail string,
	summary *wire.RunSummary, journaled bool) {
	in := c.Cfg.Install
	p := c.Packages
	switch {
	case p == nil:
		return protocol.CodeRejectedNotAllowed, "packages.update names no location", nil, false
	case !policy.Matches(p.Cfg.Update, scope.Location):
		return protocol.CodeRejectedScopeNotAllowed, "", nil, false
	case c.windowClosed(in.Window, j):
		return protocol.CodeRejectedOutsideWindow, "", nil, false
	case !c.hubAccepts(wire.FeatureVet):
		return protocol.CodePrecheckVettingUnavailable, "the hub does not vet package updates (older hub)", nil, false
	}
	if !c.checkMu.TryLock() {
		return protocol.CodeRejectedBusy, "", nil, false
	}
	defer c.checkMu.Unlock()
	var loc *inventory.Location
	for _, l := range p.Collect() {
		if l.ID == scope.Location {
			loc = &l
			break
		}
	}
	if loc == nil || !p.allows(*loc) {
		return protocol.CodeRejectedScopeNotAllowed, "no updatable location " + scope.Location, nil, false
	}
	progress := c.progressSender(j.ID)
	progress(source.Progress{Phase: protocol.PhasePrechecking, Percent: 5})
	root := c.root()
	ctx, cancel := context.WithTimeout(root, time.Duration(in.TimeoutMin)*time.Minute)
	defer cancel()
	u := &inventory.Updater{Exec: p.Exec, Reg: p.Reg, Now: c.Now, MinAge: p.Cfg.MinAge(), Vet: c.vetter(j.ID),
		Scripts: policy.Matches(p.Cfg.AllowScripts, loc.ID), Sdist: policy.Matches(p.Cfg.AllowSdist, loc.ID),
		TempDir: p.TempDir, SecurityAge: p.Cfg.MinAgeSecurity(), SecurityFix: j.SecurityFix}
	if len(j.SecurityFix) > 0 {
		c.Log.Printf("job %s: the hub names fixes for the shorter minimum age (%d days): %s", j.ID,
			p.Cfg.MinAgeDaysSecurity, strings.Join(j.SecurityFix, ", "))
	}
	start := c.Now()
	before := func() error {
		err := c.startJournal(j, nil)
		journaled = err == nil
		progress(source.Progress{Phase: protocol.PhaseInstalling, Percent: 50})
		return err
	}
	var res inventory.UpdateResult
	if scope.Kind == protocol.ScopeDeps {
		res = u.RefreshDeps(ctx, *loc, before)
	} else {
		res = u.Update(ctx, *loc, scope.Package, before)
	}
	code, detail = res.Code, res.Detail
	after := context.WithoutCancel(root)
	if root.Err() != nil && code != protocol.CodeOK {
		code = protocol.CodeInterrupted
	}
	if journaled && c.Health != nil && code == protocol.CodeOK {
		progress(source.Progress{Phase: protocol.PhaseVerifying, Percent: 97})
		if failed := c.Health.Settle(after); failed != "" {
			code, detail = protocol.CodeFailedHealth, "health: "+failed
		}
	}
	if journaled {
		summary = &wire.RunSummary{PackageCount: len(res.Changed), Seconds: int(c.Now().Sub(start).Seconds()),
			Packages: res.Changed[:min(len(res.Changed), wire.MaxSummaryPackages)]}
		summary.LogFile = c.writeRunLog(j.ID, res.Log)
		c.Log.Printf("job %s install %s: %v", j.ID, j.Scope, res.Changed)
	}
	c.checkLocked(after, sender(c.sendNow)) // the new versions reach Home Assistant before the result
	return code, detail, summary, journaled
}
