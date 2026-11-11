---
status: accepted
date: 2026-10-29
decision-makers: kay
---

# A fresh backup before every install, checked at the hub

Backfilled on 2026-10-30, decided on 2026-10-29. Sources: `design/architecture.md` 10.2 ("Backup-Prüfung vor dem
Lauf"), the operator's project notes ("backup before updates").

## Context and Problem Statement

The operator: updates only when there is a safe state to go back to. A daily backup of the guests (first
implementation: Proxmox Backup Server, PBS) that runs at another time of day than the updates gives an older state to
go back to.

## Decision Drivers

* A failed or late backup must stop the installation, not just the clock be right.
* The hub may only read for this.

## Considered Options

* only move the schedule (backup before the update window)
* move the schedule and check before every install

## Decision Outcome

Chosen option: both.

* The backup job runs right before the update window. A button pressed outside the window has the state of the last
  backup.
* Before every `install` the hub asks PBS for the last backup of the host's group: its own read token with
  `DatastoreAudit` only, certificate pinned by fingerprint, maximum age `max_age_hours` (default 26).
* Refusal before a job goes out: `backup_stale`, `backup_missing`, `backup_unreachable`, `backup_unmapped`. Hosts
  without a backup are listed explicitly with a reason in `hub.yaml` (`without`). Checking needs no backup.

### Consequences

* Good, because there is no install without a way back.
* Bad, because the way back is a full restore. A snapshot before every install was examined, but does not work
  for every guest and is not decided here.
* Bad, because a new host needs a backup first, or the hub refuses.

### Confirmation

`TestBackupGate`, `TestInstallWaitsForABackup`, `TestBackupPinsTheCertificate`, `TestBackupConfigIssues`,
`test_install_waits_for_a_backup`; `vordr-hub backup <node>` shows the decision without a job.
