package hub

import (
	"strings"
	"testing"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/source"
	"github.com/psewar/vordr/internal/wire"
)

// ADR-0210: a report whose checked source names os and os_version changes the host's system - its facts, the view's
// installed_version and the device's model; the hub keeps it. A failed source, a value beyond the bound of the hello's
// facts, one of the two alone or a report without them changes nothing.
func TestReportTakesTheSystem(t *testing.T) {
	f := newViewFixture(t)
	f.host(t, "fw", []protocol.Verb{protocol.VerbCheck}, wire.PolicySummary{}, true)
	report := func(state protocol.SourceState, facts map[string]string) wire.Report {
		return wire.Report{T: wire.TReport, CheckedAt: f.now,
			Sources: []source.Report{{Source: "device", State: state, CheckedAt: f.now, Facts: facts}}}
	}
	system := func() (string, string, string, string) {
		t.Helper()
		e, _ := f.srv.Reg.Get("fw")
		v := f.view(t, "fw", f.now)
		installed := "" // no system view before the first report
		if v.System != nil {
			installed = v.System.InstalledVersion
		}
		return e.Facts.OS, e.Facts.OSVersion, installed, v.Device.Model
	}
	if os, version, _, model := system(); os != "TestOS 1" || version != "" || model != "TestOS 1 · lxc" {
		t.Fatalf("from the hello: %q %q %q", os, version, model)
	}

	if err := f.srv.Reg.SetReport("fw", report(protocol.SourceUpToDate,
		map[string]string{source.FactOS: "Device 2.0-RELEASE", source.FactOSVersion: "2.0-RELEASE"})); err != nil {
		t.Fatal(err)
	}
	if os, version, installed, model := system(); os != "Device 2.0-RELEASE" || version != "2.0-RELEASE" ||
		installed != "Device 2.0-RELEASE" || model != "Device 2.0-RELEASE · lxc" {
		t.Fatalf("after the report: %q %q %q %q", os, version, installed, model)
	}
	reopened, err := OpenRegistry(f.srv.Reg.path)
	if err != nil {
		t.Fatal(err)
	}
	if e, _ := reopened.Get("fw"); e.Facts.OS != "Device 2.0-RELEASE" {
		t.Errorf("not kept: %+v", e.Facts)
	}

	for name, rep := range map[string]wire.Report{
		"failed check": report(protocol.SourceCheckFailed,
			map[string]string{source.FactOS: "Device 3.0", source.FactOSVersion: "3.0"}),
		"beyond the bound": report(protocol.SourceUpToDate,
			map[string]string{source.FactOS: "Device " + strings.Repeat("3", maxFactLen), source.FactOSVersion: "3.0"}),
		"control characters": report(protocol.SourceUpToDate,
			map[string]string{source.FactOS: "Device 3.0\n", source.FactOSVersion: "3.0"}),
		"os alone":         report(protocol.SourceUpToDate, map[string]string{source.FactOS: "Device 3.0"}),
		"os_version alone": report(protocol.SourceUpToDate, map[string]string{source.FactOSVersion: "3.0"}),
		"no system":        report(protocol.SourceUpdatesAvailable, map[string]string{"model": "X"}),
	} {
		if err := f.srv.Reg.SetReport("fw", rep); err != nil {
			t.Fatal(err)
		}
		if os, version, _, _ := system(); os != "Device 2.0-RELEASE" || version != "2.0-RELEASE" {
			t.Errorf("%s changed the system: %q %q", name, os, version)
		}
	}
}
