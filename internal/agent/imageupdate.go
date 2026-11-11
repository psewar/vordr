package agent

import (
	"context"
	"path/filepath"
	"time"

	"github.com/psewar/vordr/internal/images"
	"github.com/psewar/vordr/internal/policy"
	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/runner"
	"github.com/psewar/vordr/internal/source"
	"github.com/psewar/vordr/internal/wire"
)

const (
	// imageSettle: how long a recreated service may take to run steadily, and to be healthy where it has a health
	// check; imageStable: how long it must then run without a restart (a container that misses a new setting dies
	// within seconds and is restarted by its policy); imageChangeBudget: what follows the journal for one service.
	imageSettle       = 3 * time.Minute
	imageStable       = 30 * time.Second
	imageEvery        = 5 * time.Second
	imageChangeBudget = 30 * time.Minute
)

// ImageUpdater moves Compose services to the rebuild of their tag (images.Updater).
type ImageUpdater interface {
	Update(ctx context.Context, scope protocol.Scope, h images.UpdateHooks) images.UpdateResult
}

// NewImageUpdates returns what moves Compose services to the rebuild of their tag by button (ADR-0119), or nil when
// install.allowed_scopes names no service or the agent does not read the running containers (packages.containers).
func NewImageUpdates(cfg policy.Config) ImageUpdater {
	if len(cfg.Install.ImagePatterns()) == 0 || !cfg.Packages.Containers {
		return nil // a nil interface, not a nil *images.Updater in one: the client then announces no install for it
	}
	// The inventory's cache of the registries' answers, only read here: the builds it knows cost no download, and
	// anonymous pulls are counted (ADR-0113). Only the inventory writes it.
	reg := images.NewRegistry(filepath.Join(cfg.StateDir, imageCacheFile))
	return &images.Updater{Engine: images.NewEngine(containerSocket), Reg: reg, Compose: runner.Exec{}, Now: time.Now,
		MinAge: cfg.Packages.MinAge(), Free: images.FreeBytes, Settle: imageSettle, Stable: imageStable,
		Every: imageEvery, ChangeBudget: imageChangeBudget,
		Allowed: func(s images.Service) bool { return cfg.Install.AllowsImage(s.Project, s.Service) }}
}

// imageRun is install with an image scope (ADR-0119): the policy's image entries, the window, one run at a time,
// then the updater. Like a pip/npm run it is not held back by install.min_interval_h; once it changes something it
// counts as an install run like every other.
func (c *Client) imageRun(j protocol.Job, scope protocol.Scope) (code protocol.Code, detail string,
	summary *wire.RunSummary, journaled bool) {
	in := c.Cfg.Install
	u := c.Images
	switch {
	case u == nil:
		return protocol.CodeRejectedNotAllowed, "install.allowed_scopes names no image:<project>/<service>", nil, false
	case scope.Kind == protocol.ScopeImage && !in.AllowsImage(scope.Project, scope.Service):
		return protocol.CodeRejectedScopeNotAllowed, "", nil, false
	case c.windowClosed(in.Window, j):
		return protocol.CodeRejectedOutsideWindow, "", nil, false
	}
	if !c.checkMu.TryLock() {
		return protocol.CodeRejectedBusy, "", nil, false
	}
	defer c.checkMu.Unlock()
	progress := c.progressSender(j.ID)
	root := c.root()
	ctx, cancel := context.WithTimeout(root, time.Duration(in.TimeoutMin)*time.Minute)
	defer cancel()
	start := c.Now()
	res := u.Update(ctx, scope, images.UpdateHooks{
		BeforeChange: func() error {
			err := c.startJournal(j, nil)
			journaled = err == nil
			return err
		},
		Health: func(ctx context.Context) string {
			if c.Health == nil {
				return ""
			}
			return c.Health.Settle(ctx)
		},
		Progress: func(phase protocol.Phase, pct int) { progress(source.Progress{Phase: phase, Percent: pct}) },
	})
	code, detail = res.Code, res.Detail
	after := context.WithoutCancel(root)
	if root.Err() != nil && code != protocol.CodeOK {
		code = protocol.CodeInterrupted
	}
	if journaled {
		summary = &wire.RunSummary{PackageCount: len(res.Changed), Seconds: int(c.Now().Sub(start).Seconds()),
			Packages: res.Changed[:min(len(res.Changed), wire.MaxSummaryPackages)]}
		summary.LogFile = c.writeRunLog(j.ID, res.Log)
		c.Log.Printf("job %s install %s: %v, log %s", j.ID, j.Scope, res.Changed, summary.LogFile)
	}
	if detail != "" {
		c.Log.Printf("job %s install %s: %s", j.ID, j.Scope, detail)
	}
	c.checkLocked(after, sender(c.sendNow)) // the new images reach Home Assistant before the result
	return code, detail, summary, journaled
}
