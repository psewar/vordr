---
status: accepted
date: 2026-10-30
decision-makers: kay (2026-10-30, "please check configuration backups"); proposed by Claude
---

# Appliance installs need a fresh configuration backup, taken and checked by the proxy

## Context and Problem Statement

Before an install the hub asks the backup server for a fresh backup of the host
([ADR-0030](0030-backup-required.md)); hosts that are not backed up there are listed as `without` with a reason.
Appliances ([ADR-0089](0089-appliances-through-a-proxy-agent.md)) are not in the backup server. The operator chose
to check configuration backups instead of `without`.

Appliances often lack a usable configuration backup: some keep a history of versions on the device itself, some have
an occasional manual copy, many have none.

## Decision Drivers

* No firmware update without a way back to the configuration.
* The hub stays credential-free; it must not fetch backups from the devices.
* A tool does its job including its checks: the one that installs makes sure the backup exists.
* Local before cloud: no vendor account or cloud backup service.

## Considered Options

* `without` for every appliance.
* Rely on backups the devices or their vendors make (device-local history, vendor cloud backup).
* The proxy's driver takes the backup itself, checks it, keeps versions, and reports when; installs need a fresh one.

## Decision Outcome

Chosen: the proxy takes and checks the backups (the operator, 2026-10-30).

* Every driver has a backup step: export the configuration, check its form (not empty, readable, the expected root or
  header), store it with time and checksum in the instance's state directory, keep `config_backup.keep` versions.
* It runs daily with the check and again right before every install. An install without a backup younger than
  `config_backup.max_age_h` does not start (`precheck_config_backup`).
* The agent reports the time of the last good backup as the fact `config_backup_at`; HA shows a problem when it is
  older than the limit.
* The hub's backup check learns a second kind: `backup.guests` entry `config_backup: true` takes the age from that fact
  (same `max_age_hours`, same codes `backup_stale` and `backup_missing`). The hub never sees the backup itself.
* Backups stay in the proxy container, readable only by the instance's user; the container is backed up like every
  guest, which puts them off the device. They never go into logs, events or HA: they can contain password hashes and
  keys (for example VPN or Wi-Fi keys).

Rejected: `without`, because a failed firmware update could then lose the configuration (the operator). Device and
vendor backups, because many of these devices have none, a device's own history dies with the device, and vendor
accounts are a cloud dependency.

### Consequences

* Good, because every appliance gets a current configuration backup, which many of them do not have otherwise.
* Good, because the hub stays without access to devices or backups.
* Bad, because the proxy container now holds sensitive configuration of every appliance, on top of their credentials.
* Bad, because each driver needs a backup path that has to be proven on its device (design section 5).
* Bad, because the hub trusts the agent's reported backup time, as it trusts its other facts.

### Confirmation

Per driver: a test that a broken or empty export fails the backup step and blocks an install; the hub's backup check
tested with `config_backup: true` for fresh, stale and missing facts; on a live instance, the backup file present
and restorable by hand.
