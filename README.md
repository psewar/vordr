# Vördr

<img src="custom_components/vordr/brand/logo.png" alt="Vördr: a shield with the Algiz rune" height="64">

English · [Deutsch](README.de.md)

Vördr keeps hosts and services up to date and shows in Home Assistant where that does not work.

The name is the Old Norse *vörðr*, in Norse tradition the guardian spirit that goes with a person through life; here
every host has its own agent. The mark is a shield with the Algiz rune (ᛉ), the rune of protection. Sources and how
the images are made: `assets/brand/` (`python assets/brand/render.py`).

A small agent (`vordr-agent`) on each host reports pending updates, installs them on request or in its maintenance
window, and reports every outcome as a fixed state. A hub (`vordr-hub`) approves the agents, signs their jobs and passes
their state to Home Assistant: the integration `custom_components/vordr` ([ha/](ha/README.md)) connects to the hub as
an approved client and creates one device per host, with its update sources and important programs and their state.

**What is built:**

- Sources: apt (Debian, Ubuntu, Raspberry Pi OS, Proxmox VE and Backup Server), Windows Update and winget.
- Check, install and reboot as signed jobs, with a local policy, maintenance windows, a run journal and checks after
  the run; optionally automatic, in rings with canaries.
- Optionally a backup check before every install, or a fresh backup of the guest right before it (first
  implementation: Proxmox VE with Proxmox Backup Server).
- Signed self-update of the agents with rollback; releases are signed with `vordr-release` on the operator's machine.
- Packages from pip, pipx and npm, and container images: inventory, known vulnerabilities and malware from the OSV
  data, outdated versions, updates by button.
- Appliances without an agent of their own (firewall, NAS) through a proxy agent: firmware reports and configuration
  backups.
- In Home Assistant one device per host with update entities, sensors and buttons; problems as repairs and as events
  for automations.

Design documents: [design/architecture.md](design/architecture.md),
[design/ha-integration.md](design/ha-integration.md), [design/proxy-agent.md](design/proxy-agent.md).
Decisions: [docs/adr](docs/adr/README.md). Setup: [deploy/README.md](deploy/README.md).
Contributing: [AGENTS.md](AGENTS.md). Vulnerabilities: [SECURITY.md](SECURITY.md).
Every document except the ADRs and `AGENTS.md` also exists in German, as `*.de.md` beside it.

## Install

- **Home Assistant:** through HACS (add this repository as a custom repository, type Integration) or by hand; see
  [ha/README.md](ha/README.md).
- **Hub and agents:** `./build.sh` builds the binaries; setup, systemd units and hardening are in
  [deploy/README.md](deploy/README.md), as is signing your own agent releases.

## Build and try

```bash
./build.sh                                      # ADR index, site-neutral check, gofmt, vet (Linux + Windows), tests
                                                # (with -race where gcc exists), binaries into dist/
./dist/vordr-agent-linux-amd64 check --no-refresh   # report as JSON; refreshes package lists only as root
```

`build.sh` also checks that the repository stays site-neutral (ADR-0099, ADR-0134, `tools/check_neutral.py`): no
private addresses, no real domains, no paths outside the repository, no terms of the own site and no wording of an
operations log. The site terms are not in the repository but in `~/.config/vordr/site-terms.txt` (one term per line,
may be empty) or in the file `VORDR_SITE_TERMS` names. Without a list the build stops and says how to go on. If you do
not need a list of your own, build with `VORDR_SITE_TERMS="none: <reason>" ./build.sh`; then the generic rules run
alone, and the check prints the reason on every run.

Connect hub and agent (templates: `hub.example.yaml`, `agent.example.yaml`):

```bash
vordr-hub key                                   # public hub key -> agent.yaml hub.public_key
vordr-hub run                                   # tunnel over TLS, administration through the local socket
vordr-agent hub-check                           # on the host: can it reach the hub?
vordr-agent run                                 # on the host: logs in, waits for approval
vordr-agent identity                            # on the host: fingerprint to compare
vordr-hub approve <node> --fingerprint <fp>     # at the hub: approve
vordr-hub list                                  # state, online, last report
vordr-hub check <node>                          # send a signed check job
vordr-hub events <node> --since 24h             # events of a host in the last 24 h
```

| Path | Content |
|---|---|
| `cmd/vordr-agent` | the agent's command line |
| `cmd/vordr-hub` | the hub's command line |
| `cmd/vordr-release` | create the operator key, sign agent releases (on the operator's machine, never on the hub) |
| `internal/wire` | tunnel messages, login signature, views for Home Assistant |
| `internal/agent` | the agent side of the tunnel: login, heartbeat, reports, jobs, self-update |
| `internal/hub` | hub: register with approval, tunnel and HA endpoint, jobs, rings, backup check, event log, admin socket |
| `internal/protocol` | verbs, scopes, result codes, signed jobs and manifests with checks and replay protection |
| `internal/policy` | local policy `agent.yaml`: strict defaults, check rules, time windows |
| `internal/source` | what each update source reports |
| `internal/source/apt` | apt source: evaluate the simulation, security updates, reboot needed, install |
| `internal/source/winget` | programs on Windows through winget |
| `internal/source/winupdate` | Windows itself through Windows Update |
| `internal/inventory` | pip, pipx and npm packages per location, outdated versions, updates |
| `internal/images` | container images: inventory, comparison with the registry, rebuilds by button |
| `internal/osv` | matching against the OSV data at the hub |
| `internal/versions` | order versions as their ecosystem does (PEP 440, SemVer, Debian, Alpine) |
| `internal/target` | the proxy agent's drivers for appliances (Synology DSM, pfSense) |
| `internal/runner` | start external commands; exit codes as data |
| `internal/identity` | Ed25519 identity (Windows: DPAPI) |
| `internal/facts` | OS, architecture, virtualisation |
| `custom_components/vordr` | Home Assistant integration, at the root where HACS looks for it ([ha/README.md](ha/README.md)) |
| `ha/` | the integration's tests and their pinned requirements |
| `deploy/` | install scripts, systemd units, the forced command for pfSense ([deploy/README.md](deploy/README.md)) |
| `design/` | design documents (English, German as `*.de.md`) |
| `docs/adr/` | architecture decisions (English) |
| `tools/` | site-neutral check (`check_neutral.py`), export of the published tree (`export_public.py`), Windows time zone table |
| `assets/brand/` | logo and icons |

Key points:

- Go, static binaries without a runtime (agent: Linux amd64/arm64/arm, Windows amd64; hub: Linux amd64).
- The agent connects outward only; no port is open on the hosts.
- A closed set of verbs (`check`, `install`, `reboot`, `agent_update`, `cancel`, `backup`), no shell, no package names
  from outside.
- The local policy on the host has the last word; the hub can only ask.
- Technical names (verbs, states, configuration keys, entity IDs) are English. Display names in Home Assistant follow
  its system language (German or English).

## License

MIT, see [LICENSE](LICENSE).
