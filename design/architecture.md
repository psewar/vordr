# Vördr – design

English · [Deutsch](architecture.de.md)

> **Status:** This design describes what is built: agent for Linux (apt) and Windows (Windows Update, winget), hub with
> approval, signed jobs, install, reboot and self-update in rings, inventory of pip, pipx, npm and container images with
> OSV matching, backup before the install. Home Assistant talks to the hub as an approved client over `/v1/ha`
> ([ha-integration.md](ha-integration.md)); MQTT is gone ([ADR-0026](../docs/adr/0026-remove-mqtt.md)). Appliances
> without an agent of their own: [proxy-agent.md](proxy-agent.md). What is not built yet appears in its place as "not
> built yet". Technical names (verbs, states, configuration keys, entity IDs) are English, display names follow the
> system language of Home Assistant ([ADR-0075](../docs/adr/0075-display-names-follow-system-language.md)). Examples use
> placeholders (`example-host`, `vordr-hub.example`) instead of actual host names.
>
> **Rationale:** The decisions behind this design are the ADRs in [docs/adr](../docs/adr/README.md).

Vördr keeps hosts and services up to date and shows in Home Assistant where that does not work: an update cannot be
installed, a run has failed, updates stay pending or a host has stopped reporting. Components: one agent per host
(`vordr-agent`) and a hub (`vordr-hub`), both in Go ([ADR-0003](../docs/adr/0003-go.md)). The pattern
(Ed25519 identity with approval, outbound tunnel, signed self-update with a canary) comes from an earlier agent (in the
following: the earlier agent); the security boundaries are drawn tighter here because jobs run as root
([ADR-0002](../docs/adr/0002-own-project-following-an-earlier-agent.md)).

## 1. Motivation and goal

With the `update` entity and the page *Settings → Updates*, Home Assistant already has a central view of updates, but
only for devices that bring an integration (HA itself, add-ons, HACS, ESPHome, NAS, cameras …). Linux servers, VMs and
LXC containers, Proxmox hosts, Raspberry Pis, Windows PCs and the programs on them are missing. In addition, the
`update` entity has no state "failed": if an installation fails, an error message appears briefly and nothing is
stored.

Alternatives examined (status 2026-10; [ADR-0001](../docs/adr/0001-own-agent-instead-of-patchmon.md)):

| Tool | Why not |
|---|---|
| PatchMon | Central remote execution as root on all hosts and a browser SSH terminal: one compromised server means root everywhere. No HA integration. |
| updates2mqtt | Docker containers only. |
| LNXlink | Counts pending updates, but runs nothing and reports no failures. |
| HASS.Agent | Windows update sensor only in the old version; whether the current fork has it is open. |

Goals:

1. One HA device per host, with its update sources and important programs, each with its update status.
2. Trigger updates from HA (button) or install them automatically in the maintenance window.
3. Every problem as a **reported state** in HA: rejected, failed, stuck, reboot needed, host silent.

Non-goals ([ADR-0013](../docs/adr/0013-scope-limits.md)):

- No general remote administration: no shell, no file transfer, no configuration management.
- No metrics monitoring.
- Devices with vendor firmware (router, firewall, NAS, printer, ESPHome devices) get no agent. The HA integrations
  supply their update status; Vördr only watches them through the HA blueprint (section 8.5).
- Kubernetes workloads: updates there run as pull requests (e.g. Renovate) through GitOps, not through an agent.

## 2. Principles

1. **Report state instead of deriving it from errors.** Every result is a fixed code (section 7). Error texts go along
   at most as an excerpt for humans, never into a decision. If a case must be told apart, it gets its own code
   ([ADR-0007](../docs/adr/0007-fixed-codes-instead-of-error-texts.md)).
2. **The agent does its job itself, including the checks.** Before any effect it checks the preconditions. If
   something is wrong, it refuses and reports the state and the next step instead of acting halfway. What can be
   derived, it derives (e.g. the host's time zone for its window).
3. **The local policy has the last word.** What a host allows (verbs, scope, window, reboot) is in its own
   configuration. The hub can only ask ([ADR-0006](../docs/adr/0006-local-policy-decides.md)). The only exception: a
   run that a human demands with a reason, the agent also accepts outside its window
   ([ADR-0087](../docs/adr/0087-runs-outside-the-window-with-a-reason.md)). When a human presses Install in HA, the hub
   supplies this reason itself ([ADR-0109](../docs/adr/0109-the-install-button-installs-at-once.md)).
4. **Smallest attack surface.** Closed set of verbs, no shell, no package names from outside, no web interface in the
   hub. Agents talk only to the hub, never to Home Assistant
   ([ADR-0005](../docs/adr/0005-closed-verbs-without-shell.md),
   [ADR-0010](../docs/adr/0010-administration-only-local.md), [ADR-0023](../docs/adr/0023-own-ha-integration.md)).
5. **Only entities that mean something here**, and deletion only on a reliable answer (lesson from the earlier
   agent: otherwise entities flicker on every restart).
6. **Special cases only explicitly and with a reason** that stays visible in the log and in HA (e.g. a run outside the
   window, 6.4), never as a silent switch.

## 3. Overview

```
┌───────────── per host ───────────────┐
│ vordr-agent (Go, static binary)      │
│  ├─ sources: apt | winupdate | …     │
│  ├─ local policy (agent.yaml)        │
│  └─ Ed25519 identity                 │
└──────────────────┬───────────────────┘
                   │ outbound only: WebSocket over TLS
                   ▼
      vordr-hub (hub) ◀── wss /v1/ha ── Home Assistant (integration "vordr")
       ├─ register + approval                   buttons → commands (check, install, reboot)
       ├─ signs jobs
       ├─ rollout in rings
       ├─ distributes agent binaries (does not sign them)
       └─ event log

Operator key: signs the agent manifests, never on the hub
```

| Part | Runs where | Job |
|---|---|---|
| `vordr-agent` | on every managed host, as a systemd or Windows service | query sources, check and run jobs, report the result, update itself |
| `vordr-hub` | on a Linux host or in a container of its own ([ADR-0054](../docs/adr/0054-hub-in-own-container.md)), as a systemd service | log in and approve agents, sign and deliver jobs, rollout in rings, distribute agent binaries, pass state to HA |
| Home Assistant | the existing instance, with the integration `custom_components/vordr` | the only interface: display, buttons, notifications |
| Signing the releases | at the operator, never on the hub | operator key, signs the manifest of every agent version (10.4) |

Connections:

- **Agent → hub:** outbound only, WebSocket over TLS (`/v1/tunnel`), login by Ed25519 challenge. No port is open on
  the hosts, reconnect with backoff 1–30 s.
- **Home Assistant → hub:** the integration connects as an approved client over `/v1/ha`, on the same port and with
  the same certificate ([ha-integration.md](ha-integration.md),
  [ADR-0023](../docs/adr/0023-own-ha-integration.md)). The hub needs no path to HA.
- **Agent binaries:** the hub serves them over HTTPS with a one-time token (15 min). Their authenticity is secured by
  the operator signature, not by the hub.

## 4. Trust and keys

| Key | Lives | Used for | Whoever steals it can |
|---|---|---|---|
| Agent identity (Ed25519) | every host; Linux as a file with 0600, Windows protected by DPAPI | login at the hub | impersonate this host and forge its state |
| Hub key (Ed25519) | hub | signs every job; every agent has the public key fixed in its configuration | trigger allowed jobs within the local policies, with a reason also outside the window |
| Operator key (Ed25519) | at the operator, readable only by them; never on the hub. The replacement key is kept separately from it (10.4) | signs the manifest of the agent binaries | put code on all agents – hence never on the hub, plus a replacement key (10.4) |
| Code-signing certificate (Authenticode) | none ([ADR-0084](../docs/adr/0084-windows-self-update-without-code-signing.md)) | only needed if Smart App Control blocks the swap of the Windows binary | nothing beyond the manifest signature |

**Why jobs are signed although the tunnel is authenticated:** In the earlier agent, only the agent identifies itself.
Here jobs run as root, so the agent must know that a job comes from the hub, even if someone redirects DNS or the
network. A job carries `job_id`, `node`, `verb`, `params`, `issued_at`, `expires_at` (at most 5 min after
`issued_at`) and the signature. The agent discards jobs with a foreign `node`, expired jobs and `job_id`s it has
already seen (replay protection) and reports that as `rejected_invalid_job`
([ADR-0002](../docs/adr/0002-own-project-following-an-earlier-agent.md)).

TLS secures confidentiality. Trust in jobs and binaries rests on the signatures
([ADR-0032](../docs/adr/0032-self-update-signed-by-operator.md), [ADR-0035](../docs/adr/0035-backup-operator-key.md)).

## 5. Threat model

| Attacker has … | can | cannot |
|---|---|---|
| taken over a host | forge its state | influence other hosts, create jobs |
| taken over the hub | read the inventory; trigger allowed jobs, with a reason also outside the window (ADR-0087); forge the HA display | put commands or code on hosts, choose packages, widen the scope, get around the reboot ban or the minimum interval, swap agent binaries |
| an approved Home Assistant | trigger check and install, with a reason also outside the window, with role `admin` also reboots (12) – through the hub, so within the policies | more than the hub |
| the network (eavesdrop, redirect) | disturb connections | forge jobs (hub signature), slip in binaries (operator signature and SHA-256) |
| the operator key | put code on all agents | – hence never on the hub; rotation through a new public key in the agent configuration |

For comparison: with a central patch server with remote execution, one compromised server means root on all hosts.

Further brakes in the agent: at most one run at a time, minimum interval between two install runs
(`install.min_interval_h`), reboot only if needed, allowed and in the window (or with a reason, 6.4).

## 6. Verbs

The set of verbs is closed. What is not listed here does not exist
([ADR-0005](../docs/adr/0005-closed-verbs-without-shell.md)).

### 6.1 Jobs from the hub to the agent

| Verb | Parameters | Effect | Local condition |
|---|---|---|---|
| `check` | – | query the sources again (package lists, Windows Update search) | always allowed; at most every 5 min |
| `install` | `scope`: `security`, `all`, `app:<id>`, `pkg:<location>[/<name>]` (pip/npm, 11.4), `image:<project>/<service>` or `images` (container images, 11.5); `outside_window` (6.4) | install updates | scope is in `install.allowed_scopes` (for `pkg:` the location in `packages.update`; for `images` every service that an `image:` entry names), window open or a reason, preconditions met |
| `reboot` | `outside_window` (6.4) | reboot | `reboot.allowed`, window open or a reason, and a reboot is actually needed |
| `agent_update` | signed manifest, one-time token | agent swaps itself | `agent_update.enabled`; manifest signed by the operator |
| `cancel` | `job_id` | abort a run | only until the package installation begins; after that the package manager runs to the end, otherwise the system would stay half updated |
| `backup` | `guest`: `vm/<id>` or `ct/<id>` | only the hypervisor's agent: a backup of this guest to PBS, right before its install (10.2) | `guest_backup` in the `agent.yaml`; the guest lives on this host; at most one backup per guest in 10 min |

`app:<id>` points to an entry of the app list that the agent reports itself from its configuration. Package names
never come from the hub.

Deliberately absent: run commands, transfer files, change configuration, reinstall or remove packages, restart
arbitrary services.

### 6.2 Messages from the agent to the hub

| Message | Content |
|---|---|
| `hello` | name, public key, signature over the challenge (with context `vordr/hello/v1`, nonce, name and key), facts (OS and version, architecture, virtualization), agent version, verbs this version can do, summary of the local policy (from which the matching HA buttons arise) |
| `report` | state per source and app (7.2) with facts such as `apt_version`, `install_mode`, `unattended_upgrades`, reboot need with reason (7.4) |
| `job_status` | accepted or rejected with code, phase, progress, result (7.3) |
| `heartbeat` | alive, every 30 s. The hub answers with a heartbeat; 90 s of silence means on both sides: connection dead |
| `inventory` | packages of pip, pipx and npm per location (11.2), after every check; only to a hub whose `status` names the feature `inventory` |

The hub answers `hello` with `status` (`pending_approval`, `approved` or `rejected` with reason `bad_hello`,
`key_mismatch`, `too_many_pending`, `rejected`) and sends `status` again as soon as the state changes. It sends jobs
only to approved hosts and only with verbs that the agent named in the `hello`.

**Hub features** (since hub 0.18.0): Every `status` carries `features`, the message types this hub accepts beyond the
base protocol (`inventory`, `vet`, `containers`, `image_contents`; `ack` announces itself already in the challenge,
10.2). An older hub drops the connection on a message it does not know. The agent therefore sends a newer type only if
the hub has named it; it does not try it out and does not read it from a dropped connection. So the order of the
updates does not matter, yet the operating rule "hub before agent" stays.

### 6.3 Commands on the host itself

For operation on site. They only read and check; none of them installs anything:

```
vordr-agent check [--no-refresh] | identity | check-config | hub-check | version
vordr-agent packages [--plan <location>[/<name>] | --plan-deps <location>:<name>[,<name>…]]   # 11.2, 11.4
vordr-agent run                                                 # the service itself
vordr-agent install-service [--replace-config "<reason>"] | uninstall-service   # Windows only (11.1)
```

Install and reboot come only as a signed job from the hub: from HA, from the queue or from the automation (10.3),
outside the window with a reason (6.4). There is no command on the host for it.

### 6.4 Runs outside the window

[ADR-0087](../docs/adr/0087-runs-outside-the-window-with-a-reason.md): The window binds automatic runs and the reboot
button. If a human wants a run now, they give a reason. For installs the hub does that for them
([ADR-0109](../docs/adr/0109-the-install-button-installs-at-once.md)):

- **Install button:** An HA `command` with verb `install` (system packages, security updates, app, pip/npm location
  or package, refresh dependencies) without a reason gets `hub.ButtonReason` from the hub ("install started
  in Home Assistant outside the window") if the announced window (`policy.window`, `policy.tz`) is closed right now
  and the agent accepts reasons. The decision is made when it is sent; a command from the queue is therefore decided
  only when its turn comes. A reason of its own stays as it is. If the hub cannot read the window (none announced,
  unknown time zone), it passes nothing along; then the agent decides.
- **Before the backup:** An install job without a reason whose window is closed right now is rejected by the hub with
  `outside_window` before it sends it and before it has the guest backed up (10.2). This applies to every path
  (HA, queue) and affects only agents that do not accept reasons.
- **Job:** `install` and `reboot` then carry `outside_window`, the reason: not empty, at most 200 bytes UTF-8,
  no control characters (`protocol.ValidReason`). Any other verb with it is an invalid job. The hub signs the reason
  along.
- **Agent:** A job with a reason gets past `install.window` (also for `pkg:`) and `reboot.window`; the agent writes
  the reason to the log. Everything else still applies: allowed scope, `packages.update`, `reboot.allowed`, reboot
  only if needed, `install.min_interval_h`, one run at a time, the hub's backup check.
- **Older agents** read jobs strictly and would reject the unknown field as `rejected_invalid_job`. The agent
  therefore announces it in the hello (`policy.outside_window`); to any other the hub sends no reason and
  answers HA with `outside_window_unsupported`.
- **Hub:** An HA `command` may carry `reason`. The hub checks it (`bad_reason`; `bad_request` for `check` and
  `agent_update`), puts it into the run (`last_run.outside_window`) and into the event log (`command`, `job_sent`,
  `queued_job_started`). A command that waits behind a running run takes over the reason if it comes again with a
  reason. The roles stay: Reboot needs `admin`.
- **HA:** actions `vordr.install_outside_window` (update entities: system packages or a pip/npm location),
  `vordr.press_outside_window` (buttons: security updates, apps, Reboot), `vordr.update_package` and
  `vordr.refresh_dependencies` (11.4) with the field `reason`. They exist only where the hub reports
  `capabilities.outside_window`.

## 7. States

### 7.1 Host (from the hub's point of view)

`pending_approval`, `online`, `offline` (no heartbeat for 2 min), `rejected`.

### 7.2 Source or app

| State | Meaning |
|---|---|
| `up_to_date` | nothing pending |
| `updates_available` | updates pending; count, of which security updates |
| `check_failed` | query failed, with a code from 7.3 (e.g. `precheck_repo_unreachable`) |
| `unknown` | not queried yet or no reliable answer; HA entities stay unchanged |

### 7.3 Run

Phases during a run: `queued`, `prechecking`, `downloading`, `installing`, `verifying`, `rebooting`, plus
`backing_up`: an install waits for the backup of its guest (10.2). The hub sets this phase; the guest's agent does not
have the job yet.

Results (`problem` = sets `binary_sensor.<node>_problem`):

| Code | Meaning | Next step (text in HA) | `problem` |
|---|---|---|---|
| `ok` | everything in the chosen scope installed | – | no |
| `ok_reboot_required` | installed, reboot needed | reboot within the window or with the button | no |
| `rejected_invalid_job` | signature, host, expiry or replay is wrong | check hub and clock; may point to an attack | yes |
| `rejected_scope_not_allowed` | scope not allowed locally | the host's policy | no |
| `rejected_outside_window` | outside the window | wait or run outside the window with a reason (6.4) | no |
| `rejected_rate_limited` | minimum interval not reached | wait | no |
| `rejected_busy` | another run or a foreign package manager holds the lock (e.g. unattended-upgrades) | try again later; end the double operation | no |
| `rejected_not_allowed` | the local policy forbids the verb (e.g. `reboot` with `reboot.allowed: false`) | the host's policy | no |
| `rejected_unsupported` | this agent version or platform cannot do the verb; the hub should not have sent it | update the agent | yes |
| `precheck_disk_space` | too little space (package cache, `/boot`) | clean up | yes |
| `precheck_package_db_broken` | package database interrupted (`dpkg --audit` not empty) | `dpkg --configure -a` by hand | yes |
| `precheck_repo_unreachable` | package source not reachable or its signature invalid | check source and network | yes |
| `precheck_would_remove_packages` | the dry run would remove packages that no package of the run replaces | decide by hand | yes |
| `precheck_version_flagged` | pip/npm update (11.4): a version of the plan, a dependency too, is in the OSV data (malware or vulnerability) | look at the entry; wait for a fixed version | yes |
| `precheck_version_too_new` | pip/npm update: a version of the plan is younger than `packages.min_age_days` | wait until it is old enough | no |
| `precheck_vetting_unavailable` | pip/npm update: the hub could not vet the plan (no or stale OSV data, no answer) | check the hub's vulnerability data | yes |
| `precheck_not_from_registry` | npm update: an entry of the plan does not come from the registry (other host, git, file; [ADR-0128](../docs/adr/0128-check-npm-tarball-origins-and-let-npm-12-fetch-them.md)); nothing was installed | see where the dependency points; update such a location by hand | yes |
| `failed_package_manager` | package manager ended with an error; exit code and result of the verification as attributes | read the excerpt | yes |
| `failed_verification` | the run reports success, but the verification still finds pending updates or an interrupted package database | read the excerpt | yes |
| `failed_health` | the run went through, but a check from `health:` in the `agent.yaml` (service, container, HTTP) still fails after 3 minutes | look at the named service | yes |
| `failed_windows_update` | Windows Update reports an error; `hresult` as a number | look up the HRESULT | yes |
| `failed_target_auth` | proxy agent: the device rejects the service account ([proxy-agent.md](proxy-agent.md)) | check the service account on the device and the instance's credentials file | yes |
| `failed_target_tls` | proxy agent: the device's certificate does not match the pinned fingerprint or name | check the certificate; if it is new on purpose → adjust the fingerprint, otherwise someone may be in the middle | yes |
| `failed_target_hostkey` | proxy agent over SSH: the device does not offer the pinned host key ([ADR-0094](../docs/adr/0094-ssh-drivers-use-the-system-openssh-client.md)) | check the host key (`ssh-keyscan`); if it is new on purpose → adjust `target.host_key`, otherwise someone may be in the middle | yes |
| `failed_config_backup` | proxy agent: configuration backup failed or unusable ([ADR-0090](../docs/adr/0090-appliance-installs-need-a-fresh-config-backup.md)) | look at the instance's log; without a fresh backup no firmware update | yes |
| `failed_backup` | the backup right before the install did not come about (vzdump failed, PBS does not have it, the hypervisor is gone); nothing was installed ([ADR-0096](../docs/adr/0096-fresh-backup-right-before-the-install.md)). The hypervisor's agent also ends a failed `backup` job with it | look at the run's detail and the hypervisor's backup log | yes |
| `timeout` | run took longer than `install.timeout_min` | check the host | yes |
| `aborted` | aborted by `cancel`, before the installation | – | no |
| `interrupted` | agent or host ended during the run; detected from the run journal at the next start. At the hub also provisionally after a tunnel drop, until the next hello says whether the run goes on (10.2, ADR-0114) | check the package database | yes |

Rejected jobs without `problem` are expected answers and appear only in the sensor "Last run". If they repeat until
updates stay pending, `stale` (7.5) takes effect.

**A failed install stays** ([ADR-0124](../docs/adr/0124-a-failed-system-install-stays-until-one-goes-through.md)):
If an install of the system packages (scope `security` or `all`) ends with a code with `problem`, the hub remembers it
next to the last run. It remains the reason for the problem, even if afterwards a `check`, an image, pip/npm or agent
update runs, a rejection comes or a run is still under way. Only an install of the system packages that goes through
(`ok`, `ok_reboot_required`), with at least its scope (`all` covers `security`), lifts it. With a canary it thus also
holds back the later rings and the distribution of new agents (10.3, 10.4). The view carries the remembered run next to
the last one (`failed_install`, with excerpt and run log), and HA shows it on the problem sensor.

**Classification without error texts:** The agent evaluates only exit codes and structured output (lines of the dry
run, `APT::Status-Fd`, result codes of Windows Update). After a failure it asks for the state in a targeted way (lock,
`dpkg --audit`, free space, sources). The code arises from these checks; the error text goes along only as
`log_excerpt` ([ADR-0007](../docs/adr/0007-fixed-codes-instead-of-error-texts.md)).

### 7.4 Reboot

`reboot_required` (yes/no) with `reboot_reason` = `kernel`, `libraries`, `windows_update` or `other`, plus the
triggering packages as a list. Detection:

- running kernel against the newest installed kernel of the same flavor (`kernel`). The flavor is the last part after
  the version (`pve`, `amd64`, `generic`, `2712`). Raspberry Pi OS installs the kernels for Pi 5 (`2712`) and Pi 4
  (`v8`) side by side, and dpkg considers `v8` newer. Without the comparison per flavor, a Pi 5 would demand a reboot
  all the time, and every reboot would end with `failed_verification`,
- `/var/run/reboot-required` and `.pkgs`, where present (Ubuntu),
- `needrestart -b`, where installed (machine-readable output, `libraries`),
- on Windows the registry keys of Windows Update and Component Based Servicing (`windows_update`, 11.1).

### 7.5 Stuck

The hub sets `stale` (with `problem`) if updates have been pending longer than `stale_after_days` (default 7) without
a run installing them, whatever the reason. That also catches cases for which there is no code of their own
([ADR-0014](../docs/adr/0014-stale-after-seven-days.md)).

### 7.6 Self-update of the agent

Phases `downloading`, `verifying`, `rebooting`, then a result from 7.3
([ADR-0032](../docs/adr/0032-self-update-signed-by-operator.md)):

| Code | when |
|---|---|
| `ok` | the new version reports on its first approved connection; or it already runs |
| `rejected_invalid_job` | manifest or signature invalid, or the release is not newer (`older_release`) |
| `rejected_unsupported` | self-update is off on the host (e.g. without `agent_update.enabled`), or the release has no binary for the host's OS and architecture |
| `rejected_busy` | another run is open |
| `precheck_repo_unreachable` | the download fails |
| `failed_verification` | hash or size wrong; the dry run of the new binary fails; or the new version reaches no approved connection within 10 min (if it hangs, the update guard takes effect after 15 min, [ADR-0098](../docs/adr/0098-update-guard-rolls-back-a-hung-new-agent.md)), and the agent has jumped back to the old one |
| `failed_package_manager` | writing, marking, starting the update guard or swapping the binary fails |
| `timeout` | after the phase `rebooting` no agent reports within 30 min |

Except `ok` and `rejected_busy`, all set `problem`. Deadlines: one-time token for the download 15 min, confirmation by
the new version 10 min, the hub's wait 30 min (10.4). On Windows the same applies (10.4,
[ADR-0084](../docs/adr/0084-windows-self-update-without-code-signing.md)).

## 8. Home Assistant entities

The entities are created by the integration `custom_components/vordr`, which connects to the hub as an approved client
over `/v1/ha` ([ha-integration.md](ha-integration.md), sections 6 and 6a). Rationale:
[ADR-0023](../docs/adr/0023-own-ha-integration.md) (integration instead of MQTT),
[ADR-0017](../docs/adr/0017-entity-ids-with-prefix.md) (entity IDs),
[ADR-0021](../docs/adr/0021-device-name-vordr-node.md) (device name).

The integration sets entity IDs explicitly (English, with prefix `vordr_`, so they do not collide with entities of
other integrations with the same host name); display names come from the translations and follow the system language
of HA. `<id>` is the host name with `_` instead of `-` and `.`. The hub pushes the state as `HostView`. What it does
not know is `null` there, and HA shows "unknown" – never a guess. Without a connection to the hub, all entities are
unavailable.

### 8.1 Device per host

Name "Vördr <node>", identifier `("vordr", "host:<node>")`, manufacturer "Vördr", model from the facts (e.g.
"Debian 13 · lxc"), `sw_version` = agent version, under the hub device.

| Entity | Type | Display name | Content | Exists only if |
|---|---|---|---|---|
| `update.vordr_<id>_system` | update | System packages | see 8.3 | the last report names a package source |
| `update.vordr_<id>_<app>` | update | name of the app | real versions (installed, available) | not built yet; until then one button per app (below) |
| `update.vordr_<id>_agent` | update | Agent | agent version against approved version; Install = `agent_update` (10.4) | the hub distributes agent releases |
| `update.vordr_<id>_containers` | update | Container images | outdated and vulnerable images, rebuilds by button (11.5) | `packages.containers` on the host |
| `update.vordr_<id>_packages_<location>` | update | Packages <location> | pip/npm packages of a location; Install = all candidates, service `vordr.update_package` = one, `vordr.refresh_dependencies` = dependencies with a finding (11.4) | the location is in `packages.update` and the hub has OSV data |
| `sensor.vordr_<id>_last_run` | sensor (enum) | Last run | phase or result code (7.3) of the last job; attributes `job_id`, `verb`, `scope`, `started`, `finished`, `reason`, `log_excerpt` | always |
| `sensor.vordr_<id>_pending_updates` | sensor | Pending updates | count; unknown if the check failed | the last report names a package source |
| `sensor.vordr_<id>_security_updates` | sensor | Security updates | count | as above |
| `sensor.vordr_<id>_last_report` | sensor (timestamp) | Last report | time of the last `report` | always |
| `binary_sensor.vordr_<id>_reboot_required` | binary_sensor | Reboot required | on/off; **unknown** if no signal answered (`checked` empty, e.g. container); attributes `reason`, `packages`, `checked` | always |
| `binary_sensor.vordr_<id>_problem` | binary_sensor (problem) | Update problem | on for `offline` (after `offline_after_min`, default 10; not while the host has announced that it sleeps or shuts down, see 11.1), codes with `problem` (7.3), `check_failed`, `stale`; attributes `reason`, `reasons`, `next_step`, `check_failed_code` – codes that HA translates (design/ha-integration.md, 6b) –, plus `failed_install` (job, scope, code, times, excerpt, run log) as long as a failed install of the system packages is remembered (ADR-0124), not in the recorder | always |
| `binary_sensor.vordr_<id>_online` | binary_sensor (connectivity) | Connected | tunnel is up (immediately, without grace period) | always |
| `sensor.vordr_<id>_malicious_packages` | sensor | Malicious packages | installed pip/npm packages that OSV lists as malware; attribute `findings` (11.2) | the agent sends a package inventory |
| `sensor.vordr_<id>_vulnerable_packages` | sensor | Vulnerable packages | packages with a known vulnerability; attributes `findings` (per finding also `reach` and `held_by`, ADR-0129), `waiting`, `locations`, `packages_total`. The lists (`findings`, `locations`, `undecided`, for the outdated packages `updates`) are only in the state, not in the history (ADR-0101) | as above |
| `sensor.vordr_<id>_outdated_packages` | sensor | Outdated packages | direct pip/npm packages with a newer release; attribute `updates` with `latest` and `candidate` (11.3) | as above |
| `button.vordr_<id>_check` | button | Check now | command `check` | the agent can do `check` |
| `button.vordr_<id>_install_security` | button | Install security updates | command `install_security` | the agent can do `install`, `security` is allowed and not already the default scope |
| `button.vordr_<id>_reboot` | button (restart) | Restart | command `reboot` | the agent can do `reboot` and `reboot.allowed` |
| `button.vordr_<id>_install_app_<app>` | button | name of the app | command `install_app:<app>` | the app is in `apps` and `app:<app>` is allowed |

Buttons exist only for verbs that the agent announced in the hello **and** that its policy allows. A policy alone
offers nothing that the agent version cannot do. If a host has no report yet, the package entities are neither
created nor deleted (undecided); only a report decides.

### 8.2 Hub device

| Entity | Type | Display name | Content |
|---|---|---|---|
| `binary_sensor.vordr_hub_online` | binary_sensor (connectivity) | Hub | last will of the hub |
| `sensor.vordr_hub_hosts_online` | sensor | Hosts connected | count |
| `sensor.vordr_hub_hosts_with_problem` | sensor | Hosts with problem | count; attribute with the list |
| `sensor.vordr_hub_pending_approvals` | sensor | Pending approvals | count; attribute with names and fingerprints |
| `sensor.vordr_hub_vulnerability_data` | sensor (timestamp) | Vulnerability data | when the hub last fetched its OSV data (the oldest ecosystem); per ecosystem state `ok`, `stale`, `missing` and error code (11.2) |

### 8.3 The entity "System packages"

An `update` entity expects two versions; for a set of packages there is no single one. Therefore:

- `installed_version`: the operating system, e.g. `Debian GNU/Linux 13 (trixie)`.
- `latest_version`: equal to `installed_version` if nothing is pending or the Install button would take none of it,
  otherwise e.g. `12 pending (3 security)`; `null` (unknown) if the check failed. "Skip" in HA applies only until the
  set changes. Verified: HA compares for equality first; AwesomeVersion cannot compare two different free texts, and
  then the entity counts as `on`.
- **`on` only if Vördr would install something** ([ADR-0106](../docs/adr/0106-update-entity-on-only-when-vordr-would-install.md),
  since hub 0.22.0 and integration 0.20.0):
  - The hub reports `installable` per host: that many pending updates the Install button would take under the
    host's policy. With default scope `all` all of them, with `security` or only the button for security updates the
    security updates, with neither none.
  - If it is 0, the entity is `off`. A host whose policy allows only `security` then shows its other updates only in
    the sensors and the summary.
  - If they pile up, `stale` (from `pending_since`, all pending updates) still reports the problem after
    `stale_after_days`.
  - An older hub does not send the field; then the old rule applies.
- `release_summary`: the first packages with old → new version (HA allows at most 255 characters).
- `in_progress` and `update_percentage` during a run, progress from `APT::Status-Fd`.
- Install button: job `install` with the host's `install.default_scope`.

### 8.4 Commands and thresholds

Commands come as `command` over `/v1/ha` ([ha-integration.md](ha-integration.md), section 5): `check`, `install`
(default scope), `install_security`, `install_app:<id>`, `reboot`, `agent_update`, plus the commands for pip/npm
locations and container images (11.4, 11.5). The hub checks each against the verbs and the reported policy of the host
and against the client's role (12) before it signs a job; the agent checks it itself anyway.

Taken over from the earlier agent: entities appear and disappear only on a complete view, and "not yet decided" is
something other than "should not exist" (tri-state, ha-integration.md 6a).

`hub.yaml` for this: `stale_after_days` (default 7), `offline_after_min` (default 10), `reboot_pending_after_days`
(default 7), `no_report_after_hours` (default 26). `check-config` rejects an `mqtt` section: MQTT has been gone since
hub 0.6 ([ADR-0026](../docs/adr/0026-remove-mqtt.md)).

### 8.5 On the HA side

- All `update` entities appear under *Settings → Updates* without further action.
- One repair issue per host problem and the event `vordr_problem` for automations (ha-integration.md 6a).
- Not built yet: a blueprint that turns `binary_sensor.*_problem` into a notification or a task entry with
  `next_step` and checks all other `update` entities (from integrations) for "stuck for N days". 7.5 would then also
  apply to devices without an agent.

## 9. Local policy

Example `/etc/vordr/agent.yaml` (Windows: `%ProgramData%\vordr\agent.yaml`):

```yaml
node: example-host                  # default: host name
hub:
  url: wss://vordr-hub.example/v1/tunnel
  public_key: "<base64>"            # hub key; jobs without a valid signature are rejected
  ca_file: /etc/vordr/hub-ca.pem    # optional: trust only these CAs instead of the system roots (internal PKI)
install:
  allowed_scopes: [security, all]   # app:<id> is allowed if the app is listed under apps;
                                    # image:<project>/<service> ("*" allows) for Compose services (11.5, ADR-0119)
  default_scope: security           # Install button of the entity "System packages"
  auto: hub                         # off | hub (see 10.3; local not built yet)
  window: {days: [mon, tue, wed, thu, fri, sat, sun], start: "02:00", end: "05:00"}
  min_interval_h: 6
  timeout_min: 60
reboot:
  allowed: false
  window: {days: [sun], start: "03:00", end: "04:00"}
agent_update:
  enabled: true
  public_key: "<base64>"            # operator key
apps:
  - id: gitlab
    name: GitLab
    source: apt-package
    package: gitlab-ce
guest_backup:                       # only on the hypervisor (10.2, ADR-0096)
  storage: pbs                      # a Proxmox Backup Server storage of this host (/etc/pve/storage.cfg)
  timeout_min: 60                   # per backup; 5 to 240
```

The windows apply in the host's local time. If a key is missing, the stricter value applies: no reboot, no automatic
install, only `check`. `vordr-agent check-config` checks the file before the service takes it over
([ADR-0006](../docs/adr/0006-local-policy-decides.md)).

## 10. Flows

### 10.1 Enrollment and approval

1. On first start the agent creates its identity, connects, signs the challenge and sends `hello`.
2. The hub registers the host as `pending_approval`; HA shows it under "Pending approvals" with its fingerprint.
3. Approval happens only at the hub itself: `vordr-hub approve <node> --fingerprint <fp>`, after comparing with
   `vordr-agent identity` on the host. HA cannot admit hosts
   ([ADR-0010](../docs/adr/0010-administration-only-local.md)).
4. Only then do the HA entities appear, and the hub accepts reports.

### 10.2 Install run (apt)

1. Check the job: signature, host, expiry, replay. Otherwise `rejected_invalid_job`.
2. Check the policy: scope, window, minimum interval. Otherwise `rejected_*`.
3. Take the agent's own lock; if the package manager's lock is held: `rejected_busy`.
4. Preconditions: free space, `dpkg --audit` empty, `apt-get update --error-on=any` successful.
5. Dry run (`apt-get -s`), from it the plan. If it would remove packages: `precheck_would_remove_packages`.
   Excepted are packages that a package of the same run explicitly replaces. For this the new package declares
   `Replaces` and in addition `Breaks` or `Conflicts` against the old one (Debian Policy 7.6.2). This is how distributions
   rename or split packages. Example: in 2026 Raspberry Pi OS replaced `pcmanfm` with `pcmanfm-pi`; without this exception
   hundreds of updates would have stayed pending on a Pi ([ADR-0042](../docs/adr/0042-replaced-packages-are-no-removal.md)).
   Also excepted is an old library the run leaves behind when a library moves to a new soname: it was installed
   automatically, a package of the run comes from the same source package, and every installed package that depends
   on it moves to a version without it in the same run
   ([ADR-0146](../docs/adr/0146-an-old-library-the-run-supersedes-is-no-removal.md)).
   - The run lists such replacements in its summary (`replaced`).
   - If `apt-cache`, `apt-mark` or `dpkg-query` cannot say anything about it, every removal counts as a real one.
   - For scope `all`, `full-upgrade`, unless `install.mode` says `upgrade`
     ([ADR-0076](../docs/adr/0076-full-upgrade-by-default.md)).
   - For `scope=security`, only packages whose candidate comes from a security source
     (`apt-get install --only-upgrade` with the names from the agent's own dry run). Their plan may change only what comes
     from a security archive or from the host's own release, like unattended-upgrades
     ([ADR-0116](../docs/adr/0116-security-runs-stay-within-security-archives-and-the-release.md)).
     - The host's own release: label `Debian` or `Ubuntu`, suite without a hyphen, version equal to `VERSION_ID` from
       `/etc/os-release` or an intermediate version of it (`12.15` on Debian 12). If the release is unknown, only the
       security archives count.
     - Not from `-updates`, `-backports`, `-proposed`, another release or a vendor repo, even if its suite is called
       `stable`.
     - All together first. If that plan reaches further, one by one in the order of the dry run: an update stays if the
       plan with the ones kept so far and this one stays within the limits.
     - Held-back updates stay pending. The result is `ok`, and `detail` names them with what they would need.
       If the run holds back everything, it downloads nothing and writes no journal. If time runs out during selection,
       it ends with `timeout`.
6. Download (`apt-get -d`), then write the run journal (`/var/lib/vordr/run.json`) before any package is touched.
   The download only fills the package cache. If it fails (mirror unreachable, apt busy), the run has changed nothing,
   writes no journal and does not count toward `install.min_interval_h`. A mirror outage thus does not cost the whole
   window. Under Windows Update, download and installation run in one script; there the journal still comes first.
7. Execute with `DEBIAN_FRONTEND=noninteractive` and `--force-confold`, progress through `APT::Status-Fd`.
8. Verify: pending updates in scope, `dpkg --audit`, need for a reboot.
9. Report the result code, close the journal.

**Backup check before the run (hub, optional):** If `hub.yaml` has a `backup` section, the hub asks the backup server
for the latest backup of the host's group before every `install` (first implementation: Proxmox Backup Server, PBS; its
own read token, only `DatastoreAudit`, certificate pinned by fingerprint). If the backup is older than `max_age_hours`
(default 26), is missing, PBS does not answer (a query younger than 30 min still counts) or `hub.yaml` names no group, the hub
refuses with `backup_stale`, `backup_missing`, `backup_unreachable` or `backup_unmapped` respectively, before any job
goes out. Hosts without a backup are listed explicitly with a reason (`without`). Check instead of schedule: a failed or
late backup halts the installation. HA shows the sensor "Last backup" per host and the refusal as an error with the age
and the limit ([ADR-0030](../docs/adr/0030-backup-required.md)).

**Configuration backup instead of PBS (appliances):** An entry `config_backup: true` instead of `group` or `without`
applies to an appliance behind a proxy agent. The hub then takes the age from the fact `config_backup_at`, which the
agent reports in its latest report under the source `config-backup`. It does not ask PBS for this. The same limit and the
same codes `backup_stale` and `backup_missing` apply. The sensor "Last backup" shows this backup
([ADR-0090](../docs/adr/0090-appliance-installs-need-a-fresh-config-backup.md),
[Proxy agent 4.4](proxy-agent.md#44-configuration-backup-adr-0090)).

**Backup right before the install (hub and the hypervisor's agent; first implementation: Proxmox VE with PBS):** With
`backup.before_install.hypervisor: <node>`, every guest with a group `vm/<id>` or `ct/<id>` gets a fresh backup before
every install; groups `host/` and `without` stay with the age check
([ADR-0096](../docs/adr/0096-fresh-backup-right-before-the-install.md)).

- **Before the run:** The age of the latest backup no longer holds such a guest back. But the hub refuses if PBS does not
  answer (`backup_unreachable`) or the hypervisor's agent is absent, not approved or does not offer `backup`
  (`backup_host_unavailable`).
- **Run:** The install run begins in the phase `backing_up`, with its final `job_id`. The hub sends the hypervisor the
  signed job `backup` with the guest and waits for the result. Then it asks PBS with the read token whether the group has
  a backup that is not older than the moment of the job (1 min clock skew). Only then does the install go out with the
  same `job_id`.
- **Deciding again:** Waiting for the backup can take a while: other guests are backed up first, the backup itself takes
  minutes. The hub therefore re-checks twice what it checked at the start (window per
  [ADR-0109](../docs/adr/0109-the-install-button-installs-at-once.md), minimum interval per
  [ADR-0110](../docs/adr/0110-the-hub-checks-the-minimum-interval-before-the-backup.md)): when the backup's turn comes,
  and before the install goes out.
  - An install from HA without a reason then gets the button's reason if the window has closed in the meantime.
  - Every other install without a reason ends with `rejected_outside_window` without being sent. So does one that the
    minimum interval holds back (`rejected_rate_limited`).
  - If the window closes before the backup, nothing is backed up.
- **Reuse:** If the guest's latest backup is younger than 15 min (the scheduled job just ran, the same install is
  repeated), the hub takes that one.
- **One at a time:** Only one backup runs at a time. The agent executes one job at a time, and vzdump has a lock per
  host. If the hypervisor has a run of its own, the backup waits. It also waits while the hypervisor connected and has
  not reported yet: its agent runs the check after its start and holds its lock (at most 10 min). A `rejected_busy`
  (a check of its own, or its inventory) is asked again with pauses growing from 5 s to 30 s, for up to 2 min
  ([ADR-0147](../docs/adr/0147-a-backup-waits-for-a-hypervisor-busy-with-a-check-of-its-own.md)). Commands to the
  hypervisor wait while it is backing up (queue, 10.3).
- **No run of the hypervisor:** Its "Last run" in HA stays its own. The result is in the guest's run and in the event log
  (`install_awaits_backup`, `job_sent` with `guest`, `for`, `for_job`, `pre_install_backup`).
- **If it fails** (vzdump, PBS does not have the backup, hypervisor gone, more than 5 h of waiting), the run ends with
  `failed_backup` and the reason in the detail. Nothing was installed.
- **The hypervisor's agent** (`guest_backup` in `agent.yaml`, chapter 9):
  - It offers `backup` only under Linux as root, with `vzdump` and a `pbs:` storage in `/etc/pve/storage.cfg`.
    `check-config` checks the storage.
  - It backs up only guests of this host (configuration file under `/etc/pve/lxc` or `/etc/pve/qemu-server`) and each at
    most once in 10 min (`rejected_rate_limited`).
  - Call: `vzdump <id> --storage <storage> --mode snapshot --remove 0 --lockwait <timeout_min/2>
    --notes-template "vordr: before install, job <job_id>"`. For storages without snapshots, snapshot mode falls back to
    suspend by itself, never to stop. `--remove 0`: Vördr never deletes a backup; retention is handled by the prune jobs
    of PBS.
  - **Umask 022** like the Proxmox scheduler: The agent starts `vzdump` through a fixed `sh` line
    (`umask 022 && exec "$0" "$@"`; the arguments never become part of the script). The agent's unit has `UMask=0077`,
    and `vzdump` creates its temp directory and in it the `pct.conf` with the inherited umask. For unprivileged
    containers, `proxmox-backup-client` reads it as mapped root (uid 100000). Under 0077 this fails with
    "Permission denied".
  - It holds its lock like an install. A backup does not abort when the agent stops; the log is at
    `<state_dir>/runs/<job>.log`, and a failed backup names this path in the detail.
- **Hub restart:** A run that is still in `backing_up` at the restart ends with `interrupted`. The job never went out.

`vordr-hub backup <node>` shows the decision with "a fresh backup by <hypervisor> before the install". Synology
Active Backup for Business was examined for this and rejected
([ADR-0097](../docs/adr/0097-synology-active-backup-for-business-for-backups-before-installs.md)).

**Rules for the run:**

- **Window:** If `install.window` is set, it also applies to the button in HA. Without a window the button is allowed at
  any time; automatic installs (`auto: hub`) work only with a window. Outside the window only with a reason (6.4).
- **Progress:** Download first, separately (`apt-get -d`), then the installation. Percent of the whole run:
  precheck up to 5, download up to 35 (`dlstatus`), installation up to 95 (`pmstatus`, with package), verification up
  to 100. At most one message per second and phase; the hub puts phase, percent and package into the latest run, HA
  shows them in `update.*_system` (`update_percentage`) and on the sensor "Last run" (`progress`, `item`).
- **Deadlines:** `install.timeout_min` applies until the installation begins. dpkg is never stopped in the middle; the
  installation itself ends at the latest after twice the deadline with `timeout`.
- **Tunnel and reboot:** A run does not belong to the connection that brought it. Progress and result go over the
  current connection, otherwise from the journal at the next one. A result that arrives after a tunnel drop replaces the
  provisional `interrupted` at the hub. Stopping the service waits for the run (`KillMode=mixed`,
  `TimeoutStopSec=30min`; [ADR-0029](../docs/adr/0029-runs-survive-disconnects.md)).
- **Handshake on runs** (hub 0.26.0, agent 0.15.0;
  [ADR-0114](../docs/adr/0114-agent-names-its-run-and-the-hub-acknowledges-results.md)): When the tunnel drops, the hub
  has to guess what became of the run. Since the handshake, the agent says so at the next hello.
  - The hub announces `ack` already in the challenge (`features`), because the hello comes before the status and an
    older hub rejects a hello with an unknown field. Only then does the agent send `runs`.
  - `runs.running`: the jobs the agent is executing right now (from the checked job to the sent result; for a reboot of
    the host, until it actually shuts down). `runs.unacknowledged`: results it holds and sends on this connection: the
    journal until the hub has acknowledged it, a self-update and a reboot after the start.
  - The hub reconciles its latest run with this before a job can reach the host: If the agent names it as running, it is
    running again (event `run_continues`), and queue, distribution and automation wait. If the agent does not name it at
    all, the run is gone: `interrupted` with "the agent reported no such run…" (`run_lost`), and the host is free
    immediately instead of only after the job's deadline. An install that is still waiting for its guest's backup
    (`backing_up`) stays as it is: it has not gone out yet.
  - Every job named as running keeps the host busy until its result arrives, even one that is not a run of the host
    itself (a guest's backup on the hypervisor).
  - A final status without a journal that no connection takes stays in the agent's memory, appears in the next hello
    and goes out on the next approved connection.
  - If the old connection ends at the hub only after the new hello, the job keeps waiting (`job_continues`) instead of
    being recorded as `interrupted`.
  - The hub stores every final status and then acknowledges it with `ack` (job ID). Only with that does the result in
    the journal count as reported (`acknowledged`); otherwise the agent sends it again on every new connection.
    A second copy changes nothing (`job_status_again`) and is acknowledged again. The same holds for the reboot marker.
  - The self-update keeps its rule: The marker disappears as soon as the result has been sent on the first approved
    connection; the disappearance is the confirmation for the guard (ADR-0098).
  - Older agents send no `runs` and get no `ack`; with an older hub a sent result counts as reported as before. A journal
    from before (`reported`) counts as not acknowledged and goes out once more.
- **What the run did:** The result of a run that touched packages carries a summary: package count and names (at most
  100), duration, path of the run log on the host (`<state_dir>/runs/<job>.log`, the full output of apt, the last 10
  runs) and the services that, according to `needrestart -b`, still use old libraries. HA shows it on the sensor
  "Last run" and in the event `vordr_run_finished` (`summary`). Phases, package count and duration are also in the agent's
  journal.
- **Scope `all`:** `install.mode` (`upgrade` or `full-upgrade`) overrides the default `full-upgrade`.
  With `upgrade`, packages that need new dependencies stay behind (under Ubuntu typically kernel and
  `linux-firmware`); a run that would remove packages is refused in both cases
  ([ADR-0031](../docs/adr/0031-install-mode-full-upgrade.md), superseded by
  [ADR-0076](../docs/adr/0076-full-upgrade-by-default.md)). Docker packages are not held back
  ([ADR-0077](../docs/adr/0077-hold-docker-packages.md)).
  - With `upgrade`, the check additionally simulates `apt-get -s upgrade` and marks what only `full-upgrade` would take
    as `kept_back` (per package and counted per source). It stays pending but does not count as installable for scope
    `all`: The hub subtracts it from `installable` and reports `kept_back`, and the automation with `auto_scope: all`
    starts no run for it. Scope `security` installs by name and is not affected. If the second simulation fails, nothing
    is marked ([ADR-0127](../docs/adr/0127-count-only-what-the-upgrade-mode-installs.md)).
- After a successful run the agent empties the package cache (`apt-get clean`).
- **Check after the update** (`health:` in `agent.yaml`;
  [ADR-0038](../docs/adr/0038-health-checks-after-install.md),
  [ADR-0061](../docs/adr/0061-restart-containers-after-install.md)):
  - One kind per entry: `systemd` (unit active), `docker` (container is running, and healthy where it has a
    healthcheck) or `http` (GET with 2xx/3xx within 10 s; the certificate is not checked, local services).
  - After a run that changed packages and ends with `ok`/`ok_reboot_required`, the agent repeats the checks every 15 s for
    up to 3 minutes; an upgrade restarts services.
  - If one still fails after that, the run ends with `failed_health`, with the failed check in the excerpt.
  - Names are only names (pattern without options and shell); they become command arguments.
  - Containers: Before an install of the system packages, the agent notes the running containers with a restart policy
    (`always`, `unless-stopped`) and afterwards starts those that are not running, before the checks (ADR-0061). The list
    is also in the run journal: If the run is aborted (agent or host stopped), the next start of the agent brings these
    containers back before it reports; the result `interrupted` names them (`restarted`) and those that do not start
    ([ADR-0125](../docs/adr/0125-bring-back-containers-after-an-interrupted-run.md)).
- **Reboot** (verb `reboot`; [ADR-0041](../docs/adr/0041-reboot-with-window-and-proof.md)):
  - Only with `reboot.allowed` and within `reboot.window` (outside only with a reason, 6.4), only if the host really
    needs it (the same signals as the report: `reboot-required`, running versus newest kernel), and only if nothing else
    is running.
  - At most one per hour, against reboot loops.
  - Before the reboot the agent writes `reboot.json` with the boot ID (`/proc/sys/kernel/random/boot_id`) and reports the
    phase `rebooting`. The hub therefore waits for it instead of reporting `interrupted`.
  - After the start, the agent proves the reboot with a new boot ID and checks that none is needed any more. Only then
    does it report `ok`; otherwise `failed_verification`, with the reason.
  - HA offers the button only for hosts that allow it. The client's role must be `admin` (12).
  - A reboot does nothing for containers (LXC). They share the host's kernel, and the agent usually sees no need there.
- **Reboot hints** follow the host's capability: If it does not allow a reboot through Vördr, the next step for
  `ok_reboot_required` and `reboot_pending` is "reboot manually". HA shows the running and newest kernel on the sensor
  "Reboot needed".
- **Minimum interval** `install.min_interval_h` counts from the journal, so only runs that touched packages. It holds
  back runs of the system packages and of apps, not pip/npm runs and the refresh of dependencies (which do count,
  though). The hub knows it too
  ([ADR-0110](../docs/adr/0110-the-hub-checks-the-minimum-interval-before-the-backup.md)):
  - The agent reports `policy.min_interval_h` in the hello, plus `last_install` (start of the run in the journal) and,
    with the result of every run that wrote to the journal, `install_started`. The hub keeps this as `last_install` in the
    registry.
  - If `last_install` plus the minimum interval is still ahead of us, the hub refuses such an install with
    `rate_limited`: before it sends it and before it has the guest backed up, on every path. The automation then does not
    yet count the host as due; its attempt for the window stays.
  - `HostView.next_install` says from when it works again. HA names the time in the refusal.
  - Older agents report no minimum interval; then only the agent checks. The hub reads the hello strictly, which is why
    it comes before the agents.
- **Disk load:** The unit gives the agent the IO class `idle`, good for checks. Package runs run with `ionice -c2 -n7`,
  otherwise dpkg would hardly make progress on a host with constant disk load.
- After the run the agent immediately sends a new report, so that the counters in HA get ahead of the result.
- `install-agent.sh --replace-config "<reason>"` replaces a changed `agent.yaml` (backup, diff, reason into the system
  log), as with the hub. Under Windows the same applies to `install-service` (11.1).

### 10.3 Install automatically

`install.auto` determines who triggers a run ([ADR-0036](../docs/adr/0036-automatic-install-in-rings.md)):

- `hub`: The hub plans runs in rings. Ring `canary` comes first, the other rings only when the canary has run
  `canary_clean_h` hours without a problem. If the hub is gone, nothing is installed; HA then sees the hub offline.
  Under Windows the policy refuses `hub`: there only by button
  ([ADR-0086](../docs/adr/0086-windows-update-source.md)).
- `local`: The agent triggers by itself within the window, even without a hub (for hosts that should patch
  independently). It reports results afterwards as soon as the hub is reachable. **Not built yet:** Until it exists, the
  policy refuses the value (`check-config`, agent start) instead of silently accepting it
  ([ADR-0074](../docs/adr/0074-refuse-auto-local-until-built.md)).
- `off`: only on a button press.

If `unattended-upgrades` also runs on the host, the agent reports that as a fact. Both compete for the lock
(`rejected_busy`). Vördr recommends turning off unattended-upgrades on every host with `auto: hub`
([ADR-0080](../docs/adr/0080-security-updates-everywhere-unattended-upgrades-off.md)) but switches nothing itself.
Update the hypervisor only by button and never reboot it through Vördr
([ADR-0088](../docs/adr/0088-hypervisor-updates-by-button-only.md)). On Linux guests whose policy allows `all`, the
automation takes everything instead of only security updates
([ADR-0108](../docs/adr/0108-automatic-installs-take-every-update-on-linux-guests.md)). Only what comes from a
`-security` archive counts as a security update, vendor repos never. This is why a host that allows only `security`
stays with `security`.

**Rules for `auto: hub`:**

- **Both sides must want it:** The agent says `install.auto: hub` (with window and `default_scope`), the hub has an
  `auto_install` section. If either is missing, the hub starts nothing by itself.
- **Scope of the automation** ([ADR-0059](../docs/adr/0059-own-auto-scope.md)):
  `install.auto_scope` (`security` or `all`), `default_scope` if not given.
  This way a host installs only security updates by itself, while the button in HA still installs everything. Agents
  before 0.8.2 do not report the field; for them `default_scope` applies as before.
- **Window and time zone come from the agent:** It reports its `install.window` and its time zone in the hello, as an
  IANA name: under Linux from `/etc/timezone` or the link `/etc/localtime`, under Windows from the Windows zone ID in
  the registry through the CLDR table ([ADR-0112](../docs/adr/0112-windows-time-zone-through-the-cldr-table.md)). The
  hub plans with this instead of asking and collecting `rejected_outside_window`. There stays a single source, and the
  agent still checks the window itself. Agents before 0.7.4 report no window and therefore do not take part (event
  `auto_install_unavailable`, reason `no_window`).
- **When:**
  - Every 5 minutes the hub checks per host: within the window (in the host's time zone), connected, no run open,
    something to do (`security`: security updates pending, `all`: anything pending).
  - At most one attempt per host in 20 hours, so one per window, whatever comes of it.
  - The backup check runs first. If the backup is not fresh, only the event `auto_install_skipped` follows.
- **Rings** (canaries: [ADR-0058](../docs/adr/0058-site-decision.md)):
  - Hosts in `auto_install.canary` install as soon as their window is open.
  - The others only as long as every canary is without a problem. A failed run, `offline` or `stale` holds them back.
  - And only if every pending update of their automatic scope has either been pending for `delay_hours` (default 24) or
    was first reported before the start of a canary's latest clean install
    ([ADR-0107](../docs/adr/0107-ring-delay-per-update-ended-by-a-clean-canary-run.md), since hub 0.22.0).
    Clean means: verb `install`, scope `security` or `all`, result `ok` or `ok_reboot_required`, and the run changed
    packages (its summary names at least one). A run that found nothing to do ends no delay. That happens when nothing is
    pending in the scope, or with `install.mode: upgrade`, which leaves a new kernel behind in every window.
  - A clean run meets only what it did not leave behind
    ([ADR-0118](../docs/adr/0118-a-canary-run-meets-only-what-it-installed.md)).
    - It counts only once a complete report from the canary has arrived after the end of the run. For this the registry
      keeps the end of the latest clean install (`clean_install_end`) and the receipt of the complete report behind the
      list of pending updates (`pending_at`).
    - What this report still names as pending (source and package) was not installed by the run: held back (ADR-0116),
      outside its scope, deferred by apt or only now arrived at the canary. For these updates no delay ends.
  - Measured per update: The registry keeps, per host, the pending updates of the latest complete report (source,
    package, target version, security, first reported) and the start of the latest clean install. A new target version is
    a new update.
  - "First reported" is the later of the agent's check time and the receipt at the hub. A host whose clock runs slow,
    such as a board without a real-time clock before time sync, would otherwise date new updates days back and skip the
    delay. A clock that runs fast only lengthens it.
  - This way the other hosts follow the canary in the same window, minutes after its run. A host with an old backlog no
    longer takes a new update before the canary. If no canary had anything to do, the delay applies.
  - Until hub 0.21, `pending_since` (since when anything has been pending) applied to the whole host.
- The run carries `by: auto` and appears in HA like any other.

### 10.4 Self-update

As in the earlier agent: The operator signs the manifest (version, file per OS/arch, SHA-256) outside the hub, the hub
distributes it. The agent checks **the signature first**, then the entry for its OS and architecture, downloads the
binary next to itself, checks the hash, has the new file say `version`, swaps it in and restarts (systemd
`Restart=always` or the Windows service). Rollout in the same rings as 10.3 ([ADR-0032](../docs/adr/0032-self-update-signed-by-operator.md)).

**Rules:**

- **Publishing:** A release consists of `manifest.json`, its signature `manifest.sig` (context
  `vordr/agent-manifest/v1`, operator key) and the binaries from `./build.sh`; everything lies in `<state_dir>/agent`
  of the hub. Signing happens outside the hub, with `vordr-release sign`
  ([ADR-0139](../docs/adr/0139-the-release-signer-is-part-of-the-repository.md)). The operator key never lies on the hub. The hub also checks the signature
  (`agent_update.public_key` in `hub.yaml`), only to know the valid version.
- **Job** `agent_update` carries the manifest with its signature and a one-time token. With the token the agent downloads
  its binary over `https://<hub>/v1/agent/<file>`; the token is valid for 15 minutes.
- **Agent:** It checks the manifest against the key in **its own** `agent.yaml`, then hash and size. Trial run of the new
  binary: `version` must say the manifest version, `check-config` must accept the agent's own `agent.yaml`. Then it sets
  the marker `update.json`, swaps the binary (the old one stays as `.old`) and exits; systemd starts the new one. This
  exists under Linux as root and under Windows as a service, each with `agent_update.enabled`.
- **Under Windows** ([ADR-0084](../docs/adr/0084-windows-self-update-without-code-signing.md)):
  - A running `.exe` can be renamed but not overwritten. The swap renames it to `.old` and puts the new one in its
    place; until then the new one is called `vordr-agent.new.exe`, so that the trial run can start it.
  - Restart ([ADR-0103](../docs/adr/0103-windows-service-restarts-cleanly-after-a-self-update.md)):
    - Afterwards the agent stops the service cleanly (exit code 0), and the update guard starts it again with the new
      binary. A clean stop does not count as a failure for the service manager.
    - For this the guard listens for a named event of the job (`Global\vordr-update-guard-<job>`), which exists only while
      the guard runs. The swap waits until it is there; otherwise there is no swap.
    - If the agent does not reach a guard, it exits with code 3, and the service's recovery (set by `install-service`)
      starts it after 10 s.
    - Why not always this way: The service manager counts failures for a day, and the third one runs `rollback.cmd`.
      If every restart ended with code 3, the third self-update within a day would roll back. Agents up to 0.11.0
      restart this way. Because the running agent always does the swap, the clean stop takes effect only with an update
      that an agent from 0.11.1 performs.
  - Rollback after 10 minutes without a connection: The running `.exe` becomes `.failed`, the old one is put back, then
    restart as above. The next start deletes `.failed`.
  - If the new one does not start at all, the service manager runs `rollback.cmd` at the third failure instead of a
    restart. It puts the old binary back only while a self-update is open, and starts the service in any case. Since
    ADR-0103 this takes three crashes of the new binary, no longer the planned restart and two.
  - The binary is not signed with Authenticode; as under Linux, its authenticity is secured by the operator manifest.
- **Confirm or roll back:**
  - The new version reports the result `ok` on its first approved connection.
  - If that does not succeed within 10 minutes, it puts the old binary back and exits.
  - If the new one does not start at all, after four failed starts `OnFailure` → `vordr-agent-rollback.service`
    (`/usr/local/lib/vordr/rollback.sh`) does the same.
  - If the new one hangs, the update guard does the same
    ([ADR-0098](../docs/adr/0098-update-guard-rolls-back-a-hung-new-agent.md)):
    - The old agent starts it before the swap from its own binary. Under Linux it runs as the transient unit
      `vordr-agent-update-guard.service` (`systemd-run`, `Type=exec`), under Windows as a process detached from the
      service. If it cannot be started, there is no swap.
    - It ends as soon as the marker is gone.
    - If the update is still open after 15 minutes and `.old` is still there, it puts `.old` back and records the reason
      in the marker (`rolled_back`). Then it kills the hung agent hard and has the service restart.
    - Under Windows this requires that the service does not run in a job object that would also end its child
      processes (checkable with `IsProcessInJob` on the service process). Then the detached guard survives the end of
      the service, as ADR-0098 assumes.
  - The old version then reports `failed_verification`, with the reason from the marker, if one is in it.
  - The hub does not treat the end of the connection after the phase `rebooting` as an abort: The agent has announced its
    restart. The run stays open until the next agent reports the result. If none does by the end of the waiting time
    (30 minutes), it counts as `timeout`.
- **Rings** (canaries: [ADR-0058](../docs/adr/0058-site-decision.md)):
  - First `agent_update.canary`.
  - The others only when every canary has run the version for `canary_clean_hours` (default 24) without a problem. The
    registry remembers `agent_since` for this.
  - A failed attempt is repeated after `retry_after_hours` (default 6).
  - The button "Install" on the host's update entity "Agent" in HA forces the update per host immediately.
- **Only where a binary fits** ([ADR-0102](../docs/adr/0102-offer-an-agent-release-only-where-it-has-a-binary.md)):
  - Besides `arch`, the agent reports `goos` in the hello, that is, the build under which the manifest lists its binary.
  - If the release has no binary for this platform, the hub offers it to the host neither in the rollout nor through
    "Install". The button ends with `no_binary_for_platform`.
  - The host view then carries `agent_release_no_binary`, and HA shows no update there.
  - Such a canary does not hold the others back. But at least one canary must have run the release, otherwise the second
    ring stays closed.
  - Agents before 0.11.0 do not report `goos`. The hub offers them the release as before, and the agent itself refuses
    with `rejected_unsupported` if its binary is missing.
- `install-agent.sh` sets up the rollback script and unit and clears a hanging marker: A manual installation is a fresh
  start. It also restarts the running proxy instances onto the new binary
  ([proxy agent 4.3](proxy-agent.md#43-self-update-of-the-instances)).
- The swap is always done by the agent that is already running. A change to the self-update path therefore takes effect
  only with the update after it. Example: The fix for the file permissions came with 0.7.2, but 0.7.2 was still
  installed by 0.7.1 with `0700`. If a fix must take effect at once, that works only by hand (`install-agent.sh`) or
  through an intermediate release.
- During the swap the host is without an agent for about 30 s under Linux (the unit's `RestartSec`).

**Tightened after a security review:**

- **Upward only** ([ADR-0034](../docs/adr/0034-only-newer-releases.md)):
  Every manifest the operator has ever signed stays valid. An agent on a release
  (`X.Y.Z`) therefore accepts only a newer one. Otherwise it refuses with `rejected_invalid_job`, reason `older_release`.
  Otherwise whoever takes over the hub could send an old manifest again and so bring back an old agent with a known
  defect. Distribution does not offer a newer agent an older release. A hand-built agent (date or suffix in the version)
  accepts every release. Bringing back old code means publishing it under a new version.
- **Backup key** ([ADR-0035](../docs/adr/0035-backup-operator-key.md)):
  `agent_update.backup_public_key` in `agent.yaml` and `hub.yaml`. Its secret part lies only with the operator, never on
  the hub and separate from the operator key, so that both are not lost with the same device or access. If the operator
  key is lost or compromised, releases signed with the backup key still reach every host, without a visit to the device.
  Agents understand the entry only from 0.7.3: Older ones refuse the unknown line in `agent.yaml`.
- **Trigger:** Every run carries `by` (`ha:<client>`, `admin`, `rollout`), in `job_sent` and in HA.

## 11. Sources

| Source | Platform | Status | Security updates detected by | Reboot |
|---|---|---|---|---|
| `apt` | Debian, Ubuntu, Raspberry Pi OS, Proxmox VE and Backup Server | built | origin `*-security` | 7.4 |
| `winget` | Windows 10/11 (programs) | built (11.1) | – (winget has no classification) | registry, see below |
| `winupdate` | Windows (system) | built (11.1) | classification "Security Updates" | registry as for `winget` |
| `dnf`, `apk` | RHEL family, Alpine | not built yet | advisory type or – | – |
| Apps `apt-package`, `winget-package` | as `apt` or `winget` | built: button per app (8.1); separate update entity not built yet | – | – |
| Container images | Linux hosts with Docker Engine | built (11.5) | – | – |

Each source delivers a report (`source.Report`) with state, packages and facts. `source.Presence` says `present`,
`absent` or `unknown`; only `present` and `absent` trigger creating or deleting entities. In the agent
(`internal/agent`) there are two narrow interfaces for this:

```go
type Checker interface {
	Check(ctx context.Context) wire.Report
}

type Installer interface {
	Install(ctx context.Context, req source.InstallRequest, installTimeout time.Duration,
		progress func(source.Progress)) source.InstallResult
	RebootRequired(ctx context.Context) bool
}
```

Plan, execution and verification (10.2) sit in the source behind `Install`; the result always carries a code from 7.3.
Windows Update runs through the COM interface of the Windows Update Agent (`Microsoft.Update.Session`, search
`IsInstalled=0 and IsHidden=0 and Type='Software'`, `ResultCode` and `HResult` per update; details in 11.1 under
"Windows Update"); for this the agent runs as LocalSystem on Windows.

### 11.1 winget

The agent updates programs on Windows with winget (winget before Windows Update;
[ADR-0048](../docs/adr/0048-winget-before-windows-update.md)). The table from
`winget upgrade` is translated and truncated to the console width. The agent therefore does not read it but queries
Microsoft's PowerShell module `Microsoft.WinGet.Client` (the same COM interface that winget itself uses;
[ADR-0050](../docs/adr/0050-winget-via-powershell-module.md)):

- **Check:** `Get-WinGetPackage`, of which those with `IsUpdateAvailable` (id, installed and latest version, source).
  Pinned packages (`winget pin`) do not count. The module follows this like `winget upgrade`: A package with a blocking
  pin stays out, even if the catalog has a newer version.
- **Major version jumps** ([ADR-0052](../docs/adr/0052-major-version-jumps.md)):
  - **What is marked:** A package whose version starts with a different number carries `major` (e.g.
    3.9 → 4.0).
  - **What is not:** Calendar versions (first number from 1000) and versions without a leading number.
  - **Where it appears:** The hub passes it on, HA writes "new major version" into the summary of the update entry
    (integration 0.10.7).
  - **Only winget marks:** The stable updates of a distribution stay within their major version.
  - **Whoever does not want a jump** pins the package (`winget pin add --id <id> --blocking`); "all" then leaves it
    out.
- **Install:** `Update-WinGetPackage -Mode Silent` per package, one after the other, in one PowerShell process. It
  reports one JSON line per package with winget's own status (`PSInstallResultStatus`), the exit code of the installer
  and `RebootRequired`. From this:

  | winget | Code |
  |---|---|
  | `Ok`, `NoApplicableUpgrade` | `ok` (with `RebootRequired` → `ok_reboot_required`) |
  | `DownloadError`, `CatalogError` | `precheck_repo_unreachable` |
  | `InstallError` with installer code 1618 (another MSI is running) | `rejected_busy` |
  | everything else | `failed_package_manager`, status and codes in the excerpt |

  Afterwards the agent queries the list again. If a package of the run is still in it, that is `failed_verification`.
- **Prechecks:** Windows Installer busy (mutex `Global\_MSIExecute`) → `rejected_busy`. Less than
  2 GiB free on the system drive → `precheck_disk_space`, because winget does not state a download size beforehand.
- **Scope:** `all` = everything winget would update. `app:<id>` = one package from `apps` with
  `source: winget-package` and the winget id as `package`. `security` finds nothing, because winget does not classify.
- **Without admin rights** (agent in the foreground instead of as a service) only `app:<id>`. Every machine-wide
  installer would otherwise ask through UAC, and nobody answers. `all` then ends with `rejected_unsupported`. Fact
  `install_context`: `elevated` or `user`.
- **If the module is missing,** the check is `check_failed` with fact `winget_client: missing` and the next step in the
  excerpt. The source does not disappear silently then.
- **Reboot:** The registry keys of Windows Update and Component Based Servicing apply (`RebootPending`,
  `RebootRequired`), reason `windows_update`. `PendingFileRenameOperations` does not count: Edge, Office and drivers
  leave entries there for weeks, often over a thousand on an ordinary PC.
- **Service** ([ADR-0015](../docs/adr/0015-windows-service-localsystem.md)):
  `vordr-agent install-service` (as administrator) does everything in one step:
  - first checks everything that can reject, and changes nothing until then (exit 2): admin rights, `agent.yaml`, the
    hub with `hub-check`, an already installed `agent.yaml`, the service manager and an existing service;
  - replaces a differing `agent.yaml` in `%ProgramData%\vordr` only with `--replace-config "<reason>"`, like
    `install-agent.sh` on Linux: old file as `agent.yaml.bak-<time>`, difference shown, reason into
    `agent.log` (on Windows the agent's log, like the system log on Linux). Without a reason it rejects and names
    both files and the next step;
  - installs PowerShell 7 (as MSI) and the module for all users, grants `%ProgramData%\vordr` and `state_dir` to SYSTEM
    and administrators only, and backs up the old `agent.yaml` while an existing service is still running;
  - only then stops the service, puts the binary into `%ProgramFiles%\Vordr` (the previous one as
    `vordr-agent.exe.old` until success) and `agent.yaml` into `%ProgramData%\vordr`;
  - sets up the service under LocalSystem (automatic start, restart after crash) and starts it.
  - **Way back:** If a step fails after the stop, it undoes the steps in reverse (service, service configuration,
    `agent.yaml`, binary) and starts the service again if it ran before. It then asks the service manager for the state
    and reports it as a fact: exit 3 if the service is as before; exit 1 only if something could not be undone (lines
    `STUCK`).

  **PowerShell 7 is mandatory for the service**
  ([ADR-0053](../docs/adr/0053-powershell-7-for-the-service.md)):
  - As LocalSystem, `Microsoft.WinGet.Client` rejects Windows PowerShell 5.1 ("This cmdlet is not supported in
    Windows PowerShell"); with `pwsh` from `%ProgramFiles%\PowerShell\7` the check runs.
  - The agent uses PowerShell 7 for all users if it is there, otherwise 5.1.
  - If it runs as LocalSystem without PowerShell 7, it reports this before every call as fact
    `winget_client: needs_powershell_7` with the next step, and `install` rejects with `rejected_unsupported`.
  - **Trap:** `winget install Microsoft.PowerShell --scope machine` otherwise takes the msix package. That fails with
    0x80070005 and removes the user's Store version in the process. Hence `--installer-type wix`.

  Log: `<state_dir>\agent.log`.
- **Sleeping is not an outage** (hub since 0.15.0; [ADR-0051](../docs/adr/0051-sleep-is-no-outage.md)):
  A PC sleeps or is off without anything being broken.
  - **Announcement:** The service receives the announcement from Windows (`PBT_APMSUSPEND`, shutdown) and reports it
    to the hub as message `power` (`sleeping`, `shutdown`) before the connection drops.
  - **Until the PC reports again,** the missing connection does not count as `offline`; this holds for every new
    login and every message. The host view carries `power`, HA stays without a problem.
  - **Without an announcement** (crash, network gone) the rule applies as before.
  - **Feedback instead of guessing:** The state comes as a report from the PC and is not guessed from the dropped
    connection.
  - **Old hubs** (before 0.15.0) do not know `power` and drop the connection. So renew the hub first, then enroll
    Windows agents.
- **Time zone for `install.window`** (since agent 0.15.0,
  [ADR-0112](../docs/adr/0112-windows-time-zone-through-the-cldr-table.md)):
  - The agent reads the Windows zone id (`TimeZoneKeyName` under
    `HKLM\SYSTEM\CurrentControlSet\Control\TimeZoneInformation`).
  - From it, the table `internal/agent/zone_table.go` (CLDR windowsZones, territory 001) yields the IANA name.
  - The table is generated by `tools/windows_zones.ps1` from the ICU of .NET.
  - The main zone of the territory is reported, e.g. `America/New_York` for "Eastern Standard Time", also for other
    places in the same Windows zone. The rules are the same.
  - Without a time zone remain unknown ids and hosts on which daylight saving time is turned off. Older agents report
    no time zone on Windows: The hub then cannot schedule the window (`autoNoZone`), and the button outside the window
    gets no reason ([ADR-0109](../docs/adr/0109-the-install-button-installs-at-once.md)).
- **Not yet on Windows:** the reboot verb.

#### Windows Update (`winupdate`)

The agent updates Windows itself through the COM interface of the Windows Update Agent (`Microsoft.Update.Session`),
in PowerShell like winget (report and install by button;
[ADR-0086](../docs/adr/0086-windows-update-source.md)):

- **Check:** Search `IsInstalled=0 and IsHidden=0 and Type='Software'`, so without drivers. Each update is a package.
  The name is the KB number (`KB<number>`), without a KB the update id. `to` carries the title, because Windows
  updates have no version. `security` means: classification "Security Updates".
- **Not counted,** each as a count in a fact:
  - Feature updates (classification "Upgrades", `windows_update_feature_upgrade`). A new Windows version is not an
    update.
  - Definition updates (classification "Definition Updates", `windows_update_definitions`). Defender fetches its
    signatures several times a day by itself; counted, one would almost always be pending, and the host would never be
    current.
  - Optional updates (`BrowseOnly`, e.g. preview updates, `windows_update_optional`). Windows does not install them by
    itself; they would otherwise be pending for weeks and trigger `stale`.
  - Installed updates that wait for the reboot (`RebootRequired`, `windows_update_awaiting_reboot`).

  The agent recognizes classifications by their fixed ids, not by the names, which Windows may translate.
- **Facts:** `install_context` as for winget, `windows_update_search` (`ok`, `failed`, `no_answer`), on `failed`
  the HRESULT as `windows_update_hresult`, plus `windows_update_agent` (version). A failed search is
  `check_failed` with `failed_windows_update`; if PowerShell does not answer at all, `failed_package_manager`.
- **Install by button only:** The agent rejects `install.auto: hub` on Windows (`check-config` as at start). The agent
  joins both sources into one run:
  - `security`: Windows Update only, only classification "Security Updates" (winget does not classify);
  - `all`: first all counted Windows updates, then winget;
  - `app:<id>`: winget only.

  With `all`, both parts run with one run journal, regardless of how the first ends. The result is the first code that
  is not `ok`, with packages and excerpts of both parts; `ok_reboot_required` if either needs a reboot.
- **Windows Update run:**
  1. Prechecks: without admin rights and not as LocalSystem `rejected_unsupported`. Windows Installer busy
     (mutex as for winget) or `IUpdateInstaller.IsBusy` → `rejected_busy`. Under 2 GiB free →
     `precheck_disk_space`.
  2. New search, from it the plan within the scope. If nothing is pending: `ok` without a plan.
  3. Run journal. Then per update download (`IUpdateDownloader`) and installation (`IUpdateInstaller`), one after the
     other. One JSON line per update with `OperationResultCode`, `HResult` and `RebootRequired`.
  4. Verification with a new search. If an update of the plan is still pending and is not waiting for the reboot:
     `failed_verification`.

  Progress as for apt (10.2): precheck up to 5, download up to 35, installation up to 95, verification up to 100.

  | Windows Update | Code |
  |---|---|
  | 2 (succeeded), 3 (succeeded with errors) | `ok` (with `RebootRequired` → `ok_reboot_required`) |
  | 4 (failed), 5 (aborted), any other code | `failed_windows_update`, HRESULT in the excerpt |
  | exception from a COM call | `failed_windows_update`, HRESULT in the excerpt |
  | no longer offered (installed in the meantime) | `ok` |
- **License terms:** The agent accepts them for the updates of its plan (`AcceptEula`), like a click on
  "Install" in the settings. The button in HA is the consent. Every acceptance is in the run log.
- **Reboot** stays manual. `reboot_required` keeps coming from the registry (above).
- **The automatic updates of Windows** keep running. If they install an update before the run, it counts as done. If
  Windows is holding the installer at that moment, the run ends with `rejected_busy`.

### 11.2 Packages from pip, pipx and npm: inventory and findings

**Reason:** apt and winget do not see what Python and Node.js package managers have installed. Supply chain attacks
arrive precisely there, mostly as a malicious new version of a popular package: `chalk` 5.6.1 and `debug`
4.4.2 in September 2025, `axios` 0.30.4 and 1.14.1 in April 2026. Vördr therefore reports every installed package with
a known vulnerability and every one known as malware as a problem in HA.

Rationale for the decisions: [ADR-0064](../docs/adr/0064-osv-data-local-at-the-hub.md) (OSV data local),
[ADR-0065](../docs/adr/0065-scope-of-package-locations.md) (scope),
[ADR-0066](../docs/adr/0066-inventory-from-metadata.md) (inventory from metadata),
[ADR-0067](../docs/adr/0067-hub-announces-features.md) (features of the hub),
[ADR-0079](../docs/adr/0079-detect-packages-in-msix-app-containers.md) (containers of MSIX apps).

The parts:

| Part | Content |
|---|---|
| 11.2 | inventory, OSV data at the hub, matching, problem in HA |
| 11.3 | outdated packages ("newer version available") |
| 11.4 | updating by button, with protection against malicious new versions |
| 11.5 | reporting container images (rebuilt, newer tag), rebuilds by button (ADR-0113, ADR-0119) |

**Inventory (agent).** A *location* (`location`) is a place where a package manager has installed. It has a kind, an
ecosystem (`PyPI` or `npm`, as OSV writes them) and an id following the rules of the app ids:

| Kind | What | Read from |
|---|---|---|
| `npm-global` | `npm install -g` | `package.json` of each package under `node_modules`, also nested |
| `npm-dir` | a folder with `package.json` and `node_modules`: Node-RED's user folder, a project | as above; direct is what `package.json` requires |
| `pip-site` | `site-packages` of an interpreter (pip system-wide or `--user`) | `*.dist-info/METADATA`, `*.egg-info` |
| `venv` | a virtual environment (`pyvenv.cfg`), also the apps of pipx | as above |

- **No package manager runs, nobody outside is asked.** The agent reads only the metadata that pip and npm leave
  behind. This works the same as root and as LocalSystem, also in other users' profiles, and costs little
  (just under 3000 packages on Windows: about 3 s).
- **Known locations** (`packages.detect`, default on):
  - Linux: `/usr/lib/node_modules`, `/usr/local/lib/node_modules`, `/usr/local/lib/python3*/{dist,site}-packages`,
    `~/.local/lib/python3*/site-packages` of root and `/home/*`, pipx (`~/.local/share/pipx/venvs/*`,
    `/opt/pipx/venvs/*`), Node-RED (`~/.node-red`).
  - Windows: per user npm (`AppData\Roaming\npm\node_modules`), Python (`AppData\Local\Programs\Python\*`),
    `pip --user`, pipx and `.node-red`; plus `%ProgramFiles%\Python*`. The id carries the user
    (`npm-global-bob`).
  - Windows, **containers of MSIX apps** ([ADR-0079](../docs/adr/0079-detect-packages-in-msix-app-containers.md)):
    What a process of an MSIX app (e.g. PowerShell from the Store) writes to `AppData` lies under
    `AppData\Local\Packages\<package>\LocalCache`. The service does not see it at the usual path. It therefore reads
    `Roaming\npm\node_modules` and `Roaming\Python\*\site-packages` of each app there, as well as the `pip --user` of
    the Python from the Store (`local-packages\Python*\site-packages`). The id carries app and user
    (`powershell-pip-user-python312-bob`, `store-pip-user-python312-bob`). A run of the agent *inside* such an app
    (`vordr-agent packages` from the shell of an MSIX app) would see these packages twice; so start test runs from an
    ordinary PowerShell.
  - Empty folders are not a location; every Debian host has the empty `/usr/local/lib/python3.11/dist-packages`.
  - **Deliberately not:** `/usr/lib/python3/dist-packages`. apt manages that folder. Debian backports fixes
    without changing the upstream version, and OSV's PyPI ranges do not know this: There would be false findings. apt
    already reports these packages (source `apt`). The same goes for the npm that the Node.js package brings under
    `%ProgramFiles%\nodejs` (source `winget`).
- **More locations** go in `agent.yaml`, `packages.locations`, each with `id` and absolute `path`. The path may contain
  `*`, then each match is a location with the id `<id>-<what the star matched>`: `/opt/*/venv` with the `id`
  `services` gives the location `services-router` for `/opt/router/venv`, `D:\projekte\*` with `projekte` gives the
  location `projekte-webapp` for a project `D:\projekte\webapp`. A match may be the environment itself or a project that
  has it in `.venv` or `venv`; if a project has Python and npm, two locations result (`-py`, `-npm`).
- **Per package:** name, version, `direct` (installed on its own: top level of `npm -g`, required in `package.json`,
  for Python no other distribution of the environment requires it, extras not counted) and `via` (for nested
  npm packages the top package it came with; updating that one helps).
- **What a package holds** (`holds`, [ADR-0129](../docs/adr/0129-findings-say-where-their-fix-is.md)), at most
  two per package:
  - the narrowest range among everything at the location that depends on it: for npm `dependencies` and
    `optionalDependencies` of the installed packages, each for the copy that Node.js resolves the dependency to (the
    nearest `node_modules/<name>` from the dependent upward); for Python `Requires-Dist` without environment marker and
    without extra. For an environment in a project folder (`.venv`, `venv`) also the project's files: the version in
    `uv.lock`, `poetry.lock`, `pdm.lock` or `Pipfile.lock`, the entry in `requirements*.txt` (without options,
    references and markers). Form `{by: "<name>@<version>" or "<file>", range}`;
  - for npm the outermost package with `bundleDependencies` above it: It came in that package's tarball,
    `{by, bundled: true}`.
  - A range without an upper bound holds nothing and is dropped, as is one the agent cannot read (tag, URL, git,
    alias). The `package.json` of an npm folder itself does not count: An update rewrites it.
- **What does not fit stays on the host.** Names and versions later become arguments of pip and npm (11.4). Only what a
  registry would accept goes out. The rest counts as `skipped`, such as a `package.json` with
  `"name": "evil; rm -rf /"`.
- **Limits:** at most 20,000 packages per location and 3 MiB per message. If there are more, the agent halves the
  largest location until it fits, and the location carries the code `too_many_packages`. Otherwise the hub would drop
  the connection (4 MiB per message). It reads linked packages (`npm link`, on Windows junctions) but does not descend
  into them: They are somebody's working copy.
- **When:** after every check (every 6 h and on "Check now"), as message `inventory`, only to a hub with feature
  `inventory` (6.2). `vordr-agent packages` shows on the host exactly what would be sent.
- **Default on:** The inventory changes nothing and asks nobody outside, like `check`. `packages.inventory: false`
  turns it off.

**OSV data (hub).** With the section `osv:` in `hub.yaml`, the hub downloads the full dumps from osv.dev
(`<url>/<ecosystem>/all.zip`, public bucket, renewed daily) and does the matching itself. **No package list of a host
leaves the network**; `api.osv.dev` gets no request.

- Every 24 h (`refresh_hours`) it asks with the ETag of the last response. Unchanged means `304`, one request.
- New: The file lands in `<state_dir>/osv`. From it the hub builds a small index (`<ecosystem>.idx`, gob,
  gzip) and deletes the file again.
- If it fails, the old data stays. The view names the code (`download_failed`, `bad_archive`, `store_failed`,
  `implausible_dump`), a retry follows after an hour. Data older than `stale_hours` (default 72) is called
  `stale`, none yet is called `missing`. HA shows this on the sensor "Vulnerability data".
- **Implausible dumps** ([ADR-0085](../docs/adr/0085-keep-osv-data-when-a-dump-is-implausible.md)) do not replace the
  data: an empty dump or one with less than 90% of the entries or of the malware entries of the data in
  use (`implausible_dump`, both numbers in the log). Otherwise a broken dump would let the hub vouch for every plan.
  The old data keeps reporting and becomes `stale` after 72 h; then the hub vouches for nothing anymore.
  A deliberate large cleanup at OSV is adopted by deleting `<ecosystem>.idx` and restarting the hub.
- Without `osv:` the hub keeps inventories but matches nothing. The numbers are then unknown, not zero.
- Only TLS to the bucket secures the authenticity of the dumps; OSV does not sign them.

Order of magnitude (dumps from the end of October 2026):

| | PyPI | npm |
|---|---|---|
| `all.zip` | 37.5 MB | 219.6 MB |
| unpacked | 81 MB | 389 MB |
| entries (without withdrawn) | 26,124 | 230,207 |
| of which malware (`MAL-`) | 11,984 | 222,580 |
| package names | 13,580 | 225,895 |
| index on disk | 1.0 MB | 4.6 MB |
| load + build index | about 1 s | about 5 s |

Heap with both indexes +112 MB (the hub alone needs about 14 MB). On disk the peak is 220 MB
during the npm download, afterwards 6 MB remain. **A container with 512 MB RAM and a few GB of free disk is enough for
this.**

**Memory of the hub:** Without a limit, the hub reaches about 370 MB RSS while building the npm index, afterwards
about 175 MB remain. The peak is garbage from the build, not the index. The unit therefore sets
`GOMEMLIMIT=200MiB`: The garbage collector runs earlier, the peak stays near 200 MiB, without loss of time. It is
not a hard limit; the hub is never killed for it. Rationale: [ADR-0073](../docs/adr/0073-soft-memory-limit-for-the-hub.md).

**Matching.**
- **Name:** PyPI per PEP 503 (`Requests` is `requests`, `zope.interface` is `zope-interface`), npm exact.
- **List:** If the version is in the entry's `versions` list, it is affected. For PyPI, equivalence also applies here
  (`1.0` is `1.0.0`).
- **Ranges** of type `ECOSYSTEM` and `SEMVER` are evaluated as in the OSV schema:
  - sort the events by version; `introduced: "0"` comes before everything, for equal versions `introduced` before
    `fixed`;
  - then upward: `introduced` ≤ v makes it affected, `fixed` ≤ v makes it unaffected, `last_affected` < v likewise;
  - `limit` bounds from above.
  - `GIT` ranges name commits and do not count.
  - Data is taken as it stands: `dydx-v4-client` has `introduced 1.1.5.post1`, `fixed 1.1.5`; by the
    rule every version from 1.1.5.post1 on is affected.
- **Order of versions per ecosystem:**
  - PyPI per PEP 440: epoch first, release without trailing zeros (`1.0` = `1.0.0`),
    `.dev` < `a` < `b` < `rc` (= `c`, `pre`) < release < `.post` (= `-1`, `rev`, `r`), local identifier last.
  - npm per SemVer 2.0: prerelease before its release; numeric parts as a number (of any length) and before
    alphanumeric ones; build metadata does not count.
- **What does not parse decides nothing.** If a version (installed or in the entry) is not readable in the ordering of
  its ecosystem, the entry is neither a hit nor an all-clear: It counts as `undecided` and appears in HA. The
  `versions` list stays valid in that case.
- **Malware** is an entry with `MAL-` as id or alias. Withdrawn entries (`withdrawn`, e.g. a false malware
  suspicion) do not count.
- **Result per host** (`packages` in the host view):
  - locations with package count and code;
  - `malicious` (packages with a malware entry) and `vulnerable` (with other entries; malware not counted twice). Both
    `null` if data is missing for an ecosystem of the host;
  - at most 50 findings, malware first, then by the source's rating (GHSA: CRITICAL … LOW), with ids, aliases (CVE,
    GHSA), `via` and `fixed_in`;
  - `fixed_in` is the smallest version that fixes all entries of the package; empty if one names no fix. For malware
    it always stays empty: The package should be removed, not updated.
  - **Where the fix lies** (`reach`, ADR-0129), per vulnerable finding. A hold (`holds`) keeps the package below
    `fixed_in` if it is a bundle or its range allows no version from `fixed_in` on; `held_by` names it.
    | `reach` | Meaning | Problem |
    |---|---|---|
    | `update` | a direct package whose candidate or fix (ADR-0111) reaches `fixed_in`, at a location HA may update | yes |
    | `update_via` | held, but a direct package that holds it or that it came with has an update ready (it may bring the fix along) | yes |
    | `refresh` | nothing holds it; the dependencies of the location can be refreshed (ADR-0104) | yes |
    | `by_hand` | nothing on the host holds it, but Vördr does not update there or may not | yes |
    | `held` | a hold keeps it below `fixed_in`, and nothing above it has an update ready | no |
    | `no_fix` | no release fixes all entries | no |
    | `too_new` | the fix has appeared, but is younger than the minimum age | no |
  - `waiting`: how many vulnerable packages are waiting (`held`, `no_fix`, `too_new`); set together with `vulnerable`.
- **Problem:** `malicious_package` comes before all other reasons, it is the most urgent. `vulnerable_package` comes
  last and counts only findings where someone can do something now (`vulnerable` > `waiting`, ADR-0129). Repair text
  and event `vordr_problem` name these packages (up to five, `{packages}`). If another package is added behind the
  same reason, HA reports again. Waiting findings are on the sensor, with `reach` and `held_by`.
- The hub's event log keeps only numbers of the inventory, no package lists.

**Cross-check of the matching:**

- An inventory of just under 3000 packages gives a message of about 270 kB, far below the limit.
- The matching is checked against independent version comparisons: `packaging` (PyPA) for PyPI, npm's own `semver`
  for npm, plus a separately written range evaluation. Over 1000 pairs of package and entry gave
  **no deviation**.
- The cross-check is itself checked: A deliberately falsified result is reported by it as a deviation.

**In HA:** Sensors "Malicious packages" and "Vulnerable packages" per host, as soon as the agent sends an inventory.
Older agents do not get them, existing ones stay. Plus the diagnostic sensor "Vulnerability data" at the hub.

### 11.3 Outdated packages

Reason for the minimum age: [ADR-0068](../docs/adr/0068-minimum-age-for-target-versions.md).

For every direct package (11.2) the agent asks the registry which releases exist and when they were published. It does
this itself; `packages.outdated: false` turns it off (below). Only the name and version leave the host, as with pip and
npm. The package list goes nowhere else.

- **Registry:** PyPI through `https://pypi.org/pypi/<name>/json`, npm through the full document
  `https://registry.npmjs.org/<name>` (only there is `time`, the publication time of each version). Mirrors go through
  `packages.registries`.
- **Read key by key:** The npm document contains the `package.json` of every version, tens of MB for `npm` itself. Read
  as a whole, the agent needs about 90 MB for it; streamed (only one value is in memory at a time) about 14 MB. That
  matters on hosts with little RAM.
- **Two versions per package:**
  - `latest`: the newest release an update can reach. Stable, not withdrawn (PyPI: all files `yanked`; npm:
    `deprecated`, unless set empty). For npm not above the tag `latest`, because a higher version without the tag
    belongs to a different line. Newer than the installed one.
  - `candidate`: the newest such release that has been public for at least `packages.min_age_days` (default 7, 1–90,
    cannot be turned off). **Only a candidate is ever installed (11.4).** If `latest` is younger, `latest_at` gives the
    time; HA can say from when it becomes the candidate.
  - The age of a PyPI release counts from its **newest** file: A wheel can be uploaded to an old release later, and
    that would be an attack path.
  - `major`: the jump to `latest` changes the major version (as with winget, 11.1).
  - `security_candidate` (since agent 0.14.0,
    [ADR-0111](../docs/adr/0111-shorter-minimum-age-for-security-fixes.md)): the newest such release that has been
    public for at least `packages.min_age_days_security` (default 2, 1 to `min_age_days`), only if it is newer than
    `candidate`. Only the hub knows whether it fixes a finding.
- **How often:** at most every `packages.outdated_hours` (default 24), at most 500 names per run, one request per name,
  even if it is at several locations (PyPI names normalized). In between, the agent uses the last answers. A package
  whose version has changed in the meantime waits for the next run and keeps no old answer.
  - The last answers are also in the state directory (`outdated-packages.json`, for the images
    `outdated-images.json`). A restart (self-update, reboot) therefore does not ask again too early.
  - It is only a cache: If the file is missing, unreadable, comes from other settings (minimum age, registries) or
    carries a time after now (the clock ran backwards), the agent asks as if it did not exist.
- **State instead of error text:**
  - A registry that does not answer or answers with an error counts at the location as `outdated_failed`.
  - If the registry does not know the package (HTTP 404: installed from a file, a Git URL, a private index), that is
    not an error but `not_in_registry`.
- `packages.outdated: false` turns the queries off. Then `outdated` is unknown, not zero.
- **Hub:**
  - counts `outdated` and lists up to 100 packages in `updates`; first those an update can take now, then those that
    wait;
  - checks every candidate against the OSV data: `candidate_flagged` names the entries that apply to the candidate
    itself. The agent refuses an update to it (11.4). Example: `axios` 1.14.1 was malware, a fresh release of exactly
    the kind the minimum age keeps away;
  - sets `fix` to the `security_candidate` if the installed version has an OSV entry and the security candidate has
    none (ADR-0111). Such a package counts as ready; HA calls it "security fix, shorter wait".
- **Outdated is not a problem:** The problem sensor stays off. HA shows it on the sensor
  `sensor.vordr_<id>_outdated_packages` "Outdated packages" with the attribute `updates`.

**What the minimum age does:** For a good share of the outdated packages, `latest` is younger than 7 days; then the
candidate is the release before it (e.g. a release from the day before → the candidate is the one before that), and for
some there is none at all. The host view lists at most 100 packages (`MaxUpdates`); the counts are complete.

### 11.4 Updating by button

Reasons: [ADR-0069](../docs/adr/0069-package-updates-by-button-only.md) (by button only),
[ADR-0070](../docs/adr/0070-hub-vets-and-signs-the-update-plan.md) (signed vetting of the plan),
[ADR-0071](../docs/adr/0071-no-code-during-install.md) (no code during install),
[ADR-0068](../docs/adr/0068-minimum-age-for-target-versions.md) (minimum age).

pip and npm packages can be updated by button, per package or per location, triggered in HA. Supply chain attacks
usually arrive as a malicious new version, of the package itself or of a dependency. So every step is a safeguard, and
the safeguards sit in the agent (the hub can only ask):

| Protection | How |
|---|---|
| **Only allowed locations** | `packages.update` in `agent.yaml` names the locations (identifiers, `*` allowed). Default: none. Only `venv` (also pipx) and npm (`npm-global`, `npm-dir`); pip outside an environment stays with the system or the user. The maintenance window (`install.window`) applies as for apt, including the exception with a reason (6.4). |
| **No automatic update** | There is no way to it: `install.auto` knows only `security` and `all`; the hub never starts package updates itself. |
| **Minimum age** | Every target is the candidate (11.3), asked from the registry anew at the run. The **dependencies** too: npm resolves with `--before <now − min_age_days>`, so only releases that were already public then. pip has no reliable equivalent; the agent checks every version of the plan itself at the registry (newest file, not withdrawn). Too young → `precheck_version_too_new`. **Exception security fix** ([ADR-0111](../docs/adr/0111-shorter-minimum-age-for-security-fixes.md)): Packages the hub names as `security_fix` in the signed job (direct packages with `fix`, when refreshing the dependencies with a finding) the agent takes with `min_age_days_security`. Everything else in the plan stays at `min_age_days`: npm then resolves with the `--before` of the short period, and the agent checks every new entry at the registry. A job for a whole location with fixes and other packages runs in two parts: first the fixes alone with the short period, then the rest with `min_age_days`. When refreshing, the short period applies only to the release that replaces a copy with a finding (same path in the lock). |
| **Whole plan before any change** | pip: `install --dry-run --report`; npm: `install --package-lock-only` in an empty folder (global) or in a copy of `package.json` and lock (project, Node-RED). The plan is everything that would be installed afterwards and was not before (name and version, not the path: npm moves packages in the tree). Globally there is no old lock file; "before" there is what the location has installed ([ADR-0105](../docs/adr/0105-global-npm-plan-leaves-out-what-is-installed.md), since agent 0.12.1). |
| **Only from the registry** | Every entry of the plan has in the plan's lock file either no `resolved` URL (bundled in the package of its parent) or one on the registry: same scheme, same host, path below it. Another host, git or a file → `precheck_not_from_registry`, before the hub's vetting and before any change. For this the agent calls npm with `--allow-remote=all`: npm 12 considers some registry tarballs remote and would otherwise reject, for example, a global Node-RED ([ADR-0128](../docs/adr/0128-check-npm-tarball-origins-and-let-npm-12-fetch-them.md)); npm 11 ignores the option. |
| **Never a version OSV knows** | The agent sends the plan (`vet`, only to a hub with the feature `vet`). The hub checks every entry against its OSV data and answers with a **signed statement** (context `vordr/vet/v1`, bound to the job and host, at most 5 min old). The agent accepts only a statement that names every entry of the plan and no other. A flagged entry → `precheck_version_flagged`. No or stale data, no answer → `precheck_vetting_unavailable`. The hub answers only for a running package job of this host. A version that OSV's ordering cannot read does not count as clean. |
| **Exactly the vetted plan** | pip: `install --no-deps` with all versions of the plan pinned; nothing is resolved anew. npm: the same `--before`, so the same resolution. |
| **No code during install** | npm with `--ignore-scripts`, pip wheels only (`--only-binary=:all:`), unless `packages.allow_scripts` or `packages.allow_sdist` name the location. Install scripts are the usual way of malicious npm packages. |
| **Verify afterwards** | Read the location again: every target in its version, otherwise `failed_verification`. pip additionally `pip check`. Then the `health:` checks as with apt. |
| **As the owner** | On Linux, pip and npm run as the owner of the location (uid/gid, `HOME`), so that, for example, a Node-RED user keeps their files. On Windows the service runs as LocalSystem; what it writes into a user profile inherits that profile's permissions. |
| **Names never as an option** | A name begins with a letter or digit (PEP 508, npm), otherwise it enters neither the inventory nor a job. Otherwise `--index-url=…` would be a "package name". |

- **Job:** `install` with scope `pkg:<location>` (all candidates of the location) or `pkg:<location>/<name>`. Location
  and name come from the agent's inventory. The hub signs only what the host has reported as updatable (`update` at
  the location) and, for a name, where the candidate is clean. The agent chooses the version, never the hub.
- **Older agents** do not understand `pkg:`; the hub sends them nothing, because they report no location with
  `update`.
- **Flow in the agent:** precheck (5 %), plan, vetting by the hub, then run journal and installation (50 %),
  verification, `health:` (97 %). Nothing changes until the installation.
- **HA:**
  - one update entity per updatable location, `update.vordr_<id>_packages_<location>` ("Packages <location>"):
    installed "<n> packages", latest e.g. "3 outdated, 1 ready"; without a ready candidate (all too new or flagged) it
    is `off` ([ADR-0106](../docs/adr/0106-update-entity-on-only-when-vordr-would-install.md));
  - the summary shows first what an update takes now, then "too new" and "flagged by OSV";
  - Install updates the location, the service `vordr.update_package` (target: this entity, field `package`) one
    package; with the field `reason` also outside the window (6.4).
- **Dry run** (`vordr-agent packages --plan <location>[/<name>]` = everything up to the vetting, changes nothing). What
  it typically shows:
  - A global npm package whose newer releases are all younger than the minimum age: `precheck_version_too_new`.
  - A whole Python environment with a dependency that exists only as a source package: "wheels only" thereby blocks
    the whole location. Then update per package or set `allow_sdist`.
  - A Linux venv on a Windows drive: The Windows agent finds no interpreter and reports that instead of guessing.
- **Not built:** pip outside environments (`pip-site`), `npm link` packages, rolling back an update (`npm`/pip have no
  such thing; the previous state is in the run log and in the backup).

**Refreshing dependencies with a finding** ([ADR-0104](../docs/adr/0104-refresh-indirect-dependencies-within-their-ranges.md),
stage 1 since agent 0.12.0, integration 0.19.0):

- The candidates above apply to packages a location has for their own sake. Their dependencies move only with them,
  and that is where most findings are. The service `vordr.refresh_dependencies` (target: the location's update entity,
  field `reason` for outside the window) raises them within the ranges their dependents allow.
- **Where:** so far only npm folders with a lock file (project, Node-RED user folder). The agent reports this per
  location in the inventory (`deps`), the hub shows it as `capabilities.dependency_refresh` and accepts the command
  `refresh_deps:<location>` only for that. Job: `install` with scope `deps:<location>`. Global npm and pip
  environments follow.
- **Flow in the agent**, with the same safeguards as an update:
  1. Which: The agent offers the hub all dependencies installed there (`vet`); the signed answer flags those with a
     finding.
  2. Plan: `npm update <names> --package-lock-only` with the same `--before` in a copy of `package.json` and lock. The
     plan is what the new lock file has and the old one does not. Every entry must come from the registry, otherwise
     `precheck_not_from_registry` (ADR-0128, as with the update).
  3. Vetting by the hub: If the planned version of a named dependency is still flagged, it stays out, and the plan is
     made again without it. If the hub flags something the update only brings along, the run ends before any change
     with `precheck_version_flagged`.
  4. Run journal, then `npm update <names>` at the location, with the same `--before` and without install scripts
     (unless `allow_scripts` names the location): npm resolves the same.
  5. Verify afterwards: Every entry of the plan is installed, otherwise `failed_verification`.
- **What remains is named:** A dependency that no allowed version raises appears in the run's result with the packages
  that hold it back ("needs a new release of …"); likewise one whose newest allowed version has a finding itself. The
  run is `ok` all the same.
- **Dry run:** `vordr-agent packages --plan-deps <location>:<name>[,<name>…]` plans for the named dependencies, without
  a hub and without changing anything; the lock file stays unchanged. A dependency that cannot rise within its ranges
  appears there with the dependent that holds it.

### 11.5 Container images: report outdated and vulnerable ones, rebuilds by button

Own source instead of updates2mqtt, report only at first
([ADR-0081](../docs/adr/0081-container-images-own-source.md)). Built are: which images are outdated
([ADR-0113](../docs/adr/0113-report-outdated-container-images.md)), which known vulnerabilities are inside them
([ADR-0120](../docs/adr/0120-known-vulnerabilities-inside-container-images.md), below "Packages in images"), and
updating Compose services to the rebuild of their tag by button
([ADR-0119](../docs/adr/0119-update-container-images-by-button.md), below "Updating by button"). Without an `image:`
entry in `install.allowed_scopes`, Vördr only reports and pulls, starts or changes no container.

- **Turning on per host:** `packages.containers: true` in agent.yaml, default off (new network traffic from the host).
  Only on Linux and only with `packages.inventory` on; the images are part of the inventory. Otherwise `check-config`
  refuses.
- **Inventory (agent, package `internal/images`):** the running containers through the engine API on the local socket
  (`/var/run/docker.sock`; an engine with the same API, such as Podman's compatible socket, fits here), `GET` only, no
  CLI:
  - `GET /containers/json`, per container `GET /containers/<id>/json` (the reference it was created with,
    `Config.Image`) and per image `GET /images/<id>/json` (`RepoDigests`, `RepoTags`, `Created`, platform,
    `Config.Labels`).
  - One entry per image and tag, with the names of the containers. `name` is the repository including the registry
    (`docker.io/library/nginx`), `version` the tag, `digest` the repo digest it was pulled with.
  - Pinned digest (`app@sha256:…`): the comparison uses the tag the image carries on the host for the same repository
    (`RepoTags`; if there are several, first one with a version form, otherwise the alphabetically first;
    [ADR-0117](../docs/adr/0117-compare-pinned-images-under-their-tag.md)). Without such a tag, `version` is the
    digest, and the image is only listed. Two references to the same image under the same tag are one entry.
  - Built locally: reported, never compared, no registry is asked. Local means without a repo digest (classic image
    store) or built by Compose (label `com.docker.compose.project` in the image configuration): The containerd image
    store gives local builds a repo digest too
    ([ADR-0115](../docs/adr/0115-recognize-images-compose-built-on-the-host.md)). A container created from an image ID
    has no reference and counts as `skipped`.
  - State instead of error text: `no_engine` (no socket), `engine_unreachable` (socket there, engine does not answer).
    A container that disappears between the list and the query (404) is missing by itself only.
- **Location in the inventory:** `containers`, kind `container`, ecosystem `oci`. The agent sends it only to a hub that
  announces the feature `containers` ([ADR-0067](../docs/adr/0067-hub-announces-features.md)): An older hub would
  otherwise count the images among the pip/npm packages and take the findings as unknown.
- **Registry, anonymous:** at most every `outdated_hours`, only with `outdated`, through the distribution API and HTTPS.
  - Own schedule: A check that could not read the engine, or whose hub did not accept images yet, compares nothing and
    does not hold up the next comparison. The location says when it was compared (`compared_at`); until then
    `outdated` and `ready` are unknown at the hub.
  - If the registry answers with 401 and `WWW-Authenticate: Bearer`, the agent fetches a read token at the named realm
    (`https` only), without credentials. A 401 on a token from an earlier request fetches a fresh one once (those from
    Docker Hub expire after five minutes). Only 401/403 with a fresh token or 404 means `not_in_registry` (private or
    gone). 429, 5xx and network errors count as `outdated_failed`.
  - Docker Hub (`docker.io`) answers at `registry-1.docker.io`.
- **Downloads that count as a pull:** `HEAD` does not count at Docker Hub, a manifest download does, against the same
  anonymous limit as the host's own pulls, often shared with all hosts behind the same public address.
  - The agent keeps configuration and build time per digest and platform in `<state_dir>/image-cache.json` (up to
    4096, the longest unused drop out). A digest never changes its content: Neither later runs nor a restarted agent
    download the same manifests again.
  - At most 20 manifest downloads per registry and run; the rest counts as `outdated_failed` and is taken in the next
    run.
- **Rebuilt:** `HEAD` on the manifest of the tag. If the digest differs from the repo digest, the agent reads the image
  for its platform: index → manifest of the platform → configuration (`created`).
  - It is rebuilt if that is a different configuration than the running one, regardless of the build time:
    reproducible builds (distroless, ko, Bazel, Nix) keep a fixed one. An index also changes when only another platform
    or an attestation is added; that is not a rebuild.
  - The running configuration: with the engine's classic image store, the image ID. With the containerd store the ID is
    the pulled digest; then the agent reads the configuration behind it from the registry. If the registry no longer
    has that digest, the tag counts as rebuilt.
  - `rebuilt_candidate`: at least `min_age_days` old.
  - A container with a pinned digest stays on its build. Compared under its tag, `rebuilt` then means: The tag has
    moved on since the pinned build, the pin is outdated (ADR-0117).
  - If `HEAD` on the running tag answers 404 (a retention rule deleted it), the agent reads the tag list anyway; only
    if that is closed too, or the tag has no form, does the image count as `not_in_registry`.
- **Newer tag of the same form:** optional `v`, numbers with dots, same count of numbers, same suffix (`-`, `_` or `+`
  and the rest): `2.4.1-gpu` compares only with `x.y.z-gpu`, `16` with `17`.
  - A number with six or more digits is a date or a build number (`20250108`), not a version, and compares only with
    one of the same kind: `alpine:3` does not find the date tag `20250108` newer.
  - Tags without a number (`latest`, `stable`) have no form, and neither does a running prerelease tag (`rc`, `beta` …):
    only "rebuilt".
  - The tag list comes page by page (`Link: …; rel="next"`, only on the same host), at most 60 pages of 1000 and 1 MiB
    each; only valid tags are kept, at most 60000; a longer one counts as unanswered, the newest tags might be on an
    unread page. For further images of the same repository a run holds at most 120000 tags. The limit of 60 pages:
    [ADR-0121](../docs/adr/0121-read-tag-lists-of-up-to-60000-tags.md).
  - Of the newer tags, newest first, the agent reads at most 5 up to the first one that is old enough: `latest` the
    newest, `candidate` the newest with at least `min_age_days`, `latest_at` its build time while it is younger.
    `major`: The first number changes. A tag without an image for the host's platform does not count.
  - The smaller step beside it
    ([ADR-0131](../docs/adr/0131-name-the-newest-tag-of-the-running-major-version.md)): If `candidate` is a new major
    version, or `latest` is one and no tag is old enough, the agent also reads the newer tags with the running first
    number, newest first, again at most 5: `same_major` is the first of them with at least `min_age_days` (an image on
    3.5: `candidate` 4.2, `same_major` 3.9). If `candidate` is already in the running major version, the second pass
    does not apply. A question without an answer counts in it like any other (`compare_failed`).
- **Age:** `created` of the configuration. Before 2013 (reproducible builds write 1970) the age is unknown: reported
  (rebuilt or newer tag), never a candidate.
- **Limits:** at most 300 requests per run, tokens included; what is still open after that counts as `outdated_failed`.
- **Why no answer** ([ADR-0126](../docs/adr/0126-say-why-an-image-comparison-got-no-answer.md)): Every image that counts
  as `outdated_failed` carries the reason in the inventory (`compare_failed`), taken from the answer itself, never from
  an error text: `rate_limited` (429), `request_budget` (the 300 requests), `manifest_share` (the registry's 20
  manifest downloads), `tag_list_too_long` (more pages, tags or bytes than above), otherwise `no_answer` (network,
  5xx, unreadable answer, time ran out). The first reason of the image counts. The hub shows them as `compare_failed`
  (image, tag, container, reason; at most 100), HA as an attribute of the entity "Container images", not in the
  recorder. An agent before that reports only the number.
- **Hub:** keeps the location like any inventory, without OSV matching, and shows it separately from the packages in the
  host view as `containers` (`images`, `local`, `outdated`, `ready`, `compared_at`, `updates` with at most 100 images,
  those with a candidate first). As long as the images have never been compared (`compared_at` is missing), `outdated`
  and `ready` are unknown, not zero. Outdated images are not a problem of the host. An image with only `same_major` (the
  new major version still too new) counts as `ready`: There is something that is old enough (ADR-0131).
- **Home Assistant:** one update entity per host "Container images" (`update.vordr_<id>_containers`). On as long as an
  image has a candidate. Without allowed services it cannot install, and the summary begins with "Reported only,
  Vördr does not update images"; with allowed services see "Updating by button". Beside a new major version it names
  the smaller step: "app 2.2.0 → 3.1.0 (new major version, or 2.4.0 in the same major version)". The list is in the
  attribute `updates` (with `same_major`), not in the recorder
  ([ADR-0101](../docs/adr/0101-package-lists-stay-out-of-the-recorder.md)).
- On the outdated pip/npm packages (11.3): If a package is at two locations, say older under `pip --user` and newer in
  the interpreter, `pip list --outdated` shows only the first, Vördr both locations.

**Packages in images** ([ADR-0120](../docs/adr/0120-known-vulnerabilities-inside-container-images.md)):

- **Turning on:** `Debian` and/or `Alpine` in `osv.ecosystems` of the hub. Then it announces the feature
  `image_contents`, and only then do agents read the packages in their images.
- **Agent:** per image one of its containers, through `GET /containers/{id}/archive` of the engine, no `exec`, no pull:
  - `/etc/os-release` (following the link once, otherwise `/usr/lib/os-release`): distribution and release;
  - `/var/lib/dpkg/status`, for distroless `/var/lib/dpkg/status.d/`, otherwise `/lib/apk/db/installed`: the installed
    packages with source package (dpkg `Source`, apk `origin`).
  - With every comparison (`outdated_hours`); without `outdated` in the same cycle without a comparison; in between
    only for an image the agent has nothing on yet, and once. The answers stay with the comparison in the state
    directory.
  - Limits: os-release 64 KB, package database 16 MB, 5000 packages per image (`too_many_packages`). If the inventory
    does not fit in one message, the packages of the image with the most are dropped first (code `too_many_packages`
    without packages, the image stays); trimming is by size, not by count.
  - State instead of error text: `no_package_db` (distroless without a database, scratch), `unreadable`,
    `unknown_release`.
- **Hub:**
  - It loads the dump of a distribution only while an image of one of its releases is running, and builds an index only
    for those releases (`Debian:13`, `Alpine:v3.23`).
  - Beforehand it checks the space, dump plus 256 MB (`no_space`).
  - The indexes stay on disk. For a match it loads one, matches every image of this release, keeps the results and
    discards it again, one after the other. A Debian release needs about 60 MB in memory, outside of memory under 1 MB.
  - Every release has its own status: An index is valid only for the dump (ETag) and the index version it was built
    from; otherwise it fetches the dump at once. A release without entries in the dump is a state (`no_entries`), not an
    error. One that has shrunk implausibly keeps its old index (`implausible_dump`); the others take the new dump. An
    unreadable index is forgotten and rebuilt. Building also waits for the match lock.
  - Matching by source package and source version, as Debian and Alpine write their advisories. Counted per CVE: once,
    even if Debian publishes it as a DEBIAN-CVE and as a DSA; a DSA for several CVEs counts each. "unimportant" does not
    count for this package, whichever advisory names the CVE.
  - The kernel's source packages do not count
    ([ADR-0122](../docs/adr/0122-kernel-advisories-in-images-do-not-count.md)): Debian `linux` and the versioned
    `linux-<version>` (`linux-6.12`), Alpine `linux-lts` and `linux-grsec`. A container runs on the host's kernel; the
    image carries at most its headers (`linux-libc-dev`). Their advisories appear per image as `kernel`, unless another
    package of the image has the same CVE, then it counts there. Packages that merely have `linux` in the name
    (`util-linux`, `linux-pam`, `linux-base`) count like all others.
  - Per image `fixable` (a newer package fixes it) and `unfixed` (no fix yet), with the IDs; an image without a match
    with a reason (`not_read`, `unsupported_distribution`, `unsupported_release`, `no_data`, `too_many_packages`). An
    image with only kernel advisories is in the list with `kernel` and counts neither as `fixable` nor as `unfixed`.
  - Whether a newer image is available
    ([ADR-0130](../docs/adr/0130-image-entity-needs-attention-only-with-a-newer-image.md)): per image `newer` if it
    has a candidate (rebuild or newer tag, old enough). `fixable_newer` counts the images with `fixable` and a
    candidate; nil as long as `fixable` is nil or the images were never compared with their registries. Images with
    `fixable` and a candidate come first. A fixed package in the distribution does not yet mean a newer image is
    there: That comes only when the publisher rebuilds.
  - In the data overview (`osv`) only releases an image needs appear.
- **Home Assistant:** the same entity "Container images".
  - Vulnerabilities alone do not turn it on, fixable ones neither: on only with a candidate (ADR-0130, as ADR-0106 for
    the other update entities). With a candidate, `latest_version` also says for how many images with fixable advisories
    a newer image is available ("1 fixable by newer image").
  - The summary says per image whether a newer image is ready: "2 fixable, newer image ready" or "2 fixed in the
    distribution, no newer image yet"; advisories without a fix as before. A hub from before ADR-0130 sends no
    `fixable_newer`; then the numbers stay without this addition.
  - The list is in the attribute `vulnerable`, not in the recorder, per image with `kernel` where there are kernel
    advisories, and `newer`; the summary does not name the kernel advisories.
  - Not a problem of the host.
- **Ubuntu** is not included yet: OSV writes its releases differently (`Ubuntu:24.04:LTS`), and its dump is ten times
  as large as Debian's. Ubuntu images show `unsupported_distribution`.

**Updating by button** ([ADR-0119](../docs/adr/0119-update-container-images-by-button.md)):

- **Allowing per service:** `image:<project>/<service>` in `install.allowed_scopes`, `*` for any part (`image:web/*`).
  Default: none. Needs `packages.containers` and `packages.outdated`; `default_scope` stays for the system packages and
  is never an `image:` entry. Otherwise `check-config` refuses and names the services.
- **What works:** only the rebuild of the tag the Compose file names, and only from `min_age_days`, and only the image,
  never the configuration. Manual work remains for:
  - a newer tag (1.2 → 1.3) and a pinned digest: both need a changed Compose file;
  - a service with more than one container (Compose scaled it beyond its file);
  - a tag that another container still uses (it would move along). Excepted is a stopped container of the same Compose
    project (`exited`, `created`, `dead`), such as a one-off helper that sets permissions before the service: It keeps
    its image and takes the new one only when Compose recreates it with the project
    ([ADR-0123](../docs/adr/0123-stopped-containers-of-the-project-do-not-share-the-tag.md)). The run does not restart
    it (`--no-deps`); do not allow a service whose helper must run before every new version (database migration);
  - a container without Compose labels or from `docker compose run`, an image built on the host, a project whose folder
    or files are missing or not absolute;
  - a service whose Compose files or variables have changed since it started (different configuration hash): bring it
    up once by hand.
- **Inventory:** The agent reads the running containers and reports per image the services it may and can update
  (`updatable`, without the hash check, which only the run does); the hub offers only these
  (`capabilities.image_updates`), shows them per image (`services`) and counts what the button would take
  (`installable`: a rebuild, old enough, of such a service).
- **Jobs:** `image:<project>/<service>` for one service, `images` for every allowed one (the agent chooses). The job
  names only names, no image, no tag, no argument. `min_interval_h` does not hold them up (as with pip/npm), but a run
  with a change counts as an install run. The backup before the run (ADR-0030, ADR-0096) applies as for any `install`.
- **Run per service,** every check before the first change:
  1. Compose is there and at least 2.22 (`up --pull`), otherwise `rejected_unsupported`. Compose always talks to the
     agent's engine (`DOCKER_HOST`), whichever Docker context applies for root.
  2. The service can be moved (see above), otherwise `rejected_scope_not_allowed` with a reason.
  3. `docker compose … config --hash <service>` gives the hash from the container's label, otherwise
     `rejected_scope_not_allowed`.
  4. Ask the registry now what the tag points to (not the answer of the last inventory; its cache is only read): no
     rebuild → `ok` without a change; younger than `min_age_days` → `precheck_version_too_new`; no answer →
     `precheck_repo_unreachable`.
  5. Space: four times the download plus 256 MB in the engine's data folder (and with the containerd image store in its
     folder, where there is one); too little or not readable → `precheck_disk_space`.
  6. Pull the rebuild through the engine API **by digest** for the vetted platform. If that fails, nothing is changed
     (`failed_package_manager`).
  7. Write the run journal. From here the service runs to the end or back, with its own time frame (30 min), even if
     `install.timeout_min` runs out or the agent stops.
  8. Keep the running image as `<repo>:vordr-previous.<project>.<service>`, point the Compose file's tag at the
     rebuild and run `docker compose --project-name … --project-directory … --file … up --detach --no-deps --no-build
     --pull never <service>` with the files from the labels.
  9. Healthy: The container runs with the new image, with a healthcheck `healthy`, and then 30 s without a restart,
     within 3 min; then `health` from agent.yaml. A container that dies after seconds and is restarted never gets
     there.
  10. Otherwise back: The old image gets the tag again, Compose recreates the service, the keep tag goes back (to the
      way back of the run before, if there was one), the new image is removed; `failed_health` (with failed Compose
      `failed_package_manager`) with "rolled back" in the detail.
  11. If it succeeds, the agent removes the image of the run before, if no name and no container holds it any more: one
      generation stays as the way back.
- `images` works through the services one after the other. Those that cannot be moved, are too new or have no answer
  stay as they are and appear with a reason in the detail; an error after a change is rolled back and ends the run. If
  nothing happened, the code says why (`precheck_disk_space`, `precheck_repo_unreachable`).
- **Home Assistant:** "Container images" installs (`install_images`) when the host allows services and the hub counts
  something the button would take (`installable`), with progress; the summary then begins with "The Install button
  takes rebuilt images of the approved services, newer tags remain manual work". The entity also stays on for manual
  work. The action `vordr.update_image` (field `service`: `<project>/<service>`) updates one service, with `reason`
  also outside the window, likewise `vordr.install_outside_window` for all.

## 12. Hub

- Go, one binary, systemd service. State lives in files as in the earlier agent: registry (`registry.json`), event log
  (`events.jsonl`, append-only), settings and rings (`hub.yaml`). No database
  ([ADR-0011](../docs/adr/0011-state-in-files.md); location: [ADR-0054](../docs/adr/0054-hub-in-own-container.md)).
- Interfaces: tunnel endpoint (WebSocket over TLS, the certificate is reloaded on renewal without a restart), Home
  Assistant endpoint `/v1/ha` on the same port ([ha-integration.md](ha-integration.md)), download of the agent binaries
  (HTTPS, one-time token). No web interface.
- Administration only locally through a Unix socket (`admin_socket`, mode 0600 in a 0700 folder): `vordr-hub list |
  approve <node> --fingerprint <fp> | reject <node> | forget <node> | check <node> | backup <node> | events`, for
  Home Assistant clients (own registry `clients.json`) `clients | approve-client <name> --fingerprint <fp> |
  reject-client <name> | forget-client <name> | client-role <name> --role <role>`. Without the socket: `key`,
  `check-config`, `version`. Not built yet: a command for the rings (`rings`)
  ([ADR-0010](../docs/adr/0010-administration-only-local.md)).
- **Stopping** (SIGTERM): The hub accepts nothing new, closes the connections of the agents and Home Assistant clients
  and waits up to 10 s until what it started is done (a report that is being saved, the result of a job). Waiting for
  job results ends with `hub_stopping` and records nothing: the run stays open, and the agent's next hello settles it
  ([ADR-0142](../docs/adr/0142-the-hub-waits-for-its-sessions-work-when-it-stops.md)).
- **Viewing the backup check** (`vordr-hub backup <node>`): what the check before an install would decide now (10.2);
  it asks the backup server and sends nothing.
- **Reading the event log** (`vordr-hub events [<node>] [--event <name>] [--since <when>] [--limit <n>]`):
  - The hub reads `events.jsonl` itself and returns the matching lines through the socket. The socket decides who may
    read, not the file permissions of the state folder.
  - The newest matching lines are shown, 50 by default, at most 5000, oldest on top.
  - `--since` takes a duration back (`90m`, `24h`, `7d`), a date (midnight local time) or an RFC 3339 time.
  - Reading happens without the write lock: the hub keeps writing. A line that is being written at that moment is
    incomplete and is counted and named as unreadable, not skipped silently.
- **Roles of the HA clients** ([ADR-0039](../docs/adr/0039-ha-client-roles.md)):
  - `observe`: views only.
  - `operate`: check, install, agent update. This is the default.
  - `admin`: additionally reboot.

  The host's policy has the last word anyway. The role limits what a compromised Home Assistant can make the hub
  sign. A refusal is called `role_forbids`, and HA shows its own text for it.
- **Queue per host** ([ADR-0043](../docs/adr/0043-queue-per-host.md),
  [ADR-0044](../docs/adr/0044-same-command-joins-running-run.md)):
  An agent executes one job after another. But "update all" in HA presses several buttons of one host in the same
  second; without a queue, the agent update would run, for example, and the install would end with `rejected_busy`.
  - When an HA command arrives while a run is still going on the host, it waits instead of being sent.
  - A command that is already waiting does not wait twice; at most 3 wait (otherwise `queue_full`).
  - A command equal to the running one (verb and scope) does not wait at all. The answer is `ok` with the `job_id` of
    the running run, and the event log records `command_already_running`. Otherwise a second tap on "Reboot now" would
    wait and end with `rejected_rate_limited` after the first reboot.
  - After the run ends, the hub starts the next one after 30 s. An agent that has just restarted checks first.
  - The host view shows the queue (`queued`), HA shows it on the "Last run" sensor.
  - A run that is older than its job may last does not hold up the queue.
  - The same "busy" view applies to automatic installing and agent distribution: a run without a result, at most as
    long as its job may last, or a hypervisor that is currently backing up a guest. Without this limit, a run whose
    result got lost would keep the host away from both forever.
  - A result that nobody waits for any more closes the run and starts the next waiting command. This happens when the
    hub restarted during the run or the tunnel dropped and the run went on.
  - If a waiting command reports `already_in_state` at its start (the agent already has the release), the next one is
    up.
  - The backup check before installing applies at the time of the command.
  Results are fixed codes (`ok`, `unknown_node`, `fingerprint_mismatch`, `not_connected`, `not_approved`,
  `verb_unsupported_by_agent`, `timeout`, …). `vordr-hub key` shows the public hub key for `agent.yaml`.
- Browser connections (with an `Origin` header) are rejected. At most `max_pending` hosts wait for approval; a known
  host with a different key is rejected until it is removed with `forget`.
- Checks HA commands against the host's reported policy before it signs.
- Reports problem cases: host silent (`offline`), `stale`, waiting approvals, failed agent updates.

## 13. Platforms and build

- Go 1.27, `CGO_ENABLED=0`, `-trimpath -ldflags "-s -w"`: static binaries without a runtime.
- Agent targets: `linux/amd64`, `linux/arm64`, `linux/arm` (GOARM=7), `windows/amd64`. Hub: `linux/amd64`.
- Order of magnitude when choosing the language: a core with TLS, WebSocket, Ed25519 and JSON came to 6.2–7.0 MB per
  target (gzip 2.6–2.9 MB) and 12 MB RSS after one TLS connection.
- Rust would be smaller (estimated 1.5–3 MB and 2–5 MB RSS, not measured). That only pays off on devices with very
  little memory, and those get no agent here (non-goals in section 1). Go, on the other hand, allows taking over the
  proven parts of the earlier agent (section 14; [ADR-0003](../docs/adr/0003-go.md)).
- Windows binaries stay without an Authenticode signature; the operator manifest secures their authenticity
  ([ADR-0084](../docs/adr/0084-windows-self-update-without-code-signing.md)).

## 14. What is taken over from the earlier agent

Copied and adapted, without a shared library, so that both projects can release independently
([ADR-0012](../docs/adr/0012-copy-parts-of-the-earlier-agent.md)). A shared module only pays off once both keep growing
in parallel.

| The earlier agent (`agent/`) | Vördr | Adaptation |
|---|---|---|
| `identity.go` | agent identity | Ed25519, DPAPI on Windows, unchanged |
| `tunnel.go` | tunnel client | challenge/`hello`, backoff; signed jobs and reports instead of proxy data streams |
| `updater.go` | self-update | error kinds in English from the start |
| `service_windows.go`, `install_windows.go`, `setup_windows.go` | Windows service, setup with `install-service` (11.1) | service under LocalSystem (Windows Update requires it); no setup by double click |
| `mqtt.go` (`syncDiscovery`) | the pattern only | tri-state when creating and deleting entities (8.4); the MQTT code itself is removed ([ADR-0026](../docs/adr/0026-remove-mqtt.md)) |
| `sign.ps1` | – | not taken over: Windows binaries stay without an Authenticode signature ([ADR-0084](../docs/adr/0084-windows-self-update-without-code-signing.md)) |

From the server side of the earlier agent, the hub takes over only patterns (registry with approval, rollout with a
canary, event log), no code.

## 15. Stages

| Stage | Content | Status |
|---|---|---|
| 1 | Agent with source `apt` and verb `check`; hub with login, approval and HA entities | built |
| 2 | `install` and `reboot` with policy, run journal and the codes from 7.3 | built (10.2) |
| 3 | Self-update, rings, `install.auto: hub` | built (10.3, 10.4); `install.auto: local` not built yet |
| 4 | Sources `winget` and `winupdate` | built (11.1); reboot on Windows not built yet |
| 5 | Apps, HA blueprint including "stuck" for integrations | apps by button built (8.1); update entity per app and blueprint not built yet (8.5) |
| 6 | pip, pipx, npm: inventory and OSV findings, outdated packages, update by button; container images | built (11.2–11.5) |

## 16. Questions that were open earlier

1. `install.auto` per host class: who triggers, and what scope (security only or everything)?
   → [ADR-0058](../docs/adr/0058-site-decision.md),
   [ADR-0059](../docs/adr/0059-own-auto-scope.md), [ADR-0088](../docs/adr/0088-hypervisor-updates-by-button-only.md),
   [ADR-0108](../docs/adr/0108-automatic-installs-take-every-update-on-linux-guests.md)
2. Reboot: which hosts may restart themselves? Proxmox hosts probably never.
   → Procedure: [ADR-0041](../docs/adr/0041-reboot-with-window-and-proof.md); the hypervisor never through Vördr
   ([ADR-0088](../docs/adr/0088-hypervisor-updates-by-button-only.md))
3. Which hosts are canaries? → per site in `auto_install.canary` and `agent_update.canary` of `hub.yaml`;
   pattern: [ADR-0058](../docs/adr/0058-site-decision.md)
4. Where does the hub run? → [ADR-0054](../docs/adr/0054-hub-in-own-container.md); it no longer needs an MQTT account
   ([ADR-0026](../docs/adr/0026-remove-mqtt.md))
5. unattended-upgrades: replace it or let it run alongside?
   → [ADR-0080](../docs/adr/0080-security-updates-everywhere-unattended-upgrades-off.md)
6. Docker: own source `docker-image` or integrate updates2mqtt?
   → [ADR-0081](../docs/adr/0081-container-images-own-source.md)
7. License and publication → [ADR-0082](../docs/adr/0082-repo-stays-private-for-now.md),
   [ADR-0100](../docs/adr/0100-publish-as-a-new-repository-with-one-start-commit.md), MIT
   ([ADR-0135](../docs/adr/0135-mit-license.md)), work in the own repository with checked states on GitHub
   ([ADR-0140](../docs/adr/0140-work-stays-private-the-public-repository-gets-snapshots.md))
8. Appliances without an agent (NAS, firewall, mesh Wi-Fi, gateways)? → through a proxy agent in its own container, not
   through the hub, with a configuration backup before every update: design
   [proxy-agent.md](proxy-agent.md), [ADR-0089](../docs/adr/0089-appliances-through-a-proxy-agent.md),
   [ADR-0090](../docs/adr/0090-appliance-installs-need-a-fresh-config-backup.md)
