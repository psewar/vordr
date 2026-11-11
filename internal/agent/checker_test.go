package agent

import (
	"testing"

	"github.com/psewar/vordr/internal/facts"
)

// The checker hands install.mode of the policy to apt, which reports it as the fact install_mode (ADR-0095).
func TestSystemCheckerPassesPolicyMode(t *testing.T) {
	got := System(facts.Host{}, false, "upgrade")
	c, ok := got.(systemChecker)
	if !ok {
		t.Fatalf("System returned %T", got)
	}
	if m := c.aptSource().PolicyMode; m != "upgrade" {
		t.Fatalf("apt PolicyMode %q, want upgrade", m)
	}
}
