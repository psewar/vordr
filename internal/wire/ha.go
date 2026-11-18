package wire

import (
	"crypto/ed25519"
	"time"
)

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
//	ha  -> hub agent_download  {request_id, os, arch}      only to a hub whose status named FeatureAgentDownload
//	hub -> ha  agent_download_result {request_id, code, version, manifest, manifest_sig, hub_key, expires_at,
//	                                  downloads}
//	ha  -> hub approve_host    {request_id, node, fingerprint}  only to a hub whose status named FeatureApproveHost
//	hub -> ha  approve_host_result {request_id, code}      ok = the waiting host is approved
//	both       heartbeat       every HeartbeatInterval; the hub answers the client's
const (
	HAPath     = "/v1/ha"
	HAProtocol = 2 // 2: views carry codes and limits instead of German texts (H5)
	RoleHA     = "ha"

	TSnapshot            = "snapshot"
	THost                = "host"
	THostRemoved         = "host_removed"
	THub                 = "hub"
	TCommand             = "command"
	TCommandResult       = "command_result"
	TAgentDownload       = "agent_download"
	TAgentDownloadResult = "agent_download_result"
	TApproveHost         = "approve_host"
	TApproveHostResult   = "approve_host_result"
)

// FeatureAgentDownload, in the status the hub sends a Home Assistant client: the hub answers agent_download
// (ADR-0167). As with the agents (ADR-0067), a client sends the message only to a hub that named it; an older hub
// drops the connection on a message it does not know.
const FeatureAgentDownload = "agent_download"

// FeatureApproveHost, in the status the hub sends a Home Assistant client: this client may approve a waiting host
// (ADR-0192). Named only while hub.yaml sets ha_approves_hosts and the client is approved with role admin; a role
// change sends the status again. A client sends approve_host only while its last status named it.
const FeatureApproveHost = "approve_host"

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

// AgentDownload asks for the current agent release, to install the agent on a new host (ADR-0167). Only a client
// with role admin gets it.
type AgentDownload struct {
	T         string `json:"t"`
	RequestID string `json:"request_id"`
	// OS and Arch: the platform of the new host, as the manifest names it (linux/amd64, linux/arm64, linux/arm,
	// windows/amd64). Both empty: every binary of the release.
	OS   string `json:"os,omitempty"`
	Arch string `json:"arch,omitempty"`
}

// AgentDownloadResult answers AgentDownload. Everything but code comes only with code ok.
//
// The hub passes the manifest on exactly as the operator signed it. Home Assistant checks the signature with the
// operator key it has from the person who set it up, never from the hub, and takes version, hashes and sizes only from
// the manifest it checked: the hub can withhold a release, not slip in a binary (ADR-0032).
type AgentDownloadResult struct {
	T         string `json:"t"`
	RequestID string `json:"request_id"`
	Code      string `json:"code"`
	// Version: the manifest's version, for display before the check.
	Version string `json:"version,omitempty"`
	// Manifest and ManifestSig: the exact bytes of manifest.json and manifest.sig (base64 in JSON). The signature is
	// over "vordr/agent-manifest/v1\x00" followed by the manifest.
	Manifest    []byte `json:"manifest,omitempty"`
	ManifestSig []byte `json:"manifest_sig,omitempty"`
	// HubKey: the hub's public key that signs every job, as hub.public_key in agent.yaml (base64).
	HubKey []byte `json:"hub_key,omitempty"`
	// ExpiresAt: after this, the paths in Downloads open nothing.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	// Downloads: one per binary asked for.
	Downloads []AgentDownloadFile `json:"downloads,omitempty"`
}

// AgentDownloadFile opens one binary of the release, once, until ExpiresAt.
type AgentDownloadFile struct {
	// File: the binary's name in the manifest, where its platform, hash and size are.
	File string `json:"file"`
	// Path: an HTTPS GET of this path at the hub's address, with its own token, returns the binary.
	Path string `json:"path"`
}

// ApproveHost asks the hub to approve a host that waits for approval (ADR-0192). The hub approves it only when the node
// waits and Fingerprint is the one it holds for the node's key: what `vordr-agent identity` printed on the host, or
// what "Add host" read over the SSH connection whose host key the person confirmed.
type ApproveHost struct {
	T           string `json:"t"`
	RequestID   string `json:"request_id"`
	Node        string `json:"node"`
	Fingerprint string `json:"fingerprint"` // hex SHA-256 of the host's public key, 64 lower-case digits
}

// ApproveHostResult answers ApproveHost with a code: ok, or why the hub did not approve.
type ApproveHostResult struct {
	T         string `json:"t"`
	RequestID string `json:"request_id"`
	Code      string `json:"code"`
}
