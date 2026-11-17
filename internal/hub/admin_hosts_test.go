package hub

import (
	"encoding/base64"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/source"
	"github.com/psewar/vordr/internal/wire"
)

// hostsFixture: an approved host with a report and a finished run, one that waits for approval and one that was
// rejected.
func hostsFixture(t *testing.T) *viewFixture {
	t.Helper()
	f := newViewFixture(t)
	verbs := []protocol.Verb{protocol.VerbCheck, protocol.VerbInstall, protocol.VerbReboot}
	f.host(t, "alpha", verbs, wire.PolicySummary{AllowedScopes: []string{"security", "all"}, DefaultScope: "all",
		Auto: "off", RebootAllowed: true}, true)
	reboot := &source.Reboot{Required: true, Reason: protocol.RebootReason("kernel"), Checked: []string{"kernel"}}
	if err := f.srv.Reg.SetReport("alpha", aptReport(f.now, 3, 1, reboot)); err != nil {
		t.Fatal(err)
	}
	done := f.now
	if err := f.srv.Reg.SetRun("alpha", Run{JobID: "job-1", Verb: protocol.VerbInstall, Scope: "all", By: "admin",
		Code: protocol.CodeOK, Started: f.now.Add(-time.Minute), Finished: &done}); err != nil {
		t.Fatal(err)
	}
	f.host(t, "bravo", []protocol.Verb{protocol.VerbCheck}, wire.PolicySummary{}, false)
	fp := f.host(t, "charlie", []protocol.Verb{protocol.VerbCheck}, wire.PolicySummary{}, true)
	if code := f.srv.Reg.Decide("charlie", wire.StateRejected, fp, f.now); code != AdminOK {
		t.Fatal(code)
	}
	return f
}

// asMap: the document as a program reads it.
func asMap(t *testing.T, doc *HostsDoc) (map[string]any, string) {
	t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m, string(raw)
}

// noSecrets fails on any key that names a key, token or secret anywhere in the document, and on the hosts' public
// keys in any form; fingerprints are the only trace of a key.
func noSecrets(t *testing.T, f *viewFixture, m map[string]any, raw string) {
	t.Helper()
	var walk func(path string, v any)
	walk = func(path string, v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, val := range x {
				for _, bad := range []string{"public_key", "private", "secret", "token", "password", "signature"} {
					if strings.Contains(strings.ToLower(k), bad) {
						t.Errorf("%s.%s: a secret field in the host view", path, k)
					}
				}
				walk(path+"."+k, val)
			}
		case []any:
			for i, val := range x {
				walk(path+"["+strconv.Itoa(i)+"]", val)
			}
		}
	}
	walk("$", m)
	for _, e := range f.srv.Reg.List() {
		for _, enc := range []string{base64.StdEncoding.EncodeToString(e.PublicKey),
			base64.RawURLEncoding.EncodeToString(e.PublicKey)} {
			if strings.Contains(raw, enc) {
				t.Errorf("%s: its public key is in the document", e.Node)
			}
		}
	}
}

func nodeOf(t *testing.T, m map[string]any, name string) map[string]any {
	t.Helper()
	for _, n := range m["nodes"].([]any) {
		if node := n.(map[string]any); node["node"] == name {
			return node
		}
	}
	t.Fatalf("no host %s in %v", name, m["nodes"])
	return nil
}

// "list" answers one document with every host: the register's facts for each, and for an approved host the view Home
// Assistant gets, computed the same way. It is versioned, and it carries no key but fingerprints.
func TestListAnswersTheHostDocument(t *testing.T) {
	f := hostsFixture(t)
	resp := f.srv.Admin(AdminRequest{Cmd: "list"})
	if resp.Code != AdminOK || resp.Hosts == nil {
		t.Fatalf("list: %+v", resp)
	}
	doc := resp.Hosts
	if doc.Format != AdminFormat || AdminFormat != 1 || doc.Code != AdminOK || doc.HAProtocol != wire.HAProtocol ||
		doc.HubVersion != "test" || !doc.At.Equal(f.now) || doc.At.Location() != time.UTC {
		t.Fatalf("document head: %+v", doc)
	}
	m, raw := asMap(t, doc)
	for _, k := range []string{"format", "code", "ha_protocol", "hub_version", "at", "nodes"} {
		if _, ok := m[k]; !ok {
			t.Errorf("document without %q", k)
		}
	}
	var names []string
	for _, n := range doc.Nodes {
		names = append(names, n.Node)
	}
	if !slices.Equal(names, []string{"alpha", "bravo", "charlie"}) {
		t.Fatalf("hosts %v, want sorted by name", names)
	}
	for _, name := range names {
		n := nodeOf(t, m, name)
		for _, k := range []string{"node", "state", "fingerprint", "online", "agent_version", "os", "first_seen", "facts",
			"verbs", "policy"} {
			if _, ok := n[k]; !ok {
				t.Errorf("%s: no %q", name, k)
			}
		}
		if n["verbs"] == nil {
			t.Errorf("%s: verbs null, want a list", name)
		}
	}

	alpha := nodeOf(t, m, "alpha")
	if alpha["state"] != "approved" || alpha["decided_at"] == nil || alpha["pending"] != 3.0 ||
		alpha["security"] != 1.0 || alpha["reboot_required"] != true {
		t.Errorf("alpha: %v", alpha)
	}
	view, ok := alpha["view"].(map[string]any)
	if !ok {
		t.Fatalf("alpha: no view: %v", alpha)
	}
	run, _ := view["last_run"].(map[string]any)
	reboot, _ := view["reboot"].(map[string]any)
	if run["job_id"] != "job-1" || run["state"] != "ok" || run["by"] != "admin" || view["pending"] != 3.0 ||
		reboot["required"] != true || view["problem"] != nil {
		t.Errorf("alpha's view: %v", view)
	}
	// The same calculation as for Home Assistant, field for field.
	e, _ := f.srv.Reg.Get("alpha")
	want, _ := json.Marshal(f.srv.HostView(e, f.now))
	got, _ := json.Marshal(doc.Nodes[0].View)
	if string(got) != string(want) {
		t.Errorf("alpha's view differs from Home Assistant's:\n%s\n%s", got, want)
	}

	bravo := nodeOf(t, m, "bravo")
	if bravo["state"] != "pending_approval" || bravo["decided_at"] != nil || bravo["fingerprint"] == "" {
		t.Errorf("bravo: %v", bravo)
	}
	charlie := nodeOf(t, m, "charlie")
	if charlie["state"] != "rejected" || charlie["decided_at"] == nil {
		t.Errorf("charlie: %v", charlie)
	}
	for _, n := range []map[string]any{bravo, charlie} {
		if _, ok := n["view"]; ok {
			t.Errorf("%s: a view for a host that is not approved", n["node"])
		}
	}
	noSecrets(t, f, m, raw)
}

// A hub without hosts answers an empty list, not a missing one.
func TestListWithoutHosts(t *testing.T) {
	f := newViewFixture(t)
	resp := f.srv.Admin(AdminRequest{Cmd: "list"})
	if resp.Code != AdminOK || resp.Hosts == nil {
		t.Fatalf("list: %+v", resp)
	}
	if _, raw := asMap(t, resp.Hosts); !strings.Contains(raw, `"nodes":[]`) {
		t.Fatalf("no hosts: %s", raw)
	}
}

// "show" is list narrowed to one host; a host the register does not know is refused with unknown_node, a request
// without a host with bad_request, and both still carry the document with no host in it.
func TestShowOneHost(t *testing.T) {
	f := hostsFixture(t)
	for _, c := range []struct {
		node     string
		code     AdminCode
		hosts    int
		withView bool
	}{
		{"alpha", AdminOK, 1, true},
		{"bravo", AdminOK, 1, false},
		{"charlie", AdminOK, 1, false},
		{"delta", AdminUnknownNode, 0, false},
		{"", AdminBadRequest, 0, false},
	} {
		resp := f.srv.Admin(AdminRequest{Cmd: "show", Node: c.node})
		if resp.Code != c.code || resp.Hosts == nil || resp.Hosts.Code != c.code || len(resp.Hosts.Nodes) != c.hosts {
			t.Errorf("show %q: %+v", c.node, resp)
			continue
		}
		m, raw := asMap(t, resp.Hosts)
		if m["format"] != 1.0 || m["code"] != string(c.code) {
			t.Errorf("show %q: %s", c.node, raw)
		}
		if c.hosts == 0 {
			if !strings.Contains(raw, `"nodes":[]`) {
				t.Errorf("show %q refused: nodes %v, want []", c.node, m["nodes"])
			}
			continue
		}
		if n := resp.Hosts.Nodes[0]; n.Node != c.node || (n.View != nil) != c.withView {
			t.Errorf("show %q: %+v", c.node, n)
		}
		noSecrets(t, f, m, raw)
	}
}
