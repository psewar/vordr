package pfsense

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const listed = "192.0.2.40"

// pkgVersion answers like pkg version -vR, with every status: "<" the catalog has a newer version (also one with an
// epoch after a comma, and a name with dashes), ">" the installed one is newer, "?" not in the catalog, "=" the same.
const pkgVersion = `pkg-a-1.0                          <   needs updating (remote has 1.1)
pkg-tool-b-2.0_1                   >   succeeds remote (remote has 1.9)
web-c-1.24.0_12,3                  <   needs updating (remote has 1.26.0,3)
orphan-d-0.1                       ?   orphaned: misc/orphan-d
same-e-3.0                         =   up-to-date with remote
vendor-pkg-f-1.2.0                 <   needs updating (remote has 1.3.0_1)
`

// box is one installation of both scripts in a temp dir, with pfctl, pkg, logger and chown stubbed.
type box struct {
	t                                      *testing.T
	dir, forced, root, state, queue, table string
}

func newBox(t *testing.T) *box {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no /bin/sh here; the scripts are tested where sh exists")
	}
	d := t.TempDir()
	b := &box{t: t, dir: d, state: filepath.Join(d, "state"), queue: filepath.Join(d, "queue"),
		table: filepath.Join(d, "table")}
	stubs := filepath.Join(d, "bin")
	must(t, os.Mkdir(stubs, 0o755))
	write(t, filepath.Join(stubs, "pfctl"), `#!/bin/sh
# pfctl -q -t <table> -T add|delete|show|expire [arg]
op=$5; arg=${6:-}
echo "$op $arg" >> "`+d+`/pfctl.log"
touch "`+b.table+`"
case "$op" in
add) [ "${PFCTL_FAIL:-}" = add ] && exit 1; grep -qxF "$arg" "`+b.table+`" || echo "$arg" >> "`+b.table+`" ;;
delete) grep -vxF "$arg" "`+b.table+`" > "`+b.table+`.new"; mv "`+b.table+`.new" "`+b.table+`" ;;
show) sed 's/^/   /' "`+b.table+`" ;;
esac
`, 0o755)
	// pkg itself lists only "<" for -l'<'; the stand-in answers every status, so the tests show that the root job keeps
	// only "<" by itself.
	// The installed and offered versions of the release packages come from the files installed/<name> and
	// offered/<name>; core lists the installed core packages; version -t compares as sort -V does. The annotation
	// next_stage of a package, which the updater sets between the stages of a release (ADR-0209), is the file
	// stage/<name>.
	write(t, filepath.Join(stubs, "pkg"), `#!/bin/sh
case "$*" in
"version -U -vRl<") cat "`+d+`/outdated" ;;
"query -e %n ~ pfSense-kernel-* || %n ~ pfSense-base* %n") cat "`+d+`/core" ;;
"query -e %n ~ pfSense-kernel-* %n") grep '^pfSense-kernel-' "`+d+`/core" ;;
"annotate -q -S "*" next_stage") [ ! -f "`+d+`/stage/$4" ] || cat "`+d+`/stage/$4" ;;
"query %v "*) [ ! -f "`+d+`/installed/$3" ] || cat "`+d+`/installed/$3" ;;
"rquery -U %v "*) [ ! -f "`+d+`/offered/$4" ] || cat "`+d+`/offered/$4" ;;
"version -t "*)
	if [ "$3" = "$4" ]; then echo "="
	elif [ "$(printf '%s\n%s\n' "$3" "$4" | sort -V | head -n 1)" = "$3" ]; then echo "<"
	else echo ">"; fi ;;
*) echo "unexpected pkg $*" >&2; exit 1 ;;
esac
`, 0o755)
	// pfSense-upgrade -C -U answers what the file upgrade-answer holds and ends with the code in upgrade-rc. An install
	// (-y -l <log>, with -i <add-on>) ends with the code in install-rc; with 0 the offered versions become installed.
	// Where the file install-stage exists, a release ends after a stage: the kernel package notes the next one.
	write(t, filepath.Join(stubs, "pfSense-upgrade"), `#!/bin/sh
echo "$*" >> "`+d+`/upgrade.log"
case "$*" in
"-C -U")
	cat "`+d+`/upgrade-answer"
	exit "$(cat "`+d+`/upgrade-rc")" ;;
"-y -l "*)
	[ ! -f "`+d+`/install-slow" ] || sleep 1.2
	rc=$(cat "`+d+`/install-rc")
	if [ "$rc" = 0 ] && [ "${4:-}" = -i ]; then
		cp "`+d+`/offered/$5" "`+d+`/installed/$5"
	elif [ "$rc" = 0 ]; then
		for p in pfSense pfSense-base pfSense-kernel-pfSense; do cp "`+d+`/offered/$p" "`+d+`/installed/$p"; done
		[ ! -f "`+d+`/install-stage" ] || cp "`+d+`/install-stage" "`+d+`/stage/pfSense-kernel-pfSense"
	fi
	exit "$rc" ;;
*) echo "unexpected pfSense-upgrade $*" >&2; exit 1 ;;
esac
`, 0o755)
	write(t, filepath.Join(stubs, "sysctl"), `#!/bin/sh
[ "$*" = "-n kern.boottime" ] || { echo "unexpected sysctl $*" >&2; exit 1; }
echo "{ sec = $(cat "`+d+`/boot"), usec = 1 } Thu Jan  1 00:16:40 1970"
`, 0o755)
	write(t, filepath.Join(d, "boot"), "1000\n", 0o644)
	write(t, filepath.Join(d, "install-rc"), "0\n", 0o644)
	must(t, os.Mkdir(filepath.Join(d, "installed"), 0o755))
	must(t, os.Mkdir(filepath.Join(d, "offered"), 0o755))
	must(t, os.Mkdir(filepath.Join(d, "stage"), 0o755))
	write(t, filepath.Join(d, "core"), "pfSense-base\npfSense-kernel-pfSense\n", 0o644)
	for _, p := range []string{"pfSense", "pfSense-base", "pfSense-kernel-pfSense"} {
		write(t, filepath.Join(d, "installed", p), "1.2.3\n", 0o644)
		write(t, filepath.Join(d, "offered", p), "1.2.3\n", 0o644)
	}
	write(t, filepath.Join(d, "upgrade-answer"), "Your system is up to date\n", 0o644)
	write(t, filepath.Join(d, "upgrade-rc"), "0\n", 0o644)
	// awk acts as FreeBSD's, which pfSense runs: it refuses a -v value with a newline ("newline in string"), which the
	// awk of most Linux systems takes. Everything else goes to this system's awk.
	realAwk, err := exec.LookPath("awk")
	if err != nil {
		t.Skip("no awk here; the scripts are tested where awk exists")
	}
	write(t, filepath.Join(stubs, "awk"), `#!/bin/sh
nl='
'
prev=
for a in "$@"; do
	case "$a" in
	*"$nl"*)
		if [ "$prev" = -v ] || [ "${a#-v}" != "$a" ]; then
			echo "awk: newline in string $a... at source line 1" >&2
			exit 2
		fi ;;
	esac
	prev=$a
done
exec `+realAwk+` "$@"
`, 0o755)
	write(t, filepath.Join(stubs, "logger"), "#!/bin/sh\necho \"$*\" >> \""+d+"/log\"\n", 0o755)
	write(t, filepath.Join(stubs, "chown"), "#!/bin/sh\nexit 0\n", 0o755)
	write(t, filepath.Join(d, "outdated"), pkgVersion, 0o644)
	write(t, filepath.Join(d, "allow"), "# comment\n\n"+listed+"  # device\n198.51.100.9#no-space-is-not-an-entry\n", 0o644)
	write(t, filepath.Join(d, "config.xml"), "<?xml version=\"1.0\"?>\n<pfsense><version>1.0</version></pfsense>\n", 0o644)
	write(t, filepath.Join(d, "version"), "1.2.3-RELEASE\n", 0o644)
	common := [][2]string{
		{"PATH=/bin:/sbin:/usr/bin:/usr/sbin:/usr/local/bin:/usr/local/sbin", "PATH=" + stubs + ":/bin:/usr/bin"},
		{"ALLOW=/usr/local/etc/vordr-gna-allow", "ALLOW=" + filepath.Join(d, "allow")},
		{"STATE=/var/db/vordr-gna\n", "STATE=" + b.state + "\n"},
		{"QUEUE=/var/db/vordr-gna-queue\n", "QUEUE=" + b.queue + "\n"},
	}
	b.forced = install(t, "vordr-gna", d, append(common,
		[2]string{"CONFIG=/cf/conf/config.xml", "CONFIG=" + filepath.Join(d, "config.xml")},
		[2]string{"VERSION_FILE=/etc/version", "VERSION_FILE=" + filepath.Join(d, "version")}))
	b.root = install(t, "vordr-gna-root", d, append(common,
		[2]string{"UPGRADE_LOCK=/tmp/pfSense-upgrade.lock", "UPGRADE_LOCK=" + filepath.Join(d, "upgrade.lock")}))
	return b
}

// install copies a script with its fixed paths pointed into the temp dir. A path that is not in the script fails the
// test, so a renamed path cannot silently point the test at the real system.
func install(t *testing.T, name, dir string, paths [][2]string) string {
	t.Helper()
	raw, err := os.ReadFile(name)
	must(t, err)
	s := string(raw)
	for _, p := range paths {
		if !strings.Contains(s, p[0]) {
			t.Fatalf("%s: %q not found; update the test with the script", name, strings.TrimSpace(p[0]))
		}
		s = strings.ReplaceAll(s, p[0], p[1])
	}
	out := filepath.Join(dir, name)
	write(t, out, s, 0o755)
	return out
}

// call runs the forced command as the caller's SSH command would.
func (b *box) call(cmd string, env ...string) (string, int) {
	b.t.Helper()
	c := exec.Command("sh", b.forced)
	c.Dir = b.dir
	c.Env = append([]string{"SSH_ORIGINAL_COMMAND=" + cmd, "PATH=/bin:/usr/bin"}, env...)
	out, _ := c.Output()
	return string(out), c.ProcessState.ExitCode()
}

func (b *box) runRoot(env ...string) {
	b.t.Helper()
	c := exec.Command("sh", b.root)
	c.Env = append([]string{"PATH=/bin:/usr/bin"}, env...)
	if out, err := c.CombinedOutput(); err != nil {
		b.t.Fatalf("root job: %v\n%s", err, out)
	}
}

func (b *box) tableHas(a string) bool {
	raw, _ := os.ReadFile(b.table)
	for _, l := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(l) == a {
			return true
		}
	}
	return false
}

func (b *box) entry(a string) (int64, bool) {
	raw, _ := os.ReadFile(filepath.Join(b.state, "window"))
	for _, l := range strings.Split(string(raw), "\n") {
		f := strings.Fields(strings.TrimPrefix(l, "entry="))
		if strings.HasPrefix(l, "entry=") && len(f) == 2 && f[0] == a {
			n, err := strconv.ParseInt(f[1], 10, 64)
			return n, err == nil
		}
	}
	return 0, false
}

func (b *box) enqueue(content string) {
	b.t.Helper()
	write(b.t, filepath.Join(b.queue, fmt.Sprintf("req.%d", time.Now().UnixNano())), content, 0o600)
}

func TestForcedCommandRefuses(t *testing.T) {
	b := newBox(t)
	must(t, os.Mkdir(b.queue, 0o700))
	for _, tc := range []struct {
		cmd    string
		code   int
		reason string
	}{
		{"", 2, "unknown_call"},
		{"sh", 2, "unknown_call"},
		{"/bin/sh -c id", 2, "unknown_call"},
		{"version extra", 2, "bad_arguments"},
		{"backup ; sh", 2, "bad_arguments"},
		{"window", 2, "unknown_call"},
		{"window frobnicate", 2, "unknown_call"},
		{"window status now", 2, "bad_arguments"},
		{"window open " + listed, 2, "bad_arguments"},
		{"window open " + listed + " 5 ; id", 2, "bad_arguments"},
		{"window open 192.0.2.49 5", 3, "not_listed"},
		{"window open 198.51.100.9 5", 3, "not_listed"},
		{"window open 192.0.2.040 5", 3, "not_listed"},
		{"window open 1.2.3.256 5", 2, "bad_address"},
		{"window open 192.0.2.40/32 5", 2, "bad_address"},
		{"window open 1..2.3 5", 2, "bad_address"},
		{"window open .1.2.3 5", 2, "bad_address"},
		{"window open 1.2.3.4. 5", 2, "bad_address"},
		{"window open 1.2.3 5", 2, "bad_address"},
		{"window open 1.2.3.4.5 5", 2, "bad_address"},
		{"window open 1.2.3.1000 5", 2, "bad_address"},
		{"window open * 5", 2, "bad_address"},
		{"window open $(id) 5", 2, "bad_address"},
		{"window open " + listed + " 0", 2, "bad_minutes"},
		{"window open " + listed + " 00", 2, "bad_minutes"},
		{"window open " + listed + " 05", 2, "bad_minutes"},
		{"window open " + listed + " 08", 2, "bad_minutes"},
		{"window open " + listed + " 31", 2, "bad_minutes"},
		{"window open " + listed + " 100", 2, "bad_minutes"},
		{"window open " + listed + " 5x", 2, "bad_minutes"},
		{"window open " + listed + " -5", 2, "bad_minutes"},
		{"window close 192.0.2.49", 3, "not_listed"},
		{"window close 999.1.1.1", 2, "bad_address"},
		// ADR-0205: an install names its kind, an add-on and an id, nothing else.
		{"install", 2, "unknown_call"},
		{"install upgrade 0123456789abcdef", 2, "unknown_call"},
		{"install release", 2, "bad_arguments"},
		{"install release 0123456789abcdef now", 2, "bad_arguments"},
		{"install release 0123456789ABCDEF", 2, "bad_id"},
		{"install release 0123456", 2, "bad_id"},
		{"install release " + strings.Repeat("a", 33), 2, "bad_id"},
		{"install release $(id)", 2, "bad_id"},
		{"install addon pfSense-pkg-acme", 2, "bad_arguments"},
		{"install addon php85 0123456789abcdef", 2, "bad_name"},
		{"install addon pfSense-pkg- 0123456789abcdef", 2, "bad_name"},
		{"install addon pfSense-pkg-a;id 0123456789abcdef", 2, "bad_name"},
		{"install addon pfSense-pkg-" + strings.Repeat("a", 37) + " 0123456789abcdef", 2, "bad_name"},
		{"install addon pfSense-pkg-acme xyz", 2, "bad_id"},
		{"install status", 2, "bad_arguments"},
		{"install status ../../etc", 2, "bad_id"},
	} {
		out, code := b.call(tc.cmd)
		if code != tc.code || !strings.Contains(out, "state=refused\n") || !strings.Contains(out, "reason="+tc.reason+"\n") {
			t.Errorf("%q: exit %d, %q; want exit %d with reason %s", tc.cmd, code, out, tc.code, tc.reason)
		}
	}
	if left, _ := os.ReadDir(b.queue); len(left) != 0 {
		t.Errorf("refused calls left %d files in the queue", len(left))
	}
}

func TestForcedCommandAnswers(t *testing.T) {
	b := newBox(t)
	out, code := b.call("version")
	if code != 0 || out != "product=pfSense\nversion=1.2.3-RELEASE\nupdates_checked_at=never\n" {
		t.Errorf("version before the root job: exit %d %q", code, out)
	}
	out, code = b.call("backup")
	want, _ := os.ReadFile(filepath.Join(b.dir, "config.xml"))
	if code != 0 || out != string(want) {
		t.Errorf("backup: exit %d, %d bytes, want the config", code, len(out))
	}
	out, code = b.call("window status")
	if code != 0 || out != "window_checked_at=never\nrequests_pending=0\n" {
		t.Errorf("window status before the root job: exit %d %q", code, out)
	}
	if out, code = b.call("window open " + listed + " 5"); code != 4 || !strings.Contains(out, "reason=not_ready\n") {
		t.Errorf("open without a queue: exit %d %q, want not_ready", code, out)
	}
	// A queue that is a symlink is not the root job's queue: refuse, and write nothing through it.
	elsewhere := filepath.Join(b.dir, "elsewhere")
	must(t, os.Mkdir(elsewhere, 0o700))
	must(t, os.Symlink(elsewhere, b.queue))
	if out, code = b.call("window open " + listed + " 5"); code != 4 || !strings.Contains(out, "reason=not_ready\n") {
		t.Errorf("open with a symlinked queue: exit %d %q, want not_ready", code, out)
	}
	if left, _ := os.ReadDir(elsewhere); len(left) != 0 {
		t.Errorf("wrote %d files through the symlink", len(left))
	}
	must(t, os.Remove(b.queue))
	b.runRoot()
	out, code = b.call("window open " + listed + " 30")
	if code != 0 || !strings.HasPrefix(out, "state=requested\nrequest=req.") || !strings.Contains(out, "applies_within_s=60\n") {
		t.Fatalf("open: exit %d %q", code, out)
	}
	files, _ := filepath.Glob(filepath.Join(b.queue, "req.*"))
	if len(files) != 1 {
		t.Fatalf("queue holds %d requests, want 1", len(files))
	}
	if raw, _ := os.ReadFile(files[0]); string(raw) != "open "+listed+" 30\n" {
		t.Errorf("request reads %q", raw)
	}
	if out, _ = b.call("window status"); !strings.Contains(out, "requests_pending=1\n") {
		t.Errorf("status with one request: %q", out)
	}
}

func TestRootJobOpensClosesAndExpires(t *testing.T) {
	b := newBox(t)
	b.runRoot()
	b.call("window open " + listed + " 5")
	before := time.Now().Unix()
	b.runRoot()
	closes, ok := b.entry(listed)
	if !b.tableHas(listed) || !ok || closes < before+5*60-5 || closes > before+5*60+5 {
		t.Fatalf("after open: table %v, entry %d %v", b.tableHas(listed), closes, ok)
	}
	if left, _ := filepath.Glob(filepath.Join(b.queue, "req.*")); len(left) != 0 {
		t.Errorf("root job left %d requests", len(left))
	}
	out, _ := b.call("window status")
	if !strings.Contains(out, "entry="+listed+" ") || !strings.Contains(out, "requests_pending=0\n") {
		t.Errorf("status after open: %q", out)
	}

	b.call("window close " + listed)
	b.runRoot()
	if _, ok := b.entry(listed); b.tableHas(listed) || ok {
		t.Errorf("after close: table %v, entry %v", b.tableHas(listed), ok)
	}

	// Expired: no further call, only the next run.
	write(t, filepath.Join(b.state, "window"), fmt.Sprintf("window_checked_at=1\nentry=%s %d\n", listed, time.Now().Unix()-1), 0o644)
	write(t, b.table, listed+"\n", 0o644)
	b.runRoot()
	if _, ok := b.entry(listed); b.tableHas(listed) || ok {
		t.Errorf("after expiry: table %v, entry %v", b.tableHas(listed), ok)
	}

	// No longer listed: closed although not expired.
	write(t, filepath.Join(b.state, "window"), fmt.Sprintf("entry=%s %d\n", listed, time.Now().Unix()+600), 0o644)
	write(t, b.table, listed+"\n", 0o644)
	write(t, filepath.Join(b.dir, "allow"), "# empty now\n", 0o644)
	b.runRoot()
	if _, ok := b.entry(listed); b.tableHas(listed) || ok {
		t.Errorf("unlisted: table %v, entry %v", b.tableHas(listed), ok)
	}
}

func TestRootJobDistrustsTheQueue(t *testing.T) {
	b := newBox(t)
	b.runRoot()
	b.enqueue("open 192.0.2.49 5\n")
	b.enqueue("open " + listed + " 31\n")
	b.enqueue("open " + listed + " 99\n")
	b.enqueue("open " + listed + " 08\n")
	b.enqueue("open " + listed + "\n")
	b.enqueue("open " + listed + " 5 extra\n")
	b.enqueue("flush all\n")
	b.enqueue("open * 5\n")
	b.enqueue(strings.Repeat("x", 500) + " open " + listed + " 5\n")
	target := filepath.Join(b.dir, "secret")
	write(t, target, "open "+listed+" 5\n", 0o600)
	must(t, os.Symlink(target, filepath.Join(b.queue, "req.link")))
	b.runRoot()
	if raw, _ := os.ReadFile(b.table); strings.TrimSpace(string(raw)) != "" {
		t.Errorf("table after hostile requests: %q", raw)
	}
	if left, _ := os.ReadDir(b.queue); len(left) != 0 {
		t.Errorf("queue not emptied: %d entries", len(left))
	}
	if raw, _ := os.ReadFile(target); string(raw) != "open "+listed+" 5\n" {
		t.Error("the symlink's target was changed")
	}
	logged, _ := os.ReadFile(filepath.Join(b.dir, "log"))
	if strings.Contains(string(logged), "xxxx") {
		t.Error("request content reached the log")
	}
}

func TestRootJobKeepsTableAndStateTogether(t *testing.T) {
	b := newBox(t)
	b.runRoot()
	// An address pf holds but the state does not know (state lost) goes.
	write(t, b.table, "198.51.100.1\n", 0o644)
	b.runRoot()
	if b.tableHas("198.51.100.1") {
		t.Error("unknown table entry kept")
	}
	// What pf refused does not count as open.
	b.call("window open " + listed + " 5")
	b.runRoot("PFCTL_FAIL=add")
	if _, ok := b.entry(listed); ok {
		t.Error("entry recorded although pfctl failed")
	}
	// A hard limit on top: the root job always asks pf to expire entries older than 30 minutes.
	log, _ := os.ReadFile(filepath.Join(b.dir, "pfctl.log"))
	if !strings.Contains(string(log), "expire 1800\n") {
		t.Errorf("no expire 1800 in %q", log)
	}
}

func TestRootJobRepairsItsDirectories(t *testing.T) {
	b := newBox(t)
	elsewhere := filepath.Join(b.dir, "elsewhere")
	must(t, os.Mkdir(elsewhere, 0o755))
	must(t, os.Symlink(elsewhere, b.queue))
	b.runRoot()
	if fi, err := os.Lstat(b.queue); err != nil || !fi.IsDir() {
		t.Fatalf("queue after run: %v %v", fi, err)
	}
	if fi, _ := os.Stat(b.queue); fi.Mode().Perm() != 0o700 {
		t.Errorf("queue mode %v", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(filepath.Join(b.state, "window")); fi.Mode().Perm() != 0o644 {
		t.Errorf("window mode %v", fi.Mode().Perm())
	}
}

// versionFacts: the forced command's version answer, with the time of the last reading replaced by N.
func (b *box) versionFacts() string {
	b.t.Helper()
	out, code := b.call("version")
	lines := strings.Split(out, "\n")
	if code != 0 || len(lines) < 3 || !strings.HasPrefix(lines[2], "updates_checked_at=") {
		b.t.Fatalf("version: exit %d %q", code, out)
	}
	if _, err := strconv.ParseInt(strings.TrimPrefix(lines[2], "updates_checked_at="), 10, 64); err != nil {
		b.t.Fatalf("updates_checked_at is no time: %q", lines[2])
	}
	lines[2] = "updates_checked_at=N"
	return strings.Join(lines, "\n")
}

// Only "<" is an update: the catalog has a newer version. Each one is counted, listed in the earlier form, and named
// with its installed and offered version (ADR-0198).
func TestRootJobUpdateFacts(t *testing.T) {
	b := newBox(t)
	b.runRoot()
	want := "product=pfSense\nversion=1.2.3-RELEASE\nupdates_checked_at=N\nupdates_format=3\n" +
		"base_installed=1.2.3\nbase_offered=1.2.3\npackages_outdated_count=3\n" +
		"packages_outdated=pkg-a-1.0,web-c-1.24.0_12,3,vendor-pkg-f-1.2.0\n" +
		"package_offer=pkg-a 1.0 1.1\npackage_offer=web-c 1.24.0_12,3 1.26.0,3\n" +
		"package_offer=vendor-pkg-f 1.2.0 1.3.0_1\n" +
		// Form 3 (ADR-0204): no release and no add-on offered; the three come with the next release.
		"release_installed=1.2.3\nrelease_offered=\nrelease_other=\naddon_offer_count=0\npackages_behind_release=3\n"
	if got := b.versionFacts(); got != want {
		t.Errorf("version:\n%s\nwant:\n%s", got, want)
	}

	// Within the hour the facts stay; after it they are read again.
	write(t, filepath.Join(b.dir, "outdated"), "", 0o644)
	b.runRoot()
	if got := b.versionFacts(); got != want {
		t.Errorf("facts refreshed within the hour:\n%s", got)
	}
	old := time.Now().Add(-2 * time.Hour)
	must(t, os.Chtimes(filepath.Join(b.state, "updates"), old, old))
	b.runRoot()
	if got := b.versionFacts(); !strings.HasSuffix(got, "updates_format=3\nbase_installed=1.2.3\nbase_offered=1.2.3\n"+
		"packages_outdated_count=0\npackages_outdated=\nrelease_installed=1.2.3\nrelease_offered=\nrelease_other=\n"+
		"addon_offer_count=0\npackages_behind_release=0\n") {
		t.Errorf("facts not refreshed after two hours:\n%s", got)
	}
}

// ADR-0204: the release is offered when the meta package or a core package of the configured branch is newer, at the
// newest such version; add-ons get their own lines; the release packages and the add-ons are not behind the release.
func TestRootJobNamesReleaseAndAddOns(t *testing.T) {
	b := newBox(t)
	write(t, filepath.Join(b.dir, "offered", "pfSense-kernel-pfSense"), "1.2.10\n", 0o644)
	write(t, filepath.Join(b.dir, "offered", "pfSense"), "1.2.9\n", 0o644)
	write(t, filepath.Join(b.dir, "outdated"), pkgVersion+
		"pfSense-kernel-pfSense-1.2.3          <   needs updating (remote has 1.2.10)\n"+
		"pfSense-1.2.3                         <   needs updating (remote has 1.2.9)\n"+
		"pfSense-pkg-acme-1.2                  <   needs updating (remote has 1.3.2)\n"+
		"pfSense-pkg-nmap-1.4.4_11             <   needs updating (remote has 1.4.4_12)\n", 0o644)
	b.runRoot()
	got := b.versionFacts()
	for _, line := range []string{"release_installed=1.2.3\n", "release_offered=1.2.10\n", "addon_offer_count=2\n",
		"addon_offer=pfSense-pkg-acme 1.2 1.3.2\n", "addon_offer=pfSense-pkg-nmap 1.4.4_11 1.4.4_12\n",
		"packages_behind_release=3\n", "packages_outdated_count=7\n"} {
		if !strings.Contains(got, line) {
			t.Errorf("missing %q:\n%s", line, got)
		}
	}

	// A core package installed newer than its siblings makes the installed release; an offer older than it is none.
	write(t, filepath.Join(b.dir, "installed", "pfSense-base"), "1.2.11\n", 0o644)
	write(t, filepath.Join(b.dir, "offered", "pfSense-kernel-pfSense"), "1.2.3\n", 0o644)
	write(t, filepath.Join(b.dir, "offered", "pfSense"), "1.2.3\n", 0o644)
	old := time.Now().Add(-2 * time.Hour)
	must(t, os.Chtimes(filepath.Join(b.state, "updates"), old, old))
	b.runRoot()
	if got := b.versionFacts(); !strings.Contains(got, "release_installed=1.2.11\nrelease_offered=\n") {
		t.Errorf("installed release not the newest, or an older offer named:\n%s", got)
	}

	// An add-on line whose versions pkg did not give is counted but not named: the agent refuses such facts.
	write(t, filepath.Join(b.dir, "outdated"), "pfSense-pkg-odd-1.0                  <\n", 0o644)
	must(t, os.Chtimes(filepath.Join(b.state, "updates"), old, old))
	b.runRoot()
	if got := b.versionFacts(); !strings.Contains(got, "addon_offer_count=1\npackages_behind_release=0\n") ||
		strings.Contains(got, "addon_offer=") {
		t.Errorf("unread add-on not counted, or named:\n%s", got)
	}
}

// ADR-0204: a newer release in another branch is asked once a day; exit 2 names it, exit 0 clears it, any other end
// and an answer in another form keep the last value. A new value refreshes the facts at once.
func TestRootJobAsksOtherBranchesOnceADay(t *testing.T) {
	b := newBox(t)
	write(t, filepath.Join(b.dir, "upgrade-answer"), ">>> Updating repositories metadata...\n1.3.0 version of pfSense is available\n", 0o644)
	write(t, filepath.Join(b.dir, "upgrade-rc"), "2\n", 0o644)
	b.runRoot()
	if got := b.versionFacts(); !strings.Contains(got, "release_other=1.3.0\n") {
		t.Errorf("other branch not named:\n%s", got)
	}
	asks := func() int {
		raw, _ := os.ReadFile(filepath.Join(b.dir, "upgrade.log"))
		return strings.Count(string(raw), "-C -U\n")
	}
	b.runRoot()
	if n := asks(); n != 1 {
		t.Errorf("asked %d times within the day, want 1", n)
	}

	yesterday := time.Now().Add(-25 * time.Hour)
	for _, end := range []struct{ rc, answer, want string }{
		{"75", "Another instance is already running... Aborting!\n", "1.3.0"}, // keeps the last value
		{"2", "something else\n", "1.3.0"},                                    // an answer it cannot read
		{"2", "1.4.0 version of pfSense is available\n", "1.4.0"},
		{"0", "Your system is up to date\n", ""},
	} {
		write(t, filepath.Join(b.dir, "upgrade-rc"), end.rc+"\n", 0o644)
		write(t, filepath.Join(b.dir, "upgrade-answer"), end.answer, 0o644)
		must(t, os.Chtimes(filepath.Join(b.state, "release_other"), yesterday, yesterday))
		b.runRoot()
		if got := b.versionFacts(); !strings.Contains(got, "release_other="+end.want+"\n") {
			t.Errorf("exit %s %q: want release_other=%s:\n%s", end.rc, end.answer, end.want, got)
		}
	}
}

// Facts an earlier root job wrote (no updates_format) are read again at once, not only after the hour: the new form is
// there within a minute of the install.
func TestRootJobReplacesAnEarlierForm(t *testing.T) {
	b := newBox(t)
	b.runRoot()
	write(t, filepath.Join(b.state, "updates"), "updates_checked_at=1\nbase_installed=1.2.3\nbase_offered=1.2.3\n"+
		"packages_outdated_count=4\n"+
		"packages_outdated=pkg-a-1.0,pkg-tool-b-2.0_1,web-c-1.24.0_12,3,orphan-d-0.1\n", 0o644)
	b.runRoot()
	if got := b.versionFacts(); !strings.Contains(got, "updates_format=3\n") ||
		!strings.Contains(got, "packages_outdated_count=3\n") ||
		!strings.Contains(got, "package_offer=pkg-a 1.0 1.1\n") {
		t.Errorf("earlier form kept:\n%s", got)
	}
}

// A "<" line whose versions the root job cannot read is still counted and listed, but gets no package_offer line:
// the agent then refuses the facts instead of leaving the package out (ADR-0198).
func TestRootJobCountsALineItCannotRead(t *testing.T) {
	b := newBox(t)
	write(t, filepath.Join(b.dir, "outdated"), pkgVersion+
		"odd-g-1.0                          <\n"+
		"nodash                             <   needs updating (remote has 2)\n"+
		"other-h-1.0                        <   needs updating (catalog says 1.1)\n", 0o644)
	b.runRoot()
	got := b.versionFacts()
	if !strings.Contains(got, "packages_outdated_count=6\n"+
		"packages_outdated=pkg-a-1.0,web-c-1.24.0_12,3,vendor-pkg-f-1.2.0,odd-g-1.0,nodash,other-h-1.0\n") {
		t.Errorf("not every \"<\" counted and listed:\n%s", got)
	}
	if n := strings.Count(got, "package_offer="); n != 3 {
		t.Errorf("%d package_offer lines, want 3 (only those read):\n%s", n, got)
	}
}

const installID = "0123456789abcdef01234567"

// keyMap reads key=value lines; the first value of a key wins.
func keyMap(s string) map[string]string {
	m := map[string]string{}
	for _, l := range strings.Split(s, "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			if _, seen := m[k]; !seen {
				m[k] = v
			}
		}
	}
	return m
}

// installStatus: the forced command's answer to install status for id.
func (b *box) installStatus(id string) map[string]string {
	b.t.Helper()
	out, code := b.call("install status " + id)
	if code != 0 {
		b.t.Fatalf("install status: exit %d %q", code, out)
	}
	return keyMap(out)
}

func (b *box) upgradeCalls() []string {
	raw, _ := os.ReadFile(filepath.Join(b.dir, "upgrade.log"))
	var calls []string
	for _, l := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(l, "-y ") {
			calls = append(calls, l)
		}
	}
	return calls
}

// ADR-0205: the forced command queues a release install; the root job runs it with pfSense-upgrade -y, with the log
// in its state directory, and reports ok; the update facts are read again after it, at a time not before its end.
func TestRootJobInstallsTheRelease(t *testing.T) {
	b := newBox(t)
	b.runRoot()
	write(t, filepath.Join(b.dir, "offered", "pfSense"), "1.2.4\n", 0o644)
	write(t, filepath.Join(b.dir, "offered", "pfSense-base"), "1.2.4\n", 0o644)
	write(t, filepath.Join(b.dir, "install-slow"), "", 0o644) // the install ends in a later second than it began
	if st := b.installStatus(installID); st["install_state"] != "unknown" {
		t.Errorf("before any request: %v", st)
	}
	if out, code := b.call("install release " + installID); code != 0 || !strings.Contains(out, "state=requested\n") {
		t.Fatalf("install release: exit %d %q", code, out)
	}
	if st := b.installStatus(installID); st["install_state"] != "queued" {
		t.Errorf("queued: %v", st)
	}
	b.runRoot()
	if calls := b.upgradeCalls(); len(calls) != 1 || calls[0] != "-y -l "+filepath.Join(b.state, "upgrade.log") {
		t.Errorf("pfSense-upgrade calls: %q", calls)
	}
	st := b.installStatus(installID)
	if st["install_state"] != "ok" || st["install_kind"] != "release" || st["install_rc"] != "0" ||
		st["install_code"] != "" || st["install_boot"] != "1000" {
		t.Errorf("after the run: %v", st)
	}
	facts := keyMap(b.callOut("version"))
	checked, _ := strconv.ParseInt(facts["updates_checked_at"], 10, 64)
	finished, _ := strconv.ParseInt(st["install_finished"], 10, 64)
	if facts["release_installed"] != "1.2.4" || facts["release_offered"] != "" || checked < finished || finished == 0 {
		t.Errorf("facts after the install: %v (finished %d)", facts, finished)
	}
}

// An add-on is installed with pfSense-upgrade -y -i <name>.
func TestRootJobInstallsAnAddOn(t *testing.T) {
	b := newBox(t)
	b.runRoot()
	write(t, filepath.Join(b.dir, "installed", "pfSense-pkg-acme"), "1.2\n", 0o644)
	write(t, filepath.Join(b.dir, "offered", "pfSense-pkg-acme"), "1.3.2\n", 0o644)
	b.call("install addon pfSense-pkg-acme " + installID)
	b.runRoot()
	want := "-y -l " + filepath.Join(b.state, "upgrade.log") + " -i pfSense-pkg-acme"
	if calls := b.upgradeCalls(); len(calls) != 1 || calls[0] != want {
		t.Errorf("pfSense-upgrade calls: %q, want %q", calls, want)
	}
	if st := b.installStatus(installID); st["install_state"] != "ok" || st["install_name"] != "pfSense-pkg-acme" {
		t.Errorf("after the run: %v", st)
	}
}

// The root job checks an install again before it runs: an offer that is gone and another run of the updater are
// refused, and pfSense-upgrade is not started. A request in another form never becomes an install, and only the first
// install request of a run counts.
func TestRootJobRefusesAnInstall(t *testing.T) {
	b := newBox(t)
	b.runRoot()
	b.call("install release " + installID)
	b.runRoot()
	if st := b.installStatus(installID); st["install_state"] != "refused" || st["install_code"] != "not_offered" {
		t.Errorf("release not offered: %v", st)
	}
	write(t, filepath.Join(b.dir, "installed", "pfSense-pkg-acme"), "1.3.2\n", 0o644)
	write(t, filepath.Join(b.dir, "offered", "pfSense-pkg-acme"), "1.3.2\n", 0o644)
	b.call("install addon pfSense-pkg-acme " + installID)
	b.runRoot()
	if st := b.installStatus(installID); st["install_state"] != "refused" || st["install_code"] != "not_offered" {
		t.Errorf("add-on not newer: %v", st)
	}
	b.call("install addon pfSense-pkg-gone " + installID)
	b.runRoot()
	if st := b.installStatus(installID); st["install_code"] != "not_offered" {
		t.Errorf("add-on not installed: %v", st)
	}

	write(t, filepath.Join(b.dir, "offered", "pfSense"), "1.2.4\n", 0o644)
	write(t, filepath.Join(b.dir, "upgrade.lock"), "", 0o644)
	b.call("install release " + installID)
	b.runRoot()
	if st := b.installStatus(installID); st["install_state"] != "refused" || st["install_code"] != "busy" {
		t.Errorf("updater running: %v", st)
	}
	must(t, os.Remove(filepath.Join(b.dir, "upgrade.lock")))

	// Written into the queue directly, past the forced command: none of these is run - also where the package named
	// would have a newer version - and the last install's state stays as it was.
	write(t, filepath.Join(b.dir, "installed", "php85"), "8.5.2\n", 0o644)
	write(t, filepath.Join(b.dir, "offered", "php85"), "8.5.12\n", 0o644)
	for _, line := range []string{"install release " + installID + " extra", "install release XYZ",
		"install addon php85 " + installID, "install addon pfSense-pkg-a;id " + installID, "install now"} {
		b.enqueue(line)
	}
	b.runRoot()
	if calls := b.upgradeCalls(); len(calls) != 0 {
		t.Errorf("pfSense-upgrade started for a refused request: %q", calls)
	}
	if st := b.installStatus(installID); st["install_code"] != "busy" {
		t.Errorf("a refused request changed the install's state: %v", st)
	}

	// Two in one run: the first is run, the second dropped and logged.
	b.call("install release " + installID)
	b.call("install release fedcba9876543210")
	b.runRoot()
	if calls := b.upgradeCalls(); len(calls) != 1 {
		t.Errorf("%d installs in one run, want 1: %q", len(calls), calls)
	}
	first, second := b.installStatus(installID)["install_state"], b.installStatus("fedcba9876543210")["install_state"]
	if !(first == "ok" && second == "unknown" || first == "unknown" && second == "ok") {
		t.Errorf("two requests in one run: %s and %s, want one ok and one unknown", first, second)
	}
	if raw, _ := os.ReadFile(filepath.Join(b.dir, "log")); !strings.Contains(string(raw), "one install per run") {
		t.Errorf("the dropped request is not logged:\n%s", raw)
	}
}

// pfSense-upgrade ending other than 0: 75 (another instance) is refused as busy, anything else failed with its code.
func TestRootJobReportsAFailedInstall(t *testing.T) {
	b := newBox(t)
	b.runRoot()
	write(t, filepath.Join(b.dir, "offered", "pfSense"), "1.2.4\n", 0o644)
	for _, tc := range []struct{ rc, state, code string }{{"1", "failed", "failed"}, {"75", "refused", "busy"}} {
		write(t, filepath.Join(b.dir, "install-rc"), tc.rc+"\n", 0o644)
		b.call("install release " + installID)
		b.runRoot()
		st := b.installStatus(installID)
		if st["install_state"] != tc.state || st["install_code"] != tc.code || st["install_rc"] != tc.rc {
			t.Errorf("exit %s: %v", tc.rc, st)
		}
	}
}

// A run that ended with the install still running: after a reboot it is rebooted, in the same boot failed as
// interrupted, and while the updater holds its lock (a later boot stage) it stays running. The facts are read again.
func TestRootJobNotesTheEndOfAnInterruptedInstall(t *testing.T) {
	b := newBox(t)
	b.runRoot()
	running := "install_job=" + installID + "\ninstall_kind=release\ninstall_name=\ninstall_state=running\n" +
		"install_code=\ninstall_rc=\ninstall_started=5\ninstall_finished=\ninstall_boot=900\n"
	write(t, filepath.Join(b.state, "install"), running, 0o644)
	write(t, filepath.Join(b.dir, "upgrade.lock"), "", 0o644)
	b.runRoot()
	if st := b.installStatus(installID); st["install_state"] != "running" {
		t.Errorf("updater still holds its lock: %v", st)
	}
	must(t, os.Remove(filepath.Join(b.dir, "upgrade.lock")))
	before := keyMap(b.callOut("version"))["updates_checked_at"]
	time.Sleep(1100 * time.Millisecond)
	b.runRoot()
	st := b.installStatus(installID)
	if st["install_state"] != "rebooted" || st["install_started"] != "5" || st["install_finished"] == "" {
		t.Errorf("after the reboot: %v", st)
	}
	if after := keyMap(b.callOut("version"))["updates_checked_at"]; after == before {
		t.Errorf("facts not read again after the reboot (%s)", after)
	}

	write(t, filepath.Join(b.state, "install"), strings.Replace(running, "install_boot=900", "install_boot=1000", 1),
		0o644)
	b.runRoot()
	if st := b.installStatus(installID); st["install_state"] != "failed" || st["install_code"] != "interrupted" {
		t.Errorf("same boot: %v", st)
	}
}

// ADR-0209: pfSense-upgrade ends 0 after a stage of a release that has another one to come, with the reboot started in
// the background and the next stage noted on the kernel package. The install stays running - after the updater's end,
// at the next run in the same boot without its lock, and in a boot between two stages - and is rebooted once the
// firewall booted again and the last stage deleted the note; the facts are read again then. An add-on install does not
// wait on a note that a broken release upgrade left.
func TestRootJobWaitsForTheUpdatersLastStage(t *testing.T) {
	b := newBox(t)
	b.runRoot()
	write(t, filepath.Join(b.dir, "offered", "pfSense"), "1.2.4\n", 0o644)
	write(t, filepath.Join(b.dir, "offered", "pfSense-base"), "1.2.4\n", 0o644)
	write(t, filepath.Join(b.dir, "install-stage"), "2\n", 0o644)
	if out, code := b.call("install release " + installID); code != 0 || !strings.Contains(out, "state=requested\n") {
		t.Fatalf("install release: exit %d %q", code, out)
	}
	b.runRoot()
	if st := b.installStatus(installID); st["install_state"] != "running" || st["install_finished"] != "" {
		t.Errorf("after the updater's first stage: %v", st)
	}
	b.runRoot() // the same boot: the updater's lock is gone, its reboot still to come
	if st := b.installStatus(installID); st["install_state"] != "running" {
		t.Errorf("next run in the same boot: %v", st)
	}
	write(t, filepath.Join(b.dir, "stage", "pfSense-kernel-pfSense"), "3\n", 0o644)
	write(t, filepath.Join(b.dir, "boot"), "2000\n", 0o644)
	b.runRoot()
	if st := b.installStatus(installID); st["install_state"] != "running" {
		t.Errorf("in a boot between two stages: %v", st)
	}
	must(t, os.Remove(filepath.Join(b.dir, "stage", "pfSense-kernel-pfSense")))
	write(t, filepath.Join(b.dir, "boot"), "3000\n", 0o644)
	before := keyMap(b.callOut("version"))["updates_checked_at"]
	time.Sleep(1100 * time.Millisecond)
	b.runRoot()
	if st := b.installStatus(installID); st["install_state"] != "rebooted" || st["install_finished"] == "" {
		t.Errorf("after the last stage: %v", st)
	}
	facts := keyMap(b.callOut("version"))
	if facts["updates_checked_at"] == before || facts["release_installed"] != "1.2.4" {
		t.Errorf("facts after the last stage: %v (before %s)", facts, before)
	}

	write(t, filepath.Join(b.dir, "stage", "pfSense-kernel-pfSense"), "2\n", 0o644)
	write(t, filepath.Join(b.dir, "installed", "pfSense-pkg-acme"), "1.2\n", 0o644)
	write(t, filepath.Join(b.dir, "offered", "pfSense-pkg-acme"), "1.3.2\n", 0o644)
	const addonID = "fedcba9876543210fedcba98"
	b.call("install addon pfSense-pkg-acme " + addonID)
	b.runRoot()
	if st := b.installStatus(addonID); st["install_state"] != "ok" || st["install_name"] != "pfSense-pkg-acme" {
		t.Errorf("add-on with a note left: %v", st)
	}
}

func (b *box) callOut(cmd string) string {
	b.t.Helper()
	out, code := b.call(cmd)
	if code != 0 {
		b.t.Fatalf("%s: exit %d %q", cmd, code, out)
	}
	return out
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	must(t, os.WriteFile(path, []byte(content), mode))
	must(t, os.Chmod(path, mode))
}
