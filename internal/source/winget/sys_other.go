//go:build !windows

package winget

import "github.com/psewar/vordr/internal/source"

// winget exists only on Windows: without PowerShell the source is absent and none of these is asked.
func powerShellPath() string       { return "" }
func elevated() bool               { return false }
func localSystem() bool            { return false }
func freeBytes() (uint64, bool)    { return 0, false }
func installerBusy() bool          { return false }
func pendingReboot() source.Reboot { return source.Reboot{Checked: []string{}} }
