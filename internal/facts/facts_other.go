//go:build !windows

package facts

// windowsRelease: only Windows keeps its release in the registry; elsewhere /etc/os-release says it.
func windowsRelease() (name, version string, ok bool) { return "", "", false }
