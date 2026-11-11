//go:build !linux

package inventory

import (
	"os"
	"os/exec"
)

// asOwner: outside Linux the command runs as the agent. Under Windows that is LocalSystem; files it writes into a
// user's profile inherit the folder's permissions, so the user keeps access (design 11.4).
func asOwner(*exec.Cmd, string) ([]string, error) { return os.Environ(), nil }

// handTo: nothing to hand over where commands run as the agent.
func handTo(string, string) error { return nil }
