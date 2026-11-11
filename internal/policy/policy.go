// Package policy reads the agent's local policy (agent.yaml). The local policy has the last word: the hub can only
// ask, and whatever the file leaves out falls back to the stricter value (no install, no reboot, no self-update).
package policy

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/source/winget"
)

// Config is agent.yaml. See agent.example.yaml and design/architecture.md, section 9.
type Config struct {
	Node        string      `yaml:"node"`      // default: hostname
	StateDir    string      `yaml:"state_dir"` // identity and run journal; default DefaultStateDir
	Hub         Hub         `yaml:"hub"`
	Install     Install     `yaml:"install"`
	Reboot      Reboot      `yaml:"reboot"`
	AgentUpdate AgentUpdate `yaml:"agent_update"`
	Apps        []App       `yaml:"apps"`
	// Health: what must still work after an install run that changed packages; otherwise the run ends failed_health.
	Health []Check `yaml:"health"`
	// Packages: packages of pip, pipx and npm (design 11.2).
	Packages Packages `yaml:"packages"`
	// Target: this instance speaks for an appliance instead of the host it runs on (design/stellvertreter.md,
	// ADR-0089). Without it the agent looks after its own host.
	Target *Target `yaml:"target"`
	// ConfigBackup: how the instance keeps the appliance's configuration (ADR-0090); only with target.
	ConfigBackup *ConfigBackup `yaml:"config_backup"`
	// GuestBackup: this host is the hypervisor, and the hub may ask it to back up one of its guests right before that
	// guest installs (verb backup, ADR-0096). Without it the agent does not offer backups.
	GuestBackup *GuestBackup `yaml:"guest_backup"`
}

// GuestBackup: where the backups go and how long one may take. Which guest is backed up comes from the hub's job;
// the agent backs up only guests of this host, only to this storage, and never prunes or deletes anything.
type GuestBackup struct {
	Storage    string `yaml:"storage"`     // a Proxmox Backup Server storage of this host (/etc/pve/storage.cfg)
	TimeoutMin int    `yaml:"timeout_min"` // one backup; default 60
}

// PVEStorageConfig is where Proxmox VE keeps its storages; tests point it elsewhere.
var PVEStorageConfig = "/etc/pve/storage.cfg"

// PBSStorage reports whether Proxmox VE has a Proxmox Backup Server storage of this name: a "pbs: <name>" section in
// PVEStorageConfig.
func PBSStorage(name string) bool {
	raw, err := os.ReadFile(PVEStorageConfig)
	if err != nil {
		return false
	}
	for line := range strings.Lines(string(raw)) {
		if kind, id, ok := strings.Cut(strings.TrimRight(line, "\r\n"), ": "); ok && kind == "pbs" && strings.TrimSpace(id) == name {
			return true
		}
	}
	return false
}

// Target is the appliance one proxy instance reports on. The driver knows only its fixed requests; nothing comes
// from the hub. TLS is checked either against a pinned fingerprint (devices with their own certificate) or against
// server_name with the system's roots or ca_file; SSH against the pinned host key.
type Target struct {
	Driver      string `yaml:"driver"`      // one of TargetDrivers
	Address     string `yaml:"address"`     // https://<address>[:port], or ssh://<user>@<address>[:port] for SSHDrivers
	ServerName  string `yaml:"server_name"` // the name the device's certificate must carry (also sent as SNI)
	CAFile      string `yaml:"ca_file"`     // roots for server_name instead of the system's
	Fingerprint string `yaml:"fingerprint"` // SHA-256 of the device's certificate, 64 hex digits (colons allowed)
	// HostKey: for SSHDrivers, the device's SSH host key as known_hosts has it ("ssh-ed25519 AAAA..."), from
	// ssh-keyscan; the driver talks only to a device that offers exactly this key (ADR-0094).
	HostKey string `yaml:"host_key"`
	// Credentials: KEY=value lines for the driver (written from the secret store), readable by the instance only.
	Credentials string `yaml:"credentials"`
}

// TargetDrivers are the appliances an instance can speak for.
var TargetDrivers = []string{"synology-dsm", "pfsense"}

// SSHDrivers reach their appliance over SSH (the system's OpenSSH client, ADR-0094); all others over HTTPS.
var SSHDrivers = []string{"pfsense"}

// ConfigBackup: the driver exports the configuration daily and keeps the newest Keep exports; one older than MaxAgeH
// counts as missing (ADR-0090).
type ConfigBackup struct {
	Keep    int `yaml:"keep"`
	MaxAgeH int `yaml:"max_age_h"`
}

const (
	defaultBackupKeep    = 30
	defaultBackupMaxAgeH = 26
)

// TargetFingerprint returns the pinned fingerprint as 64 lowercase hex digits ("" without one).
func (t *Target) TargetFingerprint() string {
	return strings.ToLower(strings.ReplaceAll(t.Fingerprint, ":", ""))
}

// Packages says where the agent looks for packages of language package managers. Reading them changes nothing and
// asks nobody outside: it is on unless switched off, like check.
type Packages struct {
	// Inventory: report installed packages to the hub (false: none at all). Default true.
	Inventory *bool `yaml:"inventory"`
	// Detect: look in the well-known places (global npm, pip outside the distribution, pip --user, pipx, Node-RED).
	// Default true.
	Detect *bool `yaml:"detect"`
	// Locations: further places, e.g. virtual environments of services or project folders; "*" in path makes one
	// location per match.
	Locations []PackageLocation `yaml:"locations"`
	// Outdated: ask the registries which releases exist (design 11.3; only names and versions go out, as with pip and
	// npm themselves). Default true.
	Outdated *bool `yaml:"outdated"`
	// OutdatedHours: how often the registries are asked. Default 24.
	OutdatedHours int `yaml:"outdated_hours"`
	// MinAgeDays: a release must have been public this long before it counts as candidate - the only kind an update
	// installs (11.4). Default 7; there is no way to switch it off.
	MinAgeDays int `yaml:"min_age_days"`
	// MinAgeDaysSecurity: the shorter minimum age for a release that fixes an OSV advisory of the installed version
	// (ADR-0111). Only for the packages the hub names as such in the job; the rest of the plan keeps min_age_days.
	// Default 2 (or min_age_days if that is lower), from 1 to min_age_days; it cannot be switched off.
	MinAgeDaysSecurity int        `yaml:"min_age_days_security"`
	Registries         Registries `yaml:"registries"`
	// Update: locations (ids, "*" allowed: "services-*") whose packages Home Assistant may update (11.4). None by
	// default. Only virtual environments and npm locations can be updated.
	Update []string `yaml:"update"`
	// AllowScripts: npm locations where install scripts may run during an update (default: --ignore-scripts).
	AllowScripts []string `yaml:"allow_scripts"`
	// AllowSdist: Python locations where an update may build from source distributions (default: wheels only).
	AllowSdist []string `yaml:"allow_sdist"`
	// Containers: also report the images of the running containers, read from the container engine's socket, and -
	// with outdated - ask their registries, anonymously, whether a tag was rebuilt or a newer tag of the same form
	// exists (ADR-0113). Default off: it is new network traffic from the host. Only reported, never updated.
	Containers bool `yaml:"containers"`
}

// Matches reports whether a location id is named by one of the patterns (an id, or one with "*").
func Matches(patterns []string, id string) bool {
	for _, p := range patterns {
		if ok, _ := path.Match(p, id); ok { // patterns were checked by check-config
			return true
		}
	}
	return false
}

// Registries the agent asks; defaults pypi.org and registry.npmjs.org (a mirror can stand in for them).
type Registries struct {
	PyPI string `yaml:"pypi"`
	Npm  string `yaml:"npm"`
}

// PackageLocation is one place agent.yaml names.
type PackageLocation struct {
	ID   string `yaml:"id"`
	Path string `yaml:"path"`
}

// InventoryOn reports whether the agent reports packages at all.
func (p Packages) InventoryOn() bool { return p.Inventory == nil || *p.Inventory }

// DetectOn reports whether the agent looks in the well-known places.
func (p Packages) DetectOn() bool { return p.Detect == nil || *p.Detect }

// OutdatedOn reports whether the agent asks the registries.
func (p Packages) OutdatedOn() bool { return p.Outdated == nil || *p.Outdated }

// MinAge is min_age_days as a duration.
func (p Packages) MinAge() time.Duration { return time.Duration(p.MinAgeDays) * 24 * time.Hour }

// MinAgeSecurity is min_age_days_security as a duration (ADR-0111).
func (p Packages) MinAgeSecurity() time.Duration {
	return time.Duration(p.MinAgeDaysSecurity) * 24 * time.Hour
}

const (
	// MaxPackageLocations bounds packages.locations.
	MaxPackageLocations   = 50
	defaultMinAgeDays     = 7
	defaultMinAgeSecurity = 2 // days, ADR-0111
	defaultOutdatedHours  = 24
	DefaultPyPIRegistry   = "https://pypi.org"
	DefaultNpmRegistry    = "https://registry.npmjs.org"
	maxMinAgeDays         = 90
	minOutdatedHours      = 6
	maxOutdatedHoursValue = 168
)

// Check is one entry of health: exactly one of the three.
type Check struct {
	Systemd string `yaml:"systemd"` // the unit must be active
	Docker  string `yaml:"docker"`  // the container must run, and be healthy if it has a health check
	HTTP    string `yaml:"http"`    // GET must answer 2xx or 3xx within 10 s (the certificate is not checked: local services)
}

// MaxHealthChecks bounds health: (each one runs after every install).
const MaxHealthChecks = 20

type Hub struct {
	URL       string `yaml:"url"`
	PublicKey string `yaml:"public_key"` // base64 Ed25519 key; jobs without a valid signature are rejected
	// CAFile: PEM with the CA(s) the hub certificate must chain to, instead of the system roots (internal PKI).
	CAFile string `yaml:"ca_file"`
}

type Install struct {
	// AllowedScopes: security | all | app:<id> | image:<project>/<service>; empty: no install at all. An image entry
	// names a Compose service whose image Home Assistant may move to the rebuild of its tag (ADR-0119); "*" stands for
	// any part of a name ("image:stack/*").
	AllowedScopes []string `yaml:"allowed_scopes"`
	DefaultScope  string   `yaml:"default_scope"` // what the install button of "Systempakete" asks for
	Auto          string   `yaml:"auto"`          // off | hub ("local" is refused until it is built)
	// AutoScope: what install.auto installs (security | all); empty: default_scope. Lets a host install only
	// security updates on its own while the button in Home Assistant still installs everything.
	AutoScope    string  `yaml:"auto_scope"`
	Window       *Window `yaml:"window"`
	MinIntervalH int     `yaml:"min_interval_h"`
	TimeoutMin   int     `yaml:"timeout_min"`
	// Mode for scope all: upgrade (never installs new packages; ones that need new dependencies, e.g. a new kernel,
	// stay pending) or full-upgrade (installs them too; a run that would remove packages is still refused). Empty:
	// full-upgrade (ADR-0076).
	Mode string `yaml:"mode"`
}

const (
	ModeUpgrade     = "upgrade"
	ModeFullUpgrade = "full-upgrade"
)

var (
	// imageProjectGlob and imageServiceGlob: a Compose project or service name in which "*" may stand for any part.
	imageProjectGlob = regexp.MustCompile(`^[a-z0-9*][a-z0-9_*-]{0,55}$`)
	imageServiceGlob = regexp.MustCompile(`^[a-zA-Z0-9*][a-zA-Z0-9._*-]{0,55}$`)
)

// imagePattern reads an allowed_scopes entry "image:<project>/<service>" as the pattern "<project>/<service>" that
// Matches holds against a service; false when it is none.
func imagePattern(s string) (string, bool) {
	project, service, ok := strings.Cut(strings.TrimPrefix(s, "image:"), "/")
	if !strings.HasPrefix(s, "image:") || !ok || !imageProjectGlob.MatchString(project) ||
		!imageServiceGlob.MatchString(service) {
		return "", false
	}
	return project + "/" + service, true
}

// ImagePatterns: the Compose services whose image Home Assistant may update (ADR-0119), as "<project>/<service>"
// patterns for Matches. Empty: none.
func (in Install) ImagePatterns() []string {
	var out []string
	for _, s := range in.AllowedScopes {
		if p, ok := imagePattern(s); ok {
			out = append(out, p)
		}
	}
	return out
}

// AllowsImage reports whether install.allowed_scopes lets Home Assistant update the image of the Compose service.
func (in Install) AllowsImage(project, service string) bool {
	return Matches(in.ImagePatterns(), project+"/"+service)
}

type Reboot struct {
	Allowed bool    `yaml:"allowed"`
	Window  *Window `yaml:"window"`
}

type AgentUpdate struct {
	Enabled   bool   `yaml:"enabled"`
	PublicKey string `yaml:"public_key"` // base64 operator key that signs agent manifests
	// BackupPublicKey: a second operator key whose private half lies offline. If the first is lost or compromised,
	// releases signed with this one still reach every host - no visit to each machine to change agent.yaml.
	BackupPublicKey string `yaml:"backup_public_key"`
}

// Keys returns the operator keys that may sign a release: the key, then its backup if set. Invalid ones are left
// out (check-config reports them).
func (a AgentUpdate) Keys() []ed25519.PublicKey {
	var out []ed25519.PublicKey
	for _, k := range []string{a.PublicKey, a.BackupPublicKey} {
		if raw, err := base64.StdEncoding.DecodeString(k); err == nil && len(raw) == ed25519.PublicKeySize {
			out = append(out, ed25519.PublicKey(raw))
		}
	}
	return out
}

type App struct {
	ID      string `yaml:"id"`
	Name    string `yaml:"name"`
	Source  string `yaml:"source"`  // apt-package or winget-package
	Package string `yaml:"package"` // Debian package name or winget package id ("jqlang.jq")
}

const (
	AutoOff   = "off"
	AutoHub   = "hub"
	AutoLocal = "local"

	defaultMinIntervalH = 6
	defaultTimeoutMin   = 60
)

// goos is the system the policy is checked for; tests set it to check the rules of another system.
var goos = runtime.GOOS

// Issue is one problem found in agent.yaml; check-config lists all of them at once.
type Issue struct {
	Field   string
	Message string
}

func (i Issue) String() string { return i.Field + ": " + i.Message }

// Load reads and checks the file at path. A missing file is reported through found=false, not as an error; err is
// set only when the file exists but cannot be read.
func Load(path string) (cfg Config, issues []Issue, found bool, err error) {
	if _, statErr := os.Stat(path); errors.Is(statErr, fs.ErrNotExist) {
		return Config{}, nil, false, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, nil, true, err
	}
	cfg, issues = Parse(raw)
	return cfg, issues, true, nil
}

// Parse decodes agent.yaml, fills in defaults and checks every rule. Unknown keys are an issue: a typo must not
// silently weaken the policy.
func Parse(raw []byte) (Config, []Issue) {
	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if decErr := dec.Decode(&cfg); decErr != nil && len(bytes.TrimSpace(raw)) > 0 {
		return Config{}, []Issue{{"agent.yaml", decErr.Error()}}
	}
	cfg.applyDefaults()
	return cfg, cfg.Validate()
}

func (c *Config) applyDefaults() {
	if c.Node == "" {
		if h, err := os.Hostname(); err == nil {
			c.Node = strings.ToLower(h)
		}
	}
	if c.StateDir == "" {
		c.StateDir = DefaultStateDir
	}
	if c.Install.Auto == "" {
		c.Install.Auto = AutoOff
	}
	if c.Install.MinIntervalH == 0 {
		c.Install.MinIntervalH = defaultMinIntervalH
	}
	if c.Install.TimeoutMin == 0 {
		c.Install.TimeoutMin = defaultTimeoutMin
	}
	p := &c.Packages
	if p.MinAgeDays == 0 {
		p.MinAgeDays = defaultMinAgeDays
	}
	if p.MinAgeDaysSecurity == 0 {
		p.MinAgeDaysSecurity = min(defaultMinAgeSecurity, p.MinAgeDays)
	}
	if p.OutdatedHours == 0 {
		p.OutdatedHours = defaultOutdatedHours
	}
	if p.Registries.PyPI == "" {
		p.Registries.PyPI = DefaultPyPIRegistry
	}
	if p.Registries.Npm == "" {
		p.Registries.Npm = DefaultNpmRegistry
	}
	if c.GuestBackup != nil && c.GuestBackup.TimeoutMin == 0 {
		c.GuestBackup.TimeoutMin = defaultTimeoutMin
	}
	if c.Target != nil {
		// An instance reports on its appliance, not on the container it runs in: no local packages unless asked.
		if p.Inventory == nil {
			off := false
			p.Inventory = &off
		}
		if c.ConfigBackup == nil {
			c.ConfigBackup = &ConfigBackup{}
		}
		if c.ConfigBackup.Keep == 0 {
			c.ConfigBackup.Keep = defaultBackupKeep
		}
		if c.ConfigBackup.MaxAgeH == 0 {
			c.ConfigBackup.MaxAgeH = defaultBackupMaxAgeH
		}
	}
}

var (
	nodePattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,62}$`)
	packagePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]+$`) // Debian policy 5.6.1
	// unitPattern and containerPattern: names only, never options or shell (they become command arguments).
	locationPattern  = regexp.MustCompile(`^[a-z0-9*][a-z0-9_*-]{0,31}$`)
	unitPattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:_.@-]{0,250}\.(service|socket|timer|mount|target|path)$`)
	containerPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	// storagePattern: a Proxmox VE storage id (pve-storage-id).
	storagePattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,62}[a-z0-9]$`)
)

// Validate checks every rule and returns all issues.
func (c *Config) Validate() []Issue {
	var out []Issue
	add := func(field, format string, a ...any) { out = append(out, Issue{field, fmt.Sprintf(format, a...)}) }

	if !nodePattern.MatchString(c.Node) {
		add("node", "%q is not a valid node name (lowercase letters, digits, '.' and '-')", c.Node)
	}

	if c.Hub.URL != "" || c.Hub.PublicKey != "" {
		if u, err := url.Parse(c.Hub.URL); err != nil || u.Scheme != "wss" || u.Host == "" {
			add("hub.url", "must be a wss:// URL")
		}
		if !validKey(c.Hub.PublicKey) {
			add("hub.public_key", "missing or not a base64 Ed25519 public key - every job would be rejected")
		}
	}
	if c.Hub.CAFile != "" {
		if _, err := os.Stat(c.Hub.CAFile); err != nil {
			add("hub.ca_file", "%s is not readable", c.Hub.CAFile)
		}
	}

	apps := map[string]bool{}
	for i, a := range c.Apps {
		f := fmt.Sprintf("apps[%d]", i)
		switch {
		case !protocol.ValidAppID(a.ID):
			add(f+".id", "%q is not a valid app id", a.ID)
		case apps[a.ID]:
			add(f+".id", "%q appears twice", a.ID)
		}
		apps[a.ID] = true
		if strings.TrimSpace(a.Name) == "" {
			add(f+".name", "missing")
		}
		switch a.Source {
		case "apt-package":
			if !packagePattern.MatchString(a.Package) {
				add(f+".package", "%q is not a valid package name", a.Package)
			}
		case "winget-package":
			if !winget.IDPattern.MatchString(a.Package) {
				add(f+".package", "%q is not a valid winget package id", a.Package)
			}
		default:
			add(f+".source", "%q is unknown (known: apt-package, winget-package)", a.Source)
		}
	}

	in := c.Install
	images := 0 // image:<project>/<service> entries (ADR-0119)
	for _, s := range in.AllowedScopes {
		if strings.HasPrefix(s, "image:") {
			images++
			if _, ok := imagePattern(s); !ok {
				add("install.allowed_scopes", "%q is not image:<project>/<service> (Compose names; \"*\" for any part)", s)
			}
			continue
		}
		sc, ok := protocol.ParseScope(s)
		switch {
		case !ok:
			add("install.allowed_scopes", "%q is not a scope (security, all, app:<id>, image:<project>/<service>)", s)
		case sc.Kind == protocol.ScopeApp && !apps[sc.App]:
			add("install.allowed_scopes", "%q names no app from apps", s)
		case sc.Kind == protocol.ScopePkg:
			add("install.allowed_scopes", "%q: package updates are allowed per location by packages.update", s)
		case sc.Kind == protocol.ScopeImages:
			add("install.allowed_scopes", "%q: images are allowed service by service, image:<project>/<service>", s)
		}
	}
	switch {
	case len(in.AllowedScopes) > images && in.DefaultScope == "":
		add("install.default_scope", "required when install.allowed_scopes names system packages or apps")
	case strings.HasPrefix(in.DefaultScope, "image:"):
		add("install.default_scope", "%q: the default scope is what the button of the system packages installs; "+
			"images have the button of the container entity", in.DefaultScope)
	case in.DefaultScope != "" && !slices.Contains(in.AllowedScopes, in.DefaultScope):
		add("install.default_scope", "%q is not in install.allowed_scopes", in.DefaultScope)
	}
	if images > 0 {
		// The agent finds the services among the running containers it reports, and the registries' answers say which
		// image has a rebuild: without both there is nothing it could offer (ADR-0119).
		switch {
		case !c.Packages.Containers:
			add("install.allowed_scopes", "image:<project>/<service> needs packages.containers: true")
		case !c.Packages.OutdatedOn():
			add("install.allowed_scopes", "image:<project>/<service> needs packages.outdated: the registries' answers "+
				"say which image has a rebuild")
		}
	}
	switch in.Auto {
	case AutoOff:
	case AutoHub:
		if in.Window == nil {
			add("install.window", "required when install.auto is %q", in.Auto)
		}
		if in.DefaultScope == "" {
			add("install.default_scope", "required when install.auto is %q", in.Auto)
		}
		if goos == "windows" {
			// The operator's decision: Windows installs by button only (ADR-0086). Windows' own automatic updates keep running, and a
			// nightly run of Windows Update would install updates whose reboot nobody does.
			add("install.auto", "%q: on Windows, updates are installed by button only (ADR-0086); use off", in.Auto)
		}
	case AutoLocal:
		// Designed (design 10.3) but not built: nothing would ever start a run, and the host would silently never
		// install (ADR-0074).
		add("install.auto", "%q is not built yet - nothing would start a run; use hub or off", in.Auto)
	default:
		add("install.auto", "%q is unknown (off, hub)", in.Auto)
	}
	switch {
	case in.AutoScope == "":
	case in.AutoScope != "security" && in.AutoScope != "all":
		add("install.auto_scope", "%q is unknown (security, all, or leave it out for default_scope)", in.AutoScope)
	case !slices.Contains(in.AllowedScopes, in.AutoScope):
		add("install.auto_scope", "%q is not in install.allowed_scopes", in.AutoScope)
	}
	if in.Window != nil {
		out = append(out, in.Window.check("install.window")...)
	}
	if in.Mode != "" && in.Mode != ModeUpgrade && in.Mode != ModeFullUpgrade {
		add("install.mode", "%q is unknown (upgrade, full-upgrade, or leave it out)", in.Mode)
	}
	if in.MinIntervalH < 1 || in.MinIntervalH > 168 {
		add("install.min_interval_h", "must be between 1 and 168")
	}
	if in.TimeoutMin < 5 || in.TimeoutMin > 720 {
		add("install.timeout_min", "must be between 5 and 720")
	}

	if c.Reboot.Allowed && c.Reboot.Window == nil {
		add("reboot.window", "required when reboot.allowed is true")
	}
	if c.Reboot.Window != nil {
		out = append(out, c.Reboot.Window.check("reboot.window")...)
	}

	if c.AgentUpdate.Enabled && !validKey(c.AgentUpdate.PublicKey) {
		add("agent_update.public_key", "missing or not a base64 Ed25519 public key")
	}
	if b := c.AgentUpdate.BackupPublicKey; b != "" && (!validKey(b) || b == c.AgentUpdate.PublicKey) {
		add("agent_update.backup_public_key", "not a base64 Ed25519 public key, or the same as public_key")
	}

	if d := c.Packages.MinAgeDays; d < 1 || d > maxMinAgeDays {
		add("packages.min_age_days", "must be between 1 and %d (a fresh release is never installed)", maxMinAgeDays)
	}
	if d := c.Packages.MinAgeDaysSecurity; d < 1 || d > c.Packages.MinAgeDays {
		add("packages.min_age_days_security", "must be between 1 and min_age_days (%d): a fix is never installed fresh, "+
			"and never later than any other release", c.Packages.MinAgeDays)
	}
	if h := c.Packages.OutdatedHours; h < minOutdatedHours || h > maxOutdatedHoursValue {
		add("packages.outdated_hours", "must be between %d and %d", minOutdatedHours, maxOutdatedHoursValue)
	}
	if c.Packages.Containers {
		switch {
		case goos == "windows":
			add("packages.containers", "container images are read from the engine's unix socket on Linux only")
		case !c.Packages.InventoryOn():
			add("packages.containers", "the images are part of the inventory: packages.inventory must stay on")
		case slices.ContainsFunc(c.Packages.Locations, func(l PackageLocation) bool { return l.ID == "containers" }):
			add("packages.containers", "the location id \"containers\" is taken by the images; rename that location")
		}
	}
	for field, raw := range map[string]string{"packages.registries.pypi": c.Packages.Registries.PyPI,
		"packages.registries.npm": c.Packages.Registries.Npm} {
		if u, err := url.Parse(raw); err != nil || u.Scheme != "https" || u.Host == "" {
			add(field, "%q is not an https:// URL", raw)
		}
	}
	for field, list := range map[string][]string{"packages.update": c.Packages.Update,
		"packages.allow_scripts": c.Packages.AllowScripts, "packages.allow_sdist": c.Packages.AllowSdist} {
		for _, p := range list {
			if _, err := path.Match(p, "x"); err != nil || !locationPattern.MatchString(p) {
				add(field, "%q is not a location id or pattern (lowercase letters, digits, '_', '-', '*')", p)
			}
		}
	}
	if len(c.Packages.Locations) > MaxPackageLocations {
		add("packages.locations", "at most %d locations", MaxPackageLocations)
	}
	locIDs := map[string]bool{}
	for i, l := range c.Packages.Locations {
		f := fmt.Sprintf("packages.locations[%d]", i)
		switch {
		case !protocol.ValidAppID(l.ID):
			add(f+".id", "%q is not a valid id (lowercase letters, digits, '_' and '-', at most 32)", l.ID)
		case locIDs[l.ID]:
			add(f+".id", "%q appears twice", l.ID)
		}
		locIDs[l.ID] = true
		if !filepath.IsAbs(l.Path) {
			add(f+".path", "%q is not an absolute path", l.Path)
		} else if _, err := filepath.Match(l.Path, l.Path); err != nil {
			add(f+".path", "%q is not a valid pattern", l.Path)
		}
	}

	out = append(out, c.validateTarget()...)

	if b := c.GuestBackup; b != nil {
		switch {
		case goos != "linux":
			add("guest_backup", "only on a Proxmox VE host (Linux)")
		case c.Target != nil:
			add("guest_backup", "an instance speaks for an appliance; the hypervisor's own agent backs up its guests")
		case !storagePattern.MatchString(b.Storage):
			add("guest_backup.storage", "%q is not a Proxmox VE storage id", b.Storage)
		case !PBSStorage(b.Storage):
			add("guest_backup.storage", "%s has no Proxmox Backup Server storage %q - backups go to PBS only",
				PVEStorageConfig, b.Storage)
		}
		if b.TimeoutMin < 5 || b.TimeoutMin > 240 {
			add("guest_backup.timeout_min", "must be between 5 and 240")
		}
	}

	if len(c.Health) > MaxHealthChecks {
		add("health", "at most %d checks", MaxHealthChecks)
	}
	for i, h := range c.Health {
		f := fmt.Sprintf("health[%d]", i)
		set := 0
		for _, v := range []string{h.Systemd, h.Docker, h.HTTP} {
			if v != "" {
				set++
			}
		}
		switch {
		case set != 1:
			add(f, "needs exactly one of systemd, docker, http")
		case h.Systemd != "" && !unitPattern.MatchString(h.Systemd):
			add(f+".systemd", "%q is not a unit name (e.g. nginx.service)", h.Systemd)
		case h.Docker != "" && !containerPattern.MatchString(h.Docker):
			add(f+".docker", "%q is not a container name", h.Docker)
		case h.HTTP != "":
			if u, err := url.Parse(h.HTTP); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				add(f+".http", "%q is not an http(s) URL", h.HTTP)
			}
		}
	}
	return out
}

var hexFingerprint = regexp.MustCompile(`^[0-9a-f]{64}$`)

// sshHostKey: one host key line as ssh-keyscan prints it, without the host: type and base64 key.
var sshHostKey = regexp.MustCompile(`^(ssh-ed25519|ecdsa-sha2-nistp(256|384|521)|ssh-rsa) [A-Za-z0-9+/]+={0,2}$`)

// validateTarget: the target and config_backup sections, and what an instance must leave out. An instance speaks for
// an appliance: rebooting, packages, apps, local health checks and its own self-update refer to the container it
// runs in and would mean the wrong machine (design/stellvertreter.md 4.1, 4.3).
func (c *Config) validateTarget() []Issue {
	var out []Issue
	add := func(field, format string, a ...any) { out = append(out, Issue{field, fmt.Sprintf(format, a...)}) }
	t := c.Target
	if t == nil {
		if c.ConfigBackup != nil {
			add("config_backup", "only with target (an instance's appliance)")
		}
		return out
	}
	if !slices.Contains(TargetDrivers, t.Driver) {
		add("target.driver", "%q is unknown (%s)", t.Driver, strings.Join(TargetDrivers, ", "))
	}
	if slices.Contains(SSHDrivers, t.Driver) {
		// SSH: the user is part of the address, and the host key is the only way of trust (ADR-0094).
		u, err := url.Parse(t.Address)
		if err != nil || u.Scheme != "ssh" || u.User == nil || u.User.Username() == "" || u.Hostname() == "" ||
			(u.Path != "" && u.Path != "/") {
			add("target.address", "must be ssh://<user>@<address>[:port] without a path")
		}
		if !sshHostKey.MatchString(t.HostKey) {
			add("target.host_key", "must be the device's SSH host key as \"<type> <base64>\" (ssh-keyscan)")
		}
		if t.ServerName != "" || t.Fingerprint != "" || t.CAFile != "" {
			add("target", "server_name, fingerprint and ca_file are for https drivers; %s pins host_key", t.Driver)
		}
	} else {
		if u, err := url.Parse(t.Address); err != nil || u.Scheme != "https" || u.Host == "" || (u.Path != "" && u.Path != "/") {
			add("target.address", "must be https://<address>[:port] without a path")
		}
		switch {
		case (t.Fingerprint == "") == (t.ServerName == ""):
			add("target", "give either fingerprint (the device's own certificate) or server_name (a certificate for that name)")
		case t.Fingerprint != "" && !hexFingerprint.MatchString(t.TargetFingerprint()):
			add("target.fingerprint", "must be the SHA-256 of the device's certificate, 64 hex digits")
		case t.Fingerprint != "" && t.CAFile != "":
			add("target.ca_file", "only with server_name; a pinned fingerprint needs no roots")
		}
		if t.HostKey != "" {
			add("target.host_key", "only for SSH drivers (%s)", strings.Join(SSHDrivers, ", "))
		}
	}
	if t.CAFile != "" {
		if _, err := os.Stat(t.CAFile); err != nil {
			add("target.ca_file", "%s is not readable", t.CAFile)
		}
	}
	switch fi, err := os.Stat(t.Credentials); {
	case !filepath.IsAbs(t.Credentials):
		add("target.credentials", "must be an absolute path")
	case err != nil:
		add("target.credentials", "%s is not readable", t.Credentials)
	case goos != "windows" && fi.Mode().Perm()&0o077 != 0:
		add("target.credentials", "%s may be read by others (%o) - chmod 600", t.Credentials, fi.Mode().Perm())
	}
	if b := c.ConfigBackup; b != nil {
		if b.Keep < 1 || b.Keep > 365 {
			add("config_backup.keep", "must be between 1 and 365")
		}
		if b.MaxAgeH < 6 || b.MaxAgeH > 168 {
			add("config_backup.max_age_h", "must be between 6 and 168")
		}
	}
	if len(c.Install.AllowedScopes) > 0 {
		add("install", "firmware updates of an appliance are not built yet - leave install out (report and back up only)")
	}
	if c.Install.Auto != AutoOff {
		add("install.auto", "an appliance is never updated automatically (ADR-0089)")
	}
	if c.Reboot.Allowed {
		add("reboot.allowed", "an instance does not reboot; a firmware update restarts its appliance itself")
	}
	if c.AgentUpdate.Enabled {
		add("agent_update", "instances share the container's binary; the container's own agent updates it (design 4.3)")
	}
	if len(c.Apps) > 0 {
		add("apps", "an instance has no apps; they would be the container's")
	}
	if c.Packages.InventoryOn() {
		add("packages.inventory", "an instance reports its appliance, not the container's packages - leave it out")
	}
	for i, h := range c.Health {
		if h.HTTP == "" {
			add(fmt.Sprintf("health[%d]", i), "an instance can only check its appliance over http(s)")
		}
	}
	return out
}

// String names the check as it appears in a result: "systemd nginx.service".
func (h Check) String() string {
	switch {
	case h.Systemd != "":
		return "systemd " + h.Systemd
	case h.Docker != "":
		return "docker " + h.Docker
	}
	return "http " + h.HTTP
}

func validKey(b64 string) bool {
	raw, err := base64.StdEncoding.DecodeString(b64)
	return err == nil && len(raw) == ed25519.PublicKeySize
}

// HubKey returns the pinned hub key, or nil if none is configured (then every job is rejected).
func (c *Config) HubKey() ed25519.PublicKey {
	raw, err := base64.StdEncoding.DecodeString(c.Hub.PublicKey)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil
	}
	return ed25519.PublicKey(raw)
}

// Window is a weekly time window in the host's local time. If start is after end, the window runs past midnight
// and belongs to the day it starts on.
type Window struct {
	Days  []string `yaml:"days"` // mon .. sun
	Start string   `yaml:"start"`
	End   string   `yaml:"end"`
}

var dayNames = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"} // index = time.Weekday

func (w *Window) check(field string) []Issue {
	var out []Issue
	if len(w.Days) == 0 {
		out = append(out, Issue{field + ".days", "at least one day (mon .. sun)"})
	}
	for _, d := range w.Days {
		if !slices.Contains(dayNames, d) {
			out = append(out, Issue{field + ".days", fmt.Sprintf("%q is not a day (mon .. sun)", d)})
		}
	}
	s, okS := minutes(w.Start)
	e, okE := minutes(w.End)
	if !okS {
		out = append(out, Issue{field + ".start", "must be HH:MM"})
	}
	if !okE {
		out = append(out, Issue{field + ".end", "must be HH:MM"})
	}
	if okS && okE && s == e {
		out = append(out, Issue{field, "start and end are equal"})
	}
	return out
}

// Valid reports whether the window would pass check-config (for windows that arrive from elsewhere, e.g. at the hub).
func (w *Window) Valid() bool { return len(w.check("window")) == 0 }

// Contains reports whether t (in the host's local time) lies inside the window. The window must be valid.
func (w *Window) Contains(t time.Time) bool {
	s, _ := minutes(w.Start)
	e, _ := minutes(w.End)
	m := t.Hour()*60 + t.Minute()
	today := slices.Contains(w.Days, dayNames[t.Weekday()])
	if s < e {
		return today && m >= s && m < e
	}
	yesterday := slices.Contains(w.Days, dayNames[(t.Weekday()+6)%7])
	return (today && m >= s) || (yesterday && m < e)
}

func minutes(hhmm string) (int, bool) {
	t, err := time.Parse("15:04", hhmm)
	if err != nil {
		return 0, false
	}
	return t.Hour()*60 + t.Minute(), true
}
