# Setup

English · [Deutsch](README.de.md)

The hub and the agents run as systemd services. Both install scripts check everything before they change anything,
and leave an existing configuration in place or replace it only with an identical one: changing a local policy is a
decision, not a side effect of an update.

## Hub

Prerequisite: a TLS certificate for the name under which agents and Home Assistant reach the hub.

```bash
./build.sh                                               # dist/vordr-hub-linux-amd64
sudo deploy/install-hub.sh dist/vordr-hub-linux-amd64 hub.yaml
```

`install-hub.sh` creates the system user `vordr-hub` and uses `check-config` **as that user** to check that it can read
`hub.yaml`, the certificate and the key. Then the service starts; on first start it creates the hub key
(`/var/lib/vordr-hub/hub.key`, owned by `vordr-hub`). At the end the script shows the public key for `hub.public_key`
in every `agent.yaml`. `vordr-hub key` never creates a key itself: a key created by root would belong to the wrong
user.

The hub reloads a renewed certificate by itself at the next connection setup; no restart needed.

`install-hub.sh` replaces a changed `hub.yaml` only with a reason: `--replace-config "<reason>"`. The old file stays
as `hub.yaml.bak-<time>`, the difference is shown and the reason is noted in the system log.

## Home Assistant

Install the integration through HACS (custom repository, type Integration; [ha/README.md](../ha/README.md)) or copy
`custom_components/vordr` to `/config/custom_components/vordr`, restart HA, add the integration "Vördr" (address
`wss://<hub>:8443/v1/ha`). It shows its fingerprint; approve it at the hub:

```bash
vordr-hub clients                                        # waiting and approved HA clients
vordr-hub approve-client <name> --fingerprint <fp>       # after comparing with what HA shows
```

## Agent

```bash
sudo deploy/install-agent.sh dist/vordr-agent-linux-<arch> agent.yaml
```

`install-agent.sh` first runs `check-config` and, with `vordr-agent hub-check`, checks whether this host reaches the
hub: name resolution, connection, TLS chain, tunnel, challenge. If a stage fails, nothing is installed. At the end it
prints the approval command for the hub:

```bash
vordr-hub approve <node> --fingerprint <fp>              # at the hub, after comparing with the host
```

## Agent on Windows (Windows Update and winget)

A Windows PC reports the programs winget would update, and updates them at the press of a button (design 11.1).
Example `agent.yaml`:

```yaml
node: pc-a
hub: {url: "wss://hub.example.org:8443/v1/tunnel", public_key: "<vordr-hub key>"}
apps:
  - {id: firefox, name: Firefox, source: winget-package, package: Mozilla.Firefox}
install:
  allowed_scopes: [security, all, "app:firefox"]
  default_scope: all
  timeout_min: 60
```

In a PowerShell **as Administrator**:

```powershell
.\vordr-agent-windows-amd64.exe check-config --config .\agent.yaml
.\vordr-agent-windows-amd64.exe check --config .\agent.yaml       # what Windows Update and winget offer, as JSON
.\vordr-agent-windows-amd64.exe install-service --config .\agent.yaml
```

`install-service` first checks `agent.yaml`, the hub and an `agent.yaml` that is already installed; until then it
changes nothing. After that it does everything else in one step:
binary to `%ProgramFiles%\Vordr`, `agent.yaml` to `%ProgramData%\vordr` (SYSTEM and Administrators only),
PowerShell 7 and the PowerShell module `Microsoft.WinGet.Client` for all users, service `vordr-agent` under
LocalSystem. The service needs PowerShell 7 because the module runs as LocalSystem only there; the Store version in
the user profile does not count, so `install-service` installs the MSI through winget. A different
`agent.yaml` that already lies there it replaces only with a reason, like `install-agent.sh`:
`install-service --config .\agent.yaml --replace-config "<reason>"`. The old file stays as
`agent.yaml.bak-<time>`, the difference is shown and the reason is noted in `agent.log`. If a step fails after the
service is stopped, the service runs again afterwards with the previous binary and `agent.yaml` (exit 3).
Remove with `vordr-agent uninstall-service`; identity and `agent.yaml` stay.

Self-update (ADR-0084): with `agent_update` in `agent.yaml` (as on Linux) the service fetches new versions through the
hub. For this, `install-service` puts `rollback.cmd` next to the binary and sets up service recovery: restart after
10 s and 1 min, on the third failure `rollback.cmd`. That puts the old binary back only while a self-update is open,
and starts the service again. A manual installation removes a leftover update marker, like `install-agent.sh`.

The agent skips pinned packages (`winget pin add --id <id> --blocking`). This way a program stays, for example, on
the version that another tool manages.

Windows Update (ADR-0086): the agent also reports the software updates from Windows Update, without drivers, feature
updates, definition updates (Defender fetches those itself) and optional updates; security updates count as on Linux.
With `security`, the button installs only the security updates of Windows; with `all`, first Windows Update, then
winget; for this, `security` belongs in `allowed_scopes` as in the example. On Windows the agent installs nothing
automatically (it rejects `install.auto: hub`), and reboots stay manual. The automatic updates of Windows keep
running. `check` shows the status of the search in the facts `windows_update_*`.

## Proxy agent for appliances (ADR-0089)

A device without its own agent (NAS, firewall, …) gets an **instance** of the agent in the proxy agent's container,
one per device: systemd template `vordr-agent@<device>`, policy `/etc/vordr/targets/<device>.yaml`, credentials file
`/etc/vordr/targets/<device>.cred` (`KEY=value`, 0600), state `/var/lib/vordr/<device>` (identity, journal,
configuration backups). The node name is the device's.

```yaml
node: nas-a
state_dir: /var/lib/vordr/nas-a
hub: {url: "wss://hub.example.org:8443/v1/tunnel", public_key: "<vordr-hub key>"}
target:
  driver: synology-dsm
  address: https://192.0.2.10:5001
  server_name: nas-a.example.org            # or fingerprint: <sha256 of the leaf>
  credentials: /etc/vordr/targets/nas-a.cred
config_backup: {keep: 30, max_age_h: 26}
```

```bash
sudo install -m 0644 deploy/systemd/vordr-agent@.service /etc/systemd/system/
vordr-agent check-config --config /etc/vordr/targets/nas-a.yaml
sudo systemctl daemon-reload && sudo systemctl enable --now vordr-agent@nas-a
vordr-agent identity --config /etc/vordr/targets/nas-a.yaml       # fingerprint for vordr-hub approve
```

In target mode `check-config` rejects everything that would mean the container instead of the device: install,
reboot, self-update, apps, packages and health checks other than http. All instances share the container's binary:
the container's ordinary agent updates it (`vordr-agent.service` with `agent_update`, set up with `install-agent.sh`)
by self-update, and after its confirmation it restarts the running instances. `install-agent.sh` does the same when it
replaces the binary by hand: it first checks each running instance's policy with the new binary, and it restarts the
instances once the new agent stays up
(design [proxy agent 4.3](../design/proxy-agent.md#43-self-update-of-the-instances)).

## Signing agent releases

Agents update themselves only with a release signed by the operator key from their own `agent.yaml`
([ADR-0032](../docs/adr/0032-self-update-signed-by-operator.md),
[ADR-0139](../docs/adr/0139-the-release-signer-is-part-of-the-repository.md)). Signing happens on the operator's
machine, never on the hub:

```bash
vordr-release keygen operator.key                   # once: create the key (0600), shows the public key
vordr-release public-key operator.key               # -> agent_update.public_key in agent.yaml and hub.yaml
./build.sh 1.2.3                                    # binaries with exactly this version
vordr-release sign operator.key 1.2.3 dist release-1.2.3   # binaries, manifest.sig, manifest.json
```

Copy the contents of `release-1.2.3` to `<state_dir>/agent` of the hub, `manifest.json` last. A backup of the key
belongs stored separately from the hub; a replacement key (`backup_public_key`) separately from the main key
([ADR-0035](../docs/adr/0035-backup-operator-key.md)). An existing key is never replaced.

## Isolation of the services

| Service | User | Isolation | `systemd-analyze security` |
|---|---|---|---|
| `vordr-hub` | `vordr-hub` | no capabilities, system read-only, only its own state and runtime directory, system calls limited to `@system-service` | 1.3 OK |
| `vordr-agent` | root | only what never disturbs a package run (own `/tmp`, address families, realtime, architecture) | 8.5 – deliberate |
| `vordr-agent@<device>` | root without capabilities | system read-only, no `/home`, only its own state directory, no devices, no W^X break, no new privileges | 3.7 OK |

The proxy agent instance installs nothing on its container; it talks only to its device and the hub. So the tight
isolation applies to it, which would break package runs for the host agent.

The agent runs as root because apt needs it: it installs packages and, on self-update, replaces itself.
`ProtectSystem`, `ProtectHome` or `ProtectKernelModules` would break package installations: maintainer scripts write
to `/usr`, `/etc`, `/home` and `/usr/lib/modules`. Its limits are the local policy and the closed set of verbs, not
the sandbox.

## Commands from Home Assistant

Commands come only over the authenticated connection of an approved HA client. The hub checks them against the host's
verbs and policy before it signs them, and the agent checks every job once more. A client cannot approve hosts or
change anything in their policies; `vordr-hub reject-client` / `forget-client` revoke it and cut its connection
immediately.
