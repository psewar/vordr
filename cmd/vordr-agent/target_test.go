package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// check-config of a proxy instance, with either way of checking the device's certificate (the server_name variant
// once panicked on the missing fingerprint).
func TestCheckConfigTarget(t *testing.T) {
	dir := t.TempDir()
	cred := writeFile(t, filepath.Join(dir, "nas.cred"), "USER=vordr-gna\nPASS=x\n")
	base := "node: delta\nstate_dir: " + dir + "\ntarget:\n  driver: synology-dsm\n  address: https://192.0.2.10:5001\n" +
		"  credentials: " + cred + "\n"
	for name, tls := range map[string]string{
		"server_name": "  server_name: nas.example\n",
		"fingerprint": "  fingerprint: \"" + strings.Repeat("ab", 32) + "\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := writeFile(t, filepath.Join(dir, name+".yaml"), base+tls)
			if rc := checkConfig(path); rc != 0 {
				t.Fatalf("check-config: exit %d", rc)
			}
		})
	}
	// An SSH driver shows its pinned host key instead (ADR-0094).
	ssh := "node: alpha\nstate_dir: " + dir + "\ntarget:\n  driver: pfsense\n  address: ssh://vordr-gna@192.0.2.1\n" +
		"  host_key: \"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHZvcmRyLXRlc3QtaG9zdC1rZXktMDEyMzQ1Njc4OWFi\"\n" +
		"  credentials: " + cred + "\n"
	if rc := checkConfig(writeFile(t, filepath.Join(dir, "pfsense.yaml"), ssh)); rc != 0 {
		t.Fatalf("check-config pfsense: exit %d", rc)
	}
}
