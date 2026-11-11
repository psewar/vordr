//go:build !windows

package agent

import (
	"os"
	"strings"
)

// hostZone: Debian and Ubuntu keep the zone in /etc/timezone, every systemd host as the /etc/localtime link.
func hostZone() string {
	if raw, err := os.ReadFile("/etc/timezone"); err == nil && strings.TrimSpace(string(raw)) != "" {
		return strings.TrimSpace(string(raw))
	}
	if target, err := os.Readlink("/etc/localtime"); err == nil {
		if _, name, ok := strings.Cut(target, "zoneinfo/"); ok {
			return name
		}
	}
	return ""
}
