package hub

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/wire"
)

// Downloads of the agent release for installing a new host (ADR-0167). A Home Assistant client with role admin asks
// over /v1/ha (agent_download); the hub answers with the manifest exactly as the operator signed it, its own public
// key, and per binary a path with a token of its own. The new host fetches its binary with that path once, within
// downloadWindow. Home Assistant checks the manifest with the operator key it has from the person, never from the hub,
// and the binary against the manifest: the hub hands out what the operator signed and nothing else.
//
// Apart from the self-update download (AgentReleasePath): that token belongs to a job the hub chose and opens the
// release to one agent; this one comes from Home Assistant and opens one file once. Both serve only the files the
// current manifest names, from the release folder.

const (
	// AgentDownloadPath: GET <path><file>?token=<t>.
	AgentDownloadPath = "/v1/agent-download/"
	// maxAgentDownloads: download tokens that exist at once. A token exists from its issue until it expires, used or
	// not, so the bound also limits how many downloads Home Assistant can start per downloadWindow.
	maxAgentDownloads = 8
	// maxAgentDownloadsPerClient: the share of one client in maxAgentDownloads, so that one client that keeps asking
	// cannot hold back every other. One new host needs one binary.
	maxAgentDownloadsPerClient = 4
	// keepExpiredDownload: an expired token is known this much longer, so that it is refused as expired, not unknown.
	keepExpiredDownload = time.Hour
	// refusalLogBurst: refusals of unknown tokens that go into the event log per minute at most. The path answers
	// anyone who reaches the port; events.jsonl must not grow with every request of a stranger.
	refusalLogBurst = 10
	// refusalsPerToken: refusals of one issued token that go into the event log at most. They are logged apart from
	// the strangers' budget, so that strangers cannot hide them; the cap keeps someone who saw one path from writing
	// the log full with it.
	refusalsPerToken = 20
	// downloadMinRate and downloadWriteSlack: a download may take as long as its binary needs at downloadMinRate, plus
	// downloadWriteSlack (downloadWriteWindow). A reader slower than that is cut off.
	downloadMinRate    = 64 << 10 // bytes per second
	downloadWriteSlack = time.Minute
)

// Codes of agent_download besides ok, client_not_approved, role_forbids, bad_request, no_release and
// no_binary_for_platform.
const (
	// AdminReleaseIncomplete: a binary the manifest names is missing from the release folder, has another size, or
	// other bytes than the manifest's SHA-256.
	AdminReleaseIncomplete AdminCode = "release_incomplete"
	// AdminTooManyDownloads: maxAgentDownloads tokens exist, or the client holds maxAgentDownloadsPerClient; the
	// oldest expires within downloadWindow.
	AdminTooManyDownloads AdminCode = "too_many_downloads"
)

// DownloadRefusal says why the download path refused a request: the body of the answer and the code in the event log.
// Each has a fixed HTTP status (downloadStatus).
type DownloadRefusal string

const (
	DownloadBadMethod      DownloadRefusal = "bad_method"        // not a GET
	DownloadUnknownToken   DownloadRefusal = "unknown_token"     // malformed, never issued, or expired over an hour ago
	DownloadTokenExpired   DownloadRefusal = "token_expired"     // issued, but downloadWindow has passed
	DownloadTokenUsed      DownloadRefusal = "token_used"        // its one download has started already
	DownloadWrongFile      DownloadRefusal = "wrong_file"        // the token opens another file
	DownloadClientRevoked  DownloadRefusal = "client_revoked"    // its client is no longer approved with role admin
	DownloadReleaseChanged DownloadRefusal = "release_changed"   // the current release lacks the binary it was issued for
	DownloadUnreadable     DownloadRefusal = "binary_unreadable" // the binary cannot be read or has another size
	DownloadBinaryMismatch DownloadRefusal = "binary_mismatch"   // the binary's bytes are not the manifest's SHA-256
)

var downloadStatus = map[DownloadRefusal]int{
	DownloadBadMethod:      http.StatusMethodNotAllowed,
	DownloadUnknownToken:   http.StatusForbidden,
	DownloadTokenExpired:   http.StatusGone,
	DownloadTokenUsed:      http.StatusConflict,
	DownloadWrongFile:      http.StatusNotFound,
	DownloadClientRevoked:  http.StatusForbidden,
	DownloadReleaseChanged: http.StatusGone,
	DownloadUnreadable:     http.StatusInternalServerError,
	DownloadBinaryMismatch: http.StatusInternalServerError,
}

// platformPattern: the os and arch of an agent_download, as a manifest names them.
var platformPattern = regexp.MustCompile(`^[a-z0-9]{1,16}$`)

// installDownload is an issued token: one file of one release, once, until until.
type installDownload struct {
	id          string // names the token in the event log; the token itself is never logged
	client      string // the Home Assistant client it was issued to
	fingerprint string // that client's key then: a client approved again with another key does not inherit it
	version     string
	file        string
	sha256      string
	until       time.Time
	used        bool
	refusals    int // refusals of this token so far (refusalsPerToken)
}

type issuedDownload struct {
	token, file, id string
}

// refusalWindow counts the refusals logged in the current minute and those left out.
type refusalWindow struct {
	start              time.Time
	logged, suppressed int
}

// downloadID names a token in the event log without giving it away.
func downloadID(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:8])
}

// downloadWriteWindow: how long a download of size bytes may take (downloadMinRate, downloadWriteSlack).
func downloadWriteWindow(size int64) time.Duration {
	return time.Duration(size/downloadMinRate)*time.Second + downloadWriteSlack
}

// setWriteDeadline bounds the time the handler may spend writing its answer. The hub's own server supports it; a
// ResponseWriter that does not (a test recorder) answers without a bound.
func setWriteDeadline(w http.ResponseWriter, d time.Duration) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(d))
}

// binaryCheck: whether a binary in the release folder is the one the manifest names.
type binaryCheck int

const (
	binaryOK         binaryCheck = iota
	binaryUnreadable             // cannot be opened or read, not a regular file, or another size
	binaryMismatch               // its bytes are not the manifest's SHA-256
)

// binaryHash is the SHA-256 of a file of the release folder, with the size and time of change it had when read.
type binaryHash struct {
	size    int64
	modTime time.Time
	sha256  string
}

// checkBinary says whether f, opened from the release folder, holds the binary b of the manifest: its size, and its
// SHA-256. The hash is read once per file, size and time of change, not for every request; a binary replaced with
// another of the same size and time of change is not read again (Home Assistant checks the bytes anyway). It reads
// with ReadAt, so f is still at its start for the download.
func (r *Releases) checkBinary(f *os.File, b protocol.ManifestBinary) binaryCheck {
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != b.Size {
		return binaryUnreadable
	}
	r.mu.Lock()
	h, known := r.hashes[b.File]
	r.mu.Unlock()
	if !known || h.size != info.Size() || !h.modTime.Equal(info.ModTime()) {
		sum := sha256.New()
		if n, err := io.Copy(sum, io.NewSectionReader(f, 0, info.Size())); err != nil || n != info.Size() {
			return binaryUnreadable
		}
		h = binaryHash{size: info.Size(), modTime: info.ModTime(), sha256: hex.EncodeToString(sum.Sum(nil))}
		r.mu.Lock()
		r.hashes[b.File] = h
		r.mu.Unlock()
	}
	if h.sha256 != strings.ToLower(b.SHA256) {
		return binaryMismatch
	}
	return binaryOK
}

// present: the binary lies in the release folder as a regular file with the size and hash the manifest names.
func (r *Releases) present(b protocol.ManifestBinary) bool {
	f, err := os.Open(filepath.Join(r.dir, b.File))
	if err != nil {
		return false
	}
	defer f.Close()
	return r.checkBinary(f, b) == binaryOK
}

// issueDownloads opens each of bins of rel for one download until downloadWindow passes: all of them, or none when
// that would exceed maxAgentDownloads, or the client's share maxAgentDownloadsPerClient. bound names the bound that
// refused: "hub" or "client".
func (r *Releases) issueDownloads(client, fingerprint string, rel *Release, bins []protocol.ManifestBinary) (
	issued []issuedDownload, until time.Time, code AdminCode, bound string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	open, mine := 0, 0
	for t, d := range r.installs {
		switch {
		case now.Sub(d.until) > keepExpiredDownload:
			delete(r.installs, t)
		case !now.After(d.until):
			open++
			if d.client == client {
				mine++
			}
		}
	}
	switch {
	case open+len(bins) > maxAgentDownloads:
		return nil, time.Time{}, AdminTooManyDownloads, "hub"
	case mine+len(bins) > maxAgentDownloadsPerClient:
		return nil, time.Time{}, AdminTooManyDownloads, "client"
	}
	until = now.Add(downloadWindow)
	issued = make([]issuedDownload, 0, len(bins))
	for _, b := range bins {
		t := randomToken()
		d := &installDownload{id: downloadID(t), client: client, fingerprint: fingerprint,
			version: rel.Manifest.Version, file: b.File, sha256: b.SHA256, until: until}
		r.installs[t] = d
		issued = append(issued, issuedDownload{token: t, file: b.File, id: d.id})
	}
	return issued, until, AdminOK, ""
}

// lookupDownload finds the token of a request for file and says whether it opens that file now. Only the token's own
// state; its client and the release are checked after it. A token that expired over keepExpiredDownload ago is
// forgotten here as well, not only when the next tokens are issued.
func (r *Releases) lookupDownload(token, file string) (installDownload, DownloadRefusal) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.installs[token]
	now := r.now()
	switch {
	case d == nil:
		return installDownload{}, DownloadUnknownToken
	case now.Sub(d.until) > keepExpiredDownload:
		delete(r.installs, token)
		return installDownload{}, DownloadUnknownToken
	case now.After(d.until):
		return *d, DownloadTokenExpired
	case d.used:
		return *d, DownloadTokenUsed
	case file != d.file:
		return *d, DownloadWrongFile
	}
	return *d, ""
}

// useDownload marks the token used, unless another request used it or it expired since lookupDownload.
func (r *Releases) useDownload(token string) DownloadRefusal {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.installs[token]
	switch {
	case d == nil:
		return DownloadUnknownToken
	case r.now().After(d.until):
		return DownloadTokenExpired
	case d.used:
		return DownloadTokenUsed
	}
	d.used = true
	return ""
}

// mayLogRefusal says whether a refusal of an unknown token goes into the event log now (refusalLogBurst per minute),
// and how many were left out since the last one logged.
func (r *Releases) mayLogRefusal() (int, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now, w := r.now(), &r.refusals
	if now.Sub(w.start) >= time.Minute {
		w.start, w.logged = now, 0
	}
	if w.logged >= refusalLogBurst {
		w.suppressed++
		return 0, false
	}
	w.logged++
	left := w.suppressed
	w.suppressed = 0
	return left, true
}

// mayLogTokenRefusal counts a refusal of an issued token and says which one it is for that token, and whether it goes
// into the event log (the first refusalsPerToken do). ok is false also when the token is gone meanwhile.
func (r *Releases) mayLogTokenRefusal(token string) (n int, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.installs[token]
	if d == nil {
		return 0, false
	}
	d.refusals++
	return d.refusals, d.refusals <= refusalsPerToken
}

// haAgentDownload answers agent_download at once: the current release as the operator signed it, the hub's key, and
// a path with a token per binary asked for. Decided on the client register as it is now.
func (s *Server) haAgentDownload(sess *haSession, a wire.AgentDownload) wire.AgentDownloadResult {
	res := wire.AgentDownloadResult{T: wire.TAgentDownloadResult, RequestID: a.RequestID}
	fields := map[string]any{"client": sess.name, "request_id": a.RequestID}
	refused := func(code AdminCode) wire.AgentDownloadResult {
		fields["code"] = code
		s.Events.Log("", "agent_download_refused", fields)
		res.Code = string(code)
		return res
	}
	client, ok := s.approvedClient(sess)
	if !ok {
		return refused(AdminClientNotApproved)
	}
	if !RoleAllowsAgentDownload(client.Role) {
		fields["role"] = EffectiveRole(client.Role)
		return refused(AdminRoleForbids)
	}
	if (a.OS == "") != (a.Arch == "") || a.OS != "" && (!platformPattern.MatchString(a.OS) || !platformPattern.MatchString(a.Arch)) {
		return refused(AdminBadRequest)
	}
	if a.OS != "" {
		fields["os"], fields["arch"] = a.OS, a.Arch
	}
	if s.Releases == nil {
		return refused(AdminNoRelease)
	}
	rel, _ := s.Releases.Current() // why a release is broken goes to the service log with the rollout
	if rel == nil {
		return refused(AdminNoRelease)
	}
	fields["version"] = rel.Manifest.Version
	bins := rel.Manifest.Binaries
	if a.OS != "" {
		b, has := rel.Manifest.For(a.OS, a.Arch)
		if !has {
			return refused(AdminNoBinary)
		}
		bins = []protocol.ManifestBinary{b}
	}
	for _, b := range bins {
		if !s.Releases.present(b) {
			fields["file"] = b.File
			return refused(AdminReleaseIncomplete)
		}
	}
	issued, until, code, bound := s.Releases.issueDownloads(sess.name, sess.fingerprint, rel, bins)
	if code != AdminOK {
		fields["bound"] = bound
		return refused(code)
	}
	until = until.UTC()
	logged := make([]map[string]string, 0, len(issued))
	for _, d := range issued {
		res.Downloads = append(res.Downloads, wire.AgentDownloadFile{File: d.file,
			Path: AgentDownloadPath + d.file + "?token=" + d.token})
		logged = append(logged, map[string]string{"file": d.file, "download_id": d.id})
	}
	res.Code, res.Version, res.ExpiresAt = string(AdminOK), rel.Manifest.Version, &until
	res.Manifest, res.ManifestSig, res.HubKey = rel.Raw, rel.Sig, s.Key.Public()
	fields["downloads"], fields["expires_at"] = logged, until.Format(time.RFC3339)
	s.Events.Log("", "agent_download_issued", fields)
	return res
}

// downloadClientAllowed: the client a token was issued to is still approved with that key and role admin. Revoking
// or demoting a client closes its open tokens, as the register decides every request of a client.
func (s *Server) downloadClientAllowed(d installDownload) bool {
	c, ok := s.Clients.Get(d.client)
	return ok && c.State == wire.StateApproved && c.Fingerprint == d.fingerprint && RoleAllowsAgentDownload(c.Role)
}

// serveAgentDownload serves one binary of the current release to the holder of a token from agent_download:
// GET /v1/agent-download/<file>?token=<t>, once. A refusal answers with its fixed status and its code as the body,
// and changes nothing: the token stays as it was.
func (s *Server) serveAgentDownload(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	file := strings.TrimPrefix(req.URL.Path, AgentDownloadPath)
	token := req.URL.Query().Get("token")
	fields := map[string]any{"remote": req.RemoteAddr}
	if protocol.ValidFileName(file) {
		fields["file"] = file // a stranger's path is not copied into the log
	}
	issued := false // the token is one the hub issued: its refusals are logged apart from the strangers'
	refuse := func(why DownloadRefusal) {
		s.downloadRefused(fields, why, token, issued)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(downloadStatus[why])
		_, _ = io.WriteString(w, string(why)+"\n")
	}
	if req.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		refuse(DownloadBadMethod)
		return
	}
	if !protocol.ValidToken(token) {
		refuse(DownloadUnknownToken)
		return
	}
	fields["download_id"] = downloadID(token)
	d, why := s.Releases.lookupDownload(token, file)
	if issued = d.client != ""; issued {
		fields["client"], fields["version"] = d.client, d.version
	}
	if why != "" {
		refuse(why)
		return
	}
	if !s.downloadClientAllowed(d) {
		refuse(DownloadClientRevoked)
		return
	}
	// The current release must still name the binary the token was issued for, with the same hash: Home Assistant
	// checks the bytes against the manifest it got then. The hash decides, not the version.
	var b protocol.ManifestBinary
	named := false
	if rel, _ := s.Releases.Current(); rel != nil {
		b, named = rel.Manifest.Has(file)
	}
	if !named || b.SHA256 != d.sha256 {
		refuse(DownloadReleaseChanged)
		return
	}
	f, err := os.Open(filepath.Join(s.Releases.dir, b.File))
	if err != nil {
		refuse(DownloadUnreadable)
		return
	}
	defer f.Close()
	// The bytes before the token: a binary that is not the one signed would use the token for a failing install.
	switch s.Releases.checkBinary(f, b) {
	case binaryUnreadable:
		refuse(DownloadUnreadable)
		return
	case binaryMismatch:
		refuse(DownloadBinaryMismatch)
		return
	}
	if why := s.Releases.useDownload(token); why != "" {
		refuse(why)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(b.Size, 10))
	setWriteDeadline(w, s.Releases.writeWindow(b.Size))
	// A failed copy is the network, the disk or a reader too slow for the write deadline: the event says how far it
	// got, and Home Assistant asks again.
	n, _ := io.CopyN(w, f, b.Size)
	fields["bytes"], fields["complete"] = n, n == b.Size
	s.Events.Log("", "agent_downloaded", fields)
}

// downloadRefused logs a refusal of the download path. A refusal of an issued token is logged up to refusalsPerToken
// times for that token, whatever strangers do; the line that reaches the cap says so. Any other refusal comes from
// whoever reaches the port: refusalLogBurst per minute at most, and the next one logged says how many were left out.
func (s *Server) downloadRefused(fields map[string]any, why DownloadRefusal, token string, issued bool) {
	fields["code"], fields["status"] = why, downloadStatus[why]
	if issued {
		n, ok := s.Releases.mayLogTokenRefusal(token)
		if ok {
			fields["refusal"] = n
			if n == refusalsPerToken {
				fields["more_not_logged"] = true
			}
			s.Events.Log("", "agent_download_refused", fields)
			return
		}
		if n > 0 {
			return // over the cap of this token
		}
		// gone meanwhile: counted as a stranger's
	}
	left, ok := s.Releases.mayLogRefusal()
	if !ok {
		return
	}
	if left > 0 {
		fields["suppressed"] = left
	}
	s.Events.Log("", "agent_download_refused", fields)
}
