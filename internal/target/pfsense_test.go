package target

import (
	"context"
	"encoding/base64"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/psewar/vordr/internal/policy"
	"github.com/psewar/vordr/internal/protocol"
)

const (
	pinnedKey  = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHZvcmRyLXRlc3QtaG9zdC1rZXktMDEyMzQ1Njc4OWFi"
	otherKey   = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOtherOtherOtherOtherOtherOtherOtherOther"
	privateKey = "-----BEGIN OPENSSH PRIVATE KEY-----\nAAAAtest\n-----END OPENSSH PRIVATE KEY-----\n"
)

// sshFake puts stand-ins for ssh and ssh-keyscan first in PATH. ssh checks what the driver hands it (key file 0600
// with the key, known_hosts with exactly the pinned key, no user configuration, batch mode) and answers like the
// firewall's forced command; FAKE_* in the environment choose the case.
type sshFake struct {
	t        *testing.T
	dir, log string
	host     string
	port     string
}

func newSSHFake(t *testing.T) *sshFake {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh here; the SSH stand-ins are shell scripts")
	}
	d := t.TempDir()
	f := &sshFake{t: t, dir: d, log: filepath.Join(d, "ssh.log")}
	bin := filepath.Join(d, "bin")
	must(t, os.Mkdir(bin, 0o755))
	write(t, filepath.Join(bin, "ssh-keyscan"), `#!/bin/sh
host=$(eval echo \${$#})
case "${FAKE_KEYSCAN:-}" in
fail) exit 1 ;;
none) exit 0 ;;
other) echo "# $host:22 SSH-2.0-OpenSSH_9.9"; echo "$host `+otherKey+`" ;;
*) echo "# $host:22 SSH-2.0-OpenSSH_9.9"; echo "$host `+pinnedKey+`" ;;
esac
`, 0o755)
	write(t, filepath.Join(bin, "ssh"), `#!/bin/sh
key=""; known=""; prev=""
for a in "$@"; do
  case "$prev" in
  -i) key=$a ;;
  -o) case "$a" in UserKnownHostsFile=*) known=${a#UserKnownHostsFile=} ;; esac ;;
  esac
  prev=$a
done
echo "args: $*" >> "`+f.log+`"
echo "key: $key $(stat -c %a "$key") $(cat "$key" | base64 -w0)" >> "`+f.log+`"
echo "known: $(cat "$known")" >> "`+f.log+`"
case "${FAKE_SSH:-}" in
auth) echo "vordr-gna@host: Permission denied (publickey)." >&2; exit 255 ;;
refuse) printf 'state=refused\nreason=not_ready\n'; exit 4 ;;
esac
eval "last=\${$#}"
case "$last" in
version) cat "$FAKE_VERSION" ;;
backup) cat "$FAKE_BACKUP" ;;
*) printf 'state=refused\nreason=unknown_call\n'; exit 2 ;;
esac
`, 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, err)
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	f.host, f.port, _ = net.SplitHostPort(ln.Addr().String())
	version := filepath.Join(d, "version")
	write(t, version, versionAnswer, 0o644)
	t.Setenv("FAKE_VERSION", version)
	backup := filepath.Join(d, "config.xml")
	write(t, backup, "<?xml version=\"1.0\"?>\n<pfsense>\n"+strings.Repeat("  <rule/>\n", 200)+"</pfsense>\n", 0o644)
	t.Setenv("FAKE_BACKUP", backup)
	return f
}

func (f *sshFake) driver() Driver {
	f.t.Helper()
	cred := filepath.Join(f.dir, "alpha.cred")
	write(f.t, cred, "KEY="+base64.StdEncoding.EncodeToString([]byte(privateKey))+"\n", 0o600)
	d, res := Open(&policy.Target{Driver: "pfsense", Address: "ssh://vordr-gna@" + net.JoinHostPort(f.host, f.port),
		HostKey: pinnedKey, Credentials: cred})
	if !res.OK() {
		f.t.Fatalf("open: %+v", res)
	}
	return d
}

func (f *sshFake) logged() string {
	raw, _ := os.ReadFile(f.log)
	return string(raw)
}

func TestPfsenseCheck(t *testing.T) {
	f := newSSHFake(t)
	st, res := f.driver().Check(context.Background())
	if !res.OK() {
		t.Fatalf("check: %+v", res)
	}
	if st.Product != "pfSense" || st.Installed != "1.2.3-RELEASE" || st.SelfUpdate != "off" {
		t.Errorf("status: %+v", st)
	}
	sameOffers(t, st.Offers, []Offer{{Name: "pfSense", Version: "1.3.0"},
		{Name: "package pkg-a", From: "1.0", Version: "1.1"},
		{Name: "package web-c", From: "1.24.0_12,3", Version: "1.26.0,3"}})
	if st.Facts["packages_outdated_count"] != "2" || st.Facts["base_offered"] != "1.3.0" ||
		st.Facts["updates_checked_at"] == "" || st.Facts["updates_format"] != "2" {
		t.Errorf("facts: %v", st.Facts)
	}
	log := f.logged()
	for _, want := range []string{"-F /dev/null", "-o BatchMode=yes", "-o IdentitiesOnly=yes", "-o IdentityAgent=none",
		"-o StrictHostKeyChecking=yes", "-o GlobalKnownHostsFile=/dev/null", "-l vordr-gna", "-p " + f.port,
		" -- " + f.host + " version"} {
		if !strings.Contains(log, want) {
			t.Errorf("ssh not called with %q:\n%s", want, log)
		}
	}
	if !strings.Contains(log, " 600 "+base64.StdEncoding.EncodeToString([]byte(privateKey))) {
		t.Errorf("key file not 0600 with the key:\n%s", log)
	}
	if !strings.Contains(log, "known: ["+f.host+"]:"+f.port+" "+pinnedKey+"\n") {
		t.Errorf("known_hosts is not exactly the pinned key:\n%s", log)
	}
	keyFile := strings.Fields(strings.SplitN(log[strings.Index(log, "key: "):], "\n", 2)[0])[1]
	if _, err := os.Stat(keyFile); !os.IsNotExist(err) {
		t.Errorf("key file %s left behind", keyFile)
	}
}

// versionAnswer: the forced command's version answer with the update facts of a root job in form 2, which names each
// outdated package with its installed and offered version (ADR-0198); one has an epoch after a comma.
const versionAnswer = "product=pfSense\nversion=1.2.3-RELEASE\nupdates_checked_at=1700000000\nupdates_format=2\n" +
	"base_installed=1.2.3\nbase_offered=1.3.0\npackages_outdated_count=2\n" +
	"packages_outdated=pkg-a-1.0,web-c-1.24.0_12,3\n" +
	"package_offer=pkg-a 1.0 1.1\npackage_offer=web-c 1.24.0_12,3 1.26.0,3\n"

func sameOffers(t *testing.T, got, want []Offer) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("offers: %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("offer %d: %+v, want %+v", i, got[i], want[i])
		}
	}
}

// A root job before form 2 writes no updates_format and names the packages only: they are offered as before, without
// a version and without what they replace, so that a new agent works against it until the root job is replaced.
func TestPfsenseEarlierRootJob(t *testing.T) {
	f := newSSHFake(t)
	write(t, os.Getenv("FAKE_VERSION"), "product=pfSense\nversion=1.2.3-RELEASE\nupdates_checked_at=1700000000\n"+
		"base_installed=1.2.3\nbase_offered=1.2.3\npackages_outdated_count=2\n"+
		"packages_outdated=pkg-a-1.0,pkg-b-2.0_1\n", 0o644)
	st, res := f.driver().Check(context.Background())
	if !res.OK() {
		t.Fatalf("check: %+v", res)
	}
	sameOffers(t, st.Offers, []Offer{{Name: "package pkg-a-1.0", Version: UnknownVersion},
		{Name: "package pkg-b-2.0_1", Version: UnknownVersion}})
	if _, ok := st.Facts["updates_format"]; ok {
		t.Errorf("facts name a form the root job did not: %v", st.Facts)
	}
}

// Update facts not in the form the root job writes are refused whole, not read in part: a package left out would
// look like no update (ADR-0198).
func TestPfsenseRefusesFactsItDoesNotRead(t *testing.T) {
	for name, edit := range map[string][2]string{
		"more counted than named":  {"packages_outdated_count=2\n", "packages_outdated_count=3\n"},
		"fewer counted than named": {"packages_outdated_count=2\n", "packages_outdated_count=1\n"},
		"no count":                 {"packages_outdated_count=2\n", ""},
		"a line without the offer": {"package_offer=pkg-a 1.0 1.1\n", "package_offer=pkg-a 1.0\n"},
		"a line with more words":   {"package_offer=pkg-a 1.0 1.1\n", "package_offer=pkg-a 1.0 1.1 extra\n"},
		"an empty line":            {"package_offer=pkg-a 1.0 1.1\n", "package_offer=\n"},
		"an earlier form number":   {"updates_format=2\n", "updates_format=1\n"},
		"a form that is no number": {"updates_format=2\n", "updates_format=two\n"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newSSHFake(t)
			if !strings.Contains(versionAnswer, edit[0]) {
				t.Fatalf("%q is not in the answer; update the test", edit[0])
			}
			write(t, os.Getenv("FAKE_VERSION"), strings.Replace(versionAnswer, edit[0], edit[1], 1), 0o644)
			st, res := f.driver().Check(context.Background())
			if res.Code != protocol.CodeRejectedUnsupported || res.Detail == "" || len(st.Offers) != 0 {
				t.Fatalf("%s: %+v, offers %+v", name, res, st.Offers)
			}
		})
	}
	// A later form keeps these lines: it is read like form 2.
	f := newSSHFake(t)
	later := strings.Replace(versionAnswer, "updates_format=2\n", "updates_format=3\n", 1)
	write(t, os.Getenv("FAKE_VERSION"), later, 0o644)
	if st, res := f.driver().Check(context.Background()); !res.OK() || len(st.Offers) != 3 {
		t.Fatalf("later form: %+v, offers %+v", res, st.Offers)
	}
}

func TestPfsenseUpToDate(t *testing.T) {
	f := newSSHFake(t)
	write(t, os.Getenv("FAKE_VERSION"), "product=pfSense\nversion=1.2.3-RELEASE\nupdates_format=2\n"+
		"base_installed=1.2.3\nbase_offered=1.2.3\npackages_outdated_count=0\npackages_outdated=\n", 0o644)
	st, res := f.driver().Check(context.Background())
	if !res.OK() || len(st.Offers) != 0 {
		t.Fatalf("up to date: %+v %+v", st.Offers, res)
	}
	// The same from a root job before form 2.
	write(t, os.Getenv("FAKE_VERSION"), "product=pfSense\nversion=1.2.3-RELEASE\nbase_installed=1.2.3\n"+
		"base_offered=1.2.3\npackages_outdated_count=0\npackages_outdated=\n", 0o644)
	st, res = f.driver().Check(context.Background())
	if !res.OK() || len(st.Offers) != 0 {
		t.Fatalf("up to date, earlier form: %+v %+v", st.Offers, res)
	}
}

func TestPfsenseCodes(t *testing.T) {
	for name, tc := range map[string]struct {
		env  map[string]string
		want protocol.Code
	}{
		"login refused":         {map[string]string{"FAKE_SSH": "auth"}, protocol.CodeFailedTargetAuth},
		"other host key":        {map[string]string{"FAKE_KEYSCAN": "other"}, protocol.CodeFailedTargetHostKey},
		"no host key":           {map[string]string{"FAKE_KEYSCAN": "none"}, protocol.CodeFailedTargetHostKey},
		"keyscan fails":         {map[string]string{"FAKE_KEYSCAN": "fail"}, protocol.CodePrecheckRepoUnreachable},
		"forced command refuse": {map[string]string{"FAKE_SSH": "refuse"}, protocol.CodeRejectedUnsupported},
	} {
		t.Run(name, func(t *testing.T) {
			f := newSSHFake(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			_, res := f.driver().Check(context.Background())
			if res.Code != tc.want {
				t.Fatalf("code %q (%s), want %q", res.Code, res.Detail, tc.want)
			}
			if tc.want == protocol.CodeFailedTargetHostKey && strings.Contains(f.logged(), "args:") {
				t.Error("ssh ran although the host key did not match")
			}
		})
	}
	t.Run("not reachable", func(t *testing.T) {
		f := newSSHFake(t)
		d := f.driver()
		// The port of the test's own end of a connection to the fake: nothing listens there, and nobody can bind it
		// while the connection lasts. A listener on :0 closed again is no such port: some virtualised network stacks
		// hold a duplicate of each new listener for some milliseconds, and until then it still accepts.
		c, err := net.Dial("tcp", net.JoinHostPort(f.host, f.port))
		must(t, err)
		defer c.Close()
		_, closed, _ := net.SplitHostPort(c.LocalAddr().String())
		d.(*pfsense).port = closed
		if _, res := d.Check(context.Background()); res.Code != protocol.CodePrecheckRepoUnreachable {
			t.Fatalf("closed port: %+v", res)
		}
	})
	t.Run("refusal reason", func(t *testing.T) {
		f := newSSHFake(t)
		t.Setenv("FAKE_SSH", "refuse")
		if _, res := f.driver().Check(context.Background()); !strings.Contains(res.Detail, "not_ready") {
			t.Fatalf("detail without the forced command's reason: %q", res.Detail)
		}
	})
	t.Run("no pfSense", func(t *testing.T) {
		f := newSSHFake(t)
		write(t, os.Getenv("FAKE_VERSION"), "product=other\nversion=1\n", 0o644)
		if _, res := f.driver().Check(context.Background()); res.Code != protocol.CodeRejectedUnsupported {
			t.Fatalf("foreign answer: %+v", res)
		}
	})
}

func TestPfsenseBackup(t *testing.T) {
	f := newSSHFake(t)
	data, ext, res := f.driver().Backup(context.Background())
	want, _ := os.ReadFile(os.Getenv("FAKE_BACKUP"))
	if !res.OK() || ext != ".xml" || string(data) != string(want) {
		t.Fatalf("backup: %+v ext %q, %d bytes", res, ext, len(data))
	}
	for name, content := range map[string]string{
		"too small": "<?xml version=\"1.0\"?>\n<pfsense></pfsense>\n",
		"no xml":    strings.Repeat("x", 2000) + "<pfsense>",
		"other xml": "<?xml version=\"1.0\"?>\n<opnsense>" + strings.Repeat(" ", 2000) + "</opnsense>",
	} {
		t.Run(name, func(t *testing.T) {
			write(t, os.Getenv("FAKE_BACKUP"), content, 0o644)
			if _, _, res := f.driver().Backup(context.Background()); res.Code != protocol.CodeFailedConfigBackup {
				t.Fatalf("%s: %+v", name, res)
			}
		})
	}
}

func TestPfsenseCredentials(t *testing.T) {
	f := newSSHFake(t)
	for name, content := range map[string]string{
		"no key":         "USER=x\n",
		"not base64":     "KEY=***\n",
		"no private key": "KEY=" + base64.StdEncoding.EncodeToString([]byte("hello")) + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			cred := filepath.Join(f.dir, "c.cred")
			write(t, cred, content, 0o600)
			_, res := Open(&policy.Target{Driver: "pfsense", Address: "ssh://vordr-gna@" + f.host, HostKey: pinnedKey,
				Credentials: cred})
			if res.Code != protocol.CodeFailedTargetAuth {
				t.Fatalf("%s: %+v", name, res)
			}
		})
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

func TestOffersHostKey(t *testing.T) {
	for scanned, want := range map[string]bool{
		"h " + pinnedKey + "\n":                         true,
		"# h:22 SSH-2.0\nh " + pinnedKey + "\n":         true,
		"h " + otherKey + "\n":                          false,
		"h ssh-rsa AAAA\nh " + pinnedKey + "x\n":        false,
		"# h " + pinnedKey + "\n":                       false,
		"":                                              false,
		"h " + pinnedKey + " extra\n":                   false,
		"h " + strings.Replace(pinnedKey, " ", "  ", 1): true,
	} {
		if got := offersHostKey([]byte(scanned), pinnedKey); got != want {
			t.Errorf("offersHostKey(%q) = %v, want %v", scanned, got, want)
		}
	}
}
