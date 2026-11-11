package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A proxy instance (design/stellvertreter.md 4.1): one appliance, TLS either pinned or by name, credentials readable
// by the instance only, and nothing that would mean the container it runs in.
func TestTargetRules(t *testing.T) {
	dir := t.TempDir()
	cred := filepath.Join(dir, "nas.cred")
	if err := os.WriteFile(cred, []byte("USER=vordr-gna\nPASS=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	open := filepath.Join(dir, "open.cred")
	if err := os.WriteFile(open, []byte("USER=x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(open, 0o644); err != nil { // umask may have narrowed it
		t.Fatal(err)
	}
	fp := strings.Repeat("AB:", 31) + "AB"
	base := "node: delta\ntarget:\n  driver: synology-dsm\n  address: https://192.0.2.10:5001\n" +
		"  server_name: delta.example\n  credentials: " + cred + "\n"

	cfg, issues := Parse([]byte(base))
	if len(issues) > 0 {
		t.Fatalf("valid instance: %v", issues)
	}
	if cfg.Packages.InventoryOn() || cfg.ConfigBackup == nil || cfg.ConfigBackup.Keep != 30 || cfg.ConfigBackup.MaxAgeH != 26 {
		t.Fatalf("defaults: inventory %v, backup %+v", cfg.Packages.InventoryOn(), cfg.ConfigBackup)
	}
	pinned := strings.Replace(base, "  server_name: delta.example\n", "  fingerprint: \""+fp+"\"\n", 1)
	cfg, issues = Parse([]byte(pinned))
	if len(issues) > 0 || cfg.Target.TargetFingerprint() != strings.Repeat("ab", 32) {
		t.Fatalf("pinned: %v %q", issues, cfg.Target.TargetFingerprint())
	}
	// An SSH driver (ADR-0094): user in the address, trust only through the pinned host key.
	const hostKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHZvcmRyLXRlc3QtaG9zdC1rZXktMDEyMzQ1Njc4OWFi"
	ssh := "node: alpha\ntarget:\n  driver: pfsense\n  address: ssh://vordr-gna@192.0.2.1\n" +
		"  host_key: \"" + hostKey + "\"\n  credentials: " + cred + "\n"
	if cfg, issues = Parse([]byte(ssh)); len(issues) > 0 || cfg.Target.HostKey != hostKey {
		t.Fatalf("valid ssh instance: %v", issues)
	}
	if _, issues = Parse([]byte(strings.Replace(ssh, "192.0.2.1\n", "192.0.2.1:2222\n", 1))); len(issues) > 0 {
		t.Fatalf("ssh with a port: %v", issues)
	}

	for name, tc := range map[string]struct{ yaml, field string }{
		"host_key on https":     {strings.Replace(base, "  credentials:", "  host_key: \""+hostKey+"\"\n  credentials:", 1), "target.host_key"},
		"ssh: https address":    {strings.Replace(ssh, "ssh://vordr-gna@192.0.2.1", "https://192.0.2.1", 1), "target.address"},
		"ssh: no user":          {strings.Replace(ssh, "vordr-gna@", "", 1), "target.address"},
		"ssh: path":             {strings.Replace(ssh, "192.0.2.1\n", "192.0.2.1/x\n", 1), "target.address"},
		"ssh: no host key":      {strings.Replace(ssh, "  host_key: \""+hostKey+"\"\n", "", 1), "target.host_key"},
		"ssh: host key no type": {strings.Replace(ssh, "ssh-ed25519 ", "", 1), "target.host_key"},
		"ssh: host key + host":  {strings.Replace(ssh, "\""+hostKey+"\"", "\"192.0.2.1 "+hostKey+"\"", 1), "target.host_key"},
		"ssh: server_name":      {strings.Replace(ssh, "  credentials:", "  server_name: alpha.example\n  credentials:", 1), "target"},
		"ssh: fingerprint":      {strings.Replace(ssh, "  credentials:", "  fingerprint: \""+fp+"\"\n  credentials:", 1), "target"},
		"unknown driver":        {strings.Replace(base, "synology-dsm", "router-x", 1), "target.driver"},
		"http address":          {strings.Replace(base, "https://", "http://", 1), "target.address"},
		"address with path":     {strings.Replace(base, ":5001\n", ":5001/webapi\n", 1), "target.address"},
		"neither tls way":       {strings.Replace(base, "  server_name: delta.example\n", "", 1), "target"},
		"both tls ways":         {strings.Replace(base, "  credentials:", "  fingerprint: \""+fp+"\"\n  credentials:", 1), "target"},
		"short fingerprint":     {strings.Replace(pinned, fp, "04:6A", 1), "target.fingerprint"},
		"ca_file with pin":      {strings.Replace(pinned, "  credentials:", "  ca_file: "+cred+"\n  credentials:", 1), "target.ca_file"},
		"missing ca_file":       {strings.Replace(base, "  credentials:", "  ca_file: /nowhere/ca.pem\n  credentials:", 1), "target.ca_file"},
		"relative cred":         {strings.Replace(base, cred, "nas.cred", 1), "target.credentials"},
		"missing cred":          {strings.Replace(base, cred, filepath.Join(dir, "none.cred"), 1), "target.credentials"},
		"backup keep 0":         {base + "config_backup: {keep: 400}\n", "config_backup.keep"},
		"backup age":            {base + "config_backup: {max_age_h: 2}\n", "config_backup.max_age_h"},
		"install":               {base + "install: {allowed_scopes: [all], default_scope: all}\n", "install"},
		"reboot":                {base + "reboot: {allowed: true, window: {days: [mon], start: \"01:00\", end: \"02:00\"}}\n", "reboot.allowed"},
		"self-update":           {base + "agent_update: {enabled: true, public_key: \"" + key(t) + "\"}\n", "agent_update"},
		"apps":                  {base + "apps: [{id: x, name: X, source: apt-package, package: jq}]\n", "apps"},
		"packages":              {base + "packages: {inventory: true}\n", "packages.inventory"},
		"local health":          {base + "health: [{systemd: nginx.service}]\n", "health[0]"},
		"backup without target": {"node: h\nconfig_backup: {keep: 3}\n", "config_backup"},
	} {
		t.Run(name, func(t *testing.T) {
			_, issues := Parse([]byte(tc.yaml))
			for _, i := range issues {
				if i.Field == tc.field {
					return
				}
			}
			t.Fatalf("want an issue on %s, got %v", tc.field, issues)
		})
	}
	if goos != "windows" {
		_, issues := Parse([]byte(strings.Replace(base, cred, open, 1)))
		if len(issues) != 1 || issues[0].Field != "target.credentials" {
			t.Fatalf("credentials readable by others: %v", issues)
		}
	}
	if _, issues := Parse([]byte(base + "health: [{http: \"https://192.0.2.10:5001/\"}]\n")); len(issues) > 0 {
		t.Fatalf("http health check of the appliance: %v", issues)
	}
}
