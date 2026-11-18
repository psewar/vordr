package main

import (
	"testing"

	"github.com/psewar/vordr/internal/wire"
)

// vordr-hub show names the reboot window with the zone it is meant in, also on a host without an install window
// (ADR-0187), and whether a reason opens it.
func TestPolicyTextNamesTheRebootWindowAndItsZone(t *testing.T) {
	p := wire.PolicySummary{RebootAllowed: true, TZ: "America/New_York", RebootWindowOnly: true,
		RebootWindow: &wire.WindowInfo{Days: []string{"mon", "tue"}, Start: "06:00", End: "16:00"}}
	want := "no installs, reboot allowed, reboot window mon,tue 06:00-16:00 America/New_York (shut also for a reason)"
	if got := policyText(p); got != want {
		t.Fatalf("policy text:\n%s\nwant\n%s", got, want)
	}
	p.TZ, p.RebootWindowOnly = "", false
	if got, want := policyText(p), "no installs, reboot allowed, reboot window mon,tue 06:00-16:00"; got != want {
		t.Fatalf("without a zone:\n%s\nwant\n%s", got, want)
	}
}
