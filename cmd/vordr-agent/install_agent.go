package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"gopkg.in/yaml.v3"

	"github.com/psewar/vordr/deploy"
	"github.com/psewar/vordr/internal/agent"
	"github.com/psewar/vordr/internal/identity"
	"github.com/psewar/vordr/internal/policy"
)

// vordr-agent install (ADR-0169): a Linux host gets the agent from this binary and one command. install writes a
// minimal agent.yaml from its flags, checks everything before anything changes, and has deploy/install-agent.sh,
// embedded in the binary, do the install. It is made to run unattended: its outcome is the exit code and the last line
// of its output (installOutcome), never only prose.

const installUsage = `vordr-agent install --hub <wss-url> --hub-key <base64> [--node <name>] [--ca-file <path>]
                    [--operator-key <base64> [--backup-operator-key <base64>]] [--replace-config "<reason>"]`

// installArgs: what the command line asks of install.
type installArgs struct {
	hub, hubKey, node, caFile, operatorKey, backupKey string

	reason  string // --replace-config
	replace bool
	config  bool            // --config was given; install writes policy.DefaultConfigPath only
	given   map[string]bool // the flags of installFlags that were given
	problem string          // the first thing on the command line install cannot take; "" when none
}

// installFlag: one of install's own flags and where its value goes.
type installFlag struct {
	name string
	dst  *string
}

func (a *installArgs) flags() []installFlag {
	return []installFlag{{"--hub", &a.hub}, {"--hub-key", &a.hubKey}, {"--node", &a.node}, {"--ca-file", &a.caFile},
		{"--operator-key", &a.operatorKey}, {"--backup-operator-key", &a.backupKey}}
}

// takeFlag reads one of install's flags at args[*i], as "--name value" or "--name=value", and moves *i past it.
// false: args[*i] is none of them. A flag given twice, or without a value, is noted as the problem.
func (a *installArgs) takeFlag(args []string, i *int) bool {
	name, value, inline := strings.Cut(args[*i], "=")
	var dst *string
	for _, f := range a.flags() {
		if f.name == name {
			dst = f.dst
		}
	}
	if dst == nil {
		return false
	}
	if !inline {
		if *i+1 >= len(args) {
			a.note(name + " needs a value")
			return true
		}
		*i++
		value = args[*i]
	}
	if a.given == nil {
		a.given = map[string]bool{}
	}
	if a.given[name] {
		a.note(name + " is given twice")
	}
	a.given[name] = true
	*dst = value
	return true
}

// note keeps the first problem.
func (a *installArgs) note(problem string) {
	if a.problem == "" {
		a.problem = problem
	}
}

// usageProblem: what is wrong with the command line itself, before any look at the host; "" when nothing.
func (a *installArgs) usageProblem() string {
	switch {
	case a.problem != "":
		return a.problem
	case a.config:
		return "--config does not apply: install writes " + policy.DefaultConfigPath + " from its flags"
	case !a.given["--hub"] || !a.given["--hub-key"]:
		return "--hub and --hub-key are required"
	case a.given["--backup-operator-key"] && !a.given["--operator-key"]:
		return "--backup-operator-key needs --operator-key"
	case a.replace && strings.TrimSpace(a.reason) == "":
		return "--replace-config needs a reason"
	}
	for _, f := range a.flags() {
		if a.given[f.name] && strings.TrimSpace(*f.dst) == "" {
			return f.name + " is empty"
		}
	}
	return ""
}

// installCode says why install ended as it did; the last line of its output names it.
type installCode string

const (
	// exit 0: installed, the service runs
	codeInstalled installCode = "installed"

	// exit 2: refused, nothing changed
	codeUsage            installCode = "usage"             // the command line itself
	codeNotLinux         installCode = "not_linux"         // on Windows install-service is the way
	codeNotRoot          installCode = "not_root"          // the agent runs as root
	codeNoSystemd        installCode = "no_systemd"        // the agent runs as a systemd service
	codeNoApt            installCode = "no_apt"            // the agent knows the apt source only
	codeConfigInvalid    installCode = "config_invalid"    // the agent.yaml from the flags has issues
	codeConfigDiffers    installCode = "config_differs"    // an installed agent.yaml differs, no --replace-config
	codeHubUnreachable   installCode = "hub_unreachable"   // with stage=<the first stage that failed>
	codeCAFileUnsafe     installCode = "ca_file_unsafe"    // --ca-file where others than root may change or remove it
	codePrepareFailed    installCode = "prepare_failed"    // the running binary, bash or the private folder
	codeTempDirUnsafe    installCode = "temp_dir_unsafe"   // the private folder's parent is open to others
	codeInstallerRefused installCode = "installer_refused" // install-agent.sh refused, e.g. a proxy instance's policy

	// exit 1: failed after install-agent.sh began to change the host
	codeInstallFailed      installCode = "install_failed"
	codeIdentityUnreadable installCode = "identity_unreadable" // installed and started, but no fingerprint to read

	// exit 2 before install-agent.sh changed anything, 1 after: SIGINT, SIGTERM or SIGHUP ended the install
	codeInterrupted installCode = "interrupted"
)

// systemPath is the PATH install looks commands up in and gives install-agent.sh: the system's folders only, not the
// caller's PATH, as install runs as root.
const systemPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// privateParent is where the private folder goes: root's own place, cleared at boot, never $TMPDIR, which may point
// into a folder of another user when root's environment comes from one (su without -, sudo -E).
const privateParent = "/run"

// caPlace is where a hub CA belongs on the host; vordr-agent install takes --ca-file only from a place only root may
// change, and names this one (Home Assistant's install over SSH puts the CA there too).
const caPlace = "/etc/vordr/hub-ca.pem"

// interrupts end install early; it reports them as interrupted instead of ending without a last line.
var interrupts = []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP}

// installOutcome is how install ended. Its exit code gives the class: 0 installed, 2 refused with nothing changed,
// 1 failed after install-agent.sh began to change the host. line() is the last line of the output.
type installOutcome struct {
	exit              int
	code              installCode
	stage             agent.ProbeStage // hub_unreachable: the first stage of reaching the hub that failed
	node, fingerprint string           // installed
}

// line: key=value pairs whose values hold no spaces, for a program to read. Installed:
// "result=installed node=<node> fingerprint=<hex>"; otherwise "result=refused code=<code>[ stage=<stage>]" or
// "result=failed code=<code>".
func (o installOutcome) line() string {
	switch {
	case o.exit == 0:
		return "result=installed node=" + o.node + " fingerprint=" + o.fingerprint
	case o.exit == 2 && o.stage != "":
		return "result=refused code=" + string(o.code) + " stage=" + string(o.stage)
	case o.exit == 2:
		return "result=refused code=" + string(o.code)
	}
	return "result=failed code=" + string(o.code)
}

// installHost is what install reads of the host and how it acts on it: realHost on the host itself; tests point it
// into a deploytest sandbox.
type installHost struct {
	goos        string
	euid        int
	owner       int    // who system files must belong to: root; tests run as themselves
	root        string // put before every fixed system path; "" on the host itself
	path        string // PATH: where apt-get and bash are looked up, and the installer's
	tempDir     string // where the private folder goes
	hostname    func() (string, error)
	self        func() (string, error) // the binary that runs, which is the one to install
	probe       func(context.Context, policy.Config) agent.ProbeResult
	files       func(dir string) error // writes install-agent.sh and the files it needs into dir
	signals     <-chan os.Signal       // the interrupts that reach install
	out, errOut io.Writer
}

// realHost is this host as install sees it. Nothing of the caller's environment decides where things are: the PATH
// and the private folder's place are fixed. The interrupts come to install instead of ending it, and a closed output
// does not either (SIGPIPE ignored: a write fails instead).
func realHost() installHost {
	signals := make(chan os.Signal, 4)
	signal.Notify(signals, interrupts...)
	signal.Ignore(syscall.SIGPIPE)
	return installHost{goos: runtime.GOOS, euid: os.Geteuid(), owner: 0, path: systemPath, tempDir: privateParent,
		hostname: os.Hostname, self: runningBinary, probe: agent.Probe, files: deploy.WriteAgentFiles,
		signals: signals, out: os.Stdout, errOut: os.Stderr}
}

// install sets this host up with the agent and prints the outcome as its last line.
func install(h installHost, a installArgs) int {
	o := h.run(a)
	fmt.Fprintln(h.out, o.line())
	return o.exit
}

func (h installHost) run(a installArgs) installOutcome {
	refuse := func(code installCode, format string, args ...any) installOutcome {
		fmt.Fprintf(h.errOut, "install: "+format+"\n", args...)
		return installOutcome{exit: 2, code: code}
	}
	// The first interrupt that reached install, looked at before each step that takes long or changes something.
	var caught os.Signal
	interrupted := func() bool {
		if caught == nil {
			select {
			case caught = <-h.signals:
			default:
			}
		}
		return caught != nil
	}
	if p := a.usageProblem(); p != "" {
		return refuse(codeUsage, "%s\n  %s", p, installUsage)
	}
	switch {
	case h.goos == "windows":
		return refuse(codeNotLinux, "install is for Linux; on Windows, as administrator: "+
			"vordr-agent install-service --config <agent.yaml>")
	case h.goos != "linux":
		return refuse(codeNotLinux, "install is for Linux with systemd and apt, this is %s - nothing changed", h.goos)
	case h.euid != 0:
		return refuse(codeNotRoot, "run it as root: the agent runs as root, apt needs it - nothing changed")
	case !isDir(filepath.Join(h.root, "/run/systemd/system")):
		return refuse(codeNoSystemd, "systemd is not running on this host; the agent runs as its service - nothing changed")
	case h.find("apt-get") == "":
		return refuse(codeNoApt, "no apt-get here: the agent knows the apt source only - nothing changed")
	}

	raw, cfg, issues, err := a.policy(h.hostname)
	switch {
	case err != nil:
		return refuse(codePrepareFailed, "agent.yaml not written: %v - nothing changed", err)
	case len(issues) > 0:
		for _, is := range issues {
			fmt.Fprintf(h.errOut, "issue  %s: %s\n", a.issueSource(is.Field), is.Message)
		}
		return refuse(codeConfigInvalid, "the agent.yaml from these flags has %d issue(s), listed above - nothing changed",
			len(issues))
	}
	if cfg.Hub.CAFile != "" {
		if why := h.othersMayChange(cfg.Hub.CAFile); why != "" {
			// The agent trusts this CA for the hub at every start, as root: it must stay, and only root may change it.
			return refuse(codeCAFileUnsafe, "--ca-file %s: %s. The agent reads it as root at every start, so only root "+
				"may change it and it has to stay; as root: install -D -m 0644 %s %s, then --ca-file %s - nothing changed",
				cfg.Hub.CAFile, why, cfg.Hub.CAFile, caPlace, caPlace)
		}
	}
	fmt.Fprintf(h.out, "ok     agent.yaml for node %s: hub %s, %s; no install, no reboot (strict defaults)\n",
		cfg.Node, cfg.Hub.URL, selfUpdateSummary(cfg.AgentUpdate))

	installed := filepath.Join(h.root, policy.DefaultConfigPath)
	action, old, err := installedPolicy(installed, raw)
	switch {
	case err != nil:
		return refuse(codePrepareFailed, "cannot read %s: %v - nothing changed", installed, err)
	case action == configReplace && !a.replace:
		for _, l := range lineDiff(old, raw) {
			fmt.Fprintln(h.errOut, l)
		}
		// Changing a host's local policy is a decision, not a side effect of an install (ADR-0006).
		return refuse(codeConfigDiffers, "%s exists and differs from the agent.yaml these flags give (- installed, "+
			"+ new): keep it, or replace it with --replace-config \"<reason>\" (the old one is kept as "+
			"agent.yaml.bak-<time>) - nothing changed", installed)
	case action != configReplace && a.replace:
		fmt.Fprintf(h.out, "note   %s is new or the same: --replace-config has nothing to replace\n", installed)
	}

	self, err := h.self()
	if err != nil {
		return refuse(codePrepareFailed, "which binary runs: %v - nothing changed", err)
	}
	r := h.probe(context.Background(), cfg)
	if interrupted() {
		return refuse(codeInterrupted, "stopped by %v - nothing changed", caught)
	}
	if r.Failed != "" {
		hint := stageHint[r.Failed]
		if r.Failed == agent.StageConfig { // hub.url and hub.public_key are checked above; this is the CA file
			hint = "--ca-file must be a PEM file with the CA certificate(s) the hub's certificate chains to"
		}
		fmt.Fprintf(h.errOut, "hub-check %s: failed at stage %s\n  %s\n", cfg.Hub.URL, r.Failed, hint)
		if r.Detail != "" {
			fmt.Fprintf(h.errOut, "  detail: %s\n", r.Detail)
		}
		o := refuse(codeHubUnreachable, "the hub is not reachable from here - nothing changed")
		o.stage = r.Failed
		return o
	}
	fmt.Fprintf(h.out, "ok     hub %s reachable (%s)\n", cfg.Hub.URL, strings.Join(r.Addresses, ", "))

	bash := h.find("bash")
	if bash == "" {
		return refuse(codePrepareFailed, "no bash here to run install-agent.sh - nothing changed")
	}
	// A private folder (0700) for the installer and the new agent.yaml, gone again whatever happens, in a place only
	// root may change. The binary stays where it runs: the folder may be on a file system mounted noexec.
	if why := h.tempDirUnsafe(); why != "" {
		return refuse(codeTempDirUnsafe, "private folder for the installer: %s - nothing changed", why)
	}
	dir, err := os.MkdirTemp(h.tempDir, "vordr-install-")
	if err != nil {
		return refuse(codePrepareFailed, "private folder for the installer: %v - nothing changed", err)
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			fmt.Fprintf(h.errOut, "note   %s stays: %v\n", dir, err)
		}
	}()
	newCfg := filepath.Join(dir, "agent.yaml")
	if err := h.files(dir); err != nil {
		return refuse(codePrepareFailed, "installer files in %s: %v - nothing changed", dir, err)
	}
	if err := deploy.WriteNew(newCfg, raw, 0o600); err != nil {
		return refuse(codePrepareFailed, "%s: %v - nothing changed", newCfg, err)
	}

	args := []string{filepath.Join(dir, "install-agent.sh"), self, newCfg}
	if a.replace {
		args = append(args, "--replace-config", a.reason)
	}
	cmd := exec.Command(bash, args...)
	// A fixed environment: nothing of the caller's (BASH_ENV, SHELLOPTS, PS4, a PATH with relative folders) reaches a
	// script that runs as root.
	cmd.Env = []string{"PATH=" + h.path, "LANG=C", "LC_ALL=C"}
	cmd.Stdout, cmd.Stderr = keepWriting{h.out}, keepWriting{h.errOut}
	if interrupted() {
		return refuse(codeInterrupted, "stopped by %v - nothing changed", caught)
	}
	if err := cmd.Start(); err != nil {
		return refuse(codePrepareFailed, "install-agent.sh did not start: %v - nothing changed", err)
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait() // its outcome is the exit code, read below
		close(done)
	}()
	for running := true; running; {
		select {
		case s := <-h.signals:
			if caught == nil {
				caught = s
			}
			// The installer ends once its running command has ended (its trap), and says by its exit code whether it
			// changed anything; install waits for that.
			_ = cmd.Process.Signal(s)
		case <-done:
			running = false
		}
	}
	// The installer's exit code says whether it changed anything (deploy/install-agent.sh): 2 not, anything else yes.
	switch exit := cmd.ProcessState.ExitCode(); {
	case exit == 2 && caught != nil:
		return refuse(codeInterrupted, "install-agent.sh stopped on %v before it changed anything - nothing changed",
			caught)
	case exit == 2:
		return refuse(codeInstallerRefused, "install-agent.sh refused (above) - nothing changed")
	case exit != 0 && caught != nil:
		fmt.Fprintf(h.errOut, "install: install-agent.sh stopped on %v after it began to change the host (above); "+
			"run install again\n", caught)
		return installOutcome{exit: 1, code: codeInterrupted}
	case exit != 0:
		fmt.Fprintf(h.errOut, "install: install-agent.sh failed after it began to change the host (above); "+
			"once the cause is fixed, run install again\n")
		return installOutcome{exit: 1, code: codeInstallFailed}
	}

	stateDir := filepath.Join(h.root, cfg.StateDir)
	id, found, err := identity.Load(stateDir)
	if err != nil || !found {
		fmt.Fprintf(h.errOut, "install: the service runs, but its identity in %s cannot be read (found %v, %v) - "+
			"vordr-agent identity shows it\n", stateDir, found, err)
		return installOutcome{exit: 1, code: codeIdentityUnreadable}
	}
	return installOutcome{exit: 0, code: codeInstalled, node: cfg.Node, fingerprint: id.Fingerprint()}
}

// generatedPolicy is the agent.yaml install writes: the node, the hub and, with an operator key, self-update. All else
// is left out, so the strict defaults apply (ADR-0006): the agent checks and reports; installs, reboots and the rest
// stay off until the operator allows them in agent.yaml on the host.
type generatedPolicy struct {
	Node string        `yaml:"node"`
	Hub  generatedHub  `yaml:"hub"`
	Self *generatedUpd `yaml:"agent_update,omitempty"`
}

type generatedHub struct {
	URL       string `yaml:"url"`
	PublicKey string `yaml:"public_key"`
	CAFile    string `yaml:"ca_file,omitempty"`
}

type generatedUpd struct {
	Enabled         bool   `yaml:"enabled"`
	PublicKey       string `yaml:"public_key"`
	BackupPublicKey string `yaml:"backup_public_key,omitempty"`
}

const generatedHeader = `# Written by vordr-agent install (ADR-0169). What is left out has the strict default: the agent checks and reports,
# installs no updates and never reboots. Allow more here (the keys are in agent.example.yaml), check the file with
# "vordr-agent check-config", then "systemctl restart vordr-agent".
`

// policy is the agent.yaml for these flags, as written and as the agent reads it, with every issue check-config would
// list. The node defaults to the host name in lower case, as the agent's own default; it is written out, so that a
// renamed host keeps its name at the hub. A relative --ca-file is made absolute: the service reads it from elsewhere.
func (a *installArgs) policy(hostname func() (string, error)) ([]byte, policy.Config, []policy.Issue, error) {
	g := generatedPolicy{Node: a.node, Hub: generatedHub{URL: a.hub, PublicKey: a.hubKey}}
	if g.Node == "" {
		if h, err := hostname(); err == nil {
			g.Node = strings.ToLower(h)
		}
	}
	if a.caFile != "" {
		abs, err := filepath.Abs(a.caFile)
		if err != nil {
			return nil, policy.Config{}, nil, err
		}
		g.Hub.CAFile = abs
	}
	if a.operatorKey != "" {
		g.Self = &generatedUpd{Enabled: true, PublicKey: a.operatorKey, BackupPublicKey: a.backupKey}
	}
	var buf bytes.Buffer
	buf.WriteString(generatedHeader)
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(g); err != nil {
		return nil, policy.Config{}, nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, policy.Config{}, nil, err
	}
	cfg, issues := policy.Parse(buf.Bytes())
	return buf.Bytes(), cfg, issues, nil
}

// issueSource names the flag behind a field of the generated agent.yaml, so that an issue says what to change.
func (a *installArgs) issueSource(field string) string {
	flag := map[string]string{"node": "--node", "hub.url": "--hub", "hub.public_key": "--hub-key",
		"hub.ca_file": "--ca-file", "agent_update.public_key": "--operator-key",
		"agent_update.backup_public_key": "--backup-operator-key"}[field]
	switch {
	case field == "node" && !a.given["--node"]:
		return "--node (node, here from the host name)"
	case flag != "":
		return flag + " (" + field + ")"
	}
	return field
}

func selfUpdateSummary(u policy.AgentUpdate) string {
	switch {
	case !u.Enabled:
		return "self-update off (no --operator-key)"
	case u.BackupPublicKey != "":
		return "self-update with releases signed by the operator key or its backup"
	}
	return "self-update with releases signed by the operator key"
}

// installedPolicy compares the agent.yaml installed at path with want: configNew when there is none, configSame when
// it is identical, configReplace when it differs; old is its content.
func installedPolicy(path string, want []byte) (action configAction, old []byte, err error) {
	if _, statErr := os.Stat(path); errors.Is(statErr, fs.ErrNotExist) {
		return configNew, nil, nil
	}
	if old, err = os.ReadFile(path); err != nil {
		return 0, nil, err
	}
	if bytes.Equal(old, want) {
		return configSame, old, nil
	}
	return configReplace, old, nil
}

// find looks a command up in h.path, as the shell would, and returns its path; "" when there is none. Only absolute
// folders count: install runs as root, and a relative one would depend on the current folder.
func (h installHost) find(name string) string {
	for _, d := range filepath.SplitList(h.path) {
		if !filepath.IsAbs(d) {
			continue
		}
		p := filepath.Join(d, name)
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0 {
			return p
		}
	}
	return ""
}

// keepWriting passes the installer's output on and drops it once the reader is gone: a closed pipe must not end the
// install halfway (install-agent.sh would die of SIGPIPE at its next line).
type keepWriting struct{ w io.Writer }

func (k keepWriting) Write(p []byte) (int, error) {
	_, _ = k.w.Write(p) // a reader that went away: the output is lost, the install goes on
	return len(p), nil
}

// tempDirUnsafe says why the private folder must not go into h.tempDir; "" when it may. It must be a folder (not a
// symbolic link) of h.owner (root) that nobody else may write to, or only with the sticky bit set: otherwise another
// user could rename the private folder and put an installer of their own in its place before bash reads it.
func (h installHost) tempDirUnsafe() string {
	fi, err := os.Lstat(h.tempDir)
	if err != nil {
		return fmt.Sprintf("%s cannot be read: %v", h.tempDir, err)
	}
	uid, known := fileOwner(fi)
	switch {
	case !fi.IsDir():
		return h.tempDir + " is no folder"
	case !known || uid != h.owner:
		return fmt.Sprintf("%s belongs to uid %d, not to root", h.tempDir, uid)
	case fi.Mode().Perm()&0o022 != 0 && fi.Mode()&fs.ModeSticky == 0:
		return fmt.Sprintf("%s may be written by others (%v) and has no sticky bit", h.tempDir, fi.Mode())
	}
	return ""
}

// othersMayChange says why someone else than h.owner (root) could change or remove the file at path: the file, or a
// folder on its way up, belongs to someone else or may be written by group or others (a sticky bit does not help: the
// owner of a file in /tmp may still change it, and /tmp is cleared). "" when only root may. Symbolic links are
// resolved first. On a test's sandbox the way up ends at its root.
func (h installHost) othersMayChange(path string) string {
	p, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Sprintf("it cannot be resolved: %v", err)
	}
	top := filepath.Join(h.root, "/")
	for {
		fi, err := os.Lstat(p)
		if err != nil {
			return fmt.Sprintf("%s cannot be read: %v", p, err)
		}
		uid, known := fileOwner(fi)
		switch {
		case !known || uid != h.owner:
			return fmt.Sprintf("%s belongs to uid %d, not to root", p, uid)
		case fi.Mode().Perm()&0o022 != 0:
			return fmt.Sprintf("%s may be written by group or others (%v)", p, fi.Mode())
		}
		if p == top || p == filepath.Dir(p) {
			return ""
		}
		p = filepath.Dir(p)
	}
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// runningBinary is the path of the binary that runs, checked to be that very file still: install-agent.sh copies the
// binary from there.
func runningBinary() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	if p, err = filepath.EvalSymlinks(p); err != nil {
		return "", err
	}
	at, err := os.Stat(p)
	if err != nil {
		return "", err
	}
	running, err := os.Stat("/proc/self/exe")
	if err != nil {
		return "", err
	}
	if !os.SameFile(at, running) {
		return "", fmt.Errorf("%s was replaced since this run started; start install again", p)
	}
	return p, nil
}
