// Package target lets an agent instance speak for an appliance it cannot run on (design/proxy-agent.md,
// ADR-0089). A driver knows only its fixed requests: check the firmware, back up the configuration (ADR-0090) and,
// later, install. Nothing it sends comes from the hub.
package target

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/psewar/vordr/internal/policy"
	"github.com/psewar/vordr/internal/protocol"
)

// Status is what one check found on the appliance.
type Status struct {
	Product   string // "Synology DSM"
	Model     string
	Installed string // the version as the device names it
	// Offers: what the device offers to install; empty when it is up to date.
	Offers []Offer
	// SelfUpdate: whether the device installs updates by itself ("on (hotfix-security)", "off"; "" unknown). Vördr
	// reports it so that Home Assistant shows who installs (ADR-0092).
	SelfUpdate string
	Facts      map[string]string
}

// Offer is one update the device offers.
type Offer struct {
	Name     string // "DSM", "DSM (new major version)"
	Version  string // as the device names it; UnknownVersion when it does not say
	Security bool   // the vendor marks it as a security update
}

// UnknownVersion stands for an offered update whose version the device does not name.
const UnknownVersion = "unknown version"

// Result is the outcome of one driver call: an empty Code means it worked. Detail is for people and never decides
// anything.
type Result struct {
	Code   protocol.Code
	Detail string
}

// OK reports whether the call worked.
func (r Result) OK() bool { return r.Code == "" }

// Driver speaks for one appliance.
type Driver interface {
	Name() string
	Check(ctx context.Context) (Status, Result)
	// Backup exports the configuration and checks its form (not empty, the format's header); it returns the export and
	// the file extension to store it under, with its dot (".dss"). Store.Save refuses one without.
	Backup(ctx context.Context) ([]byte, string, Result)
}

// Open builds the driver agent.yaml names. cfg must have passed check-config.
func Open(t *policy.Target) (Driver, Result) {
	creds, res := ReadCredentials(t.Credentials)
	if !res.OK() {
		return nil, res
	}
	switch t.Driver {
	case "synology-dsm":
		client, res := httpClient(t)
		if !res.OK() {
			return nil, res
		}
		return newSynology(strings.TrimSuffix(t.Address, "/"), client, creds)
	case "pfsense":
		return newPfsense(t, creds)
	}
	return nil, Result{protocol.CodeRejectedUnsupported, "no driver " + t.Driver}
}

// ReadCredentials reads KEY=value lines (empty lines and # comments allowed). The file is written from the secret
// store and readable by the instance only; check-config made sure of that.
func ReadCredentials(path string) (map[string]string, Result) {
	f, err := os.Open(path)
	if err != nil {
		return nil, Result{protocol.CodeFailedTargetAuth, "credentials: " + err.Error()}
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, Result{protocol.CodeFailedTargetAuth, fmt.Sprintf("credentials line %d is not KEY=value", n)}
		}
		out[strings.TrimSpace(k)] = v
	}
	return out, Result{}
}

// errFingerprint: the device's certificate is not the pinned one.
var errFingerprint = errors.New("certificate does not match target.fingerprint")

const requestTimeout = 60 * time.Second

// httpClient checks the device's certificate the way agent.yaml says: against the pinned fingerprint, or against
// server_name with the system's roots or ca_file. The connection goes to target.address either way, so the HTTP Host
// is the address (some devices answer differently by name).
func httpClient(t *policy.Target) (*http.Client, Result) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if fp := t.TargetFingerprint(); fp != "" {
		want, _ := hex.DecodeString(fp)
		// The device signs its own certificate; trust is the fingerprint the operator pinned, as with PBS.
		cfg.InsecureSkipVerify = true
		cfg.VerifyPeerCertificate = func(certs [][]byte, _ [][]*x509.Certificate) error {
			if len(certs) == 0 {
				return errFingerprint
			}
			if sum := sha256.Sum256(certs[0]); hex.EncodeToString(sum[:]) != hex.EncodeToString(want) {
				return errFingerprint
			}
			return nil
		}
	} else {
		cfg.ServerName = t.ServerName
		if t.CAFile != "" {
			pem, err := os.ReadFile(t.CAFile)
			if err != nil {
				return nil, Result{protocol.CodeFailedTargetTLS, "ca_file: " + err.Error()}
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pem) {
				return nil, Result{protocol.CodeFailedTargetTLS, "ca_file holds no certificate"}
			}
			cfg.RootCAs = pool
		}
	}
	return &http.Client{Timeout: requestTimeout, Transport: &http.Transport{TLSClientConfig: cfg}}, Result{}
}

// transportResult turns a failed request into its code: a certificate that does not match is failed_target_tls
// (someone may be in between), anything else on the way is precheck_repo_unreachable. Decided on the error's type,
// never on its text.
func transportResult(err error) Result {
	var verify *tls.CertificateVerificationError
	var hostname x509.HostnameError
	var authority x509.UnknownAuthorityError
	var invalid x509.CertificateInvalidError
	switch {
	case errors.Is(err, errFingerprint), errors.As(err, &verify), errors.As(err, &hostname),
		errors.As(err, &authority), errors.As(err, &invalid):
		return Result{protocol.CodeFailedTargetTLS, err.Error()}
	}
	return Result{protocol.CodePrecheckRepoUnreachable, err.Error()}
}
