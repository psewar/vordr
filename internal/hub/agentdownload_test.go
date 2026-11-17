package hub

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/psewar/vordr/internal/identity"
	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/wire"
)

// The downloads of the agent release for installing a new host (ADR-0167).

var allPlatforms = []struct{ os, arch, file string }{
	{"linux", "amd64", "vordr-agent-linux-amd64"},
	{"linux", "arm64", "vordr-agent-linux-arm64"},
	{"linux", "arm", "vordr-agent-linux-arm"},
	{"windows", "amd64", "vordr-agent-windows-amd64.exe"},
}

// publishAll writes a signed release with a binary for every platform of the build into dir and returns the
// binaries' bytes by file name.
func publishAll(t *testing.T, dir string, priv ed25519.PrivateKey, version string) map[string][]byte {
	t.Helper()
	bins := map[string][]byte{}
	for _, p := range allPlatforms {
		bins[p.file] = []byte(fmt.Sprintf("binary %s/%s %s\n", p.os, p.arch, version))
	}
	publishBins(t, dir, priv, version, bins)
	return bins
}

// publishBins writes a signed release with the given binary for every platform of the build into dir.
func publishBins(t *testing.T, dir string, priv ed25519.PrivateKey, version string, bins map[string][]byte) {
	t.Helper()
	m := protocol.Manifest{Version: version, Issued: time.Now().UTC()}
	for _, p := range allPlatforms {
		sum := sha256.Sum256(bins[p.file])
		m.Binaries = append(m.Binaries, protocol.ManifestBinary{OS: p.os, Arch: p.arch, File: p.file,
			SHA256: hex.EncodeToString(sum[:]), Size: int64(len(bins[p.file]))})
	}
	raw, _ := json.Marshal(m)
	files := map[string][]byte{"manifest.json": raw,
		"manifest.sig": protocol.SignManifest(raw, func(b []byte) []byte { return ed25519.Sign(priv, b) })}
	for name, data := range bins {
		files[name] = data
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// replaceBinary writes data over a binary of the release folder and gives it a time of change of its own, so that the
// hub's cache of hashes sees a new file even on a file system with coarse times.
func replaceBinary(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	replaced++
	at := info.ModTime().Add(time.Duration(replaced) * time.Second)
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

var replaced int // replaceBinary: each call moves the time of change on

// haClient registers an approved Home Assistant client with role (empty: none set) and returns its session.
func (f *releaseFixture) haClient(t *testing.T, name, role string) *haSession {
	t.Helper()
	h, fp := haHello(t, name)
	if _, _, err := f.srv.Clients.Admit(h, fp, f.now); err != nil {
		t.Fatal(err)
	}
	if code := f.srv.Clients.Decide(name, wire.StateApproved, fp, f.now); code != AdminOK {
		t.Fatal(code)
	}
	if role != "" {
		if code := f.srv.Clients.SetRole(name, role); code != AdminOK && code != AdminAlready { // operate is the default
			t.Fatal(code)
		}
	}
	return &haSession{name: name, fingerprint: fp}
}

func (f *releaseFixture) ask(sess *haSession, goos, arch string) wire.AgentDownloadResult {
	return f.srv.haAgentDownload(sess, wire.AgentDownload{T: wire.TAgentDownload, RequestID: "r1", OS: goos, Arch: arch})
}

// eventLines returns the event log's lines of one event.
func eventLines(t *testing.T, f *viewFixture, event string) []map[string]any {
	t.Helper()
	raw, _, err := f.srv.Events.Read(EventFilter{Event: event, Limit: MaxEventLimit})
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, l := range raw {
		var m map[string]any
		if err := json.Unmarshal(l, &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

// noTokenInLog fails if any of the tokens appears anywhere in the event log.
func noTokenInLog(t *testing.T, f *viewFixture, tokens ...string) {
	t.Helper()
	raw, _, err := f.srv.Events.Read(EventFilter{Limit: MaxEventLimit})
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range raw {
		for _, tok := range tokens {
			if tok != "" && bytes.Contains(l, []byte(tok)) {
				t.Fatalf("a token in the event log: %s", l)
			}
		}
	}
}

// tokenOf takes the token out of a download path.
func tokenOf(t *testing.T, path string) string {
	t.Helper()
	_, tok, ok := strings.Cut(path, "?token=")
	if !ok || !protocol.ValidToken(tok) {
		t.Fatalf("path without a token: %q", path)
	}
	return tok
}

type getResult struct {
	status int
	body   string
	header http.Header
}

func fetch(t *testing.T, method, url string) getResult {
	t.Helper()
	req, _ := http.NewRequest(method, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return getResult{resp.StatusCode, string(body), resp.Header}
}

// An admin client gets the manifest exactly as the operator signed it, its signature, the hub's key and a download
// path per binary; for one platform only that binary. The event log names the downloads, never their tokens.
func TestAgentDownloadForAnAdmin(t *testing.T) {
	f := newReleaseFixture(t)
	publishAll(t, f.dir, f.priv, "0.9.0")
	sess := f.haClient(t, "ha", RoleAdmin)

	res := f.ask(sess, "linux", "arm64")
	if res.Code != string(AdminOK) || res.RequestID != "r1" || res.T != wire.TAgentDownloadResult {
		t.Fatalf("admin: %+v", res)
	}
	raw, _ := os.ReadFile(filepath.Join(f.dir, "manifest.json"))
	sig, _ := os.ReadFile(filepath.Join(f.dir, "manifest.sig"))
	if !bytes.Equal(res.Manifest, raw) || !bytes.Equal(res.ManifestSig, sig) {
		t.Fatal("manifest or signature not as signed")
	}
	m, ok := protocol.VerifyManifest(res.Manifest, res.ManifestSig, f.priv.Public().(ed25519.PublicKey))
	if !ok || m.Version != "0.9.0" || res.Version != "0.9.0" {
		t.Fatalf("manifest does not verify with the operator key: %v %q", ok, res.Version)
	}
	if !bytes.Equal(res.HubKey, f.srv.Key.Public()) {
		t.Fatal("hub key is not the key that signs jobs")
	}
	if res.ExpiresAt == nil || !res.ExpiresAt.Equal(f.now.Add(15*time.Minute)) {
		t.Fatalf("expires_at: %v", res.ExpiresAt)
	}
	if len(res.Downloads) != 1 || res.Downloads[0].File != "vordr-agent-linux-arm64" ||
		!strings.HasPrefix(res.Downloads[0].Path, AgentDownloadPath+"vordr-agent-linux-arm64?token=") {
		t.Fatalf("downloads for one platform: %+v", res.Downloads)
	}
	tokens := []string{tokenOf(t, res.Downloads[0].Path)}

	all := f.ask(f.haClient(t, "ha2", RoleAdmin), "", "") // another client: one holds at most maxAgentDownloadsPerClient
	if all.Code != string(AdminOK) || len(all.Downloads) != len(allPlatforms) {
		t.Fatalf("every platform: %+v", all)
	}
	seen := map[string]bool{}
	for i, d := range all.Downloads {
		tok := tokenOf(t, d.Path)
		if d.File != allPlatforms[i].file || seen[tok] {
			t.Fatalf("download %d: %+v", i, d)
		}
		seen[tok] = true
		tokens = append(tokens, tok)
	}

	issued := eventLines(t, f.viewFixture, "agent_download_issued")
	if len(issued) != 2 || issued[0]["client"] != "ha" || issued[0]["version"] != "0.9.0" || issued[0]["os"] != "linux" ||
		issued[0]["arch"] != "arm64" || issued[0]["expires_at"] == nil {
		t.Fatalf("issued events: %v", issued)
	}
	if d := issued[0]["downloads"].([]any)[0].(map[string]any); d["file"] != "vordr-agent-linux-arm64" ||
		d["download_id"] != downloadID(tokens[0]) {
		t.Fatalf("issued download: %v", d)
	}
	noTokenInLog(t, f.viewFixture, tokens...)
}

// Only role admin; other roles, clients that are not approved now, malformed platforms, no release, no binary for the
// platform and a binary missing on the hub, of another size or with other bytes are refused with their code, logged,
// and open nothing.
func TestAgentDownloadRefusals(t *testing.T) {
	f := newReleaseFixture(t)
	admin := f.haClient(t, "admin", RoleAdmin)
	if res := f.ask(admin, "", ""); res.Code != string(AdminNoRelease) || res.Manifest != nil || res.Downloads != nil {
		t.Fatalf("no release on the hub: %+v", res)
	}
	bins := publishAll(t, f.dir, f.priv, "0.9.0")

	cases := []struct {
		name       string
		sess       *haSession
		goos, arch string
		want       AdminCode
	}{
		{"operate", f.haClient(t, "operate", RoleOperate), "linux", "amd64", AdminRoleForbids},
		{"observe", f.haClient(t, "observe", RoleObserve), "linux", "amd64", AdminRoleForbids},
		{"no role set", f.haClient(t, "plain", ""), "linux", "amd64", AdminRoleForbids},
		{"session of another key", &haSession{name: "admin", fingerprint: "SHA256:old-key"}, "linux", "amd64",
			AdminClientNotApproved},
		{"unknown client", &haSession{name: "nobody", fingerprint: "x"}, "", "", AdminClientNotApproved},
		{"os without arch", admin, "linux", "", AdminBadRequest},
		{"arch without os", admin, "", "amd64", AdminBadRequest},
		{"malformed platform", admin, "linux", "../amd64", AdminBadRequest},
		{"no binary for the platform", admin, "linux", "riscv64", AdminNoBinary},
	}
	for _, c := range cases {
		res := f.ask(c.sess, c.goos, c.arch)
		if res.Code != string(c.want) || res.Manifest != nil || res.HubKey != nil || res.Downloads != nil ||
			res.ExpiresAt != nil {
			t.Errorf("%s: %+v", c.name, res)
		}
	}
	h, fp := haHello(t, "pending")
	f.srv.Clients.Admit(h, fp, f.now)
	if res := f.ask(&haSession{name: "pending", fingerprint: fp}, "", ""); res.Code != string(AdminClientNotApproved) {
		t.Errorf("pending client: %+v", res)
	}

	os.Remove(filepath.Join(f.dir, "vordr-agent-linux-arm"))
	if res := f.ask(admin, "linux", "arm"); res.Code != string(AdminReleaseIncomplete) {
		t.Errorf("binary missing on the hub: %+v", res)
	}
	if res := f.ask(admin, "", ""); res.Code != string(AdminReleaseIncomplete) {
		t.Errorf("one binary of all missing: %+v", res)
	}
	replaceBinary(t, filepath.Join(f.dir, "vordr-agent-linux-arm"), []byte("short"))
	if res := f.ask(admin, "linux", "arm"); res.Code != string(AdminReleaseIncomplete) {
		t.Errorf("binary of another size: %+v", res)
	}
	replaceBinary(t, filepath.Join(f.dir, "vordr-agent-linux-arm"), bytes.Repeat([]byte{'x'}, len(bins["vordr-agent-linux-arm"])))
	if res := f.ask(admin, "linux", "arm"); res.Code != string(AdminReleaseIncomplete) {
		t.Errorf("binary of the same size with other bytes: %+v", res)
	}
	if n := len(f.srv.Releases.installs); n != 0 {
		t.Fatalf("refusals issued %d tokens", n)
	}

	refused := eventLines(t, f.viewFixture, "agent_download_refused")
	if len(refused) != len(cases)+6 {
		t.Fatalf("refused events: %d %v", len(refused), refused)
	}
	if e := refused[1]; e["client"] != "operate" || e["code"] != string(AdminRoleForbids) || e["role"] != RoleOperate {
		t.Fatalf("role refusal event: %v", e)
	}

	// Without an agent_update section in hub.yaml there are no releases at all.
	f.srv.Releases = nil
	if res := f.ask(admin, "", ""); res.Code != string(AdminNoRelease) {
		t.Fatalf("hub without agent_update: %+v", res)
	}
}

// The path returns the exact bytes of the binary, once: the token is used from the moment its download starts.
func TestAgentDownloadServesTheExactBytesOnce(t *testing.T) {
	f := newReleaseFixture(t)
	bins := publishAll(t, f.dir, f.priv, "0.9.0")
	ts := httptest.NewServer(f.srv.Handler())
	defer ts.Close()
	res := f.ask(f.haClient(t, "ha", RoleAdmin), "windows", "amd64")
	path := res.Downloads[0].Path

	got := fetch(t, http.MethodGet, ts.URL+path)
	if got.status != http.StatusOK || got.body != string(bins["vordr-agent-windows-amd64.exe"]) ||
		got.header.Get("Content-Length") != fmt.Sprint(len(bins["vordr-agent-windows-amd64.exe"])) ||
		got.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("download: %d %q %v", got.status, got.body, got.header)
	}
	if again := fetch(t, http.MethodGet, ts.URL+path); again.status != http.StatusConflict || again.body != "token_used\n" {
		t.Fatalf("second download with the same token: %d %q", again.status, again.body)
	}

	done := eventLines(t, f.viewFixture, "agent_downloaded")
	if len(done) != 1 || done[0]["file"] != "vordr-agent-windows-amd64.exe" || done[0]["client"] != "ha" ||
		done[0]["complete"] != true || done[0]["bytes"] != float64(len(bins["vordr-agent-windows-amd64.exe"])) ||
		done[0]["download_id"] != downloadID(tokenOf(t, path)) || done[0]["version"] != "0.9.0" || done[0]["remote"] == "" {
		t.Fatalf("downloaded event: %v", done)
	}
	refused := eventLines(t, f.viewFixture, "agent_download_refused")
	if len(refused) != 1 || refused[0]["code"] != string(DownloadTokenUsed) || refused[0]["status"] != float64(409) {
		t.Fatalf("refusal of the second download: %v", refused)
	}
	noTokenInLog(t, f.viewFixture, tokenOf(t, path))
}

// Every refusal has its fixed status and its code as the body, is logged without the token, and leaves the token as
// it was: a request for the wrong file or while the binary is unreadable does not use it up.
func TestAgentDownloadTokenRefusals(t *testing.T) {
	f := newReleaseFixture(t)
	bins := publishAll(t, f.dir, f.priv, "0.9.0")
	ts := httptest.NewServer(f.srv.Handler())
	defer ts.Close()
	sess := f.haClient(t, "ha", RoleAdmin)
	res := f.ask(sess, "linux", "amd64")
	path := res.Downloads[0].Path
	tok := tokenOf(t, path)
	refusal := func(name string, got getResult, want DownloadRefusal, status int) {
		t.Helper()
		if got.status != status || got.body != string(want)+"\n" {
			t.Errorf("%s: %d %q, want %d %s", name, got.status, got.body, status, want)
		}
	}

	refusal("unknown token", fetch(t, http.MethodGet, ts.URL+AgentDownloadPath+"vordr-agent-linux-amd64?token="+
		strings.Repeat("0", 48)), DownloadUnknownToken, http.StatusForbidden)
	refusal("malformed token", fetch(t, http.MethodGet, ts.URL+AgentDownloadPath+"vordr-agent-linux-amd64?token=x"),
		DownloadUnknownToken, http.StatusForbidden)
	refusal("no token", fetch(t, http.MethodGet, ts.URL+AgentDownloadPath+"vordr-agent-linux-amd64"),
		DownloadUnknownToken, http.StatusForbidden)
	refusal("wrong file", fetch(t, http.MethodGet, ts.URL+AgentDownloadPath+"vordr-agent-linux-arm64?token="+tok),
		DownloadWrongFile, http.StatusNotFound)
	refusal("manifest with a binary's token", fetch(t, http.MethodGet, ts.URL+AgentDownloadPath+"manifest.json?token="+tok),
		DownloadWrongFile, http.StatusNotFound)
	post := fetch(t, http.MethodPost, ts.URL+path)
	refusal("post", post, DownloadBadMethod, http.StatusMethodNotAllowed)
	if post.header.Get("Allow") != http.MethodGet {
		t.Errorf("post: Allow %q", post.header.Get("Allow"))
	}

	// An unreadable binary, or one with other bytes than signed, is the hub's fault; the token stays unused.
	bin := filepath.Join(f.dir, "vordr-agent-linux-amd64")
	replaceBinary(t, bin, []byte("cut"))
	refusal("binary of another size", fetch(t, http.MethodGet, ts.URL+path), DownloadUnreadable,
		http.StatusInternalServerError)
	replaceBinary(t, bin, bytes.Repeat([]byte{'x'}, len(bins["vordr-agent-linux-amd64"])))
	refusal("same size, other bytes", fetch(t, http.MethodGet, ts.URL+path), DownloadBinaryMismatch,
		http.StatusInternalServerError)
	replaceBinary(t, bin, bins["vordr-agent-linux-amd64"])
	if got := fetch(t, http.MethodGet, ts.URL+path); got.status != http.StatusOK || got.body != string(bins["vordr-agent-linux-amd64"]) {
		t.Fatalf("download after the refusals: %d %q", got.status, got.body)
	}

	// A new release: a token of the old one opens nothing, even for a file of the same name.
	old := f.ask(sess, "linux", "arm64").Downloads[0].Path
	publishAll(t, f.dir, f.priv, "0.9.1")
	refusal("release changed", fetch(t, http.MethodGet, ts.URL+old), DownloadReleaseChanged, http.StatusGone)
	// No release at all any more.
	gone := f.ask(sess, "linux", "arm64").Downloads[0].Path
	manifest := filepath.Join(f.dir, "manifest.json")
	os.Rename(manifest, manifest+".off")
	refusal("release gone", fetch(t, http.MethodGet, ts.URL+gone), DownloadReleaseChanged, http.StatusGone)
	os.Rename(manifest+".off", manifest)

	// After downloadWindow the token is expired, an hour later it is gone, also when nothing was issued since.
	late := f.ask(sess, "linux", "arm64").Downloads[0].Path
	f.now = f.now.Add(downloadWindow + time.Second)
	refusal("expired", fetch(t, http.MethodGet, ts.URL+late), DownloadTokenExpired, http.StatusGone)
	f.now = f.now.Add(keepExpiredDownload + time.Second)
	refusal("expired long ago", fetch(t, http.MethodGet, ts.URL+late), DownloadUnknownToken, http.StatusForbidden)
	if _, known := f.srv.Releases.installs[tokenOf(t, late)]; known {
		t.Fatal("a token that expired long ago is still kept")
	}

	codes := map[string]int{}
	for _, e := range eventLines(t, f.viewFixture, "agent_download_refused") {
		if e["remote"] == nil || e["status"] == nil {
			t.Errorf("refusal without remote or status: %v", e)
		}
		codes[e["code"].(string)]++
	}
	want := map[string]int{"unknown_token": 4, "wrong_file": 2, "bad_method": 1, "binary_unreadable": 1,
		"binary_mismatch": 1, "release_changed": 2, "token_expired": 1}
	if fmt.Sprint(codes) != fmt.Sprint(want) {
		t.Fatalf("refusal codes: %v, want %v", codes, want)
	}
	noTokenInLog(t, f.viewFixture, tok, tokenOf(t, old), tokenOf(t, gone), tokenOf(t, late))
}

// The two download paths do not open each other's files: a job's token is no token for a new host and the reverse.
func TestAgentDownloadAndSelfUpdateTokensStayApart(t *testing.T) {
	f := newReleaseFixture(t)
	publishAll(t, f.dir, f.priv, "0.9.0")
	ts := httptest.NewServer(f.srv.Handler())
	defer ts.Close()
	job := f.srv.Releases.newToken("canary", "j1")
	if got := fetch(t, http.MethodGet, ts.URL+AgentDownloadPath+"vordr-agent-linux-amd64?token="+job); got.status != http.StatusForbidden {
		t.Fatalf("job token on the path for new hosts: %d", got.status)
	}
	install := tokenOf(t, f.ask(f.haClient(t, "ha", RoleAdmin), "linux", "amd64").Downloads[0].Path)
	if got := fetch(t, http.MethodGet, ts.URL+AgentReleasePath+"vordr-agent-linux-amd64?token="+install); got.status != http.StatusForbidden {
		t.Fatalf("token for a new host on the self-update path: %d", got.status)
	}
}

// At most maxAgentDownloads tokens exist, and one client holds at most maxAgentDownloadsPerClient of them; a used token
// counts until it expires, so the bounds also limit downloads per window. A request is answered in full or not at all,
// and the refusal names the bound.
func TestAgentDownloadCap(t *testing.T) {
	f := newReleaseFixture(t)
	publishAll(t, f.dir, f.priv, "0.9.0")
	ts := httptest.NewServer(f.srv.Handler())
	defer ts.Close()
	a, b, c := f.haClient(t, "a", RoleAdmin), f.haClient(t, "b", RoleAdmin), f.haClient(t, "c", RoleAdmin)
	var first string
	for i := 0; i < maxAgentDownloadsPerClient-1; i++ {
		res := f.ask(a, "linux", "amd64")
		if res.Code != string(AdminOK) {
			t.Fatalf("token %d: %s", i+1, res.Code)
		}
		if i == 0 {
			first = res.Downloads[0].Path
		}
	}
	if res := f.ask(a, "", ""); res.Code != string(AdminTooManyDownloads) {
		t.Fatalf("every platform with %d tokens of the client open: %s", maxAgentDownloadsPerClient-1, res.Code)
	}
	if n := len(f.srv.Releases.installs); n != maxAgentDownloadsPerClient-1 {
		t.Fatalf("a refused request issued tokens: %d", n)
	}
	if res := f.ask(a, "linux", "amd64"); res.Code != string(AdminOK) {
		t.Fatalf("up to the client's share: %s", res.Code)
	}
	if got := fetch(t, http.MethodGet, ts.URL+first); got.status != http.StatusOK {
		t.Fatalf("download: %d", got.status)
	}
	if res := f.ask(a, "linux", "amd64"); res.Code != string(AdminTooManyDownloads) || res.Downloads != nil {
		t.Fatalf("over the client's share, a used token included: %+v", res)
	}
	if res := f.ask(b, "", ""); res.Code != string(AdminOK) || len(res.Downloads) != maxAgentDownloadsPerClient {
		t.Fatalf("another client up to the hub's bound: %+v", res)
	}
	if res := f.ask(c, "linux", "amd64"); res.Code != string(AdminTooManyDownloads) {
		t.Fatalf("over the hub's bound: %s", res.Code)
	}
	var bounds []any
	for _, e := range eventLines(t, f.viewFixture, "agent_download_refused") {
		bounds = append(bounds, e["bound"])
	}
	if fmt.Sprint(bounds) != "[client client hub]" {
		t.Fatalf("bounds named by the refusals: %v", bounds)
	}
	f.now = f.now.Add(downloadWindow + time.Second)
	if res := f.ask(c, "", ""); res.Code != string(AdminOK) {
		t.Fatalf("after the window: %s", res.Code)
	}
}

// One admin client that keeps asking for every platform holds its share, not the hub's bound: another client still
// gets its downloads.
func TestAgentDownloadOneClientCannotStarveAnother(t *testing.T) {
	f := newReleaseFixture(t)
	publishAll(t, f.dir, f.priv, "0.9.0")
	greedy, other := f.haClient(t, "greedy", RoleAdmin), f.haClient(t, "other", RoleAdmin)
	if res := f.ask(greedy, "", ""); res.Code != string(AdminOK) {
		t.Fatalf("first request: %s", res.Code)
	}
	for i := 0; i < 3; i++ {
		if res := f.ask(greedy, "", ""); res.Code != string(AdminTooManyDownloads) {
			t.Fatalf("request %d over the share: %s", i+2, res.Code)
		}
	}
	if res := f.ask(other, "linux", "amd64"); res.Code != string(AdminOK) {
		t.Fatalf("the other client: %s", res.Code)
	}
}

// A refusal of a token the hub issued is logged whatever strangers did before: they cannot hide that someone else
// used the token first. Each token has a cap of its own, so that whoever saw a path cannot write the log full with it.
func TestAgentDownloadRefusalsOfIssuedTokensAreLogged(t *testing.T) {
	f := newReleaseFixture(t)
	publishAll(t, f.dir, f.priv, "0.9.0")
	ts := httptest.NewServer(f.srv.Handler())
	defer ts.Close()
	path := f.ask(f.haClient(t, "ha", RoleAdmin), "linux", "amd64").Downloads[0].Path
	unknown := ts.URL + AgentDownloadPath + "vordr-agent-linux-amd64?token=" + strings.Repeat("0", 48)
	for i := 0; i < refusalLogBurst+5; i++ {
		fetch(t, http.MethodGet, unknown)
	}
	if got := fetch(t, http.MethodGet, ts.URL+path); got.status != http.StatusOK {
		t.Fatalf("download: %d", got.status)
	}
	if got := fetch(t, http.MethodGet, ts.URL+path); got.status != http.StatusConflict {
		t.Fatalf("second download: %d", got.status)
	}
	id := downloadID(tokenOf(t, path))
	ofToken := func() []map[string]any {
		var out []map[string]any
		for _, e := range eventLines(t, f.viewFixture, "agent_download_refused") {
			if e["download_id"] == id {
				out = append(out, e)
			}
		}
		return out
	}
	if got := ofToken(); len(got) != 1 || got[0]["code"] != string(DownloadTokenUsed) || got[0]["client"] != "ha" ||
		got[0]["refusal"] != float64(1) {
		t.Fatalf("the refusal of the used token after the strangers' requests: %v", got)
	}
	for i := 0; i < refusalsPerToken+5; i++ {
		fetch(t, http.MethodGet, ts.URL+path)
	}
	got := ofToken()
	if len(got) != refusalsPerToken || got[refusalsPerToken-1]["more_not_logged"] != true ||
		got[refusalsPerToken-2]["more_not_logged"] != nil {
		t.Fatalf("refusals of one token: %d, last %v", len(got), got[len(got)-1])
	}
	if n := len(eventLines(t, f.viewFixture, "agent_download_refused")) - len(got); n != refusalLogBurst {
		t.Fatalf("refusals of strangers logged: %d", n)
	}
}

// A token follows its client in the register: demoted, rejected, or approved again with another key, the client's
// open tokens are refused with client_revoked and stay unused; promoted again, they work.
func TestAgentDownloadTokensFollowTheClient(t *testing.T) {
	f := newReleaseFixture(t)
	bins := publishAll(t, f.dir, f.priv, "0.9.0")
	ts := httptest.NewServer(f.srv.Handler())
	defer ts.Close()
	sess := f.haClient(t, "ha", RoleAdmin)
	revoked := func(name, path string) {
		t.Helper()
		if got := fetch(t, http.MethodGet, ts.URL+path); got.status != http.StatusForbidden ||
			got.body != string(DownloadClientRevoked)+"\n" {
			t.Fatalf("%s: %d %q", name, got.status, got.body)
		}
	}
	path := f.ask(sess, "linux", "amd64").Downloads[0].Path
	f.srv.Clients.SetRole("ha", RoleOperate)
	revoked("demoted", path)
	f.srv.Clients.SetRole("ha", RoleAdmin)
	if got := fetch(t, http.MethodGet, ts.URL+path); got.status != http.StatusOK || got.body != string(bins["vordr-agent-linux-amd64"]) {
		t.Fatalf("promoted again: %d %q", got.status, got.body)
	}

	path = f.ask(sess, "linux", "amd64").Downloads[0].Path
	f.srv.Clients.Decide("ha", wire.StateRejected, sess.fingerprint, f.now)
	revoked("rejected", path)

	f.srv.Clients.Forget("ha")
	f.haClient(t, "ha", RoleAdmin) // the same name, approved again with another key
	revoked("approved again with another key", path)
}

// A client that is not approved gets one answer to agent_download, client_not_approved, and the connection ends: a
// pending client cannot write the event log full with requests.
func TestAgentDownloadFromAPendingClientEndsTheConnection(t *testing.T) {
	f := newReleaseFixture(t)
	publishAll(t, f.dir, f.priv, "0.9.0")
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	hello := wire.HAHello{T: wire.THello, Role: wire.RoleHA, Name: "pending", PublicKey: pub, IntegrationVersion: "0.1",
		Protocol: wire.HAProtocol}
	ts := httptest.NewServer(f.srv.Handler())
	defer ts.Close()
	defer f.srv.Close(context.Background())
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+wire.HAPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	var ch wire.Challenge
	if err := ws.ReadJSON(&ch); err != nil {
		t.Fatal(err)
	}
	hello.Signature = ed25519.Sign(priv, wire.HAHelloSigned(ch.Nonce, "pending", pub))
	var st wire.Status
	if err := ws.WriteJSON(hello); err != nil || ws.ReadJSON(&st) != nil || st.State != wire.StatePendingApproval {
		t.Fatalf("status: %+v %v", st, err)
	}
	// One request at a time: requests the hub has not read when it closes would turn its close into a reset.
	for i := 0; i < 3; i++ {
		if ws.WriteJSON(wire.AgentDownload{T: wire.TAgentDownload, RequestID: fmt.Sprintf("r%d", i)}) != nil {
			break // the hub closed the connection
		}
		ws.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, data, err := ws.ReadMessage()
		var ne net.Error
		switch {
		case errors.As(err, &ne) && ne.Timeout():
			t.Fatalf("request %d: no answer and the connection still open", i)
		case err != nil:
			if i != 1 {
				t.Fatalf("request %d: the connection ended: %v", i, err)
			}
		default:
			var res wire.AgentDownloadResult
			if json.Unmarshal(data, &res) != nil || res.T != wire.TAgentDownloadResult || i != 0 ||
				res.Code != string(AdminClientNotApproved) || res.RequestID != "r0" {
				t.Fatalf("request %d: answer %s", i, data)
			}
			continue
		}
		break
	}
	time.Sleep(100 * time.Millisecond) // the hub has read what it reads by now; more lines would show
	if got := eventLines(t, f.viewFixture, "agent_download_refused"); len(got) != 1 || got[0]["client"] != "pending" {
		t.Fatalf("refusals logged: %v", got)
	}
}

// A download whose reader is slower than the write window is cut off, on the path for new hosts and on the self-update
// path alike: a slow reader holds no goroutine and no open file beyond it.
func TestDownloadsCutOffASlowReader(t *testing.T) {
	f := newReleaseFixture(t)
	bins := map[string][]byte{}
	for _, p := range allPlatforms {
		bins[p.file] = []byte("small " + p.file)
	}
	const big = 32 << 20 // more than the socket buffers on both sides hold
	bins["vordr-agent-linux-amd64"] = bytes.Repeat([]byte{0x5a}, big)
	publishBins(t, f.dir, f.priv, "0.9.0", bins)
	f.srv.Releases.writeWindow = func(int64) time.Duration { return 200 * time.Millisecond }
	ts := httptest.NewServer(f.srv.Handler())
	defer ts.Close()

	// slowGet sends the request and reads nothing until the window has long passed, then reads what comes.
	slowGet := func(path string) int64 {
		t.Helper()
		conn, err := net.Dial("tcp", strings.TrimPrefix(ts.URL, "http://"))
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_ = conn.(*net.TCPConn).SetReadBuffer(4096)
		if _, err := fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: hub.example\r\n\r\n", path); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Second)
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		n, _ := io.Copy(io.Discard, conn)
		return n
	}

	path := f.ask(f.haClient(t, "ha", RoleAdmin), "linux", "amd64").Downloads[0].Path
	if n := slowGet(path); n >= big {
		t.Fatalf("path for new hosts: the slow reader got all %d bytes", n)
	}
	done := eventLines(t, f.viewFixture, "agent_downloaded")
	if len(done) != 1 || done[0]["complete"] != false || done[0]["bytes"].(float64) >= big {
		t.Fatalf("downloaded event: %v", done)
	}
	job := f.srv.Releases.newToken("canary", "j1")
	if n := slowGet(AgentReleasePath + "vordr-agent-linux-amd64?token=" + job); n >= big {
		t.Fatalf("self-update path: the slow reader got all %d bytes", n)
	}
}

// The path answers anyone who reaches the port: its refusals reach the event log refusalLogBurst times a minute at
// most, and the next one logged says how many were left out.
func TestAgentDownloadRefusalLogIsBounded(t *testing.T) {
	f := newReleaseFixture(t)
	publishAll(t, f.dir, f.priv, "0.9.0")
	ts := httptest.NewServer(f.srv.Handler())
	defer ts.Close()
	unknown := ts.URL + AgentDownloadPath + "vordr-agent-linux-amd64?token=" + strings.Repeat("0", 48)
	for i := 0; i < refusalLogBurst+3; i++ {
		if got := fetch(t, http.MethodGet, unknown); got.status != http.StatusForbidden {
			t.Fatalf("request %d: %d", i, got.status)
		}
	}
	if n := len(eventLines(t, f.viewFixture, "agent_download_refused")); n != refusalLogBurst {
		t.Fatalf("logged %d refusals in one minute", n)
	}
	f.now = f.now.Add(time.Minute)
	fetch(t, http.MethodGet, unknown)
	got := eventLines(t, f.viewFixture, "agent_download_refused")
	if len(got) != refusalLogBurst+1 || got[refusalLogBurst]["suppressed"] != float64(3) {
		t.Fatalf("after a minute: %d %v", len(got), got[len(got)-1])
	}
	// A refusal of a request of Home Assistant is not counted: the client is known.
	f.ask(f.haClient(t, "ops", RoleOperate), "linux", "amd64")
	if n := len(eventLines(t, f.viewFixture, "agent_download_refused")); n != refusalLogBurst+2 {
		t.Fatalf("refusal of a request: %d", n)
	}
}

// Over /v1/ha: the status names the feature, an admin client asks and fetches its binary, and a malformed request ends
// the connection like any other protocol violation.
func TestAgentDownloadOverTheHAProtocol(t *testing.T) {
	f := newReleaseFixture(t)
	bins := publishAll(t, f.dir, f.priv, "0.9.0")
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	fp := identity.Fingerprint(pub)
	hello := wire.HAHello{T: wire.THello, Role: wire.RoleHA, Name: "ha", PublicKey: pub, IntegrationVersion: "0.1",
		Protocol: wire.HAProtocol}
	f.srv.Clients.Admit(hello, fp, f.now)
	f.srv.Clients.Decide("ha", wire.StateApproved, fp, f.now)
	f.srv.Clients.SetRole("ha", RoleAdmin)
	ts := httptest.NewServer(f.srv.Handler())
	defer ts.Close()
	defer f.srv.Close(context.Background())

	dial := func() *websocket.Conn {
		ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+wire.HAPath, nil)
		if err != nil {
			t.Fatal(err)
		}
		var ch wire.Challenge
		if err := ws.ReadJSON(&ch); err != nil {
			t.Fatal(err)
		}
		h := hello
		h.Signature = ed25519.Sign(priv, wire.HAHelloSigned(ch.Nonce, "ha", pub))
		if err := ws.WriteJSON(h); err != nil {
			t.Fatal(err)
		}
		var st wire.Status
		if err := ws.ReadJSON(&st); err != nil || st.State != wire.StateApproved ||
			len(st.Features) != 1 || st.Features[0] != wire.FeatureAgentDownload {
			t.Fatalf("status: %+v %v", st, err)
		}
		return ws
	}
	ws := dial()
	defer ws.Close()
	if err := ws.WriteJSON(wire.AgentDownload{T: wire.TAgentDownload, RequestID: "add-1", OS: "linux", Arch: "amd64"}); err != nil {
		t.Fatal(err)
	}
	var res wire.AgentDownloadResult
	for res.T != wire.TAgentDownloadResult {
		ws.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, data, err := ws.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		var tt wire.Type
		json.Unmarshal(data, &tt)
		if tt.T == wire.TAgentDownloadResult {
			json.Unmarshal(data, &res)
		}
	}
	if res.Code != string(AdminOK) || res.RequestID != "add-1" || len(res.Downloads) != 1 {
		t.Fatalf("result: %+v", res)
	}
	if got := fetch(t, http.MethodGet, ts.URL+res.Downloads[0].Path); got.status != http.StatusOK ||
		got.body != string(bins["vordr-agent-linux-amd64"]) {
		t.Fatalf("download: %d %q", got.status, got.body)
	}

	if err := ws.WriteJSON(wire.AgentDownload{T: wire.TAgentDownload, RequestID: "not a request id"}); err != nil {
		t.Fatal(err)
	}
	for {
		ws.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, _, err := ws.ReadMessage(); err != nil {
			break // the hub closed the connection
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(eventLines(t, f.viewFixture, "ha_protocol_violation")) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no protocol violation in the event log")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
