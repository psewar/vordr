// Package hub is the control centre (Leitstelle): it admits agents, signs their jobs, keeps the register and the
// event log. It never runs commands on hosts; all it can do is ask an agent through a signed job from the closed
// verb set, and the agent's local policy has the last word.
package hub

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"gopkg.in/yaml.v3"
)

// Config is hub.yaml. See hub.example.yaml.
type Config struct {
	Listen      string `yaml:"listen"`       // default ":8443"
	TLSCert     string `yaml:"tls_cert"`     // PEM; re-read when the file changes (certificate renewal)
	TLSKey      string `yaml:"tls_key"`      // PEM
	StateDir    string `yaml:"state_dir"`    // hub key, register, event log; default /var/lib/vordr-hub
	AdminSocket string `yaml:"admin_socket"` // local administration only; default /run/vordr-hub/admin.sock
	MaxPending  int    `yaml:"max_pending"`  // hosts waiting for approval at most; default 20
	// StaleAfterDays: updates pending longer than this without a run that installs them set the problem sensor.
	StaleAfterDays int `yaml:"stale_after_days"`
	// OfflineAfterMin: a host that stays disconnected this long sets the problem sensor (a reboot does not).
	OfflineAfterMin int `yaml:"offline_after_min"`
	// RebootPendingAfterDays: a host that has needed a reboot this long sets the problem sensor.
	RebootPendingAfterDays int `yaml:"reboot_pending_after_days"`
	// NoReportAfterHours: a connected host that has not reported a check this long sets the problem sensor (agents
	// check every 6 hours).
	NoReportAfterHours int `yaml:"no_report_after_hours"`
	// MQTT is gone since 0.6 (Home Assistant connects itself over /v1/ha). Still read, so that an old hub.yaml gets
	// told what to do instead of a bare "unknown field".
	MQTT any `yaml:"mqtt"`
	// Backup: with it, an install only starts when the host's last backup on Proxmox Backup Server is fresh.
	Backup *BackupConfig `yaml:"backup"`
	// AgentUpdate: with it, the hub hands out agent releases from <state_dir>/agent and rolls them out in rings.
	AgentUpdate *AgentUpdateConfig `yaml:"agent_update"`
	// AutoInstall: with it, the hub starts install runs itself for hosts with install.auto: hub, in rings.
	AutoInstall *AutoInstallConfig `yaml:"auto_install"`
	// OSV: with it, the hub downloads the OSV data of PyPI and npm daily and matches the hosts' packages against it.
	OSV *OSVConfig `yaml:"osv"`
}

const (
	DefaultConfigPath  = "/etc/vordr-hub/hub.yaml"
	defaultListen      = ":8443"
	defaultStateDir    = "/var/lib/vordr-hub"
	defaultAdminSocket = "/run/vordr-hub/admin.sock"
	defaultMaxPending  = 20
	defaultStaleDays   = 7
	defaultOfflineMin  = 10
	defaultRebootDays  = 7
	defaultNoReportH   = 26
)

// LoadConfig reads hub.yaml. found=false: no file; issues: everything wrong with it at once.
func LoadConfig(path string) (cfg Config, issues []string, found bool, err error) {
	if _, statErr := os.Stat(path); errors.Is(statErr, fs.ErrNotExist) {
		return Config{}, nil, false, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, nil, true, err
	}
	cfg, issues = ParseConfig(raw)
	return cfg, issues, true, nil
}

// ParseConfig decodes hub.yaml (unknown keys are an issue), fills in defaults and checks it.
func ParseConfig(raw []byte) (Config, []string) {
	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil && len(bytes.TrimSpace(raw)) > 0 {
		return Config{}, []string{"hub.yaml: " + err.Error()}
	}
	if cfg.Listen == "" {
		cfg.Listen = defaultListen
	}
	if cfg.StateDir == "" {
		cfg.StateDir = defaultStateDir
	}
	if cfg.AdminSocket == "" {
		cfg.AdminSocket = defaultAdminSocket
	}
	if cfg.MaxPending == 0 {
		cfg.MaxPending = defaultMaxPending
	}
	if cfg.StaleAfterDays == 0 {
		cfg.StaleAfterDays = defaultStaleDays
	}
	if cfg.OfflineAfterMin == 0 {
		cfg.OfflineAfterMin = defaultOfflineMin
	}
	if cfg.RebootPendingAfterDays == 0 {
		cfg.RebootPendingAfterDays = defaultRebootDays
	}
	if cfg.NoReportAfterHours == 0 {
		cfg.NoReportAfterHours = defaultNoReportH
	}
	// readable opens the file: run as the service user, check-config tells whether the hub will be able to read it.
	readable := func(path string) bool {
		f, err := os.Open(path)
		if err != nil {
			return false
		}
		f.Close()
		return true
	}
	var issues []string
	if cfg.MQTT != nil {
		issues = append(issues, "mqtt: no longer supported - Home Assistant connects itself over /v1/ha (integration "+
			"vordr, admitted with vordr-hub approve-client); delete the mqtt section")
	}
	if cfg.StaleAfterDays < 1 || cfg.StaleAfterDays > 365 {
		issues = append(issues, "stale_after_days: must be between 1 and 365")
	}
	if cfg.OfflineAfterMin < 1 || cfg.OfflineAfterMin > 1440 {
		issues = append(issues, "offline_after_min: must be between 1 and 1440")
	}
	if cfg.RebootPendingAfterDays < 1 || cfg.RebootPendingAfterDays > 365 {
		issues = append(issues, "reboot_pending_after_days: must be between 1 and 365")
	}
	if cfg.NoReportAfterHours < 7 || cfg.NoReportAfterHours > 24*30 {
		issues = append(issues, "no_report_after_hours: must be between 7 and 720 (agents check every 6 hours)")
	}
	for field, path := range map[string]string{"tls_cert": cfg.TLSCert, "tls_key": cfg.TLSKey} {
		if path == "" {
			issues = append(issues, field+": required (the tunnel only runs over TLS)")
		} else if !readable(path) {
			issues = append(issues, fmt.Sprintf("%s: %s is not readable", field, path))
		}
	}
	if cfg.MaxPending < 1 || cfg.MaxPending > 1000 {
		issues = append(issues, "max_pending: must be between 1 and 1000")
	}
	if b := cfg.Backup; b != nil {
		if b.MaxAgeHours == 0 {
			b.MaxAgeHours = defaultBackupMaxAgeH
		}
		issues = append(issues, b.check(readable)...)
	}
	if a := cfg.AgentUpdate; a != nil {
		if a.CanaryCleanHours == 0 {
			a.CanaryCleanHours = defaultCanaryCleanH
		}
		if a.RetryAfterHours == 0 {
			a.RetryAfterHours = defaultRetryAfterH
		}
		issues = append(issues, a.check()...)
	}
	if a := cfg.AutoInstall; a != nil {
		if a.DelayHours == 0 {
			a.DelayHours = defaultAutoDelayH
		}
		issues = append(issues, a.check()...)
	}
	if o := cfg.OSV; o != nil {
		o.applyDefaults()
		issues = append(issues, o.check()...)
	}
	return cfg, issues
}
