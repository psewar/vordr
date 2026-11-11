package apt

import (
	"context"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/source"
)

var kernelPackagePrefixes = []string{"linux-image-", "pve-kernel-", "proxmox-kernel-", "raspberrypi-kernel"}

// Reboot finds out whether the host needs a reboot. Two signals, each used only where it means something:
//   - the reboot-required file, where update-notifier maintains it (Ubuntu) or wherever it exists;
//   - the running kernel against the newest installed one of its flavor, not in containers (they run the host's kernel).
func (a *Apt) Reboot(ctx context.Context, inContainer bool) source.Reboot {
	r := source.Reboot{Checked: []string{}}
	if exists(a.Paths.RebootRequired) || exists(a.Paths.RebootNotifier) {
		r.Checked = append(r.Checked, "reboot_required_file")
	}
	if exists(a.Paths.RebootRequired) {
		r.Required, r.Reason = true, protocol.RebootOther
		for _, pkg := range strings.Fields(readTrim(a.Paths.RebootRequired + ".pkgs")) {
			if !slices.Contains(r.Packages, pkg) {
				r.Packages = append(r.Packages, pkg)
			}
			if isKernelPackage(pkg) {
				r.Reason = protocol.RebootKernel
			}
		}
	}
	if inContainer {
		return r
	}
	running := readTrim(a.Paths.OSRelease)
	kernels := installedKernels(a.Paths.BootDir, kernelFlavor(running))
	if running == "" || len(kernels) == 0 {
		return r
	}
	newest, known := a.newestKernel(ctx, kernels)
	if !known {
		return r
	}
	r.Checked = append(r.Checked, "kernel")
	r.Running, r.Newest = running, newest
	if newest == running {
		return r
	}
	if newer, known := a.greater(ctx, newest, running); known && newer {
		r.Required, r.Reason = true, protocol.RebootKernel
	}
	return r
}

// kernelCore: the version at the start of a kernel release ("6.8.12-4" of "6.8.12-4-pve").
var kernelCore = regexp.MustCompile(`^\d+(\.\d+)*(-\d+)?`)

// kernelFlavor: what a kernel is built for, the last dash-separated part after its version: "pve", "amd64", "generic",
// "2712" or "v8". Only kernels of the running flavor count (Raspberry Pi OS installs the Pi 5 kernel
// "+rpt-rpi-2712" and the Pi 4 kernel "+rpt-rpi-v8" side by side; dpkg ranks v8 higher, so a Pi 5 comparing both would
// ask for a reboot for ever). A name part before it, such as Debian's "+deb13" or "+bpo", stays the same flavor.
func kernelFlavor(release string) string {
	rest := release[len(kernelCore.FindString(release)):]
	return rest[strings.LastIndex(rest, "-")+1:]
}

func isKernelPackage(pkg string) bool {
	for _, p := range kernelPackagePrefixes {
		if strings.HasPrefix(pkg, p) {
			return true
		}
	}
	return false
}

// installedKernels: versions from /boot/vmlinuz-<version> of one flavor.
func installedKernels(boot, flavor string) []string {
	entries, err := os.ReadDir(boot)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if v, ok := strings.CutPrefix(e.Name(), "vmlinuz-"); ok && v != "" && !e.IsDir() && kernelFlavor(v) == flavor {
			out = append(out, v)
		}
	}
	return out
}

// newestKernel compares with dpkg's version rules. known is false as soon as one comparison gives no answer.
func (a *Apt) newestKernel(ctx context.Context, kernels []string) (string, bool) {
	newest := kernels[0]
	for _, k := range kernels[1:] {
		newer, known := a.greater(ctx, k, newest)
		if !known {
			return "", false
		}
		if newer {
			newest = k
		}
	}
	return newest, true
}

// greater asks "dpkg --compare-versions a gt b": exit code 0 is yes, 1 is no; anything else is no answer.
func (a *Apt) greater(ctx context.Context, x, y string) (newer, known bool) {
	res := a.Run.Run(ctx, env, "dpkg", "--compare-versions", x, "gt", y)
	if !res.Ran() {
		return false, false
	}
	switch res.ExitCode {
	case 0:
		return true, true
	case 1:
		return false, true
	}
	return false, false
}
