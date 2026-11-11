//go:build windows

package winget

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/source"
)

// powerShellPath: PowerShell 7 installed for all users when it is there - as LocalSystem, Microsoft.WinGet.Client
// refuses Windows PowerShell ("This cmdlet is not supported in Windows PowerShell"). Otherwise
// Windows PowerShell 5.1, which every Windows has. The Store edition of PowerShell 7 lives in the user's profile and
// is no use to the service.
func powerShellPath() string {
	if p := PowerShell7(); p != "" {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return filepath.Join(root, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
}

// PowerShell7: where PowerShell 7 for all users lives (the MSI installs it there); "" without ProgramFiles.
func PowerShell7() string {
	pf := os.Getenv("ProgramFiles")
	if pf == "" {
		return ""
	}
	return filepath.Join(pf, "PowerShell", "7", "pwsh.exe")
}

// elevated: an administrator's elevated token or LocalSystem, the account the service runs as.
func elevated() bool { return windows.GetCurrentProcessToken().IsElevated() }

// localSystem: the agent runs as LocalSystem, i.e. as the service.
func localSystem() bool {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	return err == nil && u.User.Sid.IsWellKnown(windows.WinLocalSystemSid)
}

func freeBytes() (uint64, bool) {
	drive := os.Getenv("SystemDrive")
	if drive == "" {
		drive = "C:"
	}
	path, err := windows.UTF16PtrFromString(drive + `\`)
	if err != nil {
		return 0, false
	}
	var avail, total, free uint64
	if windows.GetDiskFreeSpaceEx(path, &avail, &total, &free) != nil {
		return 0, false
	}
	return avail, true
}

// installerBusy: Windows Installer holds the mutex Global\_MSIExecute while it installs. Windows has no other way to
// ask than to open it: "not found" means free, a mutex we may not open still exists.
func installerBusy() bool {
	name, err := windows.UTF16PtrFromString(`Global\_MSIExecute`)
	if err != nil {
		return false
	}
	h, err := windows.OpenMutex(windows.SYNCHRONIZE, false, name)
	switch {
	case err == nil:
		_ = windows.CloseHandle(h)
		return true
	case errors.Is(err, windows.ERROR_ACCESS_DENIED):
		return true
	}
	return false
}

// rebootKeys: Windows Update and component servicing create these keys while a reboot is pending. Not used:
// PendingFileRenameOperations - Edge, Office and drivers leave entries there for weeks (over a thousand on
// one host), so it would say "reboot" almost always.
var rebootKeys = []string{
	`SOFTWARE\Microsoft\Windows\CurrentVersion\Component Based Servicing\RebootPending`,
	`SOFTWARE\Microsoft\Windows\CurrentVersion\WindowsUpdate\Auto Update\RebootRequired`,
}

// pendingReboot reads the keys above. The registry answers "does this key exist" only by opening it; a key that
// cannot be read for another reason leaves the signal unchecked.
func pendingReboot() source.Reboot {
	r := source.Reboot{Checked: []string{}}
	answered := 0
	for _, path := range rebootKeys {
		k, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.QUERY_VALUE)
		switch {
		case err == nil:
			_ = k.Close()
			r.Required, r.Reason = true, protocol.RebootWindowsUpdate
			answered++
		case errors.Is(err, registry.ErrNotExist):
			answered++
		}
	}
	if answered == len(rebootKeys) {
		r.Checked = append(r.Checked, "windows_update")
	}
	return r
}
