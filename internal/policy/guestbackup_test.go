package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// guest_backup (ADR-0096): only on the hypervisor, only to a Proxmox Backup Server storage that the host has.
func TestGuestBackupRules(t *testing.T) {
	defer func(was, cfgPath string) { goos, PVEStorageConfig = was, cfgPath }(goos, PVEStorageConfig)
	goos = "linux"
	PVEStorageConfig = filepath.Join(t.TempDir(), "storage.cfg")
	storage := "dir: local\n\tpath /var/lib/vz\n\nnfs: shared\n\tserver 192.0.2.10\n\npbs: pbs\n\tdatastore pbs-store\n"
	if err := os.WriteFile(PVEStorageConfig, []byte(storage), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, issues := Parse([]byte("node: hypervisor\nguest_backup:\n  storage: pbs\n"))
	if len(issues) > 0 || cfg.GuestBackup.TimeoutMin != 60 {
		t.Fatalf("valid: %v, timeout %d", issues, cfg.GuestBackup.TimeoutMin)
	}
	if cfg, issues := Parse([]byte("node: guest\n")); len(issues) > 0 || cfg.GuestBackup != nil {
		t.Fatalf("without the section the agent offers no backups: %v %+v", issues, cfg.GuestBackup)
	}
	for name, tc := range map[string]struct{ yaml, field, says string }{
		"no storage":       {"guest_backup:\n  timeout_min: 30\n", "guest_backup.storage", "not a Proxmox VE storage id"},
		"option":           {"guest_backup:\n  storage: --all\n", "guest_backup.storage", "not a Proxmox VE storage id"},
		"not pbs":          {"guest_backup:\n  storage: shared\n", "guest_backup.storage", "PBS only"},
		"unknown storage":  {"guest_backup:\n  storage: elsewhere\n", "guest_backup.storage", "PBS only"},
		"timeout too long": {"guest_backup:\n  storage: pbs\n  timeout_min: 241\n", "guest_backup.timeout_min", "between"},
		"unknown key":      {"guest_backup:\n  storage: pbs\n  prune: true\n", "agent.yaml", "prune"},
	} {
		t.Run(name, func(t *testing.T) {
			_, issues := Parse([]byte("node: hypervisor\n" + tc.yaml))
			if len(issues) != 1 || issues[0].Field != tc.field || !strings.Contains(issues[0].Message, tc.says) {
				t.Fatalf("issues %v, want one on %s saying %q", issues, tc.field, tc.says)
			}
		})
	}

	PVEStorageConfig = filepath.Join(t.TempDir(), "missing.cfg")
	if _, issues := Parse([]byte("node: guest\nguest_backup:\n  storage: pbs\n")); len(issues) != 1 {
		t.Fatalf("a host without Proxmox VE storages: %v", issues)
	}
	goos = "windows"
	if _, issues := Parse([]byte("node: pc\nguest_backup:\n  storage: pbs\n")); len(issues) != 1 ||
		issues[0].Field != "guest_backup" {
		t.Fatalf("windows: %v", issues)
	}
}
