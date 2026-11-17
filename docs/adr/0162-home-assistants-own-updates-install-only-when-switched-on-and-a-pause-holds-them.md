---
status: accepted
date: 2026-11-17
decision-makers: kay (2026-11-17, asked for Home Assistant's own updates to install under Vördr's rules behind an explicit switch and a pause; accepted with the merge); proposed by Claude
---

# Home Assistant's own updates install only when switched on, from one entry, and a pause holds them

Builds on [ADR-0158](0158-home-assistants-own-updates-follow-vordrs-rules-in-the-integration.md), which computes per
update entity a code and installs nothing. The backup before an install is
[ADR-0163](0163-a-backup-before-every-automatic-home-assistant-update.md), the order and the run
[ADR-0164](0164-home-assistant-updates-install-one-at-a-time-with-core-os-and-supervisor-last.md), failures
[ADR-0165](0165-a-failed-home-assistant-update-stays-until-an-install-of-it-goes-through.md).

## Context and Problem Statement

The plan says which of Home Assistant's own updates Vördr's rules let through now (`ready`). The goal is to keep the
smart home up to date without a person pressing every button, so Vördr is to install what is `ready`. An integration
that installs Core, OS, apps and firmware by itself is a strong power: one wrong option, or one update of the
integration that changes a default, and a whole home is updated at night.

Who switches installing on, how is it held at short notice, and which of several config entries (one per hub) acts?

## Decision Drivers

* Closed by default, like the agent's local policy: nothing installs until a person allows kinds or entities, a
  window, and installing itself.
* An existing configuration keeps meaning what it meant: options saved by the plan-only version install nothing.
* A person can hold everything at once, at short notice, without touching the rules, and the hold survives a restart.
* The hub has no say, as in ADR-0158.
* Never two installers for the same entities.

## Considered Options

1. An option "Install automatically" in the item "Home Assistant updates", off by default, on top of the allowlist and
   the window; besides, a switch entity "Pause Home Assistant updates", kept in the integration's Store.
2. Install whatever is allowed as soon as the options allow kinds or entities (no separate switch).
3. Only a switch entity, on meaning "install".
4. Let each config entry install with its own options.

## Decision Outcome

Chosen option: **1**.

* **Switch:** `auto_install` under `ha_updates` in the entry's options, a boolean, off by default. Options saved
  without it (by the plan-only version) read as off. A value that is not a boolean fails the options check, and stored
  options that fail it allow nothing (ADR-0158).
* **Pause:** `switch.vordr_ha_updates_paused` on the device "Vördr Home Assistant". While on, no run starts; a run
  that is preparing (making Home Assistant's backup) ends as `skipped_paused` at the check right before the install;
  an install already called runs to its end, because cutting it off may leave a device half flashed. Off by default;
  kept in `.storage/vordr.ha_updates_runs.<entry_id>` across restarts.
* **One entry installs:** the options flow refuses to switch installing on while another Vördr entry has it on
  (`auto_install_other_entry`); if two enabled entries have it on anyway (options edited by hand), neither installs,
  the log says so once, and the last-run sensor's attribute `installer` says `other_entry`.
* **As a fact:** the attribute `installer` of `sensor.vordr_ha_updates_last_run` is `off`, `paused`, `other_entry`,
  `idle` or `running`.
* **Never by Vördr**, whatever the options say: its own update entities (registry platform `vordr`), as ADR-0158.

Option 2 turns a plan that people already use to look at into one that acts, without a separate step; an allowlist
chosen for the dry run would install at the next window. Option 3 leaves the decision outside the options, where any
user with access to entities can flip it. Option 4 installs the same entities twice under two sets of rules.

### Consequences

* Good, because nothing installs until a person turns it on, and turning it on is a step of its own.
* Good, because the pause holds everything without changing the rules, and an automation can switch it (for example
  while someone is away, or while a backup check of the site fails).
* Bad, because the pause does not stop an install already called.
* Bad, because a second hub's entry cannot take over without switching installing off on the first.
* Neutral: canary groups across several devices of one model are not part of this; they come with a later change.

### Confirmation

`ha/tests/test_ha_updates_install.py`: nothing installs with installing off, while paused, outside the window, for any
other code than `ready`, and for Vördr's own entities even when the stored options name them
(`test_nothing_installs_until_switched_on_unpaused_and_in_the_window`, `test_only_what_is_ready_installs`); the pause
kept across a reload (`test_the_pause_is_kept_and_holds_every_install`); a pause while the backup is made ends the run
without an install (`test_checked_again_right_before_acting`); the options refuse a second installing entry and two
entries switched on by hand install nothing (`test_one_entry_installs_never_two`).
`ha/tests/test_ha_updates_install_rules.py`: off unless switched on, also for options saved before the switch existed.
`ha/tests/test_ha_updates_plan.py`: a switch that is not a boolean is refused.
