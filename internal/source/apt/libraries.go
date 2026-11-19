package apt

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"
)

// librarySignal is the reboot signal of ADR-0200 in Reboot.Checked.
const librarySignal = "libraries"

// replacedPrefixes: where packages put libraries and programs. /usr/local is not the package manager's.
var replacedPrefixes = []string{
	"/usr/lib/", "/usr/lib32/", "/usr/lib64/", "/usr/libx32/", "/usr/libexec/", "/usr/bin/", "/usr/sbin/",
	"/lib/", "/lib32/", "/lib64/", "/libx32/", "/bin/", "/sbin/",
}

// replacedIgnored: files below replacedPrefixes that are data, not code. The locale archive is rebuilt by every
// locales update, and a process reading the old one runs no old code.
var replacedIgnored = []string{"/usr/lib/locale/"}

// replacedLibraries tells whether a process still runs code from a library or program that a package update has
// replaced since the process started (ADR-0200). The kernel marks such a mapping "(deleted)" in /proc/<pid>/maps, the
// same evidence needrestart and checkrestart read, so the agent needs neither of them.
//
// Only processes of the agent's own PID namespace with the same root count: the processes of a container on this
// host are the container's business, and a path under another root names another file. answered is false where the
// agent cannot see every process - not root, /proc unreadable, or a running process whose links or map it cannot
// read - so that "no" is only said when it was checked.
func (a *Apt) replacedLibraries() (found, answered bool) {
	if !a.IsRoot {
		return false, false
	}
	own, err := os.Readlink(filepath.Join(a.Paths.Proc, "self", "ns", "pid"))
	if err != nil {
		return false, false
	}
	entries, err := os.ReadDir(a.Paths.Proc)
	if err != nil {
		return false, false
	}
	for _, e := range entries {
		if !isPID(e.Name()) {
			continue
		}
		dir := filepath.Join(a.Paths.Proc, e.Name())
		hit, seen := processReplaced(dir, own)
		switch {
		case hit:
			return true, true
		case !seen && !ended(dir):
			return false, false
		}
	}
	return false, true
}

// processReplaced: whether the process in dir maps a replaced package file. seen is false when its links or its map
// could not be read; the process may have ended meanwhile, which the caller asks before it gives up.
func processReplaced(dir, ownNS string) (hit, seen bool) {
	ns, err := os.Readlink(filepath.Join(dir, "ns", "pid"))
	if err != nil {
		return false, false
	}
	if ns != ownNS {
		return false, true
	}
	root, err := os.Readlink(filepath.Join(dir, "root"))
	if err != nil {
		return false, false
	}
	if root != "/" {
		return false, true
	}
	f, err := os.Open(filepath.Join(dir, "maps"))
	if err != nil {
		return false, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if replacedPackageFile(sc.Text()) {
			return true, true
		}
	}
	return false, sc.Err() == nil
}

// replacedPackageFile: a line of /proc/<pid>/maps that maps a library or program from a package which has been
// replaced on disk since.
func replacedPackageFile(line string) bool {
	path, deleted := strings.CutSuffix(mappedPath(line), " (deleted)")
	return deleted && hasAnyPrefix(path, replacedPrefixes) && !hasAnyPrefix(path, replacedIgnored)
}

// mappedPath: the file a line of /proc/<pid>/maps maps ("" for anonymous memory). It is the sixth field, after
// address, permissions, offset, device and inode, and may itself contain spaces.
func mappedPath(line string) string {
	rest := line
	for range 5 {
		rest = strings.TrimLeft(rest, " ")
		i := strings.IndexByte(rest, ' ')
		if i < 0 {
			return ""
		}
		rest = rest[i:]
	}
	return strings.TrimLeft(rest, " ")
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// isPID: a directory name in /proc that is a process ID.
func isPID(name string) bool {
	if name == "" {
		return false
	}
	for _, c := range name {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// ended: the process is gone or a zombie, so it runs no code (a zombie has no links and no map left to read). Its
// stat is readable for everyone; reading it fails only when the process went away between the listing and now, a race
// no check beforehand can close.
func ended(dir string) bool {
	raw, err := os.ReadFile(filepath.Join(dir, "stat"))
	if err != nil {
		return true
	}
	// "pid (comm) S ...": the name may hold spaces and parentheses, the state follows the last ")".
	i := bytes.LastIndexByte(raw, ')')
	if i < 0 || i+2 >= len(raw) {
		return false
	}
	return raw[i+2] == 'Z' || raw[i+2] == 'X'
}
