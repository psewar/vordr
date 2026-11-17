---
status: accepted
date: 2026-11-17
decision-makers: kay (2026-11-17, asked for one install at a time, inside the window, only what is ready at that moment, Core, OS and Supervisor last, verified and surviving Home Assistant's restart; accepted with the merge); proposed by Claude
---

# Home Assistant updates install one at a time, with Core, OS and Supervisor last

Within [ADR-0162](0162-home-assistants-own-updates-install-only-when-switched-on-and-a-pause-holds-them.md): how a
run goes, in which order, how it is verified, and how it survives the restart that Core, OS and Supervisor updates
cause. The backup in it is [ADR-0163](0163-a-backup-before-every-automatic-home-assistant-update.md).

## Context and Problem Statement

`update.install` returns when the entity's integration says it is done; for Core and OS that is when Home Assistant
goes down, so the call may never return to the integration that made it. Some entities report the new version only
later (a device that restarts and reconnects), some report progress, some none. Several installs at once compete for
the same Supervisor, network and radio, and an install that restarts Home Assistant cuts off everything else running.
Between choosing an update and acting on it, minutes may pass (the backup), in which a person pauses, the window
closes or a newer version appears.

How does a run go from choosing to a result, and in which order?

## Decision Drivers

* One thing at a time, so that a failure has one cause and a restart cuts nothing off.
* Check first, then act: the plan at the moment of the call decides, not the plan at the moment of choosing.
* A result is a fact: the version the entity reports, not that the call returned.
* A restart of Home Assistant in the middle is the normal case for Core and OS, not an error.
* Results as fixed codes, for automations and texts.

## Considered Options

1. One run at a time; others before Supervisor, Core, OS; one system update per opening of the window and nothing
   after it there; checked again right before `update.install`; verified by `installed_version`; the run kept in the
   Store before the call and checked first after a start.
2. Several runs in parallel, the system updates last.
3. The order of Home Assistant's "update all".
4. Trust the call's return as the result.

## Decision Outcome

Chosen option: **1**.

* **One at a time:** a run starts only when no other runs and no update entity at all reports `in_progress` (also one
  a person started) and the backup manager is `idle`.
* **Order:** every kind but the system ones first, by entity id; then `supervisor`, `core`, `os` (a Core or OS release
  may need a current Supervisor; OS restarts the whole machine). A system update starts only when nothing else is
  `ready`. After a system update has run in an opening of the window, nothing more starts in that opening: it
  restarted Home Assistant or the machine, and that stays the last step until the next opening.
* **Steps:** the backup (ADR-0163); then, right before `update.install`, the plan is computed again from the states as
  they are and the run goes on only if the switch is on, nothing is paused, no other entry installs, the entity is
  still `ready` for the same version and nothing is busy; else it ends as `skipped_paused` or `skipped_not_ready` with
  the reason (`target_changed`, `gone`, `auto_install_off`, `other_entry`, `busy`, or the plan's code such as
  `outside_window`). Then the run is written to `.storage/vordr.ha_updates_runs.<entry_id>`, then `update.install` is
  called, with `version` set to the checked version where the entity has `SPECIFIC_VERSION`.
* **Boundary:** the call is the only place where errors are caught; anything it raises ends as `failed`, a call that
  does not end within 2 hours as `timeout`.
* **Verified:** after the call, `ok` once the entity is no longer in progress and reports the target version (or, a
  newer version having come in between, is up to date on another one than it started from). After 30 minutes without:
  `timeout` while it is still in progress, else `not_reached`.
* **Restart:** at start, before anything else, a kept run is checked: once Home Assistant has started, the same
  verification from the entity's state, the result marked `restarted`. No other run starts until it is decided. If
  the entry unloads while a run goes on, the run is not cancelled (an install may be half done); what it then finds
  decides nothing, and the next start checks it from the Store.
* **Report:** every run ends with the event `vordr_ha_update_finished` (`entity_id`, `kind`, `from_version`,
  `to_version`, `installed_version`, `result`, `reason`, `backup`, `started`, `finished`, `restarted`) and the sensor
  `sensor.vordr_ha_updates_last_run`: its phase while it runs (`backing_up`, `installing`, `verifying`), then its
  result (`ok`, `failed`, `not_reached`, `timeout`, `backup_failed`, `skipped_paused`, `skipped_not_ready`); kept across
  restarts. Its attribute `queue` lists the ready updates in the order they would go.

Option 2 lets a Core update restart Home Assistant in the middle of a firmware flash. Option 3 has no order and no
backup for most entities. Option 4 calls a Core update that never returned a failure and an ESPHome update that
returned before the device came back a success.

### Consequences

* Good, because every run has one cause, one result, and a restart in it is checked, not lost.
* Good, because what changed while preparing (a pause, a closed window, a newer version) stops the run before it acts.
* Bad, because one at a time is slow: many updates take several windows, and Core and OS take one opening each.
* Bad, because the 30 minutes of verification are a guess for slow devices; a version reported later clears the
  failure afterwards ([ADR-0165](0165-a-failed-home-assistant-update-stays-until-an-install-of-it-goes-through.md)).
* Neutral: Vördr does not restart Home Assistant after updates that need a restart (HACS); that stays with the person.
  One entity of a model first (a canary across several devices) is a later change.

### Confirmation

`ha/tests/test_ha_updates_install.py`: one at a time while an install runs; the others first, then the Supervisor, then
nothing more in that opening, Core and OS in the next ones, Core with its version
(`test_one_at_a_time_and_core_os_supervisor_last`); nothing while any entity is in progress
(`test_nothing_starts_while_another_update_installs`) or while a run makes its backup
(`test_nothing_else_starts_while_a_run_makes_its_backup`); every change while preparing stops the run before the call
(`test_checked_again_right_before_acting`); `ok` with the whole event and the sensor kept across a reload
(`test_ok_is_the_target_version_with_the_whole_event`); waiting for progress and version
(`test_waits_for_progress_and_the_version`); `not_reached`, `timeout`, `failed`
(`test_a_failed_install_is_kept_held_and_a_repair`); the run kept before the call and checked first after a restart,
reached or not (`test_a_restart_in_the_middle_of_a_core_update`). `ha/tests/test_ha_updates_install_rules.py`: the order,
the opening of the window (also past midnight), what counts as reached, the fixed codes.
