package target

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/policy"
	"github.com/psewar/vordr/internal/protocol"
)

// fakeDSM answers in the shape DSM 7 uses; each test changes what it needs.
type fakeDSM struct {
	mu         sync.Mutex
	srv        *httptest.Server
	loginErr   int               // DSM error code for the login; 0 = success
	failAPI    map[string]int    // api -> DSM error code
	answers    map[string]string // api.method -> data JSON
	export     []byte
	statusN    int // how many status calls until finish; -1 never
	statusSeen int
	loggedOut  bool
	sessions   []string // session names used to log in
}

func newFakeDSM(t *testing.T) *fakeDSM {
	export := append([]byte{0xfd, '7', 'z', 'X', 'Z', 0x00}, make([]byte, 30000)...)
	f := &fakeDSM{failAPI: map[string]int{}, export: export, statusN: 1, answers: map[string]string{
		"SYNO.DSM.Info.getinfo": `{"model":"DS-TEST","version":"99999","version_string":"DSM 7.9.9-99999 Update 1"}`,
		"SYNO.Core.Upgrade.Server.check": `{"promotion":{"available":false,"rss_result":"success"},` +
			`"update":{"available":false,"rss_result":"success"}}`,
		"SYNO.Core.Upgrade.Setting.get": `{"autoupdate_enable":true,"autoupdate_type":"hotfix-security",` +
			`"schedule":{"hour":5,"minute":30,"week_day":"4"},"smart_nano_enabled":true,"upgrade_type":"hotfix"}`,
	}}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeDSM) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method != http.MethodPost || r.URL.Path != "/webapi/entry.cgi" || r.ParseForm() != nil {
		http.NotFound(w, r)
		return
	}
	a, m := r.PostForm.Get("api"), r.PostForm.Get("method")
	fail := func(code int) {
		_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "error": map[string]int{"code": code}})
	}
	ok := func(data string) { _, _ = w.Write([]byte(`{"success":true,"data":` + data + `}`)) }
	if a == "SYNO.API.Auth" {
		switch {
		case m == "login" && f.loginErr != 0:
			fail(f.loginErr)
		case m == "login":
			f.sessions = append(f.sessions, r.PostForm.Get("session"))
			ok(`{"sid":"SITZUNG","did":"GERAET"}`)
		case m == "logout":
			f.loggedOut = true
			ok(`{}`)
		}
		return
	}
	if r.PostForm.Get("_sid") != "SITZUNG" {
		fail(119)
		return
	}
	if code := f.failAPI[a]; code != 0 {
		fail(code)
		return
	}
	switch a + "." + m {
	case "SYNO.Backup.Config.Backup.start":
		ok(`{"task_id":"confbkp_bkp_1"}`)
	case "SYNO.Backup.Config.Backup.status":
		f.statusSeen++
		ok(`{"task_id":"confbkp_bkp_1","finish":` + map[bool]string{true: "true", false: "false"}[f.statusN >= 0 && f.statusSeen >= f.statusN] + `}`)
	case "SYNO.Backup.Config.Backup.download":
		w.Header().Set("Content-Type", "application/octet-stream; charset=UTF-8")
		_, _ = w.Write(f.export)
	default:
		if data, found := f.answers[a+"."+m]; found {
			ok(data)
			return
		}
		fail(103)
	}
}

// driver opens the synology driver against the fake with TLS checked the way t says.
func (f *fakeDSM) driver(t *testing.T, tgt policy.Target) (Driver, Result) {
	t.Helper()
	dir := t.TempDir()
	tgt.Driver, tgt.Address = "synology-dsm", f.srv.URL
	tgt.Credentials = filepath.Join(dir, "nas.cred")
	if err := os.WriteFile(tgt.Credentials, []byte("# from the secret store\nUSER=vordr-gna\nPASS=secret=with=equals\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d, res := Open(&tgt)
	if s, ok := d.(*synology); ok {
		s.poll, s.pollTimeout = time.Millisecond, 200*time.Millisecond
	}
	return d, res
}

func (f *fakeDSM) fingerprint() string {
	sum := sha256.Sum256(f.srv.Certificate().Raw)
	return hex.EncodeToString(sum[:])
}

func TestSynologyCheck(t *testing.T) {
	f := newFakeDSM(t)
	d, res := f.driver(t, policy.Target{Fingerprint: f.fingerprint()})
	if !res.OK() {
		t.Fatal(res)
	}
	st, res := d.Check(context.Background())
	if !res.OK() || st.Installed != "7.9.9-99999 Update 1" || st.Model != "DS-TEST" || len(st.Offers) != 0 ||
		st.SelfUpdate != "on (hotfix-security)" || st.Facts["update_check"] != "hotfix" {
		t.Fatalf("up to date: %+v %v", st, res)
	}
	if !f.loggedOut || len(f.sessions) != 1 || f.sessions[0] != "webui" {
		t.Fatalf("session: logged out %v, sessions %v", f.loggedOut, f.sessions)
	}

	f.answers["SYNO.Core.Upgrade.Server.check"] = `{"update":{"available":true,"version":"DSM 7.9.9-99999 Update 2",` +
		`"type":"hotfix-security"},"promotion":{"available":true}}`
	f.answers["SYNO.Core.Upgrade.Setting.get"] = `{"autoupdate_enable":false,"upgrade_type":"hotfix"}`
	st, res = d.Check(context.Background())
	want := []Offer{{"DSM", "DSM 7.9.9-99999 Update 2", true}, {"DSM (new major version)", UnknownVersion, false}}
	if !res.OK() || len(st.Offers) != 2 || st.Offers[0] != want[0] || st.Offers[1] != want[1] || st.SelfUpdate != "off" {
		t.Fatalf("offers: %+v %v", st, res)
	}
}

func TestSynologyCodes(t *testing.T) {
	for name, tc := range map[string]struct {
		setup func(*fakeDSM)
		code  protocol.Code
	}{
		"wrong password":      {func(f *fakeDSM) { f.loginErr = 400 }, protocol.CodeFailedTargetAuth},
		"needs second factor": {func(f *fakeDSM) { f.loginErr = 403 }, protocol.CodeFailedTargetAuth},
		"no permission":       {func(f *fakeDSM) { f.failAPI["SYNO.Core.Upgrade.Server"] = 105 }, protocol.CodeFailedTargetAuth},
		"other DSM error":     {func(f *fakeDSM) { f.failAPI["SYNO.DSM.Info"] = 117 }, protocol.CodeFailedPackageManager},
		"device gone":         {func(f *fakeDSM) { f.srv.Close() }, protocol.CodePrecheckRepoUnreachable},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeDSM(t)
			d, res := f.driver(t, policy.Target{Fingerprint: f.fingerprint()})
			if !res.OK() {
				t.Fatal(res)
			}
			tc.setup(f)
			if _, res := d.Check(context.Background()); res.Code != tc.code {
				t.Fatalf("got %v, want %s", res, tc.code)
			}
		})
	}
}

// The device's certificate decides, either pinned or by name; a mismatch is failed_target_tls, not "unreachable".
func TestSynologyTLS(t *testing.T) {
	f := newFakeDSM(t)
	other := strings.Repeat("ab", 32)
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		tgt  policy.Target
		code protocol.Code
	}{
		"pinned":             {policy.Target{Fingerprint: f.fingerprint()}, ""},
		"other pin":          {policy.Target{Fingerprint: other}, protocol.CodeFailedTargetTLS},
		"name with its ca":   {policy.Target{ServerName: "example.com", CAFile: ca}, ""},
		"wrong name":         {policy.Target{ServerName: "nas.example", CAFile: ca}, protocol.CodeFailedTargetTLS},
		"name, system roots": {policy.Target{ServerName: "example.com"}, protocol.CodeFailedTargetTLS},
	} {
		t.Run(name, func(t *testing.T) {
			d, res := f.driver(t, tc.tgt)
			if !res.OK() {
				t.Fatal(res)
			}
			if _, res := d.Check(context.Background()); res.Code != tc.code {
				t.Fatalf("got %v, want %q", res, tc.code)
			}
		})
	}
}

func TestSynologyBackup(t *testing.T) {
	f := newFakeDSM(t)
	d, _ := f.driver(t, policy.Target{Fingerprint: f.fingerprint()})
	f.statusN = 3
	data, ext, res := d.Backup(context.Background())
	if !res.OK() || ext != ".dss" || len(data) != len(f.export) || f.statusSeen != 3 || !f.loggedOut {
		t.Fatalf("export: %d bytes %q %v, status calls %d", len(data), ext, res, f.statusSeen)
	}
	for name, setup := range map[string]func(*fakeDSM){
		"not xz":         func(f *fakeDSM) { f.export = make([]byte, 30000) },
		"too small":      func(f *fakeDSM) { f.export = f.export[:100] },
		"never finishes": func(f *fakeDSM) { f.statusN = -1 },
		"start refused":  func(f *fakeDSM) { f.failAPI["SYNO.Backup.Config.Backup"] = 117 },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeDSM(t)
			d, _ := f.driver(t, policy.Target{Fingerprint: f.fingerprint()})
			setup(f)
			if data, _, res := d.Backup(context.Background()); res.Code != protocol.CodeFailedConfigBackup || data != nil {
				t.Fatalf("got %d bytes, %v", len(data), res)
			}
		})
	}
}

func TestCredentials(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c")
	_ = os.WriteFile(p, []byte("USER=a\n\n# x\nPASS=b=c\n"), 0o600)
	if c, res := ReadCredentials(p); !res.OK() || c["USER"] != "a" || c["PASS"] != "b=c" {
		t.Fatalf("%v %v", c, res)
	}
	_ = os.WriteFile(p, []byte("USER a\n"), 0o600)
	if _, res := ReadCredentials(p); res.Code != protocol.CodeFailedTargetAuth || strings.Contains(res.Detail, "USER") {
		t.Fatalf("broken line must be refused without echoing it: %v", res)
	}
	_ = os.WriteFile(p, []byte("USER=a\n"), 0o600)
	tgt := policy.Target{Driver: "synology-dsm", Address: "https://127.0.0.1:1", Fingerprint: strings.Repeat("ab", 32),
		Credentials: p}
	if _, res := Open(&tgt); res.Code != protocol.CodeFailedTargetAuth {
		t.Fatalf("missing PASS: %v", res)
	}
}

func TestStore(t *testing.T) {
	s := Store{Dir: filepath.Join(t.TempDir(), "config-backups"), Keep: 2}
	if _, ok := s.Last(); ok {
		t.Fatal("empty store has a last backup")
	}
	t0 := time.Unix(1700000000, 0).UTC()
	for i := 0; i < 3; i++ {
		if _, err := s.Save("synology-dsm", []byte{byte(i)}, ".dss", t0.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	last, ok := s.Last()
	if !ok || !last.At.Equal(t0.Add(2*time.Hour)) || last.Bytes != 1 || len(last.SHA256) != 64 {
		t.Fatalf("last: %+v %v", last, ok)
	}
	entries, _ := os.ReadDir(s.Dir)
	var exports []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".dss") {
			exports = append(exports, e.Name())
			if fi, _ := e.Info(); fi.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
				t.Errorf("%s readable by others: %o", e.Name(), fi.Mode().Perm())
			}
		}
	}
	if len(exports) != 2 || exports[0] != "synology-dsm-20231114T231320Z.dss" {
		t.Fatalf("kept: %v", exports)
	}
}

// A driver names its extension with the dot; without it the file would be "<driver>-<time>xml". Save refuses such an
// extension before it writes anything, and a refused save is no backup.
func TestStoreRefusesAnExtensionWithoutItsDot(t *testing.T) {
	s := Store{Dir: filepath.Join(t.TempDir(), "config-backups"), Keep: 2}
	for _, ext := range []string{"xml", "", ".", ".x.y", "./x", `.a\b`} {
		if _, err := s.Save("pfsense", []byte("<pfsense/>"), ext, time.Unix(1700000000, 0)); err == nil {
			t.Errorf("extension %q accepted", ext)
		}
	}
	if entries, _ := os.ReadDir(s.Dir); len(entries) != 0 {
		t.Fatalf("refused saves left files: %v", entries)
	}
	if _, ok := s.Last(); ok {
		t.Fatal("a refused save counts as a backup")
	}
	if saved, err := s.Save("pfsense", []byte("<pfsense/>"), ".xml", time.Unix(1700000000, 0)); err != nil ||
		saved.File != "pfsense-20231114T221320Z.xml" {
		t.Fatalf("accepted extension: %+v %v", saved, err)
	}
}
