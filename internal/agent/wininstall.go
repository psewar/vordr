package agent

import (
	"context"
	"strings"
	"time"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/source"
)

// sourceInstaller is one Windows source's install run (winupdate, winget).
type sourceInstaller interface {
	Install(ctx context.Context, req source.InstallRequest, installTimeout time.Duration,
		progress func(source.Progress)) source.InstallResult
}

// windowsInstaller combines the two Windows sources into one install run (ADR-0086):
//   - security: Windows Update only; winget does not classify updates,
//   - all: Windows Update, then winget,
//   - app:<id>: winget only.
//
// Both parts of all run, whatever the first one ended with: they change different things. The result is the first
// code that is not ok, with the packages and details of both, and ok_reboot_required if either part needs a reboot.
type windowsInstaller struct {
	update  sourceInstaller // Windows Update; nil where there is none
	winget  sourceInstaller
	pending func() bool // what Windows records about a pending reboot
}

type installPart struct {
	name string
	src  sourceInstaller
}

func (i windowsInstaller) parts(scope protocol.Scope) []installPart {
	var parts []installPart
	if i.update != nil && (scope.Kind == protocol.ScopeSecurity || scope.Kind == protocol.ScopeAll) {
		parts = append(parts, installPart{"winupdate", i.update})
	}
	if i.winget != nil && (scope.Kind == protocol.ScopeAll || scope.Kind == protocol.ScopeApp) {
		parts = append(parts, installPart{"winget", i.winget})
	}
	return parts
}

func (i windowsInstaller) Install(ctx context.Context, req source.InstallRequest, installTimeout time.Duration,
	progress func(source.Progress)) source.InstallResult {
	parts := i.parts(req.Scope)
	if len(parts) == 0 {
		return source.InstallResult{Code: protocol.CodeRejectedUnsupported, Detail: "no Windows source installs scope " +
			req.Scope.String()}
	}
	req.BeforeChange = once(req.BeforeChange) // one journal for the whole run, before the first part changes anything
	var out source.InstallResult
	var details []string
	reboot := false
	for n, p := range parts {
		res := p.src.Install(ctx, req, installTimeout, shareProgress(progress, n, len(parts)))
		out.Planned += res.Planned
		out.Packages = append(out.Packages, res.Packages...)
		if len(res.Log) > 0 {
			if len(parts) > 1 {
				out.Log = append(out.Log, "== "+p.name+" ==\n"...)
			}
			out.Log = append(out.Log, res.Log...)
		}
		if res.Detail != "" {
			if len(parts) > 1 {
				res.Detail = p.name + ": " + res.Detail
			}
			details = append(details, res.Detail)
		}
		switch res.Code {
		case protocol.CodeOK:
		case protocol.CodeOKRebootRequired:
			reboot = true
		default:
			if out.Code == "" { // the first failure says best where the run went wrong
				out.Code = res.Code
			}
		}
	}
	out.Detail = strings.Join(details, "\n")
	switch {
	case out.Code != "":
	case reboot:
		out.Code = protocol.CodeOKRebootRequired
	default:
		out.Code = protocol.CodeOK
	}
	return out
}

func (i windowsInstaller) RebootRequired(context.Context) bool { return i.pending() }

// once runs f the first time and hands its answer to every later call.
func once(f func() error) func() error {
	if f == nil {
		return nil
	}
	done := false
	var err error
	return func() error {
		if !done {
			done, err = true, f()
		}
		return err
	}
}

// shareProgress gives part n of total its share of the run's percent.
func shareProgress(progress func(source.Progress), n, total int) func(source.Progress) {
	return func(p source.Progress) {
		p.Percent = (n*100 + p.Percent) / total
		progress(p)
	}
}
