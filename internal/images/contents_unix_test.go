//go:build !windows

package images

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/psewar/vordr/internal/inventory"
)

// archiveEngine answers GET /containers/{id}/archive like the Docker Engine: a tar of the path, the path's stat in
// X-Docker-Container-Path-Stat, 404 for a path that is not there. It records every method and path asked.
type archiveEngine struct {
	mu    sync.Mutex
	asked []string
	files map[string]map[string]archiveEntry // container id -> path -> what is there
}

type archiveEntry struct {
	data  string
	link  string            // a symbolic link to this target (absolute, as the engine's stat names it)
	files map[string]string // a directory: its files
}

func tarOf(t *testing.T, name string, e archiveEntry) []byte {
	t.Helper()
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	switch {
	case e.link != "":
		_ = w.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeSymlink, Linkname: "../usr/lib/os-release", Mode: 0o777})
	case e.files != nil:
		_ = w.WriteHeader(&tar.Header{Name: name + "/", Typeflag: tar.TypeDir, Mode: 0o755})
		for n, d := range e.files {
			_ = w.WriteHeader(&tar.Header{Name: name + "/" + n, Typeflag: tar.TypeReg, Size: int64(len(d)), Mode: 0o644})
			_, _ = w.Write([]byte(d))
		}
	default:
		_ = w.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Size: int64(len(e.data)), Mode: 0o644})
		_, _ = w.Write([]byte(e.data))
	}
	_ = w.Close()
	return b.Bytes()
}

func (a *archiveEngine) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		a.asked = append(a.asked, r.Method+" "+r.URL.Path+"?"+r.URL.Query().Get("path"))
		a.mu.Unlock()
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/containers/"), "/archive")
		p := r.URL.Query().Get("path")
		e, ok := a.files[id][p]
		if !ok {
			http.NotFound(w, r)
			return
		}
		stat, _ := json.Marshal(map[string]any{"name": filepath.Base(p), "linkTarget": e.link})
		w.Header().Set("X-Docker-Container-Path-Stat", base64.StdEncoding.EncodeToString(stat))
		w.Header().Set("Content-Type", "application/x-tar")
		_, _ = w.Write(tarOf(t, filepath.Base(p), e))
	})
}

func serveEngine(t *testing.T, h http.Handler) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "engine")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "e.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { srv.Close() })
	return sock
}

const dpkgStatus = `Package: libssl3t64
Status: install ok installed
Priority: optional
Version: 3.5.1-1+deb13u1
Source: openssl
Description: Secure Sockets Layer toolkit
 with a continuation line: Status: deinstall ok config-files

Package: old-thing
Status: deinstall ok config-files
Version: 1.0-1

Package: zlib1g
Status: hold ok installed
Version: 1:1.3.dfsg+really1.3.1-1+b1
Source: zlib (1:1.3.dfsg+really1.3.1-1)

Package: base-files
Status: install ok installed
Version: 13.8+deb13u1
`

const apkInstalled = `C:Q1abc=
P:libcrypto3
V:3.5.4-r0
o:openssl

P:busybox
V:1.37.0-r19
o:busybox
`

var (
	ctrDebian     = strings.Repeat("1", 64)
	ctrAlpine     = strings.Repeat("2", 64)
	ctrDistroless = strings.Repeat("3", 64)
	ctrScratch    = strings.Repeat("4", 64)
)

func newArchiveEngine() *archiveEngine {
	return &archiveEngine{files: map[string]map[string]archiveEntry{
		ctrDebian: {
			osReleasePath:     {link: osReleaseFallback},
			osReleaseFallback: {data: "PRETTY_NAME=\"Debian GNU/Linux 13 (trixie)\"\nID=debian\nVERSION_ID=\"13\"\n"},
			dpkgStatusPath:    {data: dpkgStatus},
		},
		ctrAlpine: {
			osReleasePath:    {data: "NAME=\"Alpine Linux\"\nID=alpine\nVERSION_ID=3.23.4\n"},
			apkInstalledPath: {data: apkInstalled},
		},
		ctrDistroless: {
			osReleasePath: {data: "ID=debian\nVERSION_ID='12'\n"},
			// control files written at build time: no Status field
			dpkgStatusDir: {files: map[string]string{
				"base": "Package: base-files\nVersion: 12.4+deb12u11\n",
				"tz":   "Package: tzdata\nVersion: 2025b-0+deb12u1\n",
			}},
		},
		ctrScratch: {},
	}}
}

// What an image carries inside, read through the engine's archive endpoint: os-release (following its link once),
// then the dpkg database, a distroless image's status.d, or apk's. Only GET, only those paths.
func TestContents(t *testing.T) {
	a := newArchiveEngine()
	e := NewEngine(serveEngine(t, a.handler(t)))
	ctx := context.Background()

	deb := e.Contents(ctx, ctrDebian)
	want := []inventory.OSPackage{
		{Name: "base-files", Version: "13.8+deb13u1"},
		{Name: "libssl3t64", Version: "3.5.1-1+deb13u1", Source: "openssl"},
		{Name: "zlib1g", Version: "1:1.3.dfsg+really1.3.1-1+b1", Source: "zlib", SourceVersion: "1:1.3.dfsg+really1.3.1-1"},
	}
	if deb.ID != "debian" || deb.Version != "13" || deb.Code != "" || !slices.Equal(deb.Packages, want) {
		t.Errorf("debian: %+v", deb)
	}
	alp := e.Contents(ctx, ctrAlpine)
	if alp.ID != "alpine" || alp.Version != "3.23.4" || !slices.Equal(alp.Packages, []inventory.OSPackage{
		{Name: "busybox", Version: "1.37.0-r19"}, {Name: "libcrypto3", Version: "3.5.4-r0", Source: "openssl"}}) {
		t.Errorf("alpine: %+v", alp)
	}
	dl := e.Contents(ctx, ctrDistroless)
	if dl.ID != "debian" || dl.Version != "12" || len(dl.Packages) != 2 || dl.Packages[1].Name != "tzdata" {
		t.Errorf("distroless: %+v", dl)
	}
	if s := e.Contents(ctx, ctrScratch); s.Code != inventory.CodeNoPackageDB || s.Packages != nil {
		t.Errorf("no package database: %+v", s)
	}
	if u := e.Contents(ctx, "not-an-id"); u.Code != inventory.CodeUnreadable {
		t.Errorf("a bad container id: %+v", u)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	allowed := []string{osReleasePath, osReleaseFallback, dpkgStatusPath, dpkgStatusDir, apkInstalledPath}
	for _, q := range a.asked {
		method, rest, _ := strings.Cut(q, " ")
		_, p, _ := strings.Cut(rest, "?")
		if method != http.MethodGet || !slices.Contains(allowed, p) {
			t.Errorf("asked the engine: %s", q)
		}
	}
}

// Bounds: a package database larger than allowed is unreadable, not half read; more packages than an image may
// report are cut, and the code says so.
func TestContentsBounds(t *testing.T) {
	a := newArchiveEngine()
	e := NewEngine(serveEngine(t, a.handler(t)))
	was := maxPackageDB
	maxPackageDB = 100
	if got := e.Contents(context.Background(), ctrDebian); got.Code != inventory.CodeUnreadable || got.Packages != nil {
		t.Errorf("too large: %+v", got)
	}
	maxPackageDB = was
	wasN := MaxOSPackages
	MaxOSPackages = 2
	defer func() { MaxOSPackages = wasN }()
	if got := e.Contents(context.Background(), ctrDebian); got.Code != inventory.CodeTooManyPackages || len(got.Packages) != 2 {
		t.Errorf("too many: %+v", got)
	}
}

func TestParseOSRelease(t *testing.T) {
	got := parseOSRelease([]byte("# a comment\nID=ubuntu\nVERSION_ID=\"24.04\"\nNAME='Ubuntu'\nbroken line\n"))
	if got["ID"] != "ubuntu" || got["VERSION_ID"] != "24.04" || got["NAME"] != "Ubuntu" || len(got) != 3 {
		t.Errorf("%v", got)
	}
}
