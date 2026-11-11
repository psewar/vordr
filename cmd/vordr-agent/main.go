// vordr-agent keeps one host up to date and reports every outcome as a fixed state (design/architecture.md).
// Phase 1: tunnel to the hub with approval, reports and signed check jobs. Install and reboot follow in phase 2.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/psewar/vordr/internal/agent"
	"github.com/psewar/vordr/internal/facts"
	"github.com/psewar/vordr/internal/identity"
	"github.com/psewar/vordr/internal/policy"
	"github.com/psewar/vordr/internal/protocol"
	"github.com/psewar/vordr/internal/runner"
	"github.com/psewar/vordr/internal/source"
	"github.com/psewar/vordr/internal/target"
)

var version = "dev"

func usage() {
	fmt.Fprintf(os.Stderr, `vordr-agent %s

  vordr-agent <verb> [--config <path>]

  run                   connect to the hub (hub.url, hub.public_key), wait for approval, report, carry out jobs
  hub-check             can this host reach its hub? dns, connect, tls, websocket, challenge - without logging in
  check [--no-refresh]  check every update source once and print the report as JSON
                        (refreshes the package lists only as root and only if apt's lock is free)
  check-config          load agent.yaml and list every issue (exit 0 ok, 1 issues, 2 not found or unreadable)
  packages              print the pip/pipx/npm packages this agent reports, as JSON (packages: in agent.yaml)
  packages --plan <location>[/<name>]   what an update would install there (candidates and dependencies);
                        asks the registry and lets pip/npm resolve, changes nothing, asks no hub
  packages --plan-deps <location>:<name>[,<name>...]   what refreshing these dependencies would install there
                        (ADR-0104: npm folders with a lock file); the hub would name them, here you do
  identity             show fingerprint and public key of this agent; creates the key pair on first use
  install-service [--replace-config "<reason>"]
                        Windows, as administrator: set the agent up as a service (checks config and hub first).
                        An installed agent.yaml that differs is replaced only with a reason: kept as
                        agent.yaml.bak-<time>, difference shown, reason in agent.log. Exit 0 done, 2 refused
                        (nothing changed), 3 failed and put back (service as before), 1 failed, state printed
  uninstall-service     Windows: stop and remove the service (identity and agent.yaml stay)
  update-guard --job <id> --exe <path>
                        started by the agent itself before it swaps its binary (ADR-0098): if that update is still
                        pending after 15 min, put <path>.old back and restart the agent. Exit 0, 1 rollback failed
  version

  Default config: %s
`, version, policy.DefaultConfigPath)
}

func main() {
	cfgPath := policy.DefaultConfigPath
	var verb, plan, planDeps, reason, job, exe string
	refresh, replace := true, false
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--config" && i+1 < len(args):
			cfgPath = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--config="):
			cfgPath = strings.TrimPrefix(args[i], "--config=")
		case args[i] == "--no-refresh":
			refresh = false
		case args[i] == "--plan" && i+1 < len(args):
			plan = args[i+1]
			i++
		case args[i] == "--plan-deps" && i+1 < len(args):
			planDeps = args[i+1]
			i++
		case args[i] == "--replace-config" && i+1 < len(args):
			replace, reason = true, args[i+1]
			i++
		case strings.HasPrefix(args[i], "--replace-config="):
			replace, reason = true, strings.TrimPrefix(args[i], "--replace-config=")
		case args[i] == "--job" && i+1 < len(args):
			job = args[i+1]
			i++
		case args[i] == "--exe" && i+1 < len(args):
			exe = args[i+1]
			i++
		case verb == "" && !strings.HasPrefix(args[i], "-"):
			verb = args[i]
		default:
			usage()
			os.Exit(2)
		}
	}
	switch {
	case replace && verb != "install-service":
		fmt.Fprintln(os.Stderr, "--replace-config belongs to install-service; on Linux: deploy/install-agent.sh")
		os.Exit(2)
	case replace && strings.TrimSpace(reason) == "":
		fmt.Fprintln(os.Stderr, "--replace-config needs a reason")
		os.Exit(2)
	case verb == "update-guard" && (job == "" || exe == ""), verb != "update-guard" && (job != "" || exe != ""):
		fmt.Fprintln(os.Stderr, "--job and --exe belong to update-guard, which needs both")
		os.Exit(2)
	case (plan != "" || planDeps != "") && verb != "packages", plan != "" && planDeps != "":
		fmt.Fprintln(os.Stderr, "--plan and --plan-deps belong to packages, one of them at a time")
		os.Exit(2)
	}
	switch verb {
	case "version":
		fmt.Println(version)
	case "check-config":
		os.Exit(checkConfig(cfgPath))
	case "identity":
		os.Exit(showIdentity(cfgPath))
	case "check":
		os.Exit(check(cfgPath, refresh))
	case "packages":
		os.Exit(packages(cfgPath, plan, planDeps))
	case "run":
		if asService() {
			os.Exit(runService(cfgPath))
		}
		os.Exit(runAgent(cfgPath))
	case "hub-check":
		os.Exit(hubCheck(cfgPath))
	case "install-service":
		os.Exit(installService(cfgPath, reason))
	case "uninstall-service":
		os.Exit(uninstallService())
	case "update-guard":
		os.Exit(updateGuard(cfgPath, job, exe))
	default:
		usage()
		os.Exit(2)
	}
}

// loadPolicy: a missing file means the strict defaults; a file with issues is not used at all.
func loadPolicy(path string) (policy.Config, bool) {
	cfg, issues, found, err := policy.Load(path)
	switch {
	case err != nil:
		fmt.Fprintf(os.Stderr, "config %s: %v\n", path, err)
		return policy.Config{}, false
	case !found:
		cfg, _ = policy.Parse(nil)
		return cfg, true
	case len(issues) > 0:
		fmt.Fprintf(os.Stderr, "config %s has %d issue(s) - run check-config\n", path, len(issues))
		return policy.Config{}, false
	}
	return cfg, true
}

func checkConfig(path string) int {
	cfg, issues, found, err := policy.Load(path)
	switch {
	case err != nil:
		fmt.Fprintf(os.Stderr, "config %s: %v\n", path, err)
		return 2
	case !found:
		fmt.Fprintf(os.Stderr, "config %s: not found - the agent runs with the strict defaults (check only)\n", path)
		return 2
	case len(issues) > 0:
		for _, i := range issues {
			fmt.Println("issue  " + i.String())
		}
		return 1
	}
	hub := "not configured (every job is rejected)"
	if cfg.HubKey() != nil {
		hub = cfg.Hub.URL
	}
	scopes := strings.Join(cfg.Install.AllowedScopes, ", ")
	if scopes == "" {
		scopes = "none"
	}
	fmt.Printf("ok     %s\n", path)
	fmt.Printf("       node %s, state_dir %s\n", cfg.Node, cfg.StateDir)
	fmt.Printf("       hub: %s\n", hub)
	mode := cfg.Install.Mode
	if mode == "" {
		mode = "full-upgrade (default)"
	}
	auto := cfg.Install.Auto
	if cfg.Install.AutoScope != "" {
		auto += " (scope " + cfg.Install.AutoScope + ")"
	}
	fmt.Printf("       install: scopes %s, default %q, auto %s, mode %s\n", scopes, cfg.Install.DefaultScope, auto, mode)
	backup := ""
	if cfg.AgentUpdate.BackupPublicKey != "" {
		backup = " (with backup key)"
	}
	fmt.Printf("       reboot allowed: %v, agent_update: %v%s, apps: %d, health checks: %d\n", cfg.Reboot.Allowed,
		cfg.AgentUpdate.Enabled, backup, len(cfg.Apps), len(cfg.Health))
	for _, h := range cfg.Health {
		fmt.Printf("         after installs: %s\n", h)
	}
	if b := cfg.GuestBackup; b != nil {
		fmt.Printf("       guest backups before installs: to %s, at most %d min each (never pruned)\n", b.Storage, b.TimeoutMin)
	}
	if t := cfg.Target; t != nil {
		tlsWay := "certificate for " + t.ServerName
		switch fp := t.TargetFingerprint(); {
		case t.HostKey != "": // SSH drivers pin the host key instead (ADR-0094)
			kind, k, _ := strings.Cut(t.HostKey, " ")
			tlsWay = "host key " + kind + " " + k[:min(len(k), 16)] + "..."
		case fp != "": // check-config allows exactly one of the two
			tlsWay = "fingerprint " + fp
		}
		fmt.Printf("       target: %s at %s, %s, credentials %s\n", t.Driver, t.Address, tlsWay, t.Credentials)
		fmt.Printf("       config backup: keep %d, max age %d h, in %s\n", cfg.ConfigBackup.Keep,
			cfg.ConfigBackup.MaxAgeH, filepath.Join(cfg.StateDir, "config-backups"))
	}
	fmt.Printf("       %s\n", packagesSummary(cfg.Packages, cfg.Install.ImagePatterns()))
	return 0
}

// packagesSummary is check-config's line about packages: where the agent looks, and whether it reports the images of
// the running containers (ADR-0113) - new traffic to their registries when outdated is on, so it is shown either way -
// and whose rebuilds it installs by button (ADR-0119): the agent then writes to the engine.
func packagesSummary(pk policy.Packages, images []string) string {
	if !pk.InventoryOn() {
		return "packages: off"
	}
	s := fmt.Sprintf("packages: well-known places %v, %d location(s) from agent.yaml", pk.DetectOn(), len(pk.Locations))
	switch {
	case !pk.Containers:
		return s + ", container images off"
	case pk.OutdatedOn() && len(images) > 0:
		return s + ", container images on (compared with their registries; rebuilds installed by button for " +
			strings.Join(images, ", ") + ")"
	case pk.OutdatedOn():
		return s + ", container images on (compared with their registries, only reported)"
	default:
		return s + ", container images on (listed only: outdated is off)"
	}
}

// packages prints the inventory exactly as the agent would send it; an empty list when packages.inventory is false.
func packages(path, plan, planDeps string) int {
	cfg, ok := loadPolicy(path)
	if !ok {
		return 2
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	var out any
	switch {
	case plan != "":
		sc, ok := protocol.ParseScope("pkg:" + plan)
		if !ok {
			fmt.Fprintf(os.Stderr, "--plan %q: <location>[/<name>]\n", plan)
			return 2
		}
		out = agent.PlanUpdate(context.Background(), cfg.Packages, sc.Location, sc.Package)
	case planDeps != "":
		loc, list, _ := strings.Cut(planDeps, ":")
		names := strings.Split(list, ",")
		if !protocol.ValidAppID(loc) || slices.ContainsFunc(names, func(n string) bool {
			return !protocol.PackageNamePattern.MatchString(n)
		}) {
			fmt.Fprintf(os.Stderr, "--plan-deps %q: <location>:<name>[,<name>...]\n", planDeps)
			return 2
		}
		out = agent.PlanDeps(context.Background(), cfg.Packages, loc, names)
	default:
		// as to a hub that keeps images
		out = agent.Inventory(cfg, cfg.StateDir, version, log.New(os.Stderr, "", 0), time.Now)(context.Background(),
			agent.Wants{Containers: true, ImageContents: true})
	}
	if err := enc.Encode(out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	return 0
}

func showIdentity(path string) int {
	cfg, ok := loadPolicy(path)
	if !ok {
		return 2
	}
	id, created, err := identity.LoadOrCreate(cfg.StateDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "identity in %s: %v\n", cfg.StateDir, err)
		return 2
	}
	if created {
		fmt.Println("new key pair created")
	}
	fmt.Printf("node        %s\nfingerprint %s\npublic_key  %s\n", cfg.Node, id.Fingerprint(), id.PublicB64())
	return 0
}

type checkOutput struct {
	Node         string          `json:"node"`
	AgentVersion string          `json:"agent_version"`
	Host         facts.Host      `json:"host"`
	Sources      []source.Report `json:"sources"`
	Reboot       *source.Reboot  `json:"reboot,omitempty"`
}

// check prints the report; the outcome is in the report (state, code), the exit code only says whether there is one.
func check(path string, refresh bool) int {
	cfg, ok := loadPolicy(path)
	if !ok {
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	host := facts.Collect(ctx, runner.Exec{})
	checker := agent.System(host, refresh, cfg.Install.Mode)
	if cfg.Target != nil {
		var res target.Result
		if checker, host, res = targetSetup(ctx, cfg); !res.OK() {
			fmt.Fprintf(os.Stderr, "target %s: %s %s\n", cfg.Target.Driver, res.Code, res.Detail)
			return 2
		}
	}
	rep := checker.Check(ctx)
	out := checkOutput{Node: cfg.Node, AgentVersion: version, Host: host, Sources: rep.Sources, Reboot: rep.Reboot}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	return 0
}

var stageHint = map[agent.ProbeStage]string{
	agent.StageConfig:    "set hub.url (wss://...) and hub.public_key (vordr-hub key) in agent.yaml",
	agent.StageDNS:       "the hub's name does not resolve here - check DNS for this network",
	agent.StageConnect:   "no connection - is the hub running, the port right, a firewall in between?",
	agent.StageTLS:       "certificate not trusted - check name, chain and hub.ca_file",
	agent.StageWebSocket: "TLS works, but no vordr tunnel at this URL - check the path (/v1/tunnel) and port",
	agent.StageChallenge: "something answers, but it is not a vordr hub",
}

// hubCheck reports the first stage that fails, or every detail of a working connection.
func hubCheck(path string) int {
	cfg, issues, found, err := policy.Load(path)
	if err != nil || !found || len(issues) > 0 {
		fmt.Fprintf(os.Stderr, "config %s not usable - run check-config\n", path)
		return 2
	}
	r := agent.Probe(context.Background(), cfg)
	if r.Failed != "" {
		fmt.Printf("hub-check %s: failed at stage %s\n  %s\n", cfg.Hub.URL, r.Failed, stageHint[r.Failed])
		if r.Detail != "" {
			fmt.Printf("  detail: %s\n", r.Detail)
		}
		return 1
	}
	fmt.Printf("hub-check %s: ok\n  addresses: %s\n  chain: %s\n", cfg.Hub.URL, strings.Join(r.Addresses, ", "),
		strings.Join(r.Chain, " <- "))
	return 0
}

// updateGuard watches the self-update job until it is settled, or rolls it back (ADR-0098). The agent starts it, on
// Linux in the transient unit vordr-agent-update-guard (its journal), on Windows detached (agent.log).
func updateGuard(path, job, exe string) int {
	cfg, ok := loadPolicy(path)
	if !ok {
		return 2
	}
	logger := log.New(serviceLog(cfg.StateDir), "update-guard: ", log.LstdFlags)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	g := agent.NewGuard(cfg.StateDir, exe, job, restartAgent, logger)
	var err error
	if g.Requests, g.Start, err = plannedRestarts(ctx, job); err != nil {
		// The agent then finds no guard to ask (armGuard refuses the swap, an agent's own rollback ends with an error).
		logger.Printf("job %s: planned restart not available: %v", job, err)
	}
	out := g.Watch(ctx)
	logger.Printf("job %s: %s", job, out)
	if out == agent.GuardFailed {
		return 1
	}
	return 0
}

// targetSetup opens the instance's driver, its backup store under the state directory and the appliance's facts.
func targetSetup(ctx context.Context, cfg policy.Config) (agent.Checker, facts.Host, target.Result) {
	d, res := target.Open(cfg.Target)
	if !res.OK() {
		return nil, facts.Host{}, res
	}
	store := target.Store{Dir: filepath.Join(cfg.StateDir, "config-backups"), Keep: cfg.ConfigBackup.Keep}
	checker := agent.TargetChecker(d, store, time.Duration(cfg.ConfigBackup.MaxAgeH)*time.Hour, time.Now)
	return checker, agent.TargetFacts(ctx, cfg.Node, d), target.Result{}
}

// runAgent runs in the foreground until interrupted.
func runAgent(path string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runAgentCtx(ctx, path, false, nil)
}

// runAgentCtx needs a valid agent.yaml with the hub configured: without the pinned hub key every job would be
// rejected, so there is no point in connecting. toFile: log to <state_dir>\agent.log (a Windows service has no
// console). onClient, if set, gets the client before it connects (the Windows service tells it about sleep).
func runAgentCtx(ctx context.Context, path string, toFile bool, onClient func(*agent.Client)) int {
	cfg, issues, found, err := policy.Load(path)
	switch {
	case err != nil:
		fmt.Fprintf(os.Stderr, "config %s: %v\n", path, err)
		return 2
	case !found:
		fmt.Fprintf(os.Stderr, "config %s: not found - run needs hub.url and hub.public_key\n", path)
		return 2
	case len(issues) > 0:
		fmt.Fprintf(os.Stderr, "config %s has %d issue(s) - run check-config\n", path, len(issues))
		return 2
	case cfg.Hub.URL == "" || cfg.HubKey() == nil:
		fmt.Fprintf(os.Stderr, "config %s: hub.url and hub.public_key are required for run\n", path)
		return 2
	}
	logger := log.New(os.Stderr, "", log.LstdFlags)
	if toFile {
		logger.SetOutput(serviceLog(cfg.StateDir))
	}
	id, created, err := identity.LoadOrCreate(cfg.StateDir)
	if err != nil {
		logger.Printf("identity in %s: %v", cfg.StateDir, err)
		return 2
	}
	if created {
		logger.Printf("new key pair created")
	}
	if cfg.Target != nil {
		// A proxy instance (design/proxy-agent.md): it speaks for its appliance only. No installer, reboot, containers,
		// packages or self-update - they would act on the container it runs in (4.3: the container's own agent updates
		// the shared binary and restarts the instances).
		checker, host, res := targetSetup(ctx, cfg)
		if !res.OK() {
			logger.Printf("target %s: %s %s", cfg.Target.Driver, res.Code, res.Detail)
			return 2
		}
		client := agent.NewClient(cfg, id, version, host, checker, logger)
		logger.Printf("vordr-agent %s, node %s for %s at %s, fingerprint %s, hub %s, verbs %v", version, cfg.Node,
			cfg.Target.Driver, cfg.Target.Address, id.Fingerprint(), cfg.Hub.URL, client.Verbs())
		if onClient != nil {
			onClient(client)
		}
		client.Run(ctx)
		return 0
	}
	host := facts.Collect(ctx, runner.Exec{})
	client := agent.NewClient(cfg, id, version, host, agent.System(host, true, cfg.Install.Mode), logger)
	client.Installer = agent.SystemInstaller(host)
	client.Health = agent.NewHealth(cfg.Health)
	client.Rebooter = agent.NewRebooter(cfg, client.Installer)
	client.Containers = agent.NewContainers()
	if cfg.Packages.InventoryOn() {
		client.Inventory = agent.Inventory(cfg, cfg.StateDir, version, logger, time.Now)
		client.Packages = agent.NewPackageUpdates(cfg.Packages)
		client.Images = agent.NewImageUpdates(cfg) // ADR-0119: only with image:<project>/<service> in allowed_scopes
	}
	updater, why := agent.NewSelfUpdate(cfg, path)
	client.Updater = updater
	if updater == nil && cfg.AgentUpdate.Enabled {
		logger.Printf("self-update off: %s", why)
	}
	client.Backups, why = agent.NewGuestBackups(cfg)
	if why != "" {
		logger.Printf("guest backups off: %s", why)
	}
	logger.Printf("vordr-agent %s, node %s, fingerprint %s, hub %s, verbs %v", version, cfg.Node, id.Fingerprint(),
		cfg.Hub.URL, client.Verbs())
	if onClient != nil {
		onClient(client)
	}
	client.Run(ctx)
	client.WaitRuns() // a stop request does not cut an install run short
	return 0
}
