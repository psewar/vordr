package images

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"

	"github.com/psewar/vordr/internal/inventory"
)

// The files the agent reads inside a container (ADR-0120), and nothing else.
const (
	osReleasePath     = "/etc/os-release"
	osReleaseFallback = "/usr/lib/os-release"
	dpkgStatusPath    = "/var/lib/dpkg/status"
	dpkgStatusDir     = "/var/lib/dpkg/status.d" // distroless images: one file per package
	apkInstalledPath  = "/lib/apk/db/installed"
)

// Bounds of one read. A package database of a large image stays far below; os-release is a few hundred bytes.
// Variables for tests.
var (
	maxOSRelease int64 = 64 << 10
	maxPackageDB int64 = 16 << 20
	// MaxOSPackages: packages one image reports; past it the rest is left out and the code says so.
	MaxOSPackages = 5000
)

type archiveAnswer int

const (
	archiveOK      archiveAnswer = iota
	archiveMissing               // 404: no such path in the container
	archiveFailed                // no usable answer, or larger than allowed
)

// pathStat: what the engine says about the path in X-Docker-Container-Path-Stat.
type pathStat struct {
	LinkTarget string `json:"linkTarget"`
}

// archive reads path from a container through GET /containers/{id}/archive. The engine hands it out as a tar: a file
// as one entry, a directory with its files. It returns the regular files with their contents (at most max bytes
// together) and, when path is a symbolic link, the link's target as the engine resolves it.
func (e Engine) archive(ctx context.Context, id, p string, max int64) (files map[string][]byte, link string,
	ans archiveAnswer) {
	if !containerID.MatchString(id) {
		return nil, "", archiveFailed
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"http://engine/containers/"+id+"/archive?path="+url.QueryEscape(p), nil)
	if err != nil {
		return nil, "", archiveFailed
	}
	resp, err := e.Client.Do(req)
	if err != nil {
		return nil, "", archiveFailed
	}
	defer resp.Body.Close()
	defer func() { _, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, max)) }()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, "", archiveMissing
	default:
		return nil, "", archiveFailed
	}
	var st pathStat
	if raw, err := base64.StdEncoding.DecodeString(resp.Header.Get("X-Docker-Container-Path-Stat")); err == nil {
		_ = json.Unmarshal(raw, &st) // the header is optional; without it the tar's own entry tells
	}
	files = map[string][]byte{}
	var total int64
	tr := tar.NewReader(io.LimitReader(resp.Body, max+1<<20)) // the tar's own headers on top of the contents
	for {
		h, err := tr.Next()
		switch {
		case err == io.EOF:
			return files, "", archiveOK
		case err != nil:
			return nil, "", archiveFailed
		}
		switch h.Typeflag {
		case tar.TypeSymlink:
			if len(files) == 0 && strings.Count(strings.Trim(h.Name, "/"), "/") == 0 { // the path itself is the link
				target := st.LinkTarget
				switch {
				case target != "":
				case path.IsAbs(h.Linkname):
					target = h.Linkname
				default:
					target = path.Join(path.Dir(p), h.Linkname)
				}
				return nil, target, archiveOK
			}
		case tar.TypeReg:
			total += h.Size
			if total > max {
				return nil, "", archiveFailed
			}
			b, err := io.ReadAll(io.LimitReader(tr, h.Size))
			if err != nil {
				return nil, "", archiveFailed
			}
			files[h.Name] = b
		}
	}
}

// readFile reads one file, following a symbolic link once.
func (e Engine) readFile(ctx context.Context, id, p string, max int64) ([]byte, archiveAnswer) {
	files, link, ans := e.archive(ctx, id, p, max)
	if ans == archiveOK && link != "" {
		files, link, ans = e.archive(ctx, id, link, max)
		if link != "" {
			return nil, archiveFailed // a link to a link: not followed further
		}
	}
	if ans != archiveOK {
		return nil, ans
	}
	if len(files) != 1 {
		return nil, archiveFailed // a directory where a file belongs
	}
	for _, b := range files {
		return b, archiveOK
	}
	return nil, archiveFailed
}

// Contents reads what a running container's image carries inside (ADR-0120): the distribution and its release from
// os-release, and the installed packages from the dpkg or apk database. GET only, a few files; no exec, nothing
// written, nothing pulled.
func (e Engine) Contents(ctx context.Context, id string) inventory.ImageOS {
	var out inventory.ImageOS
	rel, ans := e.readFile(ctx, id, osReleasePath, maxOSRelease)
	if ans == archiveMissing {
		rel, ans = e.readFile(ctx, id, osReleaseFallback, maxOSRelease)
	}
	switch ans {
	case archiveFailed:
		out.Code = inventory.CodeUnreadable
		return out
	case archiveMissing:
		out.Code = inventory.CodeUnknownRelease
	default:
		fields := parseOSRelease(rel)
		out.ID, out.Version = fields["ID"], fields["VERSION_ID"]
		if out.ID == "" {
			out.Code = inventory.CodeUnknownRelease
		}
	}
	pkgs, code := e.packageDB(ctx, id)
	if code != "" {
		out.Code = code
		return out
	}
	if len(pkgs) > MaxOSPackages {
		pkgs, out.Code = pkgs[:MaxOSPackages], inventory.CodeTooManyPackages
	}
	out.Packages = pkgs
	return out
}

// packageDB reads the dpkg database (one file, or one per package as distroless images keep it), else apk's.
func (e Engine) packageDB(ctx context.Context, id string) ([]inventory.OSPackage, string) {
	if b, ans := e.readFile(ctx, id, dpkgStatusPath, maxPackageDB); ans != archiveMissing {
		if ans != archiveOK {
			return nil, inventory.CodeUnreadable
		}
		return parseDpkgStatus(b, true), ""
	}
	if files, _, ans := e.archive(ctx, id, dpkgStatusDir, maxPackageDB); ans != archiveMissing {
		if ans != archiveOK {
			return nil, inventory.CodeUnreadable
		}
		var all []inventory.OSPackage
		for _, b := range files {
			all = append(all, parseDpkgStatus(b, false)...)
		}
		return sortedPackages(all), ""
	}
	if b, ans := e.readFile(ctx, id, apkInstalledPath, maxPackageDB); ans != archiveMissing {
		if ans != archiveOK {
			return nil, inventory.CodeUnreadable
		}
		return parseApkInstalled(b), ""
	}
	return nil, inventory.CodeNoPackageDB
}

// parseOSRelease reads os-release: KEY=value lines, values optionally quoted (os-release(5)).
func parseOSRelease(b []byte) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		k, v, ok := strings.Cut(line, "=")
		if !ok || k == "" || strings.HasPrefix(k, "#") {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		out[k] = v
	}
	return out
}

// parseDpkgStatus reads dpkg's status file: stanzas separated by blank lines; only packages whose Status ends in
// "installed" count (not ones removed with their configuration left). Source is "name" or "name (version)" when the
// source package or its version differ from the binary's. A distroless image's status.d holds the packages' control
// files, written at build time without a Status field (dpkg adds it when it installs): there needStatus is false, and
// a stanza without one counts as installed.
func parseDpkgStatus(b []byte, needStatus bool) []inventory.OSPackage {
	var out []inventory.OSPackage
	var cur map[string]string
	flush := func() {
		if cur == nil {
			return
		}
		st := strings.Fields(cur["Status"])
		installed := len(st) > 0 && st[len(st)-1] == "installed"
		if _, has := cur["Status"]; !has && !needStatus {
			installed = true
		}
		if cur["Package"] != "" && cur["Version"] != "" && installed {
			p := inventory.OSPackage{Name: cur["Package"], Version: cur["Version"]}
			if src := strings.TrimSpace(cur["Source"]); src != "" {
				name, ver, _ := strings.Cut(src, " ")
				p.Source = name
				p.SourceVersion = strings.Trim(strings.TrimSpace(ver), "()")
			}
			out = append(out, p)
		}
		cur = nil
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64<<10), 1<<20) // long Description and Depends lines
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.TrimSpace(line) == "":
			flush()
		case line[0] == ' ' || line[0] == '\t': // a continuation line
		default:
			k, v, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			if cur == nil {
				cur = map[string]string{}
			}
			cur[k] = strings.TrimSpace(v)
		}
	}
	flush()
	return sortedPackages(out)
}

// parseApkInstalled reads apk's installed database: one record per package, blank lines between, "P:" the name,
// "V:" the version, "o:" the origin (the source package advisories name).
func parseApkInstalled(b []byte) []inventory.OSPackage {
	var out []inventory.OSPackage
	var p inventory.OSPackage
	flush := func() {
		if p.Name != "" && p.Version != "" {
			if p.Source == p.Name {
				p.Source = ""
			}
			out = append(out, p)
		}
		p = inventory.OSPackage{}
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		if len(line) < 2 || line[1] != ':' {
			continue
		}
		switch line[0] {
		case 'P':
			p.Name = line[2:]
		case 'V':
			p.Version = line[2:]
		case 'o':
			p.Source = line[2:]
		}
	}
	flush()
	return sortedPackages(out)
}

func sortedPackages(p []inventory.OSPackage) []inventory.OSPackage {
	slices.SortFunc(p, func(a, b inventory.OSPackage) int { return strings.Compare(a.Name, b.Name) })
	return slices.CompactFunc(p, func(a, b inventory.OSPackage) bool { return a.Name == b.Name })
}
