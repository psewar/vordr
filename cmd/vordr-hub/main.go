// vordr-hub is the control centre (Leitstelle): agents connect to it, the operator admits them here, and it signs
// every job. Administration runs only locally through the admin socket (design/architecture.md, section 12).
package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
  list [--json]                        all hosts: state, fingerprint, online, last report
  show <node> [--json]                 one host in full: agent, policy, updates, reboot, problem, last run
                                       --json: one JSON document for programs, versioned by its field "format"
                                       (design/architecture.md, section 12); the text is for people and may change.
                                       Exit 0 ok; 1 refused, the code says why (unknown_node); 2 wrong command line
                                       or no usable answer from the hub (nothing on stdout)
  approve <node> --fingerprint <fp>    admit a host; compare the fingerprint with "vordr-agent identity" on the host
  reject <node>                        refuse a host; its connection is closed and stays refused
  forget <node>                        remove a host; its next hello starts over as pending (new key after reinstall)
  check <node>                         send a signed check job and wait for its result
  backup <node>                        what the backup check before an install decides now (asks PBS, sends nothing)
  events [<node>] [--event <name>] [--since <when>] [--limit <n>]
                                       the newest events of the event log (default 50, at most 5000), oldest first,
                                       across its rotated files (hub.yaml event_log);
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

// cmdLine is what the command line asks for.
type cmdLine struct {
	cfgPath, verb, node, fingerprint, role, event, since, limit string
	asJSON                                                      bool
}

// parseArgs reads the command line; false: an argument or option it does not take, or --json with a verb other than
// list and show.
func parseArgs(args []string) (cmdLine, bool) {
	c := cmdLine{cfgPath: hub.DefaultConfigPath}
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--config" && i+1 < len(args):
			c.cfgPath = args[i+1]
			i++
		case args[i] == "--fingerprint" && i+1 < len(args):
			c.fingerprint = args[i+1]
			i++
		case args[i] == "--role" && i+1 < len(args):
			c.role = args[i+1]
			i++
		case args[i] == "--event" && i+1 < len(args):
			c.event = args[i+1]
			i++
		case args[i] == "--since" && i+1 < len(args):
			c.since = args[i+1]
			i++
		case args[i] == "--limit" && i+1 < len(args):
			c.limit = args[i+1]
			i++
		case args[i] == "--json":
			c.asJSON = true
		case c.verb == "" && !strings.HasPrefix(args[i], "-"):
			c.verb = args[i]
		case c.node == "" && !strings.HasPrefix(args[i], "-"):
			c.node = args[i]
		default:
			return c, false
		}
	}
	// Only list and show have a document for programs (ADR-0171); elsewhere --json would be ignored silently.
	if c.asJSON && c.verb != "list" && c.verb != "show" {
		return c, false
	}
	return c, true
}

func main() {
	c, ok := parseArgs(os.Args[1:])
	if !ok {
		usage()
		os.Exit(2)
	}
	cfgPath, verb, node, fingerprint, role, event, since, limit := c.cfgPath, c.verb, c.node, c.fingerprint, c.role,
		c.event, c.since, c.limit
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
		if k := cfg.KEV; k != nil {
			fmt.Printf("       known exploited: KEV catalog from %s every %d h, kept in %s (stale after %d h); marks "+
				"findings, changes no problem or timing\n", k.URL, k.RefreshHours, filepath.Join(cfg.StateDir, "kev"),
				k.StaleHours)
		} else {
			fmt.Printf("       known exploited: off (no kev section) - findings are not marked\n")
		}
		fmt.Printf("       %s\n", eventLogSummary(cfg))
	case "run":
		os.Exit(run(cfg))
	case "key":
		os.Exit(showKey(cfg))
	case "list", "clients":
		os.Exit(adminTo(os.Stdout, cfg.AdminSocket, hub.AdminRequest{Cmd: verb}, c.asJSON))
	case "show":
		if node == "" {
			usage()
			os.Exit(2)
		}
		os.Exit(adminTo(os.Stdout, cfg.AdminSocket, hub.AdminRequest{Cmd: verb, Node: node}, c.asJSON))
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
	if l := cfg.EventLog; l != nil {
		events.SetRotation(l.MaxBytes(), l.Keep)
	}
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
	if cfg.KEV != nil {
		k, err := hub.NewKEV(cfg.StateDir, *cfg.KEV, time.Now, logger)
		if err != nil {
			return fail("kev", err)
		}
		srv.Packages.SetKEV(k)
		go k.Run(context.Background(), srv.ViewsChanged)
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
	return adminTo(os.Stdout, cfg.AdminSocket, req, false)
}

// adminTo sends one request to the admin socket and writes the answer to w. Exit status: 0 ok, 1 the hub answered
// with another code, 2 no usable answer (the socket does not answer, or the hub that runs is older than this command
// and has no host view); with 2, w stays empty. asJSON: list and show print the hub's document (ADR-0171).
func adminTo(w io.Writer, socket string, req hub.AdminRequest, asJSON bool) int {
	timeout := 30 * time.Second
	if req.Cmd == "check" {
		timeout = 21 * time.Minute
	}
	resp, err := hub.AdminCall(socket, req, timeout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "admin socket %s: %v (does vordr-hub run, and are you allowed to use the socket?)\n", socket, err)
		return 2
	}
	switch req.Cmd {
	case "events":
		if resp.Code == hub.AdminOK {
			printEvents(w, resp.Events, resp.Unreadable, time.Local)
			return 0
		}
		fmt.Fprintf(w, "events  %s\n", resp.Code)
	case "list", "show":
		doc := resp.Hosts
		if doc == nil {
			// The answer itself says so: a hub before ADR-0171 sends list without the document and refuses show.
			fmt.Fprintf(os.Stderr, "the running hub has no host view (answer %s): it is older than this command - "+
				"restart vordr-hub after the update\n", resp.Code)
			return 2
		}
		switch {
		case asJSON:
			enc := json.NewEncoder(w)
			enc.SetEscapeHTML(false)
			enc.SetIndent("", "  ")
			_ = enc.Encode(doc)
		case req.Cmd == "list":
			printList(w, doc.Nodes)
		case doc.Code == hub.AdminOK && len(doc.Nodes) == 1:
			printShow(w, doc.Nodes[0], time.Local)
		default:
			fmt.Fprintf(w, "%s  %s\n", req.Node, doc.Code)
		}
	case "clients":
		printClients(w, resp.Clients)
	case "approve-client", "reject-client", "forget-client", "client-role":
		fmt.Fprintf(w, "%s  %s\n", req.Client, resp.Code)
	case "check":
		st := resp.JobStatus
		if resp.Code == hub.AdminOK && st != nil {
			fmt.Fprintf(w, "%s  job %s: %s", req.Node, st.JobID, st.Code)
			if st.Reason != "" {
				fmt.Fprintf(w, " (%s)", st.Reason)
			}
			fmt.Fprintln(w)
			if st.Detail != "" {
				fmt.Fprintln(w, "  "+st.Detail)
			}
			return 0
		}
		fmt.Fprintf(w, "%s  %s\n", req.Node, resp.Code)
	case "backup":
		fmt.Fprintf(w, "%s  %s", req.Node, resp.Code)
		if b := resp.Backup; b != nil {
			fmt.Fprintf(w, " (state %s", b.State)
			if b.Group != "" {
				fmt.Fprintf(w, ", group %s", b.Group)
			}
			if b.Last != nil {
				fmt.Fprintf(w, ", last %s, %.1f h ago", b.Last.Local().Format("2006-01-02 15:04"), time.Since(*b.Last).Hours())
			}
			if b.Exempt != "" {
				fmt.Fprintf(w, ", without: %s", b.Exempt)
			}
			if b.BeforeInstall != "" {
				fmt.Fprintf(w, ", a fresh backup by %s before the install)", b.BeforeInstall)
			} else {
				fmt.Fprintf(w, ", limit %d h)", b.MaxAgeHours)
			}
		}
		fmt.Fprintln(w)
	default:
		fmt.Fprintf(w, "%s  %s\n", req.Node, resp.Code)
	}
	if resp.Code != hub.AdminOK {
		return 1
	}
	return 0
}

func printList(w io.Writer, nodes []hub.NodeView) {
	if len(nodes) == 0 {
		fmt.Fprintln(w, "no hosts")
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
		fmt.Fprintf(w, "%-20s %-17s %-7s %s  agent %s  %s\n  fingerprint %s\n", n.Node, n.State, online, n.OS,
			n.AgentVersion, report, n.Fingerprint)
	}
}

func printClients(w io.Writer, clients []hub.ClientView) {
	if len(clients) == 0 {
		fmt.Fprintln(w, "no Home Assistant clients")
		return
	}
	for _, c := range clients {
		conn := "not connected"
		if c.Connected {
			conn = "connected"
		}
		fmt.Fprintf(w, "%-20s %-17s %-13s role %-8s integration %s  first seen %s\n  fingerprint %s\n", c.Name, c.State,
			conn, c.Role, c.IntegrationVersion, c.FirstSeen.Local().Format("2006-01-02 15:04"), c.Fingerprint)
	}
}
