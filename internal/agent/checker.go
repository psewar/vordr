// Package agent is the agent side of the tunnel: it connects to the hub, proves its key, waits for approval,
// reports, and carries out signed jobs within its local policy.
package agent

import (
	"context"
	"time"

	"github.com/psewar/vordr/internal/facts"
	"github.com/psewar/vordr/internal/source"
	"github.com/psewar/vordr/internal/source/apt"
	"github.com/psewar/vordr/internal/source/winget"
	"github.com/psewar/vordr/internal/source/winupdate"
	"github.com/psewar/vordr/internal/wire"
)

// SystemInstaller returns the installer for this host, or nil when it cannot install: apt only as root (it changes
// packages only then); on Windows Windows Update and winget together (windowsInstaller; elevated for everything,
// otherwise only the apps agent.yaml names). An agent without an installer does not announce install.
func SystemInstaller(host facts.Host) Installer {
	if a := apt.New(); a.Detect() == source.Present {
		if !a.IsRoot {
			return nil
		}
		return newAptInstaller(a, host)
	}
	if w := winget.New(); w.Detect() == source.Present {
		i := windowsInstaller{winget: w, pending: w.RebootRequired}
		if u := winupdate.New(); u.Detect() == source.Present {
			i.update = u
		}
		return i
	}
	return nil
}

type aptInstaller struct {
	a    *apt.Apt
	host facts.Host
}

// newAptInstaller gives apt the host's release: a security run takes dependencies from it (ADR-0116).
func newAptInstaller(a *apt.Apt, host facts.Host) aptInstaller {
	if host.OSVersion != facts.Unknown {
		a.Release = host.OSVersion
	}
	return aptInstaller{a: a, host: host}
}

func (i aptInstaller) Install(ctx context.Context, req source.InstallRequest, installTimeout time.Duration,
	progress func(source.Progress)) source.InstallResult {
	return i.a.Install(ctx, req, installTimeout, progress)
}

func (i aptInstaller) RebootRequired(ctx context.Context) bool {
	return i.a.Reboot(ctx, i.host.InContainer()).Required
}

// Checker checks every update source of the host once.
type Checker interface {
	Check(ctx context.Context) wire.Report
}

// System returns the checker for the real sources of this host. refresh: refresh package lists (apt only does it
// as root and with its lock free; winget keeps its catalog fresh itself). mode: install.mode of the local policy,
// which apt reports as the fact install_mode (ADR-0095).
func System(host facts.Host, refresh bool, mode string) Checker {
	return systemChecker{host: host, refresh: refresh, mode: mode}
}

type systemChecker struct {
	host    facts.Host
	refresh bool
	mode    string
}

// aptSource is the apt source as this host's policy configures it.
func (c systemChecker) aptSource() *apt.Apt {
	a := apt.New()
	a.PolicyMode = c.mode
	return a
}

func (c systemChecker) Check(ctx context.Context) wire.Report {
	a := c.aptSource()
	rep := wire.Report{T: wire.TReport, CheckedAt: a.Now().UTC(), Sources: []source.Report{}}
	if a.Detect() == source.Present {
		rep.Sources = append(rep.Sources, a.Check(ctx, c.refresh))
		r := a.Reboot(ctx, c.host.InContainer())
		rep.Reboot = &r
	}
	// Windows: Windows Update first (the system and its security updates), then the programs winget upgrades.
	if u := winupdate.New(); u.Detect() == source.Present {
		rep.Sources = append(rep.Sources, u.Check(ctx))
	}
	if w := winget.New(); w.Detect() == source.Present {
		rep.Sources = append(rep.Sources, w.Check(ctx))
		r := w.Reboot()
		rep.Reboot = &r
	}
	return rep
}
