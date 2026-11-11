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
	write(t, filepath.Join(stubs, "pkg"), `#!/bin/sh
case "$*" in
"version -U -qRL=") cat "`+d+`/outdated" ;;
"query %v pfSense-base") echo 1.2.3 ;;
"rquery -U %v pfSense-base") echo 1.2.3 ;;
*) echo "unexpected pkg $*" >&2; exit 1 ;;
esac
`, 0o755)
	write(t, filepath.Join(stubs, "logger"), "#!/bin/sh\necho \"$*\" >> \""+d+"/log\"\n", 0o755)
	write(t, filepath.Join(stubs, "chown"), "#!/bin/sh\nexit 0\n", 0o755)
	write(t, filepath.Join(d, "outdated"), "pkg-a-1.0 <\npkg-b-2.0_1 <\n", 0o644)
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
	b.root = install(t, "vordr-gna-root", d, common)
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

func TestRootJobUpdateFacts(t *testing.T) {
	b := newBox(t)
	b.runRoot()
	out, _ := b.call("version")
	for _, want := range []string{"base_installed=1.2.3\n", "base_offered=1.2.3\n", "packages_outdated_count=2\n",
		"packages_outdated=pkg-a-1.0,pkg-b-2.0_1\n", "updates_checked_at="} {
		if !strings.Contains(out, want) {
			t.Errorf("version lacks %q: %q", want, out)
		}
	}
	// Within the hour the facts stay; after it they are read again.
	write(t, filepath.Join(b.dir, "outdated"), "", 0o644)
	b.runRoot()
	if out, _ = b.call("version"); !strings.Contains(out, "packages_outdated_count=2\n") {
		t.Errorf("facts refreshed within the hour: %q", out)
	}
	old := time.Now().Add(-2 * time.Hour)
	must(t, os.Chtimes(filepath.Join(b.state, "updates"), old, old))
	b.runRoot()
	if out, _ = b.call("version"); !strings.Contains(out, "packages_outdated_count=0\npackages_outdated=\n") {
		t.Errorf("facts not refreshed after two hours: %q", out)
	}
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
