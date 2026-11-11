// vordr-hub is the control centre (Leitstelle): agents connect to it, the operator admits them here, and it signs
// every job. Administration runs only locally through the admin socket (design/architecture.md, section 12).
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	// The hub reads each agent's time zone for its install window; embedded, so it does not hinge on the host's tzdata.
	_ "time/tzdata"

	"github.com/psewar/vordr/internal/hub"
	"github.com/psewar/vordr/internal/identity"
	"github.com/psewar/vordr/internal/wire"
)

var version = "dev"

// stopTimeout: how long a stopping hub waits for the work its sessions started (ADR-0142).
const stopTimeout = 10 * time.Second

func usage() {
	fmt.Fprintf(os.Stderr, `vordr-hub %s

  vordr-hub <verb> [args] [--config <path>]

  run                                  serve the tunnel (TLS) and the local admin socket
  key                                  show the hub's public key for agent.yaml (hub.public_key); "run" creates it
  list                                 all hosts: state, fingerprint, online, last report
  approve <node> --fingerprint <fp>    admit a host; compare the fingerprint with "vordr-agent identity" on the host
  reject <node>                        refuse a host; its connection is closed and stays refused
  forget <node>                        remove a host; its next hello starts over as pending (new key after reinstall)
  check <node>                         send a signed check job and wait for its result
  backup <node>                        what the backup check before an install decides now (asks PBS, sends nothing)
  events [<node>] [--event <name>] [--since <when>] [--limit <n>]
                                       the newest events of the event log (default 50, at most 5000), oldest first;
                                       <when>: a duration back (90m, 24h, 7d), a date (2006-01-02) or a time (RFC 3339)
  clients                              Home Assistant clients: state, fingerprint, connected
  approve-client <name> --fingerprint <fp>
                                       admit a Home Assistant client; compare with the fingerprint HA shows
  reject-client <name>                 refuse a client; its connection is closed and stays refused
  forget-client <name>                 remove a client; its next hello starts over as pending
  client-role <name> --role <role>     what a client may start: observe (nothing), operate (check, install, agent
                                       update; the default), admin (also reboot)
  check-config                         load hub.yaml and list every issue
  version

  Default config: %s
`, version, hub.DefaultConfigPath)
}

func main() {
	cfgPath := hub.DefaultConfigPath
	var verb, node, fingerprint, role, event, since, limit string
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--config" && i+1 < len(args):
			cfgPath = args[i+1]
			i++
		case args[i] == "--fingerprint" && i+1 < len(args):
			fingerprint = args[i+1]
			i++
		case args[i] == "--role" && i+1 < len(args):
			role = args[i+1]
			i++
		case args[i] == "--event" && i+1 < len(args):
			event = args[i+1]
			i++
		case args[i] == "--since" && i+1 < len(args):
			since = args[i+1]
			i++
		case args[i] == "--limit" && i+1 < len(args):
			limit = args[i+1]
			i++
		case verb == "" && !strings.HasPrefix(args[i], "-"):
			verb = args[i]
		case node == "" && !strings.HasPrefix(args[i], "-"):
			node = args[i]
		default:
			usage()
			os.Exit(2)
		}
	}
	if verb == "version" {
		fmt.Println(version)
		return
	}
	cfg, status := loadConfig(cfgPath, verb == "check-config")
	if status != 0 {
		os.Exit(status)
	}
	switch verb {
	case "check-config":
		fmt.Printf("ok     %s\n       listen %s, state_dir %s, admin_socket %s, max_pending %d\n",
			cfgPath, cfg.Listen, cfg.StateDir, cfg.AdminSocket, cfg.MaxPending)
		fmt.Printf("       Home Assistant through %s (vordr-hub clients)\n       stale after %d days, offline after "+
			"%d min, reboot pending after %d days, no report after %d h\n", wire.HAPath, cfg.StaleAfterDays,
			cfg.OfflineAfterMin, cfg.RebootPendingAfterDays, cfg.NoReportAfterHours)
		if b := cfg.Backup; b != nil {
			fmt.Printf("       backup check: installs need a backup younger than %d h on %s (datastore %s), "+
				"%d guest(s)\n", b.MaxAgeHours, b.PBSURL, b.Datastore, len(b.Guests))
			if bi := b.BeforeInstall; bi != nil {
				fmt.Printf("       before each install of a vm/ or ct/ guest: a fresh backup by %s's agent\n", bi.Hypervisor)
			}
		} else {
			fmt.Printf("       backup check: off (no backup section) - installs do not wait for a backup\n")
		}
		if a := cfg.AgentUpdate; a != nil {
			fmt.Printf("       agent releases from %s, canary %v first, the others after %d h without a problem\n",
				filepath.Join(cfg.StateDir, "agent"), a.Canary, a.CanaryCleanHours)
		} else {
			fmt.Printf("       agent releases: off (no agent_update section)\n")
		}
		if a := cfg.AutoInstall; a != nil {
			fmt.Printf("       auto installs (install.auto: hub): canary %v first, the others once each update is pending "+
				"%d h or a canary installed cleanly after it appeared, inside each host's window\n", a.Canary, a.DelayHours)
		} else {
			fmt.Printf("       auto installs: off (no auto_install section) - installs only on request\n")
		}
		if o := cfg.OSV; o != nil {
			fmt.Printf("       package advisories: OSV data of %v from %s every %d h, kept in %s (stale after %d h)\n",
				o.Ecosystems, o.URL, o.RefreshHours, filepath.Join(cfg.StateDir, "osv"), o.StaleHours)
		} else {
			fmt.Printf("       package advisories: off (no osv section) - pip/npm inventories are kept, not matched\n")
		}
	case "run":
		os.Exit(run(cfg))
	case "key":
		os.Exit(showKey(cfg))
	case "list", "clients":
		os.Exit(admin(cfg, hub.AdminRequest{Cmd: verb}))
	case "events":
		f, ok := eventFilter(node, event, since, limit, time.Now())
		if !ok {
			usage()
			os.Exit(2)
		}
		os.Exit(admin(cfg, hub.AdminRequest{Cmd: verb, Events: &f}))
	case "approve", "reject", "forget", "check", "backup":
		if node == "" || (verb == "approve" && fingerprint == "") {
			usage()
			os.Exit(2)
		}
		os.Exit(admin(cfg, hub.AdminRequest{Cmd: verb, Node: node, Fingerprint: fingerprint}))
	case "approve-client", "reject-client", "forget-client":
		if node == "" || (verb == "approve-client" && fingerprint == "") {
			usage()
			os.Exit(2)
		}
		os.Exit(admin(cfg, hub.AdminRequest{Cmd: verb, Client: node, Fingerprint: fingerprint}))
	case "client-role":
		if node == "" || !hub.ValidRole(role) {
			usage()
			os.Exit(2)
		}
		os.Exit(admin(cfg, hub.AdminRequest{Cmd: verb, Client: node, Role: role}))
	default:
		usage()
		os.Exit(2)
	}
}

// loadConfig returns the exit status like the agent's check-config: 0 ok, 1 issues, 2 not found or unreadable.
func loadConfig(path string, listIssues bool) (hub.Config, int) {
	cfg, issues, found, err := hub.LoadConfig(path)
	switch {
	case err != nil:
		fmt.Fprintf(os.Stderr, "config %s: %v\n", path, err)
		return cfg, 2
	case !found:
		fmt.Fprintf(os.Stderr, "config %s: not found\n", path)
		return cfg, 2
	case len(issues) > 0:
		if listIssues {
			for _, i := range issues {
				fmt.Println("issue  " + i)
			}
		} else {
			fmt.Fprintf(os.Stderr, "config %s has %d issue(s) - run check-config\n", path, len(issues))
		}
		return cfg, 1
	}
	return cfg, 0
}

func keyPath(cfg hub.Config) string { return filepath.Join(cfg.StateDir, "hub.key") }

// showKey only reads: the hub service creates its key on first start, as the user it runs as. A key created here
// (often by root) would belong to the wrong user.
func showKey(cfg hub.Config) int {
	id, found, err := identity.LoadAt(keyPath(cfg))
	switch {
	case err != nil:
		fmt.Fprintf(os.Stderr, "hub key: %v\n", err)
		return 2
	case !found:
		fmt.Fprintf(os.Stderr, "no hub key in %s yet - the hub creates it on its first start (systemctl start vordr-hub)\n", keyPath(cfg))
		return 2
	}
	fmt.Printf("public_key  %s\nfingerprint %s\n", id.PublicB64(), id.Fingerprint())
	return 0
}

func run(cfg hub.Config) int {
	logger := log.New(os.Stderr, "", log.LstdFlags)
	fail := func(what string, err error) int {
		logger.Printf("%s: %v", what, err)
		return 1
	}
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return fail("state dir", err)
	}
	key, created, err := identity.LoadOrCreateAt(keyPath(cfg))
	if err != nil {
		return fail("hub key", err)
	}
	if created {
		logger.Printf("new hub key created, fingerprint %s", key.Fingerprint())
	}
	reg, err := hub.OpenRegistry(filepath.Join(cfg.StateDir, "registry.json"))
	if err != nil {
		return fail("register", err)
	}
	clients, err := hub.OpenClientRegistry(filepath.Join(cfg.StateDir, "clients.json"))
	if err != nil {
		return fail("client register", err)
	}
	events, err := hub.OpenEvents(filepath.Join(cfg.StateDir, "events.jsonl"), time.Now, logger)
	if err != nil {
		return fail("event log", err)
	}
	defer events.Close()
	certs, err := hub.NewCertReloader(cfg.TLSCert, cfg.TLSKey, logger)
	if err != nil {
		return fail("tls", err)
	}
	srv := hub.NewServer(reg, clients, events, key, cfg.MaxPending, logger)
	srv.Version = version
	srv.Limits = hub.Limits{StaleAfter: time.Duration(cfg.StaleAfterDays) * 24 * time.Hour,
		OfflineAfter:  time.Duration(cfg.OfflineAfterMin) * time.Minute,
		RebootAfter:   time.Duration(cfg.RebootPendingAfterDays) * 24 * time.Hour,
		NoReportAfter: time.Duration(cfg.NoReportAfterHours) * time.Hour}
	if cfg.Backup != nil {
		srv.Backups, err = hub.NewBackups(*cfg.Backup, time.Now)
		if err != nil {
			return fail("backup check", err)
		}
		go srv.Backups.Run(context.Background(), func() {
			if f := srv.Backups.Failure(); f != "" {
				logger.Printf("backup check: PBS not readable: %s", f)
			}
			srv.ViewsChanged()
		})
	}
	if cfg.AgentUpdate != nil {
		srv.Releases = hub.NewReleases(*cfg.AgentUpdate, filepath.Join(cfg.StateDir, "agent"), time.Now)
		go srv.RunRollout(context.Background())
	}
	if cfg.AutoInstall != nil {
		srv.AutoInstall = hub.NewAutoInstall(*cfg.AutoInstall)
		go srv.RunAutoInstall(context.Background())
	}
	srv.Packages, err = hub.NewPackages(cfg.StateDir, cfg.OSV, time.Now, logger)
	if err != nil {
		return fail("packages", err)
	}
	go srv.Packages.Run(context.Background(), srv.ViewsChanged)

	adminLn, err := hub.ListenAdmin(cfg.AdminSocket)
	if err != nil {
		return fail("admin socket", err)
	}
	defer adminLn.Close()
	go srv.ServeAdmin(adminLn)

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fail("listen", err)
	}
	httpSrv := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		TLSConfig:         &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: certs.GetCertificate},
	}
	served := make(chan error, 1)
	go func() { served <- httpSrv.ServeTLS(ln, "", "") }()
	events.Log("", "hub_started", map[string]any{"version": version, "listen": cfg.Listen,
		"paths": []string{wire.TunnelPath, wire.HAPath}})

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	select {
	case sig := <-stop:
		events.Log("", "hub_stopping", map[string]any{"signal": sig.String()})
		_ = httpSrv.Close()
		_ = adminLn.Close()
		// The sessions end; what they started (a report being stored, a job's result) finishes before the hub exits
		// (ADR-0142).
		ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
		defer cancel()
		if !srv.Close(ctx) {
			logger.Printf("stopping: the sessions' work did not end within %s", stopTimeout)
		}
		return 0
	case err := <-served:
		if errors.Is(err, http.ErrServerClosed) {
			return 0
		}
		return fail("serve", err)
	}
}

func admin(cfg hub.Config, req hub.AdminRequest) int {
	timeout := 30 * time.Second
	if req.Cmd == "check" {
		timeout = 21 * time.Minute
	}
	resp, err := hub.AdminCall(cfg.AdminSocket, req, timeout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "admin socket %s: %v (does vordr-hub run, and are you allowed to use the socket?)\n", cfg.AdminSocket, err)
		return 2
	}
	switch req.Cmd {
	case "events":
		if resp.Code == hub.AdminOK {
			printEvents(os.Stdout, resp.Events, resp.Unreadable, time.Local)
			return 0
		}
		fmt.Printf("events  %s\n", resp.Code)
	case "list":
		printList(resp.Nodes)
	case "clients":
		printClients(resp.Clients)
	case "approve-client", "reject-client", "forget-client", "client-role":
		fmt.Printf("%s  %s\n", req.Client, resp.Code)
	case "check":
		st := resp.JobStatus
		if resp.Code == hub.AdminOK && st != nil {
			fmt.Printf("%s  job %s: %s", req.Node, st.JobID, st.Code)
			if st.Reason != "" {
				fmt.Printf(" (%s)", st.Reason)
			}
			fmt.Println()
			if st.Detail != "" {
				fmt.Println("  " + st.Detail)
			}
			return 0
		}
		fmt.Printf("%s  %s\n", req.Node, resp.Code)
	case "backup":
		fmt.Printf("%s  %s", req.Node, resp.Code)
		if b := resp.Backup; b != nil {
			fmt.Printf(" (state %s", b.State)
			if b.Group != "" {
				fmt.Printf(", group %s", b.Group)
			}
			if b.Last != nil {
				fmt.Printf(", last %s, %.1f h ago", b.Last.Local().Format("2006-01-02 15:04"), time.Since(*b.Last).Hours())
			}
			if b.Exempt != "" {
				fmt.Printf(", without: %s", b.Exempt)
			}
			if b.BeforeInstall != "" {
				fmt.Printf(", a fresh backup by %s before the install)", b.BeforeInstall)
			} else {
				fmt.Printf(", limit %d h)", b.MaxAgeHours)
			}
		}
		fmt.Println()
	default:
		fmt.Printf("%s  %s\n", req.Node, resp.Code)
	}
	if resp.Code != hub.AdminOK {
		return 1
	}
	return 0
}

func printList(nodes []hub.NodeView) {
	if len(nodes) == 0 {
		fmt.Println("no hosts")
		return
	}
	for _, n := range nodes {
		online := "offline"
		if n.Online {
			online = "online"
		}
		report := "no report"
		if n.Pending != nil {
			reboot := "reboot unknown"
			if n.Reboot != nil {
				reboot = map[bool]string{true: "reboot required", false: "no reboot needed"}[*n.Reboot]
			}
			report = fmt.Sprintf("%d pending (%d security), %s, checked %s", *n.Pending, *n.Security, reboot,
				n.ReportAt.Local().Format("2006-01-02 15:04"))
		}
		fmt.Printf("%-20s %-17s %-7s %s  agent %s  %s\n  fingerprint %s\n", n.Node, n.State, online, n.OS,
			n.AgentVersion, report, n.Fingerprint)
	}
}

func printClients(clients []hub.ClientView) {
	if len(clients) == 0 {
		fmt.Println("no Home Assistant clients")
		return
	}
	for _, c := range clients {
		conn := "not connected"
		if c.Connected {
			conn = "connected"
		}
		fmt.Printf("%-20s %-17s %-13s role %-8s integration %s  first seen %s\n  fingerprint %s\n", c.Name, c.State,
			conn, c.Role, c.IntegrationVersion, c.FirstSeen.Local().Format("2006-01-02 15:04"), c.Fingerprint)
	}
}
