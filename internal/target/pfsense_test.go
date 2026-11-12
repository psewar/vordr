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
	write(t, version, "product=pfSense\nversion=1.2.3-RELEASE\nupdates_checked_at=1700000000\nbase_installed=1.2.3\n"+
		"base_offered=1.3.0\npackages_outdated_count=2\npackages_outdated=pkg-a-1.0,pkg-b-2.0_1\n", 0o644)
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
	want := []Offer{{"pfSense", "1.3.0", false}, {"package pkg-a-1.0", UnknownVersion, false},
		{"package pkg-b-2.0_1", UnknownVersion, false}}
	if len(st.Offers) != len(want) {
		t.Fatalf("offers: %+v", st.Offers)
	}
	for i := range want {
		if st.Offers[i] != want[i] {
			t.Errorf("offer %d: %+v, want %+v", i, st.Offers[i], want[i])
		}
	}
	if st.Facts["packages_outdated_count"] != "2" || st.Facts["base_offered"] != "1.3.0" || st.Facts["updates_checked_at"] == "" {
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

func TestPfsenseUpToDate(t *testing.T) {
	f := newSSHFake(t)
	write(t, os.Getenv("FAKE_VERSION"), "product=pfSense\nversion=1.2.3-RELEASE\nbase_installed=1.2.3\n"+
		"base_offered=1.2.3\npackages_outdated_count=0\npackages_outdated=\n", 0o644)
	st, res := f.driver().Check(context.Background())
	if !res.OK() || len(st.Offers) != 0 {
		t.Fatalf("up to date: %+v %+v", st.Offers, res)
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
