package hub

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/source"
	"github.com/psewar/vordr/internal/wire"
)

// An install changes a host. Before it starts there has to be a state to go back to (ADR-0030), so the hub
// asks Proxmox Backup Server how old the host's last backup is - itself, not by trusting a schedule: a backup that
// failed or ran late stops the install, with a code that says why.

// BackupConfig is the backup section of hub.yaml. Without it there is no backup check.
type BackupConfig struct {
	PBSURL      string `yaml:"pbs_url"`       // https://<pbs>:8007
	Datastore   string `yaml:"datastore"`     // the datastore the hosts are backed up to
	Fingerprint string `yaml:"fingerprint"`   // SHA-256 of PBS's certificate as PBS shows it, AA:BB:...
	TokenFile   string `yaml:"token_file"`    // API token "<user>@<realm>!<name>:<secret>", readable by the hub only
	MaxAgeHours int    `yaml:"max_age_hours"` // default 26: a daily backup plus slack
	// Guests maps every host that may install to its PBS backup group - or says why it has none.
	Guests []BackupGuest `yaml:"guests"`
	// BeforeInstall: every guest of the hypervisor (group vm/<id> or ct/<id>) gets a fresh backup right before it
	// installs, made by the hypervisor's agent (ADR-0096). The age check stays for host/ groups. Without it the age
	// check is the only rule.
	BeforeInstall *BeforeInstall `yaml:"before_install"`
}

// BeforeInstall names the agent that makes the backups: the hypervisor's, with guest_backup in its agent.yaml.
type BeforeInstall struct {
	Hypervisor string `yaml:"hypervisor"`
}

// BackupGuest: exactly one of Group, Without and ConfigBackup.
type BackupGuest struct {
	Node    string `yaml:"node"`
	Group   string `yaml:"group"`   // "vm/301", "ct/302" or "host/<name>"
	Without string `yaml:"without"` // why this host has no backup to check; shown in Home Assistant
	// ConfigBackup: an appliance behind a proxy agent (ADR-0089). Its proxy exports the configuration and reports when
	// (ADR-0090); the age check takes that time instead of asking PBS. The hub never sees the backup itself.
	ConfigBackup bool `yaml:"config_backup"`
}

// kinds counts how many of group, without and config_backup the entry gives.
func (g BackupGuest) kinds() int {
	n := 0
	for _, set := range []bool{g.Group != "", strings.TrimSpace(g.Without) != "", g.ConfigBackup} {
		if set {
			n++
		}
	}
	return n
}

const (
	defaultBackupMaxAgeH = 26
	backupRefresh        = 10 * time.Minute
	backupFetchTimeout   = 10 * time.Second
	// backupCacheGrace: when PBS does not answer right before an install, a fetch this recent still counts.
	backupCacheGrace = 30 * time.Minute
)

// Backup states in wire.BackupView.
const (
	BackupOK          = "ok"
	BackupStale       = "stale"       // older than max_age_hours
	BackupMissing     = "missing"     // PBS has no backup in the host's group
	BackupUnreachable = "unreachable" // PBS did not answer (recently enough)
	BackupUnmapped    = "unmapped"    // hub.yaml names no group for the host
	BackupExempt      = "exempt"      // no backup by decision (Without)
)

var (
	backupGroupPattern = regexp.MustCompile(`^(vm|ct)/[0-9]+$|^host/[A-Za-z0-9._-]+$`)
	datastorePattern   = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*$`)
	fingerprintPattern = regexp.MustCompile(`^([0-9A-Fa-f]{2}:){31}[0-9A-Fa-f]{2}$`)
)

// check lists everything wrong with the backup section; readable tells whether the hub can open a file.
func (b *BackupConfig) check(readable func(string) bool) []string {
	var issues []string
	add := func(format string, a ...any) { issues = append(issues, "backup."+fmt.Sprintf(format, a...)) }
	if u, err := url.Parse(b.PBSURL); err != nil || u.Scheme != "https" || u.Host == "" || u.Path != "" {
		add("pbs_url: must be https://<host>:<port> without a path")
	}
	if !datastorePattern.MatchString(b.Datastore) {
		add("datastore: %q is not a datastore name", b.Datastore)
	}
	if !fingerprintPattern.MatchString(b.Fingerprint) {
		add("fingerprint: must be the SHA-256 fingerprint of PBS's certificate (AA:BB:... 32 bytes)")
	}
	switch {
	case b.TokenFile == "":
		add("token_file: required (API token with Datastore.Audit on the datastore)")
	case !readable(b.TokenFile):
		add("token_file: %s is not readable", b.TokenFile)
	}
	if b.MaxAgeHours < 1 || b.MaxAgeHours > 24*30 {
		add("max_age_hours: must be between 1 and 720")
	}
	seen := map[string]bool{}
	for i, g := range b.Guests {
		switch {
		case !nodePattern.MatchString(g.Node):
			add("guests[%d].node: %q is not a node name", i, g.Node)
		case seen[g.Node]:
			add("guests[%d].node: %q appears twice", i, g.Node)
		}
		seen[g.Node] = true
		switch {
		case g.kinds() != 1:
			add("guests[%d]: give exactly one of group (its PBS backup group), config_backup: true (an appliance whose "+
				"proxy backs up its configuration) or without (why it has none)", i)
		case g.Group != "" && !backupGroupPattern.MatchString(g.Group):
			add("guests[%d].group: %q is not a PBS group (vm/<id>, ct/<id>, host/<name>)", i, g.Group)
		}
	}
	if bi := b.BeforeInstall; bi != nil {
		switch {
		case !nodePattern.MatchString(bi.Hypervisor):
			add("before_install.hypervisor: %q is not a node name (the hypervisor's agent)", bi.Hypervisor)
		case slices.ContainsFunc(b.Guests, func(g BackupGuest) bool { return g.Node == bi.Hypervisor && freshGroup(g.Group) }):
			add("before_install.hypervisor: %s would back up itself as a guest; give it a host/ group or without", bi.Hypervisor)
		}
	}
	return issues
}

// freshGroup: a group vzdump makes, so the hypervisor's agent can back it up before an install (ADR-0096).
func freshGroup(group string) bool {
	_, _, ok := protocol.ParseGuest(group)
	return ok
}

// Backups knows each backup group's newest backup on PBS, refreshed every backupRefresh and right before an install.
type Backups struct {
	cfg    BackupConfig
	token  string
	client *http.Client
	now    func() time.Time

	mu      sync.Mutex
	last    map[string]time.Time // group -> newest backup
	fetched time.Time            // last successful fetch
	failure string               // why the last fetch failed; "" after a success
}

// NewBackups reads the token and pins PBS's certificate. cfg must have passed ParseConfig.
func NewBackups(cfg BackupConfig, now func() time.Time) (*Backups, error) {
	raw, err := os.ReadFile(cfg.TokenFile)
	if err != nil {
		return nil, err
	}
	want, _ := hex.DecodeString(strings.ReplaceAll(cfg.Fingerprint, ":", ""))
	// PBS signs its own certificate; trust is the fingerprint the operator copied from PBS, like PVE does it.
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true,
		VerifyPeerCertificate: func(certs [][]byte, _ [][]*x509.Certificate) error {
			if len(certs) == 0 {
				return errors.New("PBS sent no certificate")
			}
			if sum := sha256.Sum256(certs[0]); hex.EncodeToString(sum[:]) != hex.EncodeToString(want) {
				return errors.New("PBS certificate does not match backup.fingerprint")
			}
			return nil
		}}
	return &Backups{cfg: cfg, token: strings.TrimSpace(string(raw)), now: now, last: map[string]time.Time{},
		client: &http.Client{Timeout: backupFetchTimeout, Transport: &http.Transport{TLSClientConfig: tlsCfg}}}, nil
}

// Run refreshes until ctx ends and calls changed after every refresh, so that the views follow.
func (b *Backups) Run(ctx context.Context, changed func()) {
	for {
		_ = b.Refresh(ctx)
		changed()
		select {
		case <-ctx.Done():
			return
		case <-time.After(backupRefresh):
		}
	}
}

type pbsGroups struct {
	Data []struct {
		Type string `json:"backup-type"`
		ID   string `json:"backup-id"`
		Last int64  `json:"last-backup"`
	} `json:"data"`
}

// Refresh asks PBS for every group's newest backup in one call.
func (b *Backups) Refresh(ctx context.Context) error {
	err := b.fetch(ctx)
	b.mu.Lock()
	defer b.mu.Unlock()
	if err != nil {
		b.failure = err.Error()
		return err
	}
	b.failure, b.fetched = "", b.now()
	return nil
}

func (b *Backups) fetch(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, backupFetchTimeout)
	defer cancel()
	u := b.cfg.PBSURL + "/api2/json/admin/datastore/" + url.PathEscape(b.cfg.Datastore) + "/groups"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "PBSAPIToken="+b.token)
	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("PBS answered %s", resp.Status)
	}
	var g pbsGroups
	if err := json.NewDecoder(resp.Body).Decode(&g); err != nil {
		return fmt.Errorf("PBS answer: %w", err)
	}
	last := map[string]time.Time{}
	for _, d := range g.Data {
		if d.Last > 0 {
			last[d.Type+"/"+d.ID] = time.Unix(d.Last, 0).UTC()
		}
	}
	b.mu.Lock()
	b.last = last
	b.mu.Unlock()
	return nil
}

func (b *Backups) guest(node string) *BackupGuest {
	for i := range b.cfg.Guests {
		if b.cfg.Guests[i].Node == node {
			return &b.cfg.Guests[i]
		}
	}
	return nil
}

// FreshFor is the group that gets a fresh backup right before an install on node (ADR-0096); "" when node keeps the
// age check: no before_install, no group, or a host/ group (vzdump backs up guests only).
func (b *Backups) FreshFor(node string) string {
	if g := b.guest(node); b.cfg.BeforeInstall != nil && g != nil && freshGroup(g.Group) {
		return g.Group
	}
	return ""
}

// Hypervisor is the node whose agent makes the backups before installs; "" without before_install.
func (b *Backups) Hypervisor() string {
	if b.cfg.BeforeInstall == nil {
		return ""
	}
	return b.cfg.BeforeInstall.Hypervisor
}

// ReportedConfigBackup is the time of the appliance's newest good configuration backup as its proxy agent last
// reported it (ADR-0090); nil when the report names none or not in RFC 3339.
func ReportedConfigBackup(r *wire.Report) *time.Time {
	if r == nil {
		return nil
	}
	for _, s := range r.Sources {
		if s.Source != source.ConfigBackup {
			continue
		}
		if t, err := time.Parse(time.RFC3339, s.Facts[source.FactConfigBackupAt]); err == nil {
			t = t.UTC()
			return &t
		}
		return nil
	}
	return nil
}

// View is the backup state of node as of now, from what the last refresh found. configAt is the configuration backup
// node's agent reported (ReportedConfigBackup); it only counts for a guest with config_backup.
func (b *Backups) View(node string, now time.Time, configAt *time.Time) *wire.BackupView {
	b.mu.Lock()
	defer b.mu.Unlock()
	v := &wire.BackupView{MaxAgeHours: b.cfg.MaxAgeHours, State: BackupUnmapped}
	guest := b.guest(node)
	switch {
	case guest == nil:
		return v
	case guest.Without != "":
		v.State, v.Exempt = BackupExempt, guest.Without
		return v
	case guest.ConfigBackup:
		// PBS plays no part: whether it answers says nothing about the appliance's backup.
		v.ConfigBackup, v.Last = true, configAt
		switch {
		case configAt == nil:
			v.State = BackupMissing
		case now.Sub(*configAt) > time.Duration(b.cfg.MaxAgeHours)*time.Hour:
			v.State = BackupStale
		default:
			v.State = BackupOK
		}
		return v
	}
	v.Group = guest.Group
	if b.FreshFor(node) != "" {
		v.BeforeInstall = b.cfg.BeforeInstall.Hypervisor
	}
	if t, ok := b.last[guest.Group]; ok {
		v.Last = &t
	}
	switch {
	case b.fetched.IsZero() || now.Sub(b.fetched) > backupCacheGrace:
		v.State = BackupUnreachable
	case v.Last == nil:
		v.State = BackupMissing
	case now.Sub(*v.Last) > time.Duration(b.cfg.MaxAgeHours)*time.Hour:
		v.State = BackupStale
	default:
		v.State = BackupOK
	}
	return v
}

// Gate decides whether an install may start on node now. It asks PBS again first; a failed answer falls back on a
// fetch younger than backupCacheGrace, never on an older one. A guest that gets a fresh backup first (FreshFor)
// passes whatever the age of its last one, as long as PBS answers - the backup it waits for has to be checked there.
// An appliance with config_backup is decided on configAt alone, without asking PBS.
func (b *Backups) Gate(ctx context.Context, node string, configAt *time.Time) (AdminCode, *wire.BackupView) {
	if g := b.guest(node); g == nil || !g.ConfigBackup {
		_ = b.Refresh(ctx) // a failure shows as BackupUnreachable once the cache is too old
	}
	v := b.View(node, b.now(), configAt)
	if v.BeforeInstall != "" && v.State != BackupUnreachable {
		return AdminOK, v
	}
	switch v.State {
	case BackupOK, BackupExempt:
		return AdminOK, v
	case BackupStale:
		return AdminBackupStale, v
	case BackupMissing:
		return AdminBackupMissing, v
	case BackupUnmapped:
		return AdminBackupUnmapped, v
	}
	return AdminBackupUnreachable, v
}

// Since asks PBS now for the newest backup of group and reports it if it is not older than since. why says what is
// missing otherwise: PBS did not answer, or it has no such backup.
func (b *Backups) Since(ctx context.Context, group string, since time.Time) (at time.Time, why string) {
	if err := b.Refresh(ctx); err != nil {
		return time.Time{}, "PBS did not answer: " + err.Error()
	}
	b.mu.Lock()
	last, ok := b.last[group]
	b.mu.Unlock()
	if !ok || last.Before(since) {
		return time.Time{}, "PBS has no backup of " + group + " since " + since.UTC().Format(time.RFC3339)
	}
	return last, ""
}

// Failure is why the last fetch failed, "" when it worked (for the hub's log).
func (b *Backups) Failure() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.failure
}
