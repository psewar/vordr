---
status: accepted
date: 2026-10-31
decision-makers: kay (2026-10-31, the approach, and that a failed backup stops the install; 2026-11-01, accepted with the wording independent of the backup system); proposed by Claude
---

# A fresh backup right before every install of a hypervisor guest, made by the hypervisor's agent

Refines [ADR-0030](0030-backup-required.md): for guests of a hypervisor the backup is made right before the install
instead of being at most 26 h old. The age check stays for other hosts and wherever `before_install` is not
configured. Answers the question [ADR-0083](0083-snapshot-before-install.md) left open: a recent way back, without
snapshots on the guests' storage.

## Context and Problem Statement

Since ADR-0030 the hub requires, before every install, a backup in the backup store that is younger than 26 h.
Scheduled backups typically run once a day. An install by button, or an automatic install in its window, therefore
has a way back of up to a day; whatever changed since then (a service's database, its configuration) is not
in it.

Hypervisors with incremental backups make a fresh backup of a single guest cheap. With Proxmox VE and Proxmox Backup
Server (PBS), the backup system Vördr supports first, one guest takes seconds to a few minutes: for a VM, PBS reads
only the blocks changed since the last backup (dirty bitmap), and a container on storage without snapshots is frozen
for a few seconds at the end of its backup (suspend mode).

Who makes such a backup, and how does the hub know it is there, without the hub gaining a right to write?

## Decision Drivers

* The way back is the state right before the install, not the state of the last scheduled backup.
* The hub gets no right to write. It only reads the backup store (ADR-0030). In common backup systems the right to make
  a backup comes with the right to restore one; in Proxmox VE, `VM.Backup` also allows restoring a backup over an
  existing guest.
* No snapshots on the guests' storage: many guests cannot take them, and they fill thin pools (ADR-0083).
* Vördr never deletes a backup.
* A failed backup stops the install, and the run says so with its own code.
* It works the same for the button and for automatic installs in the window.
* The flow does not depend on one backup system. What is specific to a hypervisor or backup store stays in as few places
  as possible.

## Considered Options

1. Keep ADR-0030 alone: a backup at most 26 h old.
2. The hub starts the backup through the hypervisor's API, with credentials of its own.
3. A closed verb `backup` for the hypervisor's agent. The hub orders it before the install and checks the result in the
   backup store.
4. A snapshot of the guest before the install (ADR-0083, rejected).
5. A backup by a separate backup product instead of the hypervisor's own (ADR-0097, rejected).

## Decision Outcome

Chosen: option 3. The operator agreed to the approach and decided that a failed backup stops the install (2026-10-31).

### The flow, independent of the backup system

* **hub.yaml:** `backup.before_install.hypervisor: <node>` names the agent that makes the backups. Every host that the
  backup store knows as a guest of that hypervisor gets a fresh backup before it installs. Other hosts keep the age
  check. The hypervisor itself must not be such a guest (check-config).
* **Hub, before the run:** The age of the last backup no longer stops such a guest. The hub refuses with
  `backup_unreachable` when the backup store does not answer. It refuses with `backup_host_unavailable` when the
  hypervisor's agent is not there: unknown, not approved, not connected, or without the verb.
* **Hub, the run:**
  * The install's run starts in the new phase `backing_up`, already with its final job id.
  * The hub sends the hypervisor's agent a signed job `backup` naming the guest and waits for the result.
  * Then it asks the backup store, with its read-only access, for a backup of the guest that is no older than the
    moment it asked. One minute is allowed for clock skew.
  * Only then does the install go out, with the same job id.
* **Reuse:** A backup of the guest younger than 15 min is used instead of a new one: the scheduled backup ran just
  before, or the same install is tried again.
* **One at a time:**
  * The hub makes one backup at a time. The agent runs one job at a time, and backup tools usually lock per host.
  * A backup waits for a run of the hypervisor's own. When the agent answers `rejected_busy`, the hub tries up to three
    times.
  * Commands for the hypervisor wait while it backs up a guest.
* **No run of the hypervisor:** The backup job is not recorded as the hypervisor's last run. Its outcome belongs to the
  guest's run and to the event log (`install_awaits_backup`, `job_sent` with `guest` and `for`, `pre_install_backup`).
* **Failure:** The run ends with the new code `failed_backup` (a problem), and the reason is in its detail. Nothing was
  installed. A hub restart during the wait ends the run `interrupted`. The hub does not fall back to the age check: a
  fallback would quietly make the way back up to a day old again, the very thing this decision removes. To give up the
  fresh backup on purpose, remove `before_install`.
* **Agent (`guest_backup` in agent.yaml: `storage`, `timeout_min`):**
  * It offers the verb only where it can make backups: the right platform and privileges, the backup tool present, and
    the configured backup target known to the hypervisor. `check-config` checks the target.
  * It backs up only guests of its own host.
  * It backs up each guest at most once per 10 min.
  * It only adds backups; it never prunes.
  * It holds the agent's lock like an install, and a stopping agent waits for the backup.

### What is specific to a backup system

Two places:

1. how the hypervisor's agent makes the backup (`internal/agent/guestbackup.go`);
2. how the hub finds it in the backup store, read-only (`internal/hub/backup.go`).

Neither is a plug-in interface yet. Another hypervisor or backup store needs both places implemented for it; the flow,
the codes and the split of rights above stay as they are.

The first implementation is Proxmox VE with PBS:

* Agent: on Linux, as root, with `vzdump` present and a `pbs:` storage of the configured name in
  `/etc/pve/storage.cfg`. It recognises its own guests by their configuration file under `/etc/pve`. It runs
  `vzdump <id> --storage <s> --mode snapshot --remove 0 --lockwait <timeout/2>`, with notes that name the job. Snapshot
  mode falls back to suspend by itself where the storage cannot take snapshots, never to stop.
* Hub: the PBS API with the read token of ADR-0030 (`DatastoreAudit`). Guests are the groups `vm/<id>` and `ct/<id>`;
  `host/` groups keep the age check.

Rejected:
* Option 1, because the way back may be a day old.
* Option 2, because the hub would hold the right to make backups, which commonly includes restoring them. A compromised
  hub could then roll every guest back to an old backup, and it would act on the hypervisor past its agent's local
  policy.
* Option 4, for the reasons in ADR-0083.
* Option 5, for the reasons in ADR-0097.

### Consequences

* Good, because the way back is minutes old, and with incremental backups it costs little.
* Good, because the hub gets no new right, and the hypervisor's own `agent.yaml` decides whether it backs up and where
  the backups go.
* Good, because a failed or missing backup stops the install visibly (problem and repair in Home Assistant).
* Good, because a further backup system changes two places, not the flow.
* Bad, because an install takes longer by its backup, seconds to minutes per guest.
  * Automatic installs of the same window back up one after the other, so a window with many guests takes the sum of
    their backups.
  * An install that only starts after its host's window has closed is refused by the agent (`rejected_outside_window`)
    and comes in the next window.
* Bad, because depending on the backup system and storage, a guest may be paused for seconds at the end of its backup.
* Bad, because installs of guests now depend on the hypervisor's agent and on the backup store. While either is away,
  no guest installs, and the refusal names the reason. To go back to the age check, remove `before_install`.
* Bad, because there is one more backup per install. The backup system's own retention prunes; Vördr never prunes.
* Neutral: the order of the rollout matters.
  1. The Home Assistant integration that knows the new phase and code.
  2. The hub.
  3. The agent release.
  4. `guest_backup` in the hypervisor's `agent.yaml`.
  5. Only then `before_install` in `hub.yaml`; before that, the hub would refuse every guest install with
     `backup_host_unavailable`.

### Confirmation

Tests:
* Protocol: `TestBackupGuest`.
* Policy: `TestGuestBackupRules`.
* Agent: `TestGuestBackup`, `TestGuestBackupRefusals` and `TestGuestBackupHoldsTheLock`.
* End to end, with the real hub, two agents over the tunnel and a stand-in for the backup store:
  `TestInstallWaitsForAFreshBackup`, `TestRecentBackupIsUsed`, `TestNoFreshBackupNoInstall` and `TestHypervisorAway`.
* Hub: `TestBackupBeforeInstallGate`, `TestOrphanedBackupEndsOnStart`, `TestHypervisorCommandWaitsForABackup` and
  `TestBackupConfigIssues`.

A mutation probe of ten rules (no check in the backup store after the backup, pruning, no rate limit, and others) was
caught 10/10. `vordr-hub backup <node>` names the hypervisor that backs the guest up.

Live check: an install of a guest logs `pre_install_backup` with `code: ok`, and the backup store shows the backup with
the notes "vordr: before install, job <id>".
