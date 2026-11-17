package main

import (
	"bytes"
	"context"
	"encoding/pem"
	"io"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/agent"
	"github.com/psewar/vordr/internal/hub"
	"github.com/psewar/vordr/internal/identity"
	"github.com/psewar/vordr/internal/wire"
)

// hub-check's exit code is its finding (ADR-0170): install-agent.sh and install-service tell an unreachable hub from
// one with another key by it, not by its text.
func TestReportProbeExitCodes(t *testing.T) {
	for name, tc := range map[string]struct {
		r    agent.ProbeResult
		code int
		says string
	}{
		"key matches": {agent.ProbeResult{HubKey: wire.HubKeyMatches}, hubCheckOK, "hub key: matches hub.public_key"},
		"older hub":   {agent.ProbeResult{HubKey: wire.HubKeyUnsigned}, hubCheckOK, "hub key: not checked"},
		"another key": {agent.ProbeResult{Failed: agent.StageHubKey, HubKey: wire.HubKeyMismatch}, hubCheckWrongKey, "vordr-hub key"},
		"stopped signing": {agent.ProbeResult{Failed: agent.StageHubKey, HubKey: wire.HubKeyDowngrade, Detail: "hub-signed.json"},
			hubCheckWrongKey, "signed its challenge with hub.public_key before"},
		"not reachable":   {agent.ProbeResult{Failed: agent.StageConnect, Detail: "refused"}, hubCheckUnreachable, "detail: refused"},
		"not a vordr hub": {agent.ProbeResult{Failed: agent.StageChallenge}, hubCheckUnreachable, "not a vordr hub"},
	} {
		var out bytes.Buffer
		if code := reportProbe(&out, "wss://hub.example/v1/tunnel", tc.r); code != tc.code || !strings.Contains(out.String(), tc.says) {
			t.Errorf("%s: exit %d, want %d:\n%s", name, code, tc.code, out.String())
		}
	}
}

// install-service goes on only with hub-check's exit 0; every other code refuses with its own reason before anything
// changes (ADR-0170). The mapping is apart from the Windows service code, so it is tested on every platform.
func TestHubCheckRefusal(t *testing.T) {
	const cfg = "conf/agent.yaml"
	for code, says := range map[int]string{
		hubCheckOK:          "",
		hubCheckWrongKey:    "does not sign with hub.public_key of " + cfg,
		hubCheckUnreachable: "not reachable",
		hubCheckNoConfig:    cfg + " is not usable",
		42:                  "not reachable",
	} {
		got := hubCheckRefusal(code, cfg)
		if (says == "") != (got == "") || !strings.Contains(got, says) ||
			got != "" && !strings.HasSuffix(got, "nothing changed") {
			t.Errorf("exit %d: %q", code, got)
		}
	}
}

// hub-check from agent.yaml against a real hub: exit 0 with its key, 3 with another one.
func TestHubCheckAgainstTheHub(t *testing.T) {
	dir := t.TempDir()
	key, _, err := identity.LoadOrCreateAt(filepath.Join(dir, "hub.key"))
	if err != nil {
		t.Fatal(err)
	}
	reg, _ := hub.OpenRegistry(filepath.Join(dir, "registry.json"))
	events, _ := hub.OpenEvents(filepath.Join(dir, "events.jsonl"), time.Now, nil)
	t.Cleanup(func() { events.Close() })
	clients, _ := hub.OpenClientRegistry(filepath.Join(dir, "clients.json"))
	srv := hub.NewServer(reg, clients, events, key, 5, log.New(io.Discard, "", 0))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Close(ctx)
	})
	ts := httptest.NewTLSServer(srv.Handler())
	t.Cleanup(ts.Close)
	ca := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ts.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	other, _, _ := identity.LoadOrCreateAt(filepath.Join(dir, "other.key"))
	url := "wss" + strings.TrimPrefix(ts.URL, "https") + wire.TunnelPath
	for name, tc := range map[string]struct {
		pinned string
		code   int
	}{
		"the hub's key": {key.PublicB64(), hubCheckOK},
		"another key":   {other.PublicB64(), hubCheckWrongKey},
	} {
		agentDir := t.TempDir()
		cfg := writeFile(t, filepath.Join(agentDir, "agent.yaml"), "node: host-a\nstate_dir: "+agentDir+
			"\nhub: {url: '"+url+"', public_key: "+tc.pinned+", ca_file: '"+ca+"'}\n")
		if code := hubCheck(cfg); code != tc.code {
			t.Errorf("%s: exit %d, want %d", name, code, tc.code)
		}
	}
	if _, known := reg.Get("host-a"); known {
		t.Fatal("hub-check logged in")
	}
}
