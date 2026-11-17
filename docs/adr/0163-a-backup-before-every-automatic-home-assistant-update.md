---
status: accepted
date: 2026-11-17
decision-makers: kay (2026-11-17, asked for update.install with backup where the entity supports it and for a decision about the others; accepted with the merge); proposed by Claude
---

# A backup before every automatic Home Assistant update

Carries [ADR-0030](0030-backup-required.md) (no install without a fresh backup, 26 hours by
default) over to Home Assistant's own updates, within
[ADR-0162](0162-home-assistants-own-updates-install-only-when-switched-on-and-a-pause-holds-them.md).

## Context and Problem Statement

Home Assistant's update entities differ in what they offer. Those with the feature `BACKUP` (Core, OS, apps) make a
backup themselves when `update.install` is called with `backup: true`: Core a full automatic backup to the configured
locations, an app a backup of that app. The others (the Supervisor, HACS repositories, ESPHome devices, Zigbee, Matter
and other firmware) do not; Home Assistant ignores `backup` for them or refuses it. Home Assistant has its own
automatic backup (the backup integration, `backup.create_automatic`, the sensors "Backup Manager state" and "Last
successful automatic backup"), usually run once a night.

Which backup comes before an automatic install, and what happens without one?

## Decision Drivers

* No automatic install without a way back, as for hosts (ADR-0030).
* Check first, then act: the feature flag and the backup's age are facts read before the call.
* Do not flood the backup locations: Home Assistant's automatic backups follow its retention, so every extra one
  pushes an older one out.
* A failed backup must not turn into a loop of new attempts.

## Considered Options

1. With `BACKUP`: `update.install` with `backup: true`. Without: Home Assistant's last successful automatic backup must
   be at most 26 hours old; if it is older or missing, Vördr calls `backup.create_automatic` and waits for the new one.
   No backup, no install.
2. As 1, but a new automatic backup right before every entity without `BACKUP`.
3. `backup: true` where supported; nothing for the others.
4. No backup by Vördr; leave it to the person's schedule.

## Decision Outcome

Chosen option: **1**.

* **With `BACKUP`:** `update.install` with `backup: true`; the entity makes it. The run's `backup` is `entity`.
* **Without:** the state of the backup integration's sensor "Last successful automatic backup" (found in the registry
  by platform `backup` and unique id, not by entity id). At most 26 hours old (`BACKUP_MAX_AGE`, like ADR-0030's
  default; not an option): used, `backup` is `existing`. Older or none: `backup.create_automatic`, then the sensor must
  show a backup from that moment on within 5 minutes: `created`. One made for a run serves the next ones as `existing`.
* **No backup, no install:** if the sensor or the action is missing (`backup_unavailable`), the action fails, or it
  does not end within 2 hours (`timeout`), the run ends as `backup_failed` with that reason and installs nothing.
* **No loop:** after `backup_failed`, Vördr makes no new backup for 24 hours and installs no entity without `BACKUP`
  until Home Assistant shows a newer automatic backup (a scheduled one, or one made by hand). Entities with their own
  backup go on. A repair `ha_updates_backup_failed` stays until that newer backup shows.
* **Not while busy:** no run starts and none acts while the backup manager's sensor shows anything but `idle`.

Option 2 makes an automatic backup per HACS repository or device firmware: on a night with twenty such updates it pushes
every older automatic backup out under a retention of a few copies. Option 3 installs HACS repositories, which replace
code inside Home Assistant, without a way back. Option 4 is what ADR-0030 removed for hosts.

### Consequences

* Good, because every automatic install has a backup no older than a day, and the usual nightly backup is used instead
  of a new one.
* Good, because the check is a fact (feature flag, timestamp), not a guess from a failing call.
* Bad, because for device firmware (ESPHome, Zigbee, Matter) a backup of Home Assistant is no way back for the device
  itself; it only keeps Home Assistant's side of it. Such firmware still needs Home Assistant's backup, to keep one
  rule; whether firmware should be exempt is open.
* Bad, because the way back is a restore of Home Assistant, which also undoes other changes made since.
* Bad, because Vördr depends on the backup integration's sensor and action names; a rename leaves entities without
  `BACKUP` uninstalled (`backup_unavailable`), which errs on the safe side.

### Confirmation

`ha/tests/test_ha_updates_install.py`: `backup: true` only for entities with `BACKUP` and no other backup for them; a
backup made when the last one is older than 26 hours and used for the next one
(`test_the_entitys_own_backup_or_home_assistants`); no install after a failed backup, no second attempt, entities with
their own backup go on, the repair goes with a newer backup (`test_no_install_without_a_backup`); no backup integration,
no install (`test_no_backup_integration_no_install`). `ha/tests/test_ha_updates_install_rules.py`: the 26-hour boundary,
a backup from the future does not count, entities without `BACKUP` wait after a failed backup.
