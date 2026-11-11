package agent

import (
	"testing"

	"github.com/psewar/vordr/internal/facts"
	"github.com/psewar/vordr/internal/source/apt"
)

// apt learns the host's release from its facts: a security run takes dependencies from it (ADR-0116). Unknown leaves
// it empty, so only the security archives count.
func TestAptInstallerKnowsTheRelease(t *testing.T) {
	if i := newAptInstaller(&apt.Apt{}, facts.Host{OSVersion: "12"}); i.a.Release != "12" {
		t.Errorf("release %q", i.a.Release)
	}
	if i := newAptInstaller(&apt.Apt{}, facts.Host{OSVersion: facts.Unknown}); i.a.Release != "" {
		t.Errorf("unknown release taken as %q", i.a.Release)
	}
}
