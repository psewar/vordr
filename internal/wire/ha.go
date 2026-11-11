package wire

import "crypto/ed25519"

// The Home Assistant side of the hub (design/ha-integration.md, section 5): JSON messages over a WebSocket at HAPath,
// opened by the integration.
//
//	hub -> ha  challenge       {nonce}
//	ha  -> hub hello           {role: "ha", name, public_key, signature, integration_version, protocol}
//	hub -> ha  status          {state, reason}             again whenever the client's state changes
//	hub -> ha  snapshot        {hub, hosts}                after approval and on every connect
//	hub -> ha  host            {host}                      a host's view changed
//	hub -> ha  host_removed    {node}                      a host was forgotten or rejected
//	hub -> ha  hub             {hub}                       the hub's own state changed
//	ha  -> hub command         {request_id, node, command}
//	hub -> ha  command_result  {request_id, code, job_id}  ok = job signed and sent; the run's result follows in host
//	both       heartbeat       every HeartbeatInterval; the hub answers the client's
const (
	HAPath     = "/v1/ha"
	HAProtocol = 2 // 2: views carry codes and limits instead of German texts (H5)
	RoleHA     = "ha"

	TSnapshot      = "snapshot"
	THost          = "host"
	THostRemoved   = "host_removed"
	THub           = "hub"
	TCommand       = "command"
	TCommandResult = "command_result"
)

// ReasonProtocolMismatch: the client speaks another protocol version than the hub.
const ReasonProtocolMismatch StatusReason = "protocol_mismatch"

type HAHello struct {
	T                  string `json:"t"`
	Role               string `json:"role"`
	Name               string `json:"name"`
	PublicKey          []byte `json:"public_key"`
	Signature          []byte `json:"signature"` // over HAHelloSigned(nonce, name, public_key)
	IntegrationVersion string `json:"integration_version"`
	Protocol           int    `json:"protocol"`
}

const haHelloContext = "vordr/ha-hello/v1\x00"

// HAHelloSigned is what a Home Assistant client signs; its own context keeps it apart from agent hellos.
func HAHelloSigned(nonce []byte, name string, pub ed25519.PublicKey) []byte {
	b := append([]byte(haHelloContext), nonce...)
	b = append(b, 0)
	b = append(b, name...)
	b = append(b, 0)
	return append(b, pub...)
}

type Snapshot struct {
	T     string     `json:"t"`
	Hub   HubView    `json:"hub"`
	Hosts []HostView `json:"hosts"`
}

type HostMsg struct {
	T    string   `json:"t"`
	Host HostView `json:"host"`
}

type HostRemoved struct {
	T    string `json:"t"`
	Node string `json:"node"`
}

type HubMsg struct {
	T   string  `json:"t"`
	Hub HubView `json:"hub"`
}

type Command struct {
	T         string `json:"t"`
	RequestID string `json:"request_id"`
	Node      string `json:"node"`
	Command   string `json:"command"` // check | install | install_security | install_app:<id> | reboot | agent_update
	// Reason: a person asks for this install or reboot outside the host's window, and why (ADR-0087). Empty: the
	// window holds.
	Reason string `json:"reason,omitempty"`
}

type CommandResult struct {
	T         string `json:"t"`
	RequestID string `json:"request_id"`
	Code      string `json:"code"`
	JobID     string `json:"job_id,omitempty"`
}
