package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"log"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/facts"
	"github.com/psewar/vordr/internal/hub"
	"github.com/psewar/vordr/internal/identity"
	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/source"
	"github.com/psewar/vordr/internal/wire"
)

// --json belongs to list and show only; anywhere else the command line is refused instead of ignoring it.
func TestParseArgsJSON(t *testing.T) {
	for _, c := range []struct {
		args   string
		ok     bool
		asJSON bool
		node   string
	}{
		{"list", true, false, ""},
		{"list --json", true, true, ""},
		{"show alpha", true, false, "alpha"},
		{"show alpha --json", true, true, "alpha"},
		{"--json show alpha --config /x.yaml", true, true, "alpha"},
		{"approve alpha --fingerprint ab --json", false, true, "alpha"},
		{"events --json", false, true, ""},
		{"clients --json", false, true, ""},
		{"check-config --json", false, true, ""},
		{"show alpha --yaml", false, false, "alpha"},
		{"show alpha bravo", false, false, "alpha"},
	} {
		got, ok := parseArgs(strings.Fields(c.args))
		if ok != c.ok || got.asJSON != c.asJSON || got.node != c.node {
			t.Errorf("%q: ok %v, %+v", c.args, ok, got)
		}
	}
}

// adminHub runs a hub's admin socket with an approved host that reported and ran a job, a waiting and a rejected one.
func adminHub(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	reg, err := hub.OpenRegistry(filepath.Join(dir, "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	clients, _ := hub.OpenClientRegistry(filepath.Join(dir, "clients.json"))
	events, _ := hub.OpenEvents(filepath.Join(dir, "events.jsonl"), time.Now, nil)
	t.Cleanup(func() { events.Close() })
	key, _, _ := identity.LoadOrCreateAt(filepath.Join(dir, "hub.key"))
	srv := hub.NewServer(reg, clients, events, key, 5, log.New(io.Discard, "", 0))
	srv.Version = "test"
	now := time.Now()
	for _, h := range []struct {
		node string
		to   wire.NodeState
	}{{"alpha", wire.StateApproved}, {"bravo", ""}, {"charlie", wire.StateRejected}} {
		pub, _, _ := ed25519.GenerateKey(rand.Reader)
		fp := identity.Fingerprint(pub)
		hello := wire.Hello{Node: h.node, PublicKey: pub, AgentVersion: "1.0",
			Verbs: []protocol.Verb{protocol.VerbCheck, protocol.VerbInstall}, Facts: facts.Host{OS: "TestOS 1"},
			Policy: wire.PolicySummary{AllowedScopes: []string{"all"}, DefaultScope: "all", Auto: "off"}}
		if _, _, err := reg.Admit(hello, fp, now, 5); err != nil {
			t.Fatal(err)
		}
		if h.to != "" && reg.Decide(h.node, h.to, fp, now) != hub.AdminOK {
			t.Fatal(h.node)
		}
	}
	rep := wire.Report{T: wire.TReport, CheckedAt: now, Reboot: &source.Reboot{Required: false, Checked: []string{"kernel"}},
		Sources: []source.Report{{Source: "apt", State: protocol.SourceUpdatesAvailable, Pending: 2, Security: 1,
			Packages: []source.Package{{Name: "pkga", From: "1", To: "2", Security: true}, {Name: "pkgb", From: "1", To: "2"}}}}}
	if err := reg.SetReport("alpha", rep); err != nil {
		t.Fatal(err)
	}
	if err := reg.SetRun("alpha", hub.Run{JobID: "job-1", Verb: protocol.VerbInstall, Scope: "all", By: "admin",
		Code: protocol.CodeOK, Started: now.Add(-time.Minute), Finished: &now}); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "run", "admin.sock")
	ln, err := hub.ListenAdmin(sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go srv.ServeAdmin(ln)
	return sock
}

// The command line prints the hub's document for programs and the text for people; its exit status says ok (0), a
// refusal with a code (1) or no usable answer (2).
func TestListAndShowOverTheSocket(t *testing.T) {
	sock := adminHub(t)
	call := func(req hub.AdminRequest, asJSON bool) (int, string) {
		var out bytes.Buffer
		status := adminTo(&out, sock, req, asJSON)
		return status, out.String()
	}
	parse := func(out string) map[string]any {
		var m map[string]any
		if err := json.Unmarshal([]byte(out), &m); err != nil {
			t.Fatalf("not one JSON document: %v\n%s", err, out)
		}
		return m
	}

	status, out := call(hub.AdminRequest{Cmd: "list"}, true)
	doc := parse(out)
	nodes, _ := doc["nodes"].([]any)
	if status != 0 || doc["format"] != 1.0 || doc["code"] != "ok" || doc["ha_protocol"] != float64(wire.HAProtocol) ||
		doc["hub_version"] != "test" || len(nodes) != 3 {
		t.Fatalf("list --json: %d %s", status, out)
	}
	var states []string
	for _, n := range nodes {
		node := n.(map[string]any)
		_, hasView := node["view"]
		states = append(states, node["node"].(string)+"="+node["state"].(string)+map[bool]string{true: "+view"}[hasView])
	}
	if got := strings.Join(states, " "); got != "alpha=approved+view bravo=pending_approval charlie=rejected" {
		t.Fatalf("list --json hosts: %s", got)
	}
	if strings.Contains(out, "public_key") {
		t.Fatalf("list --json carries a public key:\n%s", out)
	}

	status, out = call(hub.AdminRequest{Cmd: "show", Node: "alpha"}, true)
	doc = parse(out)
	nodes, _ = doc["nodes"].([]any)
	if status != 0 || doc["code"] != "ok" || len(nodes) != 1 {
		t.Fatalf("show alpha --json: %d %s", status, out)
	}
	view := nodes[0].(map[string]any)["view"].(map[string]any)
	if view["last_run"].(map[string]any)["job_id"] != "job-1" || view["pending"] != 2.0 || view["security"] != 1.0 {
		t.Fatalf("show alpha --json view: %v", view)
	}

	status, out = call(hub.AdminRequest{Cmd: "show", Node: "delta"}, true)
	doc = parse(out)
	if status != 1 || doc["code"] != "unknown_node" || doc["format"] != 1.0 || len(doc["nodes"].([]any)) != 0 {
		t.Fatalf("show delta --json: %d %s", status, out)
	}

	// For people.
	status, out = call(hub.AdminRequest{Cmd: "show", Node: "alpha"}, false)
	for _, want := range []string{"alpha  approved  offline", "fingerprint", "agent           1.0 since",
		"updates         2 pending (1 security, 2 installable), reported", "reboot          not needed",
		"problem         none", "last run        install all by admin: ok, started", "policy          install all (default all)"} {
		if !strings.Contains(out, want) {
			t.Errorf("show alpha: no %q in\n%s", want, out)
		}
	}
	if status != 0 {
		t.Errorf("show alpha: exit %d", status)
	}
	status, out = call(hub.AdminRequest{Cmd: "show", Node: "bravo"}, false)
	if status != 0 || !strings.Contains(out, "bravo  pending_approval  offline") || !strings.Contains(out, "no report yet") ||
		strings.Contains(out, "problem") {
		t.Errorf("show bravo: %d\n%s", status, out)
	}
	if status, out = call(hub.AdminRequest{Cmd: "show", Node: "delta"}, false); status != 1 || out != "delta  unknown_node\n" {
		t.Errorf("show delta: %d %q", status, out)
	}
	if status, out = call(hub.AdminRequest{Cmd: "list"}, false); status != 0 || !strings.Contains(out, "alpha") ||
		!strings.Contains(out, "2 pending (1 security), no reboot needed") || !strings.Contains(out, "charlie") {
		t.Errorf("list: %d\n%s", status, out)
	}

	// No hub: nothing on stdout, exit 2.
	var out2 bytes.Buffer
	if status := adminTo(&out2, filepath.Join(t.TempDir(), "none.sock"), hub.AdminRequest{Cmd: "list"}, true); status != 2 ||
		out2.Len() != 0 {
		t.Errorf("no hub: %d %q", status, out2.String())
	}
}

// A hub that runs an older version than the command (the binary was replaced, the service not yet restarted) answers
// list without the document and refuses show. The command says so and prints nothing, rather than an empty list.
func TestOlderHubGivesNoHostView(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "admin.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			var req hub.AdminRequest
			_ = json.NewDecoder(c).Decode(&req)
			answer := `{"code":"bad_request"}`
			if req.Cmd == "list" {
				answer = `{"code":"ok","nodes":[{"node":"alpha","state":"approved"}]}`
			}
			_, _ = c.Write([]byte(answer + "\n"))
			c.Close()
		}
	}()
	for _, req := range []hub.AdminRequest{{Cmd: "list"}, {Cmd: "show", Node: "alpha"}} {
		for _, asJSON := range []bool{true, false} {
			var out bytes.Buffer
			if status := adminTo(&out, sock, req, asJSON); status != 2 || out.Len() != 0 {
				t.Errorf("%s (json %v) against an older hub: %d %q", req.Cmd, asJSON, status, out.String())
			}
		}
	}
}
