package agent

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net"
	"net/url"
	"time"

	"github.com/gorilla/websocket"

	"github.com/psewar/vordr/internal/policy"
	"github.com/psewar/vordr/internal/wire"
)

// ProbeStage is the step of reaching the hub. Probe walks them in order; the first that fails is the finding, so
// nothing is read out of an error text.
type ProbeStage string

const (
	StageConfig    ProbeStage = "config"    // hub.url / hub.public_key missing or unusable
	StageDNS       ProbeStage = "dns"       // the hub's name does not resolve
	StageConnect   ProbeStage = "connect"   // no TCP connection (firewall, port, hub not running)
	StageTLS       ProbeStage = "tls"       // certificate not trusted (chain, name, ca_file)
	StageWebSocket ProbeStage = "websocket" // TLS fine, but no vordr tunnel at that path
	StageChallenge ProbeStage = "challenge" // tunnel opened, but no valid challenge: not a vordr hub
	// StageHubKey: the hub signs its challenge, but not with hub.public_key over this connection, or it does not sign
	// although state_dir remembers that it did (ADR-0170). A hub that does not sign and never did passes, with HubKey
	// unsigned.
	StageHubKey ProbeStage = "hub_key"
)

// ProbeResult: Failed is empty when every stage passed. Detail is for humans only.
type ProbeResult struct {
	Failed    ProbeStage `json:"failed,omitempty"`
	Detail    string     `json:"detail,omitempty"`
	Addresses []string   `json:"addresses,omitempty"`
	Chain     []string   `json:"chain,omitempty"` // subjects of the verified chain, leaf first
	// HubKey: what the challenge says about hub.public_key, once the challenge stage passed (ADR-0170).
	HubKey wire.HubKeyCheck `json:"hub_key,omitempty"`
}

// Probe checks whether this host can reach its hub, stage by stage, without logging in. The last stage checks
// hub.public_key against the hub's signature on its challenge.
func Probe(ctx context.Context, cfg policy.Config) ProbeResult {
	var r ProbeResult
	fail := func(s ProbeStage, err error) ProbeResult {
		r.Failed = s
		if err != nil {
			r.Detail = err.Error()
		}
		return r
	}
	u, err := url.Parse(cfg.Hub.URL)
	if err != nil || u.Scheme != "wss" || u.Hostname() == "" || cfg.HubKey() == nil {
		return fail(StageConfig, err)
	}
	host, port := u.Hostname(), u.Port()
	if port == "" {
		port = "443"
	}
	tlsCfg, err := TLSConfig(cfg.Hub.CAFile)
	if err != nil {
		return fail(StageConfig, err)
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	if r.Addresses, err = net.DefaultResolver.LookupHost(ctx, host); err != nil {
		return fail(StageDNS, err)
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		return fail(StageConnect, err)
	}
	tlsCfg.ServerName = host
	tc := tls.Client(conn, tlsCfg)
	if err := tc.HandshakeContext(ctx); err != nil {
		conn.Close()
		return fail(StageTLS, err)
	}
	if chains := tc.ConnectionState().VerifiedChains; len(chains) > 0 {
		for _, c := range chains[0] {
			r.Chain = append(r.Chain, c.Subject.String())
		}
	}
	tc.Close()

	nonce, header := wire.NewChallengeNonce()
	tlsCfg, _ = TLSConfig(cfg.Hub.CAFile)
	ws, _, err := (&websocket.Dialer{TLSClientConfig: tlsCfg, HandshakeTimeout: 15 * time.Second}).DialContext(ctx, cfg.Hub.URL, header)
	if err != nil {
		return fail(StageWebSocket, err)
	}
	c := wire.NewConn(ws)
	defer c.Close()
	t, raw, err := c.Receive(wire.HelloTimeout)
	var ch wire.Challenge
	if err != nil || t != wire.TChallenge || json.Unmarshal(raw, &ch) != nil || len(ch.Nonce) != wire.NonceSize {
		return fail(StageChallenge, err)
	}
	// What state_dir remembers counts here as at login, so that hub-check finds a downgrade too. Read only: hub-check
	// changes nothing, the record is written by the agent's own login.
	rec := readHubSigned(cfg)
	if r.HubKey = judgeHubKey(ch.CheckHubKey(nonce, cfg.HubKey()), rec); r.HubKey.Refused() {
		_ = c.CloseWith(r.HubKey.CloseCode()) // the hub logs the refusal, as at login
		r.Failed = StageHubKey
		if r.HubKey == wire.HubKeyDowngrade {
			r.Detail = hubSignedNote(cfg, rec)
		}
		return r
	}
	return r
}
