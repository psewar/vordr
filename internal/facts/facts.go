// Package facts collects what the agent reports about its host: OS, architecture, virtualization.
package facts

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"runtime"
	"strings"

	"github.com/psewar/vordr/internal/runner"
)

// Unknown marks a fact the agent could not find out. It never means "no".
const Unknown = "unknown"

type Host struct {
	Hostname       string `json:"hostname"`
	OS             string `json:"os"`         // PRETTY_NAME from os-release, else GOOS
	OSID           string `json:"os_id"`      // debian, ubuntu, raspbian, windows, ...
	OSVersion      string `json:"os_version"` // VERSION_ID
	GOOS           string `json:"goos"`       // the agent's build, as a release manifest names it: linux, windows
	Arch           string `json:"arch"`
	Virtualization string `json:"virtualization"` // systemd-detect-virt: none, kvm, lxc, wsl, ...
	Container      string `json:"container"`      // systemd-detect-virt -c: none, lxc, docker, wsl, ...
}

// InContainer: the host shares its kernel with another system. Unknown counts as no.
func (h Host) InContainer() bool { return h.Container != "none" && h.Container != Unknown }

// Platform is the os and arch a release manifest names the agent binary by. ok is false while either is not known:
// an older agent does not report goos, and a device driven through a target runs no agent of its own.
func (h Host) Platform() (goos, arch string, ok bool) {
	known := func(v string) bool { return v != "" && v != Unknown }
	return h.GOOS, h.Arch, known(h.GOOS) && known(h.Arch)
}

// Collect reads the facts of this host.
func Collect(ctx context.Context, r runner.Runner) Host {
	h := Host{OS: runtime.GOOS, OSID: runtime.GOOS, OSVersion: Unknown, GOOS: runtime.GOOS, Arch: runtime.GOARCH,
		Virtualization: Unknown, Container: Unknown}
	if name, err := os.Hostname(); err == nil {
		h.Hostname = strings.ToLower(name)
	}
	if raw, err := os.ReadFile("/etc/os-release"); err == nil {
		rel := parseOSRelease(raw)
		h.OS, h.OSID, h.OSVersion = first(rel["PRETTY_NAME"], h.OS), first(rel["ID"], h.OSID), first(rel["VERSION_ID"], Unknown)
	}
	if name, version, ok := windowsRelease(); ok {
		h.OS, h.OSVersion = name, version
	}
	if runtime.GOOS == "linux" {
		h.Virtualization = detectVirt(ctx, r)
		h.Container = detectVirt(ctx, r, "-c")
	}
	return h
}

// detectVirt: systemd-detect-virt prints "none" with exit code 1 when it finds nothing; the printed word is the
// answer either way. Only a command that cannot run leaves the fact unknown.
func detectVirt(ctx context.Context, r runner.Runner, args ...string) string {
	res := r.Run(ctx, nil, "systemd-detect-virt", args...)
	if !res.Ran() {
		return Unknown
	}
	if v := strings.TrimSpace(string(res.Stdout)); v != "" {
		return v
	}
	return Unknown
}

func parseOSRelease(raw []byte) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		k, v, found := strings.Cut(sc.Text(), "=")
		if found && k != "" && !strings.HasPrefix(k, "#") {
			out[k] = strings.Trim(v, `"'`)
		}
	}
	return out
}

func first(v, fallback string) string {
	if v != "" {
		return v
	}
	return fallback
}
