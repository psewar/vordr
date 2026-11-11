package agent

import (
	"context"
	"time"

	"github.com/psewar/vordr/internal/facts"
	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/source"
	"github.com/psewar/vordr/internal/target"
	"github.com/psewar/vordr/internal/wire"
)

// ConfigBackupSource is the name of the report entry that says how fresh the appliance's configuration backup is.
const ConfigBackupSource = source.ConfigBackup

// TargetChecker reports on an appliance through its driver (design/proxy-agent.md) and keeps its configuration
// backup fresh (ADR-0090): with every check it exports again once the newest good export is older than maxAge less a
// margin, so a daily check keeps it inside maxAge.
func TargetChecker(d target.Driver, store target.Store, maxAge time.Duration, now func() time.Time) Checker {
	return targetChecker{d: d, store: store, maxAge: maxAge, now: now}
}

type targetChecker struct {
	d      target.Driver
	store  target.Store
	maxAge time.Duration
	now    func() time.Time
}

// backupMargin: an export this much younger than max_age_h is still fresh enough; older ones are renewed.
const backupMargin = 2 * time.Hour

func (c targetChecker) Check(ctx context.Context) wire.Report {
	at := c.now().UTC()
	st, res := c.d.Check(ctx)
	fw := source.Report{Source: c.d.Name(), CheckedAt: at, Facts: map[string]string{}}
	for k, v := range st.Facts {
		fw.Facts[k] = v
	}
	if st.SelfUpdate != "" {
		fw.Facts["self_update"] = st.SelfUpdate
	}
	if st.Model != "" {
		fw.Facts["model"] = st.Model
	}
	switch {
	case !res.OK():
		fw.State, fw.Code, fw.Detail = protocol.SourceCheckFailed, res.Code, res.Detail
	case len(st.Offers) == 0:
		fw.State = protocol.SourceUpToDate
	default:
		fw.State, fw.Pending = protocol.SourceUpdatesAvailable, len(st.Offers)
		for _, o := range st.Offers {
			if o.Security {
				fw.Security++
			}
			fw.Packages = append(fw.Packages, source.Package{Name: o.Name, From: st.Installed, To: o.Version,
				Security: o.Security})
		}
	}
	return wire.Report{T: wire.TReport, CheckedAt: at, Sources: []source.Report{fw, c.backup(ctx, at, res)}}
}

// backup renews the export when due and reports how fresh the newest good one is. When the device could not be
// checked (unreachable, login refused, certificate), an export would fail the same way: it is not tried then.
func (c targetChecker) backup(ctx context.Context, at time.Time, checked target.Result) source.Report {
	rep := source.Report{Source: ConfigBackupSource, CheckedAt: at, Facts: map[string]string{}}
	last, have := c.store.Last()
	why := ""
	renew := !have || at.Sub(last.At) > c.maxAge-backupMargin
	switch {
	case !renew:
	case !checked.OK():
		why = "device not reachable for an export: " + string(checked.Code)
	default:
		data, ext, res := c.d.Backup(ctx)
		if !res.OK() {
			why = res.Detail
			if res.Code != protocol.CodeFailedConfigBackup {
				why = string(res.Code) + ": " + res.Detail
			}
			break
		}
		saved, err := c.store.Save(c.d.Name(), data, ext, at)
		if err != nil {
			why = "storing the export: " + err.Error()
			break
		}
		last, have = saved, true
	}
	if have {
		rep.Facts[source.FactConfigBackupAt] = last.At.Format(time.RFC3339)
		rep.Facts[source.FactConfigBackupSH] = last.SHA256
	}
	switch {
	case have && why == "":
		// Fresh by construction: no renewal was due (younger than max_age less the margin), or one just worked.
		rep.State = protocol.SourceUpToDate
	case have && at.Sub(last.At) <= c.maxAge:
		// The renewal failed, but the last export is still within max_age: fresh enough, the failure shows in Detail.
		rep.State, rep.Detail = protocol.SourceUpToDate, "renewal failed: "+why
	default:
		if why == "" {
			why = "no export yet"
		}
		rep.State, rep.Code, rep.Detail = protocol.SourceCheckFailed, protocol.CodeFailedConfigBackup, why
	}
	return rep
}

// TargetFacts are the appliance's facts for the hello: product and firmware as the device names them, the driver as
// the system id. When the device does not answer at start, the driver's name stands in until the next start.
func TargetFacts(ctx context.Context, node string, d target.Driver) facts.Host {
	h := facts.Host{Hostname: node, OS: d.Name(), OSID: d.Name(), OSVersion: facts.Unknown, GOOS: facts.Unknown,
		Arch: facts.Unknown, Virtualization: "none", Container: "none"}
	if st, res := d.Check(ctx); res.OK() {
		h.OS, h.OSVersion = st.Product+" "+st.Installed, st.Installed
		if st.Model != "" {
			h.Arch = st.Model
		}
	}
	return h
}
