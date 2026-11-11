// Package winupdate is the update source for Windows itself: the software updates Windows Update offers
// (design/architecture.md, 11.1, "Windows Update"; ADR-0086).
//
// It talks to the Windows Update Agent through its COM interface (Microsoft.Update.Session) in PowerShell, as the
// winget source talks to winget. The scripts print JSON, one object per line, with Windows Update's own result codes
// and HRESULTs; error texts only go into Detail.
package winupdate

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/runner"
	"github.com/psewar/vordr/internal/source"
	"github.com/psewar/vordr/internal/source/winget"
)

var (
	//go:embed search.ps1
	searchScript string
	//go:embed install.ps1
	installScript string
)

// Update classifications of Windows Update, by their fixed ids (the names are translated on some systems).
const (
	ClassSecurity    = "0fa1201d-4330-4fa8-8ae9-b877473b6441" // "Security Updates"
	ClassUpgrades    = "3689bdc8-b205-4af4-8d4a-a63924c5e9d5" // "Upgrades": feature upgrades to a new Windows version
	ClassDefinitions = "e0789628-ce08-4437-be74-2495b842f43b" // "Definition Updates": Defender's security intelligence
)

// searchTimeout: a search of Windows Update can take tens of seconds; one that hangs must not hold the agent's lock (a
// check or an install at a time) for longer than this. It then counts as no answer.
const searchTimeout = 15 * time.Minute

// idPattern: an update id is a GUID. Only ids of this form are passed on to the install script.
var idPattern = regexp.MustCompile(`^[0-9A-Fa-f]{8}(-[0-9A-Fa-f]{4}){3}-[0-9A-Fa-f]{12}$`)

// kbPattern: a Knowledge Base article number as Windows Update lists it ("5034441").
var kbPattern = regexp.MustCompile(`^[0-9]{1,10}$`)

// Facts about Windows Update, reported with every check.
const (
	FactSearch         = "windows_update_search"          // SearchOK, SearchFailed or SearchNoAnswer
	FactHResult        = "windows_update_hresult"         // with SearchFailed: the HRESULT, e.g. 0x8024402C
	FactAgent          = "windows_update_agent"           // version of the Windows Update Agent
	FactOptional       = "windows_update_optional"        // optional updates left out (BrowseOnly, e.g. previews)
	FactUpgrades       = "windows_update_feature_upgrade" // feature upgrades left out (classification Upgrades)
	FactDefinitions    = "windows_update_definitions"     // definition updates left out (Defender installs them)
	FactAwaitingReboot = "windows_update_awaiting_reboot" // installed updates that wait for a reboot

	SearchOK       = "ok"        // the Windows Update service answered the search
	SearchFailed   = "failed"    // it answered with an error (FactHResult)
	SearchNoAnswer = "no_answer" // PowerShell did not run the search or gave no answer
)

// WinUpdate is the source. It asks Windows the same questions as the winget source (PowerShell, elevation, Windows
// Installer, reboot keys), so New takes them from there.
type WinUpdate struct {
	Run        runner.Runner
	Lines      winget.LineRunner
	PowerShell string // as for winget: PowerShell 7 for all users if installed, else Windows PowerShell 5.1
	Elevated   bool   // administrator or LocalSystem
	Now        func() time.Time
	FreeBytes  func() (uint64, bool) // free space on the system drive; false: unknown
	Busy       func() bool           // Windows Installer is in another installation
	Pending    func() source.Reboot  // what Windows says about a reboot
}

// New returns the source for this host; on other systems it finds no PowerShell and is absent.
func New() *WinUpdate {
	w := winget.New()
	return &WinUpdate{Run: w.Run, Lines: w.Lines, PowerShell: w.PowerShell, Elevated: w.Elevated, Now: w.Now,
		FreeBytes: w.FreeBytes, Busy: w.Busy, Pending: w.Pending}
}

// Detect: every Windows has the Windows Update Agent. Whether its service answers is the check's business, so that a
// stopped service shows up as a failed check with its fact instead of no source at all.
func (u *WinUpdate) Detect() source.Presence {
	if u.PowerShell == "" {
		return source.Absent
	}
	if _, err := os.Stat(u.PowerShell); err != nil {
		return source.Absent
	}
	return source.Present
}

// update is one update as search.ps1 reports it.
type update struct {
	ID              string   `json:"id"`
	Revision        int64    `json:"revision"`
	Title           string   `json:"title"`
	KB              []string `json:"kb"`
	Categories      []string `json:"categories"`
	BrowseOnly      bool     `json:"browse_only"`
	RebootRequired  bool     `json:"reboot_required"`
	EulaAccepted    bool     `json:"eula_accepted"`
	Downloaded      bool     `json:"downloaded"`
	MaxDownloadSize int64    `json:"max_download_size"`
}

func (up update) in(class string) bool {
	for _, c := range up.Categories {
		if strings.EqualFold(c, class) {
			return true
		}
	}
	return false
}

// name: the stable name of an update, its KB number ("KB5034441") where it has one, else its update id.
func (up update) name() string {
	for _, kb := range up.KB {
		if kbPattern.MatchString(kb) {
			return "KB" + kb
		}
	}
	return up.ID
}

type found struct {
	Status        string   `json:"status"`
	AgentVersion  string   `json:"agent_version"`
	User          string   `json:"user"`
	InstallerBusy bool     `json:"installer_busy"`
	HResult       int64    `json:"hresult"`
	ErrorType     string   `json:"error_type"`
	Detail        string   `json:"detail"`
	Updates       []update `json:"updates"`
}

// search asks Windows Update for the software updates it offers. code is empty when the answer is usable.
func (u *WinUpdate) search(ctx context.Context) (found, runner.Result, protocol.Code) {
	var f found
	res := u.Run.Run(ctx, nil, u.PowerShell, winget.ScriptArgs(searchScript)...)
	switch {
	case !res.Ran() || res.ExitCode != 0:
		return f, res, protocol.CodeFailedPackageManager
	case json.Unmarshal([]byte(lastLine(res.Stdout)), &f) != nil:
		return f, res, protocol.CodeFailedPackageManager
	case f.Status == "failed":
		return f, res, protocol.CodeFailedWindowsUpdate
	case f.Status != "ok":
		return f, res, protocol.CodeFailedPackageManager
	}
	return f, res, ""
}

// answer: the fact windows_update_search for what search returned.
func answer(f found, code protocol.Code) string {
	switch {
	case code == "":
		return SearchOK
	case f.Status == "failed":
		return SearchFailed
	}
	return SearchNoAnswer
}

// classified is what a search means: the updates that count as pending, and how many were left out and why.
type classified struct {
	pending                                   []update
	optional, upgrades, definitions, awaiting int
}

// classify: an update counts as pending unless it is a feature upgrade (a new Windows version is not an update of this
// one), a definition update (Defender fetches its security intelligence itself several times a day; counted, one
// would be pending at almost every check and set stale), optional (BrowseOnly: previews Windows itself does not
// install, which would otherwise stay pending for weeks) or installed and waiting for a reboot. An id that is no GUID
// could not be named in an install run.
func classify(f found) classified {
	var s classified
	for _, up := range f.Updates {
		switch {
		case !idPattern.MatchString(up.ID):
		case up.RebootRequired:
			s.awaiting++
		case up.in(ClassUpgrades):
			s.upgrades++
		case up.in(ClassDefinitions):
			s.definitions++
		case up.BrowseOnly:
			s.optional++
		default:
			s.pending = append(s.pending, up)
		}
	}
	return s
}

// Check reports the software updates Windows Update offers. Security updates are those of the classification
// "Security Updates".
func (u *WinUpdate) Check(ctx context.Context) source.Report {
	rep := source.Report{Source: "winupdate", State: protocol.SourceUnknown, CheckedAt: u.Now(), Facts: map[string]string{}}
	rep.Facts["install_context"] = u.context()
	ctx, cancel := context.WithTimeout(ctx, searchTimeout)
	defer cancel()
	f, res, code := u.search(ctx)
	rep.Facts[FactSearch] = answer(f, code)
	if code != "" {
		if f.Status == "failed" {
			rep.Facts[FactHResult] = fmtCode(f.HResult)
		}
		rep.State, rep.Code, rep.Detail = protocol.SourceCheckFailed, code, detail(f, res)
		return rep
	}
	rep.Facts[FactAgent] = f.AgentVersion
	s := classify(f)
	rep.Facts[FactOptional] = strconv.Itoa(s.optional)
	rep.Facts[FactUpgrades] = strconv.Itoa(s.upgrades)
	rep.Facts[FactDefinitions] = strconv.Itoa(s.definitions)
	rep.Facts[FactAwaitingReboot] = strconv.Itoa(s.awaiting)
	for _, up := range s.pending {
		// Windows updates have no version: "to" carries the title, the name the KB number.
		pkg := source.Package{Name: up.name(), To: up.Title, Security: up.in(ClassSecurity)}
		if pkg.Security {
			rep.Security++
		}
		rep.Packages = append(rep.Packages, pkg)
	}
	rep.Pending = len(rep.Packages)
	rep.State = protocol.SourceUpToDate
	if rep.Pending > 0 {
		rep.State = protocol.SourceUpdatesAvailable
	}
	return rep
}

// Reboot: what Windows itself records about a pending reboot (the same registry keys as for winget).
func (u *WinUpdate) Reboot() source.Reboot { return u.Pending() }

func (u *WinUpdate) context() string {
	if u.Elevated {
		return winget.ContextElevated
	}
	return winget.ContextUser
}

// withIDs puts the update ids in front of the install script. Every id matches idPattern, which allows no quote.
func withIDs(ids []string) string {
	quoted := make([]string, len(ids))
	for i, id := range ids {
		quoted[i] = "'" + strings.ReplaceAll(id, "'", "''") + "'"
	}
	return "$VordrIds = @(" + strings.Join(quoted, ", ") + ")\n" + installScript
}

func lastLine(out []byte) string {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

func detail(f found, res runner.Result) string {
	switch {
	case f.Status == "failed":
		return clip("hresult " + fmtCode(f.HResult) + " (" + f.ErrorType + ": " + f.Detail + ")")
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
