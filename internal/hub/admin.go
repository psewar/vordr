package hub

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/wire"
)

// Administration runs only through a local Unix socket (mode 0600 in a 0700 directory): whoever may use it is
// already root on the hub host. There is no web interface, and Home Assistant cannot admit hosts.

type AdminRequest struct {
	// list | approve | reject | forget | check | backup | events, and for Home Assistant clients:
	// clients | approve-client | reject-client | forget-client | client-role
	Cmd         string       `json:"cmd"`
	Node        string       `json:"node,omitempty"`
	Client      string       `json:"client,omitempty"`
	Fingerprint string       `json:"fingerprint,omitempty"`
	Role        string       `json:"role,omitempty"`   // client-role: observe | operate | admin
	Events      *EventFilter `json:"events,omitempty"` // events: which ones; nil: the newest DefaultEventLimit
}

type AdminResponse struct {
	Code      AdminCode        `json:"code"`
	Nodes     []NodeView       `json:"nodes,omitempty"`
	Clients   []ClientView     `json:"clients,omitempty"`
	JobStatus *wire.JobStatus  `json:"job_status,omitempty"`
	Backup    *wire.BackupView `json:"backup,omitempty"` // backup: what the check before an install decides now
	// events: the matching lines of the event log as the hub wrote them, oldest first; Unreadable counts lines that
	// were no event (the one being written that moment).
	Events     []json.RawMessage `json:"events,omitempty"`
	Unreadable int               `json:"unreadable,omitempty"`
}

const (
	// AdminBackupCheckOff: "backup" asked of a hub without a backup section.
	AdminBackupCheckOff AdminCode = "backup_check_off"
	// AdminEventsUnreadable: the hub could not read its own event log.
	AdminEventsUnreadable AdminCode = "events_unreadable"
)

// ClientView is one line of "clients".
type ClientView struct {
	Name               string         `json:"name"`
	State              wire.NodeState `json:"state"`
	Fingerprint        string         `json:"fingerprint"`
	Connected          bool           `json:"connected"`
	IntegrationVersion string         `json:"integration_version"`
	FirstSeen          time.Time      `json:"first_seen"`
	Role               string         `json:"role"`
}

// NodeView is one line of "list".
type NodeView struct {
	Node         string         `json:"node"`
	State        wire.NodeState `json:"state"`
	Fingerprint  string         `json:"fingerprint"`
	Online       bool           `json:"online"`
	LastSeen     *time.Time     `json:"last_seen,omitempty"`
	AgentVersion string         `json:"agent_version"`
	OS           string         `json:"os"`
	Pending      *int           `json:"pending,omitempty"`
	Security     *int           `json:"security,omitempty"`
	Reboot       *bool          `json:"reboot_required,omitempty"` // only when some signal answered
	ReportAt     *time.Time     `json:"report_at,omitempty"`
}

// checkTimeout: a check may refresh package lists over a slow link.
const checkTimeout = 20 * time.Minute

// installTimeout: the longest install.timeout_min an agent accepts (720), twice for the installation itself, which
// is never stopped half way, plus the verification. The agent ends the run itself; this only bounds the wait.
const installTimeout = 25 * time.Hour

// waitTimeout is how long the hub waits for a job's result before it counts the run as timed out.
func waitTimeout(verb protocol.Verb) time.Duration {
	switch verb {
	case protocol.VerbInstall:
		return installTimeout
	case protocol.VerbAgentUpdate:
		return agentUpdateTimeout
	}
	return checkTimeout
}

// ListenAdmin opens the admin socket. A socket file left behind by a crashed hub is replaced; one that a running
// hub answers on is not.
func ListenAdmin(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); err == nil {
		// The only way to tell a live socket from a stale file is to try it; a refused dial is the answer "stale".
		if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
			c.Close()
			return nil, errors.New("another hub answers on " + path)
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

// ServeAdmin answers one request per connection until ln is closed.
func (s *Server) ServeAdmin(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			var req AdminRequest
			_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
			if json.NewDecoder(c).Decode(&req) != nil {
				_ = json.NewEncoder(c).Encode(AdminResponse{Code: AdminBadRequest})
				return
			}
			_ = c.SetReadDeadline(time.Time{})
			_ = json.NewEncoder(c).Encode(s.Admin(req))
		}()
	}
}

// Admin carries out one request.
func (s *Server) Admin(req AdminRequest) AdminResponse {
	switch req.Cmd {
	case "list":
		return AdminResponse{Code: AdminOK, Nodes: s.views()}
	case "approve", "reject":
		to := wire.StateApproved
		if req.Cmd == "reject" {
			to = wire.StateRejected
		}
		code := s.Reg.Decide(req.Node, to, req.Fingerprint, s.Now())
		s.Events.Log(req.Node, req.Cmd, map[string]any{"code": code})
		if code == AdminOK {
			s.Notify(req.Node)
			s.changed(req.Node)
		}
		return AdminResponse{Code: code}
	case "forget":
		code := s.Reg.Forget(req.Node)
		s.Events.Log(req.Node, "forget", map[string]any{"code": code})
		if code == AdminOK {
			if s.Packages != nil {
				s.Packages.Forget(req.Node)
			}
			s.Notify(req.Node)
			s.changed(req.Node)
		}
		return AdminResponse{Code: code}
	case "check":
		ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
		defer cancel()
		st, code := s.SendJob(ctx, req.Node, protocol.VerbCheck, "")
		return AdminResponse{Code: code, JobStatus: &st}
	case "backup":
		// The same decision as before an install (PBS asked anew), without sending anything.
		if s.Backups == nil {
			return AdminResponse{Code: AdminBackupCheckOff}
		}
		ctx, cancel := context.WithTimeout(context.Background(), backupFetchTimeout)
		defer cancel()
		code, v := s.backupGate(ctx, req.Node)
		s.changedHA()
		return AdminResponse{Code: code, Backup: v}
	case "events":
		f := EventFilter{}
		if req.Events != nil {
			f = *req.Events
		}
		lines, unreadable, err := s.Events.Read(f)
		if err != nil {
			return AdminResponse{Code: AdminEventsUnreadable}
		}
		return AdminResponse{Code: AdminOK, Events: lines, Unreadable: unreadable}
	case "clients":
		return AdminResponse{Code: AdminOK, Clients: s.clientViews()}
	case "client-role":
		code := s.Clients.SetRole(req.Client, req.Role)
		s.Events.Log("", "client_role", map[string]any{"client": req.Client, "role": req.Role, "code": code})
		return AdminResponse{Code: code}
	case "approve-client", "reject-client":
		to := wire.StateApproved
		if req.Cmd == "reject-client" {
			to = wire.StateRejected
		}
		code := s.Clients.Decide(req.Client, to, req.Fingerprint, s.Now())
		s.Events.Log("", req.Cmd, map[string]any{"client": req.Client, "code": code})
		if code == AdminOK {
			s.notifyClient(req.Client)
		}
		return AdminResponse{Code: code}
	case "forget-client":
		code := s.Clients.Forget(req.Client)
		s.Events.Log("", req.Cmd, map[string]any{"client": req.Client, "code": code})
		if code == AdminOK {
			s.notifyClient(req.Client)
		}
		return AdminResponse{Code: code}
	}
	return AdminResponse{Code: AdminBadRequest}
}

func (s *Server) clientViews() []ClientView {
	var out []ClientView
	for _, c := range s.Clients.List() {
		s.mu.Lock()
		sess := s.haSessions[c.Name]
		s.mu.Unlock()
		out = append(out, ClientView{Name: c.Name, State: c.State, Fingerprint: c.Fingerprint,
			Connected: sess != nil && sess.fingerprint == c.Fingerprint, IntegrationVersion: c.IntegrationVersion,
			FirstSeen: c.FirstSeen, Role: EffectiveRole(c.Role)})
	}
	return out
}

func (s *Server) views() []NodeView {
	var out []NodeView
	for _, e := range s.Reg.List() {
		v := NodeView{Node: e.Node, State: e.State, Fingerprint: e.Fingerprint, AgentVersion: e.AgentVersion, OS: e.Facts.OS}
		if online, seen := s.Online(e.Node); online {
			v.Online, v.LastSeen = true, &seen
		}
		if r := e.LastReport; r != nil {
			at := r.CheckedAt
			v.ReportAt = &at
			pending, security := 0, 0
			for _, src := range r.Sources {
				pending += src.Pending
				security += src.Security
			}
			v.Pending, v.Security = &pending, &security
			if r.Reboot != nil && len(r.Reboot.Checked) > 0 {
				req := r.Reboot.Required
				v.Reboot = &req
			}
		}
		out = append(out, v)
	}
	return out
}

// AdminCall sends one request to the hub's admin socket and waits for the answer.
func AdminCall(path string, req AdminRequest, timeout time.Duration) (AdminResponse, error) {
	c, err := net.DialTimeout("unix", path, 5*time.Second)
	if err != nil {
		return AdminResponse{}, err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(timeout))
	if err := json.NewEncoder(c).Encode(req); err != nil {
		return AdminResponse{}, err
	}
	var resp AdminResponse
	if err := json.NewDecoder(c).Decode(&resp); err != nil {
		return AdminResponse{}, err
	}
	return resp, nil
}
