# Vördr – proxy agent for appliances

English · [Deutsch](proxy-agent.de.md)

> **Status:** Built are reporting and backing up through the drivers `synology-dsm` and `pfsense` (4.1), the instance
> units (4.7), the configuration backup with its check at the hub (4.4) and the fixed command on the firewall (4.6).
> Installing through a driver is not built yet; `check-config` rejects it. Rationale:
> [ADR-0089](../docs/adr/0089-appliances-through-a-proxy-agent.md) (proxy agent instead of hub),
> [ADR-0090](../docs/adr/0090-appliance-installs-need-a-fresh-config-backup.md) (configuration backup before every
> update), [ADR-0091](../docs/adr/0091-firmware-for-devices-without-internet.md) (offline devices),
> [ADR-0092](../docs/adr/0092-mesh-keeps-its-vendor-self-update.md) (mesh Wi-Fi keeps its self-update).
> Complements [architecture.md](architecture.md). Technical names and text are English. Like the other design documents,
> without host names and addresses. Devices appear with their class; a product only where its driver is built
> (ADR-0099).

## 1. Motivation

If every machine that can run an agent has one, appliances with vendor firmware remain: firewall, NAS, mesh Wi-Fi,
gateways, printers. The obvious idea would be to run them as "virtual agents" directly on the hub, with a different
way for each device (scripts, REST, SSH).

## 2. Decisions

1. **One proxy agent in its own, isolated container**, not the hub. It talks only to the registered devices and to the
   hub. This also solves a second problem: devices in another network or segment, which you otherwise reach only
   through a jump host, get their own proxy agent there; the hub needs no route to them.
2. **Device classes:** firewall, smart home gateway, NAS, mesh Wi-Fi (every unit); the drivers for pfSense and Synology
   DSM are built. More device classes in section 6.
3. **No HA integrations for firewall and mesh Wi-Fi:** They give Home Assistant too much access to these systems; the
   firewall in particular would be a risk in HA. Own drivers instead.
4. **Check configuration backups** instead of `without`: Before every update a fresh backup is required (section 4.4).
5. **Offline devices:** Devices that do not need internet themselves do not get it permanently for the update either:
   Either the proxy agent fetches the firmware, or a fixed command opens internet briefly and narrowly (section 4.6,
   ADR-0091). A printer that prints only on the local network belongs here.
6. **Mesh Wi-Fi:** The master's self-update stays; Vördr reports and backs up (ADR-0092).
7. **One proxy agent per network:** Anyone with devices in a separated segment (for example a management network for
   remote server management) sets up a separate proxy agent there (sections 3 and 4.7).
8. **A separate service account per device** (section 7).

The "in the hub" variant and its consequences: ADR-0089. In short: The hub would otherwise hold admin access to the
devices and would thus be the central server with remote execution that Vördr replaces
([ADR-0001](../docs/adr/0001-own-agent-instead-of-patchmon.md),
[ADR-0006](../docs/adr/0006-local-policy-decides.md)).

## 3. Overview

```
┌─────────────── Proxy container ───────────────┐
│ vordr-agent@firewall ── fixed commands ───────┼──▶ firewall (report only)
│ vordr-agent@gateway  ── web API ──────────────┼──▶ smart home gateway
│ vordr-agent@nas      ── DSM web API ──────────┼──▶ NAS
│ vordr-agent@mesh-…   ── web login ────────────┼──▶ mesh Wi-Fi, each unit (report only)
│   per instance: identity, policy,             │
│   credentials (0600), own state,              │
│   configuration backups                       │
└──────────────┬────────────────────────────────┘
               │ outbound only, wss /v1/tunnel (like every agent)
               ▼
           vordr-hub ──▶ Home Assistant (one device per appliance)
```

- **One agent instance per device** (systemd template `vordr-agent@<name>`): own Ed25519 identity, own node name, own
  `agent.yaml`, own state directory. To the hub every instance is an ordinary host: approval by fingerprint, problem
  logic, buttons, events. Hub and protocol do not change; only the backup check learns a second kind (4.4).
- **Driver instead of package source:** With `target:` the instance does not read the container's packages but asks the
  device through its driver. A driver can do exactly four things: check, back up, install, verify. None runs arbitrary
  commands; where SSH or an admin interface is the way, the requests are fixed in the driver.
- **Several networks:** A proxy agent stands where it can reach its devices. For a separated segment (e.g. the
  management network for remote server management) a separate container in that segment; toward the outside it needs
  only the route to the hub.

## 4. Build

### 4.1 `agent.yaml` of an instance

```yaml
node: <name>                       # device name, as it should appear in HA
hub: {url: ..., public_key: ...}
target:
  driver: synology-dsm             # built: synology-dsm, pfsense; more per device class
  address: https://<address>:5001  # the driver connects there; the HTTP host is this address
  # TLS exactly one of the two, never "do not verify":
  server_name: <name>              # certificate for this name (also as SNI), system roots or ca_file
  # fingerprint: "<sha256>"        # or: device with its own certificate, pinned (64 hex digits, ":" allowed)
  credentials: /etc/vordr/targets/<name>.cred   # USER=/PASS=, 0600
config_backup:                     # ADR-0090; these values if omitted
  keep: 30                         # this many versions stay in the state directory
  max_age_h: 26                    # older: HA shows a problem (failed_config_backup)
health:
  - http: https://<device>/         # after an update, from the proxy agent
```

- **Built:** reporting and backing up. `install` is not allowed with `target:` yet; installing comes with the first
  driver that can do it.
- `check-config` checks the section like any other: known driver, `https` address without a path, exactly one TLS way,
  credentials file absolute and readable only by its owner. With `target:`, `install`, `install.auto`,
  `reboot.allowed`, `agent_update`, `apps`, `packages` (the inventory is off) and `health` other than `http` are not
  allowed: They meant the container, not the device.
- **NAS (synology-dsm):** If DSM has its own rule for its host name (e.g. reverse proxy), it returns 404 everywhere on
  port 5001 under the name and the web API under the address. Hence `address` = address, `server_name` = name.
  A service account without the admin group may sign in only to a known application (session `webui`) and gets
  error 105 for update check and settings; the account needs the group `administrators`. Export:
  `SYNO.Backup.Config.Backup` `start` → `status` until done → `download`, an xz-compressed `.dss` file.
- **Firewall (pfsense, [ADR-0094](../docs/adr/0094-ssh-drivers-use-the-system-openssh-client.md)):** over SSH to the
  fixed command `vordr-gna` ([ADR-0093](../docs/adr/0093-firewall-command-unprivileged-root-job-works-a-checked-queue.md)),
  with the system's OpenSSH client. Instead of `server_name`/`fingerprint`:

  ```yaml
  target:
    driver: pfsense
    address: ssh://vordr-gna@192.0.2.1           # the user belongs to the address
    host_key: "ssh-ed25519 AAAA..."              # pinned, from ssh-keyscan
    credentials: /etc/vordr/targets/fw-a.cred    # KEY=<base64 of the private OpenSSH key>
  ```

  Before `ssh` the driver checks facts: address reachable (otherwise `precheck_repo_unreachable`), device offers exactly
  the pinned host key (`ssh-keyscan`, otherwise `failed_target_hostkey`). An `ssh` abort (255) after that is the login
  (`failed_target_auth`); any other return value is the fixed command's refusal with its `reason`. `ssh` runs with
  `-F /dev/null`, `BatchMode`, `IdentitiesOnly`, without an agent, with its own `known_hosts` containing only the pinned
  key; the private key exists only during the call, in a 0600 file. Report: firmware from `version` (form
  `<version>-RELEASE`), an offer "pfSense" if the catalog has a newer `pfSense-base`, and for each outdated package an
  offer without a version; `self_update: off`. Backup: `config.xml` (XML header, root `<pfsense>`, at least 1 KB).
- **Facts** come from the driver: product and firmware as `os`, the driver as `os_id`, model, update channel, whether
  the device updates itself, time and checksum of the last configuration backup.

### 4.2 Driver interface (agent, Go)

| Step | What the driver does | Result |
|---|---|---|
| check | read the installed firmware, ask the device for the offered version | Report as with apt: source = driver, `pending`, one "package" per firmware or per device package (from → to); `security` only where the vendor marks it, otherwise 0 with fact `security_classification: none` |
| back up | export the configuration, check its form (not empty, readable, expected root or header), store it with time and checksum, delete old versions down to `keep` | Fact `config_backup_at`; failed: `check_failed` with its own code |
| install | precheck (fresh backup, device healthy), trigger the update | Phase `installing`, then `rebooting` while the device does not answer |
| verify | wait until the device answers again and reports the new version (deadline `timeout_min`) | `ok` or `failed_verification` with reason; then `health:` |

Fixed codes as everywhere ([ADR-0007](../docs/adr/0007-fixed-codes-instead-of-error-texts.md)), decided by the type of
the error, never by its text: device not reachable → `precheck_repo_unreachable`; new: device rejects the service
account (`failed_target_auth`), certificate does not match fingerprint or name (`failed_target_tls`), SSH host key does
not match (`failed_target_hostkey`), backup failed or older than `max_age_h` (`failed_config_backup`). Other errors of
the device: `failed_package_manager`.

**Report of an instance:** two sources. The firmware under the driver's name (facts `model`, `self_update`,
device-specific ones such as `update_check`), next to it `config-backup` with `config_backup_at` and
`config_backup_sha256`. An export is renewed as soon as the last one is older than `max_age_h` minus 2 hours; if the
device does not answer at all, no export is attempted. If the renewal fails while the last one is still younger than
`max_age_h`, the source stays ok and names the reason in the excerpt; after that `failed_config_backup`.

### 4.3 Self-update of the instances

All instances share one binary. Only the ordinary agent of the container (it reports the container's own Debian
packages) fetches new releases. The instances have `agent_update` off.

- As soon as the container's new agent has confirmed its update (first approved connection), it restarts the running
  instances: `systemctl try-restart --no-block vordr-agent@*.service`. Only then, because a new binary that fails would
  otherwise drag the instances down too; with the rollback they would not come back.
- If the update rolls back, the instances stay untouched. They keep running with the binary that is back in its place.
- A manual install with `deploy/install-agent.sh` replaces the binary as well and follows the same order:
  - Before it changes anything, it checks the policy of each running instance with the new binary. If one fails, it
    installs nothing.
  - Once the new agent stays up, it restarts the running instances (`systemctl try-restart`).
  - If an instance does not come back, the install fails and names it.
- Without this step an instance would keep running with the replaced file until its next start, and HA would show an
  agent update for it permanently.

### 4.4 Configuration backup (ADR-0090)

- **Who:** the driver itself, daily with the check and once more immediately before every install. Tools do their job
  including the check: An update without a fresh, checked backup does not begin.
- **Where:** in the instance's state directory (0700, root). If the container is backed up like a guest (for example to
  PBS), the versions are also stored outside the device. No vendor account, no cloud.
- **Hub:** For appliances `backup.guests` gets `config_backup: true` instead of a PBS group or `without`. The backup
  check before a job then takes the age from the reported fact `config_backup_at` (same limit `max_age_hours`) and
  rejects as before with `backup_stale` or `backup_missing`. The hub needs no access to the backups for this. The agent
  checks the same again itself. The agent's last report (source `config-backup`) is authoritative; PBS is not asked for
  these hosts. The sensor "Last backup" shows the reported time. Without an entry the appliance stays `unmapped`, and
  every install is rejected.
- **The content is secret:** Configurations can contain password hashes, VPN and Wi-Fi keys. They leave the container
  only through its backup; never into logs, events or HA.

### 4.5 Window and reboot

- **Never automatic through Vördr.** Firmware updates by button; outside the window with a reason as everywhere
  ([ADR-0087](../docs/adr/0087-runs-outside-the-window-with-a-reason.md)). If a device updates itself (mesh master,
  NAS security hotfixes, printer), the driver reports that as a fact (`self_update: on`).
- **Reboot** belongs to the firmware update and is not a verb of its own; the instance does not announce `reboot`.

### 4.6 Offline devices (ADR-0091, ADR-0093)

Some devices are deliberately meant to have no internet (for example through a firewall rule for a group of such
devices). For their update there are two ways, in this order of preference:

1. **The proxy agent fetches the firmware**, checks the vendor's signature or checksum and offers it to the device,
   which pulls it from the local network or has it uploaded. Works where the device accepts a file or a local
   repository: remote server management (signed vendor catalog as local repository), printers (firmware file through
   the interface, to be verified), NAS (update file through the API, to be verified).
2. **Short-term approval**, where the device can load only itself from the vendor (for example consumer electronics
   with network update).
   - In the firewall a runtime table `vordr_update_window` and a rule in front of it: Devices in the table may reach
     only the update servers of their vendor (FQDN alias per vendor), nothing else.
   - The proxy agent does **not change the firewall configuration** but calls a fixed command on the firewall host
     over SSH (`command=` in the key, only for this purpose). The command accepts only addresses from a list that lies
     on the firewall, enters them into the table and removes them itself after 30 minutes at most.
   - The approval belongs to the run: It begins after backup and precheck, ends after verification, and every run
     checks at the start that the table is empty. Event and reason are in the log.
   - The proxy agent thus gets exactly one capability for this: letting listed devices reach listed vendors briefly.
     It gets no access to rules, aliases or settings.

**Built (`deploy/pfsense/`, ADR-0093):**

| Part | Place on the firewall | runs as | does |
|---|---|---|---|
| fixed command `vordr-gna` | `/usr/local/sbin/vordr-gna` (0755) | `vordr-gna`, only through the proxy agent's key (`command=` in the key line) | `version`, `backup`, `window status`, `window open <address> <minutes>`, `window close <address>`; puts jobs only into the queue |
| root job `vordr-gna-root` | `/usr/local/sbin/vordr-gna-root` (0700), cron every minute under `lockf` | root | checks every job again, enters into the pf table `vordr_update_window`, closes what has expired, reconciles table and state, writes the update facts hourly |
| device list | `/usr/local/etc/vordr-gna-allow` (0644) | – | addresses of the devices that may get a window, one per line, e.g. `192.0.2.40`; the list belongs to the site, the repo ships it empty |
| queue, state | `/var/db/vordr-gna-queue` (vordr-gna, 0700), `/var/db/vordr-gna/` (root) | – | `window`: `entry=<address> <end>`; `updates`: `base_installed`, `base_offered`, `packages_outdated(_count)` |

- Answers are `key=value` lines; a refusal says `state=refused` and `reason=<code>` (`unknown_call`, `bad_arguments`,
  `bad_address`, `bad_minutes`, `not_listed`, `not_ready`), the return value the kind (2, 3, 4).
- A window opens or closes up to a minute after the job; the driver waits for `window status`.
- The rule "table → vendor's update servers" is not part of `deploy/pfsense/`: It comes with the first driver that needs
  a window and names the update servers of its vendor. Without it an entry in the table has no effect.
- The account `vordr-gna` gets a random password that is written nowhere; it is meant to sign in only with the key. If
  the firewall's SSH service also accepts passwords, the binding to the fixed command depends on this password; SSH
  with key only is safer.

### 4.7 Container

- **Name of the parts:** The parts on the firewall are called `vordr-gna…` after Gná, Frigg's messenger, who runs
  errands for others in foreign worlds: proxy agent and jump into other networks in one.
- **Place:** a small, unprivileged container per network from which devices must be reached (first environment: LXC
  under Proxmox VE with Debian), fixed address, backed up like any guest. A proxy agent in a separated segment needs
  toward the outside only the route to the hub (8443) and no leg in another network.
- **Outbound blocked by the hypervisor:** The rules are in the hypervisor's firewall for this container (outbound drop
  by default; allowed: hub 8443, DNS, the devices on their ports). Inside the container an attacker with root could
  switch off nftables, but not the hypervisor's rules.
  - Under Proxmox VE they are in `/etc/pve/firewall/<vmid>.fw`: allows DNS, the hub, the registered devices on their
    ports (e.g. SSH to the firewall), plus 80/443 to the internet (packages, vendor firmware); everything else in the
    private network is dropped.
  - The rules take effect only if the Proxmox firewall is switched on at datacenter and guest level (datacenter on,
    the container's rule file with `enable: 1`). Guests without their own rule file stay unfiltered as long as the
    datacenter policy allows everything.
  - Verification from the container: every allowed target open, a non-allowed one closed.
  - If the hub admits port 8443 only to known senders ([ADR-0056](../docs/adr/0056-firewall-8443.md)), the proxy agent
    container goes on this list as soon as its agent is admitted.
- **No SSH service;** it is managed from the hypervisor (under Proxmox VE `pct exec`).
- **Instances:** one systemd instance `vordr-agent@<device>` per device (template
  `deploy/systemd/vordr-agent@.service`, setup in `deploy/README.md`). Node name = device name, own identity and own
  state per instance. The instance runs as root without capabilities, with a read-only system and only its state
  directory (`systemd-analyze security` 3.7).
  - **Admission:** approve at the hub like any host, after comparing the fingerprint in the container
    (`vordr-agent identity --config …`) with the one at the hub. After that the instance reports the firmware and stores
    configuration backups (0600).
  - **Certificate:** If the instance checks the device's certificate by name with the system roots, the device must,
    for a Let's Encrypt certificate, deliver the cross-signature of the new root over ISRG Root X1; otherwise
    `fingerprint` instead of `server_name`.
  - **Credentials:** The credentials file (`KEY=value`, 0600) is put into the container during setup, for example from
    the hypervisor (`pct push`), and exists outside the container nowhere longer than necessary. The instance reads it
    at start. Vördr does not renew it itself: A new password means rewriting the file and restarting the instance
    (`systemctl restart vordr-agent@<device>`).

## 5. First device classes

How a driver talks to the device class and what to watch for. "To be verified" means: to be proven on the device
before the build, read-only.

| Device | Access | Update status | Back up (driver) | Install | Note |
|---|---|---|---|---|---|
| Firewall (pfSense) | admin interface or SSH, fixed commands | firmware and offered base version, plus package updates; without a driver this appears nowhere in HA (section 2, item 3) | read `config.xml`, check XML root | report only | Versions on the device (at every change) are not stored off-site; the vendor's cloud backup is none (4.4: no vendor account) |
| Smart home gateway | web API, SSH | installed version, update channel, offered update | backup function of the interface (to be verified which way is the official one) | by button | – |
| NAS (Synology DSM) | DSM web API | the NAS's update check, also with major versions; whether it installs security hotfixes itself | export through `SYNO.Backup.Config.Backup` (4.1) | by button, later | If the vendor's download list for the model ends, no new major version comes any more; how long the last one still gets security updates is told by the vendor's support status |
| Mesh Wi-Fi | web login (SSH mostly off) | firmware per unit, the vendor's notice; whether a unit updates itself (mostly only the master) | settings file of the interface (to be verified) | report only (ADR-0092) | often **one admin session**: a query can kick an open session out of the browser → query rarely, sign out afterwards |
| Smart home server | web API with app token | installed version; whether it reports an offered update is to be verified (not every one does) | to be verified | report the version only, where it reports no offer | – |

## 6. Further device classes

| Device | Check | Update | Internet |
|---|---|---|---|
| Remote server management (BMC) | Redfish with a **read-only account** (the firmware inventory often needs an account); some BMCs check against a vendor repository themselves on request and name the matching updates without installing | by button from a **local repository at the proxy agent in the management network** (vendor catalog, signed) – BMC, BIOS, RAID controller, network cards | management network, way 1 from 4.6 |
| Printer | version through the device API (often readable without login); offered update and auto-update often only in the admin interface (to be verified) | by button, firmware file through the interface (to be verified) | If it is registered with the vendor's cloud service, it may update itself; better without internet, then way 1 from 4.6 |
| Consumer electronics with network update (e.g. AV receiver) | local API, often without login; a message "new firmware available" can date from the time before the device was cut off from the internet | by button, network update with short approval (way 2 from 4.6) | better without, then way 2 from 4.6 |
| Bridges | local API with its own key | – (later) | – |
| Switches | version often through SSH, but without comparison with the vendor | no | – |
| Devices whose firmware an HA integration already reports (cameras, ESPHome devices, speakers …) | – | leave to HA | – |
| Devices that the vendor updates through its cloud (household appliances, heating, TV boxes …) | – | no | – |

## 7. Security

- **Hub:** holds no credentials. If taken over, it can start checks and, where an instance's policy allows it, a
  firmware update – as with hosts. The threat model (architecture.md 5) gets one line: "proxy agent taken over → can
  operate the devices whose credentials it holds and read their configuration backups".
- **Container:** only for this purpose; no SSH service, management through the hypervisor. Outbound only what the
  hypervisor's firewall allows (4.7): hub (8443), DNS, the registered devices on their ports, plus HTTP(S) to the
  internet for packages and vendor firmware.
- **Own credentials file and own state per instance,** readable only by root; the unit lets the instance write only into
  its own state directory (4.7). A separate user per instance is not built yet: An instance can also read the files of
  the others in the same container.
- **Own service accounts** on every device, never a personal account or one that another tool already uses. Read-only
  rights where only reporting happens – as far as the device knows them; some (mesh Wi-Fi, firewall interface) know
  only admin.
- **Credentials only in the instance's credentials file** (4.7), never in `agent.yaml`, logs, events or HA, never on the
  hub. Where the operator gets them from (secret store, by hand) is up to the operator; Vördr reads only the file.
- **Closed drivers:** fixed requests or commands; none of it comes from the hub.

## 8. Steps

1. Agent: `target:`, `config_backup:`, driver interface, instance units, codes. Hub: `config_backup: true` in
   `backup.guests`. Tests with recorded answers of the devices; mutation check. **Built.**
2. Set up the proxy agent container (hypervisor firewall, credentials files, service accounts on the devices; 4.7).
3. Driver per device class, each first report + back up; where a device reports no offered update, only the version.
   **Built for pfSense and Synology DSM.**
4. Proxy agent in a management network, driver for remote server management (report through a local vendor
   repository).
5. Install by button, per device class with backup before and verification after. Offline devices that can load only
   themselves, only with the short-term approval (4.6).

## 9. Decided and open

Decided:

1. **Mesh Wi-Fi:** The master's self-update stays (ADR-0092). It brings the nodes along; security fixes thus arrive
   without effort. Vördr reports, backs up daily and shows a problem if a unit deviates or an offered update has been
   pending for 7 days.
2. **NAS:** Vördr reports what the NAS's update check offers, also major versions. If the hardware gets no newer one any
   more (vendor's download list for the model), it stays at its version with the vendor's security hotfixes (section 5).
3. **Offline devices:** ADR-0091; a printer that needs no internet belongs here.

Open:

1. **Service accounts per device class:** read-only account at the remote server management, service account at the NAS,
   SSH user with fixed command at the firewall, app token at the smart home server. Mesh Wi-Fi, some printers and
   gateways know only the one admin account; then the proxy agent holds its credentials, with the consequences from
   section 7.
