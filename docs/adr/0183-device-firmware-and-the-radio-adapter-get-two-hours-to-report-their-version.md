---
status: accepted
date: 2026-11-17
decision-makers: kay (2026-11-17, decided after the merge of ADR-0164); proposed by Claude
---

# Device firmware and the radio adapter get two hours to report their version

Refines [ADR-0164](0164-home-assistant-updates-install-one-at-a-time-with-core-os-and-supervisor-last.md), which waits
30 minutes after `update.install` for the entity to report the target version. ADR-0164 stays as it is otherwise.

## Context and Problem Statement

After `update.install` returns, Vördr waits until the entity is no longer in progress and reports the target version;
after 30 minutes without that it ends the run as `timeout` (still in progress) or `not_reached`, keeps the failure and
makes a repair ([ADR-0165](0165-a-failed-home-assistant-update-stays-until-an-install-of-it-goes-through.md)). A Zigbee
or Thread device that flashes its firmware over the mesh, restarts and rejoins can take far longer than that; so can a
radio adapter. Thirty minutes turned slow but sound firmware updates into failures.

How long does a run wait for the version, per kind?

## Decision Drivers

* A slow but sound update must not become a kept failure.
* A run that hangs must still end: one install at a time means a long wait holds every other update.
* The length is a fact of the kind, from the registries, not an option.

## Considered Options

1. 30 minutes for every kind (as before).
2. Device firmware and radio 2 hours, all others 30 minutes.
3. 2 hours for every kind.
4. An option in the form.

## Decision Outcome

Chosen option: **2**.

* `verify_timeout(kind)`: `VERIFY_TIMEOUT_FIRMWARE` (2 hours) for the kinds `firmware` and `radio`,
  `VERIFY_TIMEOUT` (30 minutes) for Core, OS, Supervisor, apps, HACS, ESPHome and `other`. It holds after
  `update.install` and for a run checked again after a restart of Home Assistant.
* The time limit of the call itself (2 hours) and everything else of ADR-0164 stay.

Option 1 fails sound firmware updates. Option 3 holds every other update for 2 hours behind a Core or app run that
will not come back. Option 4 asks the person for a number they cannot know.

### Consequences

* Good, because a firmware update that takes an hour ends `ok`.
* Bad, because a firmware run that hangs holds the next update up to 2 hours longer.

### Confirmation

`ha/tests/test_ha_updates_install_rules.py`: 2 hours for `firmware` and `radio`, 30 minutes for every other kind.
`ha/tests/test_ha_updates_install.py`: a firmware run is still verifying after 31 and 119 minutes and ends
`not_reached` after 2 hours; an app's run ends after 30 minutes
(`test_a_failed_install_is_kept_held_and_a_repair`).
