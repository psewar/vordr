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
// and its offered version (ADR-0198); updatesFormatRelease the first that names what pfSense's own updater and package
// manager offer (ADR-0204). A root job before form 2 writes no updates_format.
const (
	updatesFormat        = 2
	updatesFormatRelease = 3
)

// releaseOther names the offer of a release in another branch.
const releaseOther = "pfSense (other branch)"

// Check reads the firmware and the outdated packages from the root job's update facts (written hourly from the local
// package catalog). From form 3 on it offers what pfSense's own updater and package manager would install
// (releaseOffers); before it, the base system when the catalog has a newer pfSense-base and every outdated package,
// from the version installed to the one in the catalog (packageOffers).
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
		"packages_outdated_count", "release_installed", "release_offered", "release_other", "packages_behind_release"} {
		if v := kv[k]; v != "" {
			st.Facts[k] = v
		}
	}
	if n, err := strconv.Atoi(kv["updates_format"]); err == nil && n >= updatesFormatRelease {
		offers, res := releaseOffers(out, kv)
		if !res.OK() {
			return Status{}, res
		}
		st.Offers = offers
		return st, Result{}
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

// releaseOffers reads what pfSense's own updater and package manager offer, from form 3 of the update facts on
// (ADR-0204):
//   - the release of the configured branch, from release_installed to release_offered, where the root job names one;
//   - one package per addon_offer line, an installed add-on package from its installed to its offered version; there
//     must be as many lines as addon_offer_count says, else the root job could not read one, and the facts are refused
//     as in form 2;
//   - a newer release in another branch (release_other) as an offer no install through Vördr takes (KeptBack): the
//     operator switches the branch in pfSense's own settings. It is left out where it is the release already offered.
//
// Other newer packages of the catalog are no offers: they come with the next release or an add-on's update, and
// packages_behind_release counts them.
func releaseOffers(out []byte, kv map[string]string) ([]Offer, Result) {
	installed := kv["release_installed"]
	if installed == "" {
		return nil, Result{protocol.CodeRejectedUnsupported, "the update facts name no installed release"}
	}
	var offers []Offer
	offered := kv["release_offered"]
	if offered != "" && offered != installed {
		offers = append(offers, Offer{Name: "pfSense", From: installed, Version: offered, Install: "release"})
	}
	lines := keyLines(out, "addon_offer")
	for i, line := range lines {
		f := strings.Fields(line)
		if len(f) != 3 {
			return nil, Result{protocol.CodeRejectedUnsupported, fmt.Sprintf(
				"addon_offer line %d is not <name> <installed> <offered>", i+1)}
		}
		offers = append(offers, Offer{Name: "package " + f[0], From: f[1], Version: f[2], Install: "addon " + f[0]})
	}
	if count := kv["addon_offer_count"]; count != strconv.Itoa(len(lines)) {
		return nil, Result{protocol.CodeRejectedUnsupported, fmt.Sprintf(
			"the root job counts %q outdated add-on packages but names the versions of %d", count, len(lines))}
	}
	if other := kv["release_other"]; other != "" && other != installed && other != offered {
		offers = append(offers, Offer{Name: releaseOther, From: installed, Version: other, KeptBack: true})
	}
	return offers, Result{}
}

// Queue asks the root job to install one offer (ADR-0205): the release of the configured branch ("release") or one
// add-on package ("addon <name>"). The forced command checks the words, the root job checks the offer again before it
// runs pfSense-upgrade. Only offers of form 3 name how they are installed.
func (p *pfsense) Queue(ctx context.Context, o Offer, id string) Result {
	if o.Install == "" {
		return Result{protocol.CodeRejectedUnsupported, fmt.Sprintf("%s is not installed through Vördr", o.Name)}
	}
	out, res := p.call(ctx, "install "+o.Install+" "+id)
	if !res.OK() {
		return res
	}
	if keyValues(out)["state"] != "requested" {
		return Result{protocol.CodeFailedPackageManager, "the firewall's command did not take the install request"}
	}
	return Result{}
}

// InstallStatus asks where the install request of id stands. An answer for another id, a state the root job does not
// write, or an end that is no time is refused: the agent would read it as an outcome.
func (p *pfsense) InstallStatus(ctx context.Context, id string) (InstallState, Result) {
	out, res := p.call(ctx, "install status "+id)
	if !res.OK() {
		return InstallState{}, res
	}
	kv := keyValues(out)
	if kv["install_job"] != id {
		return InstallState{}, Result{protocol.CodeRejectedUnsupported, "the install status names another request"}
	}
	st := InstallState{State: kv["install_state"], Code: kv["install_code"], RC: kv["install_rc"]}
	switch st.State {
	case InstallQueued, InstallRunning, InstallOK, InstallFailed, InstallRefused, InstallRebooted, InstallUnknown:
	default:
		return InstallState{}, Result{protocol.CodeRejectedUnsupported, fmt.Sprintf(
			"the install status says %q, which the root job does not write", st.State)}
	}
	if f := kv["install_finished"]; f != "" {
		n, err := strconv.ParseInt(f, 10, 64)
		if err != nil || n <= 0 {
			return InstallState{}, Result{protocol.CodeRejectedUnsupported, "the install's end is no time"}
		}
		st.Finished = n
	}
	return st, Result{}
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

// call runs one call of the forced command over one connection, ssh's own (ADR-0206). ssh compares the device's host
// key with the pinned one in the key exchange, before it offers the client key, so a device without the pinned key
// never sees a login. An exit 255 means ssh did not get through; why is established afterwards (whyNotThrough). Any
// other exit is the forced command's refusal, whose own answer says why. Decided on facts, never on ssh's text.
func (p *pfsense) call(ctx context.Context, words string) ([]byte, Result) {
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
		return nil, p.whyNotThrough(ctx, strings.TrimSpace(stderr.String()))
	case errors.As(err, &exit):
		return nil, Result{protocol.CodeRejectedUnsupported, fmt.Sprintf("the firewall's command refused %q: %s",
			words, keyValues(stdout.Bytes())["reason"])}
	}
	return nil, Result{protocol.CodePrecheckRepoUnreachable, err.Error()}
}

// whyNotThrough establishes, after ssh ended with 255, why it did not get through: the address does not answer
// (precheck_repo_unreachable; a login protection that blocks the agent's address looks the same), the device does not
// offer the pinned host key (failed_target_hostkey), or else the login (failed_target_auth, with ssh's text for
// people). These probes open connections that end before a login, which a login protection such as sshguard counts
// as attacks; that is why they run only after a failure and never before each call (ADR-0206).
func (p *pfsense) whyNotThrough(ctx context.Context, sshText string) Result {
	d := net.Dialer{Timeout: p.dialTimeout}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(p.host, p.port))
	if err != nil {
		return Result{protocol.CodePrecheckRepoUnreachable, err.Error()}
	}
	conn.Close()
	keyType, _, _ := strings.Cut(p.hostKey, " ")
	scanned, err := exec.CommandContext(ctx, p.keyscan, "-T", "10", "-p", p.port, "-t", keyType, p.host).Output()
	if err != nil {
		return Result{protocol.CodePrecheckRepoUnreachable, "ssh-keyscan: " + err.Error()}
	}
	if !offersHostKey(scanned, p.hostKey) {
		return Result{protocol.CodeFailedTargetHostKey, "the device does not offer the pinned host key " + keyType}
	}
	return Result{protocol.CodeFailedTargetAuth, sshText}
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
