package hub

import (
	"sort"
	"sync"
	"time"

	"github.com/psewar/vordr/internal/wire"
)

// maxPendingClients: Home Assistant clients waiting for approval at most. There are one or two in practice; the
// bound keeps a stranger from filling the register.
const maxPendingClients = 5

// ClientEntry is one Home Assistant client (design/ha-integration.md, section 4). Like a host, its key is fixed at
// the first hello; another key under the same name is refused until the operator forgets the client.
type ClientEntry struct {
	Name               string         `json:"name"`
	PublicKey          []byte         `json:"public_key"`
	Fingerprint        string         `json:"fingerprint"`
	State              wire.NodeState `json:"state"`
	FirstSeen          time.Time      `json:"first_seen"`
	DecidedAt          *time.Time     `json:"decided_at,omitempty"`
	IntegrationVersion string         `json:"integration_version"`
	// Role: what this client may start (roles.go); empty = operate.
	Role string `json:"role,omitempty"`
}

// ClientRegistry is the register of Home Assistant clients, apart from the hosts: clients.json.
type ClientRegistry struct {
	mu      sync.Mutex
	path    string
	clients map[string]*ClientEntry
}

// OpenClientRegistry loads the register from path; a missing file is an empty register.
func OpenClientRegistry(path string) (*ClientRegistry, error) {
	list, err := loadList[*ClientEntry](path)
	if err != nil {
		return nil, err
	}
	r := &ClientRegistry{path: path, clients: map[string]*ClientEntry{}}
	for _, c := range list {
		r.clients[c.Name] = c
	}
	return r, nil
}

// save writes the register; the caller holds mu.
func (r *ClientRegistry) save() error {
	list := make([]*ClientEntry, 0, len(r.clients))
	for _, c := range r.clients {
		list = append(list, c)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return saveList(r.path, list)
}

// Admit decides about a hello whose signature is already verified, like Registry.Admit for hosts.
func (r *ClientRegistry) Admit(h wire.HAHello, fingerprint string, now time.Time) (wire.NodeState, wire.StatusReason, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.clients[h.Name]
	if c == nil {
		pending := 0
		for _, o := range r.clients {
			if o.State == wire.StatePendingApproval {
				pending++
			}
		}
		if pending >= maxPendingClients {
			return wire.StateRejected, wire.ReasonTooManyPending, nil
		}
		c = &ClientEntry{Name: h.Name, PublicKey: h.PublicKey, Fingerprint: fingerprint, State: wire.StatePendingApproval,
			FirstSeen: now}
		r.clients[h.Name] = c
	}
	if c.Fingerprint != fingerprint {
		return wire.StateRejected, wire.ReasonKeyMismatch, nil
	}
	if c.State == wire.StateRejected {
		return wire.StateRejected, wire.ReasonRejected, nil
	}
	c.IntegrationVersion = h.IntegrationVersion
	return c.State, wire.ReasonNone, r.save()
}

// Decide approves or rejects a client. Approval needs the fingerprint the operator compared with the one Home
// Assistant shows.
func (r *ClientRegistry) Decide(name string, to wire.NodeState, fingerprint string, now time.Time) AdminCode {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.clients[name]
	switch {
	case c == nil:
		return AdminUnknownNode
	case to == wire.StateApproved && fingerprint != c.Fingerprint:
		return AdminFingerprintMismatch
	case c.State == to:
		return AdminAlready
	}
	old, oldAt := c.State, c.DecidedAt
	c.State, c.DecidedAt = to, &now
	if err := r.save(); err != nil {
		c.State, c.DecidedAt = old, oldAt
		return AdminStoreFailed
	}
	return AdminOK
}

// Forget removes a client; its next hello starts over as pending.
func (r *ClientRegistry) Forget(name string) AdminCode {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.clients[name]
	if c == nil {
		return AdminUnknownNode
	}
	delete(r.clients, name)
	if err := r.save(); err != nil {
		r.clients[name] = c
		return AdminStoreFailed
	}
	return AdminOK
}

// Get returns a copy of one client.
func (r *ClientRegistry) Get(name string) (ClientEntry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c := r.clients[name]; c != nil {
		return *c, true
	}
	return ClientEntry{}, false
}

// List returns copies of all clients, sorted by name.
func (r *ClientRegistry) List() []ClientEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]ClientEntry, 0, len(r.clients))
	for _, c := range r.clients {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
