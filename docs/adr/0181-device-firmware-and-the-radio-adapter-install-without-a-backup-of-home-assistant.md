---
status: accepted
date: 2026-11-17
decision-makers: kay (2026-11-17, decided on the open question of ADR-0163 after its merge); proposed by Claude
---

# Device firmware and the radio adapter install without a backup of Home Assistant

Refines [ADR-0163](0163-a-backup-before-every-automatic-home-assistant-update.md), which requires a backup before every
automatic install and left open whether device firmware should be exempt. ADR-0163 stays as it is for every other kind.

## Context and Problem Statement

ADR-0163 makes Vördr wait for Home Assistant's automatic backup (at most 26 hours old, made if there is none) before
an entity without its own backup feature is installed, and installs nothing without it. For device firmware
(kind `firmware`) and the radio adapter's firmware (kind `radio`) such a backup is no way back: it restores Home
Assistant, not the device. It still made them wait for a backup, and after a failed backup they waited a day or until a
newer backup showed, for nothing they could be rolled back with.

Do device firmware and the radio adapter need Home Assistant's backup before Vördr installs them?

## Decision Drivers

* A rule should protect what it says it protects.
* Core, OS, Supervisor, apps, HACS and ESPHome keep the backup rule: a backup of Home Assistant does undo those (or the
  configuration they change).
* The kinds come from the registries, as in [ADR-0158](0158-home-assistants-own-updates-follow-vordrs-rules-in-the-integration.md).
* What protects firmware instead is its age and verification.

## Considered Options

1. Keep the backup rule for all kinds (as before).
2. Device firmware and the radio adapter install without Home Assistant's backup; an entity of theirs with its own
   backup feature still gets `backup: true`.
3. No backup rule for any entity without its own backup feature.

## Decision Outcome

Chosen option: **2**.

* **Exempt:** the kinds `firmware` and `radio` need no backup of Home Assistant (`needs_ha_backup`). A run of theirs
  does not check or make one; its `backup` is `exempt` in the event `vordr_ha_update_finished` and on the last-run
  sensor. An entity of theirs that has the `BACKUP` feature is still installed with `backup: true` (`entity`).
* **Not held:** after a failed backup of Home Assistant, they go on; only entities that need it wait (ADR-0163).
* **Unchanged:** Core, OS, Supervisor, apps, HACS, ESPHome and `other` keep ADR-0163 in full.
* What stands in for the backup: the longer minimum age
  ([ADR-0180](0180-device-firmware-and-the-radio-adapter-wait-at-least-14-days.md)), one install at a time and the
  verification by the version ([ADR-0164](0164-home-assistant-updates-install-one-at-a-time-with-core-os-and-supervisor-last.md)).

Option 1 keeps a wait that protects nothing and blocks firmware behind a failed backup. Option 3 would install HACS
repositories, which replace code inside Home Assistant, without a way back.

### Consequences

* Good, because firmware no longer waits for, or is held by, a backup that cannot roll it back.
* Good, because fewer extra automatic backups are made for runs that do not need them.
* Bad, because a broken firmware still has no way back through Vördr; the age, the hold after a failure
  ([ADR-0165](0165-a-failed-home-assistant-update-stays-until-an-install-of-it-goes-through.md)) and the vendor's own
  means are what is left.
* Bad, because ESPHome devices, also firmware, keep the backup rule: they are told by their integration as a kind of
  their own, and a backup of Home Assistant does keep their configuration.

### Confirmation

`ha/tests/test_ha_updates_install_rules.py`: `firmware` and `radio` need no backup of Home Assistant, every other kind
does unless it backs itself up; after a failed backup a firmware entity is next and a HACS card waits.
`ha/tests/test_ha_updates_install.py`: with no backup integration at all, device firmware and the radio adapter
install with `backup` `exempt`, while a HACS card ends `backup_failed` and an ESPHome device waits.
`ha/tests/test_texts.py`: `exempt` has its text in every language.
