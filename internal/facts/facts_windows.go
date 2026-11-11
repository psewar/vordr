//go:build windows

package facts

import (
	"strconv"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// windowsRelease reads name and build from the registry: "Windows 11 Pro 24H2" and "10.0.26100.1000". ProductName
// still says "Windows 10" on Windows 11; build 22000 and later is Windows 11.
func windowsRelease() (name, version string, ok bool) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.QUERY_VALUE)
	if err != nil {
		return "", "", false
	}
	defer k.Close()
	product, _, _ := k.GetStringValue("ProductName")
	display, _, _ := k.GetStringValue("DisplayVersion")
	build, _, _ := k.GetStringValue("CurrentBuild")
	ubr, _, _ := k.GetIntegerValue("UBR")
	if product == "" || build == "" {
		return "", "", false
	}
	if n, err := strconv.Atoi(build); err == nil && n >= 22000 {
		product = strings.Replace(product, "Windows 10", "Windows 11", 1)
	}
	name = strings.TrimSpace(product + " " + display)
	return name, "10.0." + build + "." + strconv.FormatUint(ubr, 10), true
}
