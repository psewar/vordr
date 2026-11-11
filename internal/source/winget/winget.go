// Package winget is the update source for Windows: the applications winget manages (design/architecture.md, 11).
//
// It talks to winget through Microsoft's PowerShell module Microsoft.WinGet.Client, which hands out packages and
// install results as objects with fixed status values. The table winget.exe prints is translated and cut to the
// console width, so nothing here reads it. The scripts print JSON, one object per line; error texts only go into
// Detail.
package winget

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/runner"
	"github.com/psewar/vordr/internal/source"
)

var (
	//go:embed list.ps1
	listScript string
	//go:embed upgrade.ps1
	upgradeScript string
)

// IDPattern: what a winget package id looks like ("jqlang.jq", "OpenJS.NodeJS.LTS"). agent.yaml checks app
// packages against it; ids from winget's own list are only passed on after the same check.
var IDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.+_-]{0,127}$`)

// Facts about the WinGet client, reported with every check.
const (
	ClientMissing = "missing" // fact winget_client: Microsoft.WinGet.Client is not installed for this account
	// fact winget_client: the agent runs as LocalSystem, where Microsoft.WinGet.Client works only in PowerShell 7,
	// and PowerShell 7 is not installed for all users
	ClientNeedsPowerShell7 = "needs_powershell_7"

	ContextElevated = "elevated" // fact install_context: installers may change the machine
	ContextUser     = "user"     // not elevated: only packages agent.yaml names one by one (no UAC prompt to answer)
)

// MinFree: free space the system drive needs before an install run. winget names no download size in advance.
const MinFree = 2 << 30

// LineRunner runs a command and hands every line it writes to stdout to onLine while it runs.
type LineRunner interface {
	Lines(ctx context.Context, onLine func(line string), name string, args ...string) runner.Result
}

type Winget struct {
	Run        runner.Runner
	Lines      LineRunner
	PowerShell string // PowerShell 7 for all users if installed, else Windows PowerShell 5.1; "" where there is none
	Elevated   bool   // administrator or LocalSystem
	System     bool   // LocalSystem: the agent runs as the service
	Now        func() time.Time
	FreeBytes  func() (uint64, bool) // free space on the system drive; false: unknown
	Busy       func() bool           // Windows Installer is in another installation
	Pending    func() source.Reboot  // what Windows says about a reboot
}

// New returns the source for this host; on other systems it finds no PowerShell and is absent.
func New() *Winget {
	return &Winget{Run: runner.Exec{}, Lines: execLines{}, PowerShell: powerShellPath(), Elevated: elevated(),
		System: localSystem(), Now: time.Now, FreeBytes: freeBytes, Busy: installerBusy, Pending: pendingReboot}
}

// Detect: Windows PowerShell is there on every Windows since 7; whether winget answers is the check's business, so
// that a missing client shows up as a failed check with its fact instead of no source at all.
func (w *Winget) Detect() source.Presence {
	if w.PowerShell == "" {
		return source.Absent
	}
	if _, err := os.Stat(w.PowerShell); err != nil {
		return source.Absent
	}
	return source.Present
}

type listed struct {
	Status        string `json:"status"`
	WingetVersion string `json:"winget_version"`
	ClientVersion string `json:"client_version"`
	User          string `json:"user"`
	ErrorType     string `json:"error_type"`
	Detail        string `json:"detail"`
	Packages      []struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Installed string `json:"installed"`
		Available string `json:"available"`
		Source    string `json:"source"`
	} `json:"packages"`
}

// list asks winget for the packages it would upgrade. code is empty when the answer is usable.
func (w *Winget) list(ctx context.Context) (listed, runner.Result, protocol.Code) {
	var l listed
	res := w.Run.Run(ctx, nil, w.PowerShell, w.args(listScript)...)
	switch {
	case !res.Ran() || res.ExitCode != 0:
		return l, res, protocol.CodeFailedPackageManager
	case json.Unmarshal([]byte(lastLine(res.Stdout)), &l) != nil:
		return l, res, protocol.CodeFailedPackageManager
	case l.Status != "ok":
		return l, res, protocol.CodeFailedPackageManager
	}
	return l, res, ""
}

// Check reports what winget would upgrade. Pending packages carry no security flag: winget does not classify them.
func (w *Winget) Check(ctx context.Context) source.Report {
	rep := source.Report{Source: "winget", State: protocol.SourceUnknown, CheckedAt: w.Now(), Facts: map[string]string{}}
	rep.Facts["install_context"] = w.context()
	if w.needsPowerShell7() {
		rep.Facts["winget_client"] = ClientNeedsPowerShell7
		rep.State, rep.Code, rep.Detail = protocol.SourceCheckFailed, protocol.CodeFailedPackageManager, needsPowerShell7Detail
		return rep
	}
	l, res, code := w.list(ctx)
	if l.User != "" {
		rep.Facts["winget_user"] = l.User
	}
	if l.Status == "client_missing" {
		rep.Facts["winget_client"] = ClientMissing
	}
	if code != "" {
		rep.State, rep.Code = protocol.SourceCheckFailed, code
		rep.Detail = detail(l, res)
		return rep
	}
	rep.Facts["winget_version"] = l.WingetVersion
	rep.Facts["winget_client"] = l.ClientVersion
	var unknown []string
	for _, p := range l.Packages {
		if !IDPattern.MatchString(p.ID) {
			continue // nothing the agent could name in an install run
		}
		if unknownVersion(p.Installed) {
			unknown = append(unknown, p.ID)
			continue
		}
		pkg := source.Package{Name: p.ID, From: p.Installed, To: p.Available, Major: source.MajorJump(p.Installed, p.Available)}
		if p.Source != "" {
			pkg.Origins = []string{p.Source}
		}
		rep.Packages = append(rep.Packages, pkg)
	}
	if len(unknown) > 0 {
		rep.Facts["winget_unknown_version"] = strings.Join(unknown, ", ")
	}
	rep.Pending = len(rep.Packages)
	rep.State = protocol.SourceUpToDate
	if rep.Pending > 0 {
		rep.State = protocol.SourceUpdatesAvailable
	}
	return rep
}

// Reboot: what Windows itself records about a pending reboot (pendingReboot).
func (w *Winget) Reboot() source.Reboot { return w.Pending() }

// unknownVersion: winget does not know which version is installed (it says "Unknown", e.g. some game launchers).
// "winget upgrade" leaves such packages out unless asked with --include-unknown, and upgrading them failed
// (CatalogError 0x8A15005F while the other packages went through). They are neither pending
// nor upgraded; the fact winget_unknown_version names them.
func unknownVersion(installed string) bool {
	return installed == "" || strings.EqualFold(installed, "Unknown")
}

// needsPowerShell7: as LocalSystem only PowerShell 7 runs Microsoft.WinGet.Client. With Windows PowerShell the module
// would fail on every call with its own error, so this is asked before anything runs.
func (w *Winget) needsPowerShell7() bool {
	return w.System && !strings.HasSuffix(strings.ToLower(w.PowerShell), "pwsh.exe")
}

const needsPowerShell7Detail = "the agent runs as LocalSystem, where Microsoft.WinGet.Client works only in PowerShell 7 - " +
	"install it for all users (winget install --id Microsoft.PowerShell --scope machine --installer-type wix) " +
	"or run install-service again, which does it"

func (w *Winget) context() string {
	if w.Elevated {
		return ContextElevated
	}
	return ContextUser
}

func (w *Winget) args(script string) []string { return ScriptArgs(script) }

// ScriptArgs: the PowerShell arguments that run a script without profile or prompts. The script travels base64
// encoded (UTF-16LE, as PowerShell wants it), so no quoting of the command line can change it. The Windows Update
// source runs its scripts the same way.
func ScriptArgs(script string) []string {
	units := utf16.Encode([]rune(script))
	raw := make([]byte, 2*len(units))
	for i, u := range units {
		raw[2*i], raw[2*i+1] = byte(u), byte(u>>8)
	}
	return []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-EncodedCommand",
		base64.StdEncoding.EncodeToString(raw)}
}

// withIDs puts the package ids in front of the upgrade script. Every id matches IDPattern, which allows no quote.
func withIDs(ids []string) string {
	quoted := make([]string, len(ids))
	for i, id := range ids {
		quoted[i] = "'" + strings.ReplaceAll(id, "'", "''") + "'"
	}
	return "$VordrIds = @(" + strings.Join(quoted, ", ") + ")\n" + upgradeScript
}

func lastLine(out []byte) string {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

func detail(l listed, res runner.Result) string {
	switch {
	case l.Status == "client_missing":
		return "the PowerShell module Microsoft.WinGet.Client is not installed for " + l.User +
			" (Install-Module Microsoft.WinGet.Client -Scope AllUsers)"
	case l.Detail != "":
		return clip(l.ErrorType + ": " + l.Detail)
	case !res.Ran():
		return res.Err.Error()
	}
	return clip(strings.TrimSpace(string(res.Stderr) + "\n" + string(res.Stdout)))
}

func clip(s string) string {
	if len(s) > 800 {
		return s[len(s)-800:]
	}
	return s
}

func fmtCode(v int64) string { return fmt.Sprintf("0x%08X", uint32(v)) }
