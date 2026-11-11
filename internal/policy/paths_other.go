//go:build !windows

package policy

const (
	DefaultConfigPath = "/etc/vordr/agent.yaml"
	DefaultStateDir   = "/var/lib/vordr"
)
