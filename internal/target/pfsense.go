package target

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/psewar/vordr/internal/policy"
	"github.com/psewar/vordr/internal/protocol"
)

// pfsense speaks for a pfSense firewall through the forced command of its SSH key (deploy/pfsense, ADR-0093) and the
// system's OpenSSH client (ADR-0094). Whatever this driver sends, the firewall runs only that command; the driver
// uses two of its calls: version and backup.
type pfsense struct {
	user, host, port string
	hostKey          string // "<type> <base64>", pinned in agent.yaml
	key              []byte // the private key in OpenSSH format, from the credentials file
	// ssh and keyscan: the OpenSSH clients; tests put stand-ins in their place.
	ssh, keyscan string
	dialTimeout  time.Duration
}

func newPfsense(t *policy.Target, creds map[string]string) (*pfsense, Result) {
	u, err := url.Parse(t.Address) // check-config made sure of ssh://<user>@<host>[:port]
	if err != nil || u.User == nil {
		return nil, Result{protocol.CodeRejectedUnsupported, "target.address is not ssh://<user>@<host>[:port]"}
	}
	key, err := base64.StdEncoding.DecodeString(creds["KEY"])
	if creds["KEY"] == "" || err != nil || !bytes.Contains(key, []byte("PRIVATE KEY")) {
		return nil, Result{protocol.CodeFailedTargetAuth, "credentials need KEY: the base64 of the OpenSSH private key"}
	}
	port := u.Port()
	if port == "" {
		port = "22"
	}
	p := &pfsense{user: u.User.Username(), host: u.Hostname(), port: port, hostKey: t.HostKey, key: key,
		ssh: "ssh", keyscan: "ssh-keyscan", dialTimeout: 10 * time.Second}
	for _, bin := range []string{p.ssh, p.keyscan} {
		if _, err := exec.LookPath(bin); err != nil {
			return nil, Result{protocol.CodeRejectedUnsupported, bin + " is missing: the pfsense driver needs openssh-client"}
		}
	}
	return p, Result{}
}

func (p *pfsense) Name() string { return "pfsense" }

// updatesFormat is the first form of the root job's update facts that names each outdated package with its installed
// and its offered version (ADR-0198). A root job before it writes no updates_format.
const updatesFormat = 2

// Check reads the firmware and the outdated packages from the root job's update facts (written hourly from the local
// package catalog). The base system counts as offered when the catalog has a newer pfSense-base; every outdated
// package is one offer, from the version installed to the one in the catalog (packageOffers).
func (p *pfsense) Check(ctx context.Context) (Status, Result) {
	out, res := p.call(ctx, "version")
	if !res.OK() {
		return Status{}, res
	}
	kv := keyValues(out)
	if kv["product"] != "pfSense" || kv["version"] == "" {
		return Status{}, Result{protocol.CodeRejectedUnsupported, "the version answer names no pfSense version"}
	}
	st := Status{Product: "pfSense", Installed: kv["version"], SelfUpdate: "off", Facts: map[string]string{}}
	for _, k := range []string{"updates_checked_at", "updates_format", "base_installed", "base_offered",
		"packages_outdated_count"} {
		if v := kv[k]; v != "" {
			st.Facts[k] = v
		}
	}
	if offered := kv["base_offered"]; offered != "" && offered != kv["base_installed"] {
		st.Offers = append(st.Offers, Offer{Name: "pfSense", Version: offered})
	}
	packages, res := packageOffers(out, kv)
	if !res.OK() {
		return Status{}, res
	}
	st.Offers = append(st.Offers, packages...)
	return st, Result{}
}

// packageOffers reads the outdated packages of the update facts. From form 2 on (updates_format) one package_offer line
// per package names it with its installed and its offered version, and there are as many lines as
// packages_outdated_count says. An answer that is not so is not what the root job writes, and it is refused: the
// root job counts every package the catalog has a newer version of, and writes a line only for those whose versions
// it could read from pkg. A root job before form 2 writes no updates_format and names the packages only
// (packages_outdated, with their versions in the name): they are offered without a version, as before.
func packageOffers(out []byte, kv map[string]string) ([]Offer, Result) {
	var offers []Offer
	format := kv["updates_format"]
	if format == "" {
		for _, pkg := range strings.Split(kv["packages_outdated"], ",") {
			if pkg = strings.TrimSpace(pkg); pkg != "" {
				offers = append(offers, Offer{Name: "package " + pkg, Version: UnknownVersion})
			}
		}
		return offers, Result{}
	}
	if n, err := strconv.Atoi(format); err != nil || n < updatesFormat {
		return nil, Result{protocol.CodeRejectedUnsupported, fmt.Sprintf(
			"the update facts have updates_format %q; this agent reads form %d and later, or none",
			format, updatesFormat)}
	}
	lines := keyLines(out, "package_offer")
	for i, line := range lines {
		f := strings.Fields(line)
		if len(f) != 3 {
			return nil, Result{protocol.CodeRejectedUnsupported, fmt.Sprintf(
				"package_offer line %d is not <name> <installed> <offered>", i+1)}
		}
		offers = append(offers, Offer{Name: "package " + f[0], From: f[1], Version: f[2]})
	}
	if count := kv["packages_outdated_count"]; count != strconv.Itoa(len(lines)) {
		return nil, Result{protocol.CodeRejectedUnsupported, fmt.Sprintf(
			"the root job counts %q outdated packages but names the versions of %d: pkg version answered in a form "+
				"it does not read", count, len(lines))}
	}
	return offers, Result{}
}

// Backup reads config.xml and checks that it is one: an XML declaration, the root element <pfsense>, at least 1 KB.
func (p *pfsense) Backup(ctx context.Context) ([]byte, string, Result) {
	out, res := p.call(ctx, "backup")
	if !res.OK() {
		return nil, "", res
	}
	if len(out) < 1024 || !bytes.HasPrefix(bytes.TrimSpace(out), []byte("<?xml")) || !bytes.Contains(out, []byte("<pfsense>")) {
		return nil, "", Result{protocol.CodeFailedConfigBackup, fmt.Sprintf("the export is no pfSense config.xml (%d bytes)", len(out))}
	}
	return out, ".xml", Result{}
}

// call runs one call of the forced command. Before ssh it proves the way and the device, so that ssh's own failure
// can only mean one thing: the address answers (otherwise precheck_repo_unreachable), and the device offers the pinned
// host key (otherwise failed_target_hostkey). An ssh exit 255 after that is the login: failed_target_auth. Any other
// exit is the forced command's refusal, whose own answer says why. Decided on these facts, never on ssh's text.
func (p *pfsense) call(ctx context.Context, words string) ([]byte, Result) {
	d := net.Dialer{Timeout: p.dialTimeout}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(p.host, p.port))
	if err != nil {
		return nil, Result{protocol.CodePrecheckRepoUnreachable, err.Error()}
	}
	conn.Close()
	keyType, _, _ := strings.Cut(p.hostKey, " ")
	scanned, err := exec.CommandContext(ctx, p.keyscan, "-T", "10", "-p", p.port, "-t", keyType, p.host).Output()
	if err != nil {
		return nil, Result{protocol.CodePrecheckRepoUnreachable, "ssh-keyscan: " + err.Error()}
	}
	if !offersHostKey(scanned, p.hostKey) {
		return nil, Result{protocol.CodeFailedTargetHostKey, "the device does not offer the pinned host key " + keyType}
	}

	dir, err := os.MkdirTemp("", "vordr-ssh-") // 0700; the key lives here only for this call
	if err != nil {
		return nil, Result{protocol.CodeFailedTargetAuth, "temporary key file: " + err.Error()}
	}
	defer os.RemoveAll(dir)
	keyFile, known := filepath.Join(dir, "key"), filepath.Join(dir, "known_hosts")
	hostEntry := p.host
	if p.port != "22" {
		hostEntry = "[" + p.host + "]:" + p.port
	}
	if err := os.WriteFile(keyFile, p.key, 0o600); err != nil {
		return nil, Result{protocol.CodeFailedTargetAuth, "temporary key file: " + err.Error()}
	}
	if err := os.WriteFile(known, []byte(hostEntry+" "+p.hostKey+"\n"), 0o600); err != nil {
		return nil, Result{protocol.CodeFailedTargetHostKey, "temporary known_hosts: " + err.Error()}
	}
	// No user configuration, no agent, no other keys, no prompt; only the pinned host key counts.
	cmd := exec.CommandContext(ctx, p.ssh, "-F", "/dev/null", "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes",
		"-o", "IdentityAgent=none", "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile="+known,
		"-o", "GlobalKnownHostsFile=/dev/null", "-o", "ConnectTimeout=10", "-i", keyFile, "-p", p.port, "-l", p.user,
		"--", p.host, words)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return stdout.Bytes(), Result{}
	case errors.As(err, &exit) && exit.ExitCode() == 255:
		return nil, Result{protocol.CodeFailedTargetAuth, strings.TrimSpace(stderr.String())}
	case errors.As(err, &exit):
		return nil, Result{protocol.CodeRejectedUnsupported, fmt.Sprintf("the firewall's command refused %q: %s",
			words, keyValues(stdout.Bytes())["reason"])}
	}
	return nil, Result{protocol.CodePrecheckRepoUnreachable, err.Error()}
}

// offersHostKey: ssh-keyscan printed "<host> <type> <base64>" with exactly the pinned type and key.
func offersHostKey(scanned []byte, pinned string) bool {
	for _, line := range strings.Split(string(scanned), "\n") {
		f := strings.Fields(line)
		if len(f) == 3 && !strings.HasPrefix(f[0], "#") && f[1]+" "+f[2] == pinned {
			return true
		}
	}
	return false
}

// keyLines reads every value of a key that the answer repeats, one per line, in order.
func keyLines(b []byte, key string) []string {
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok && k == key {
			out = append(out, v)
		}
	}
	return out
}

// keyValues reads the forced command's key=value lines; the first value of a key counts.
func keyValues(b []byte) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok && k != "" {
			if _, seen := out[k]; !seen {
				out[k] = v
			}
		}
	}
	return out
}
