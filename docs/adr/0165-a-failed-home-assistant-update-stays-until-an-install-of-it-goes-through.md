---
status: accepted
date: 2026-11-17
decision-makers: kay (2026-11-17, asked for a repair per failed entity that stays until a later install goes through, mirroring ADR-0124 and ADR-0148, and for no automatic retry; accepted with the merge); proposed by Claude
---

# A failed Home Assistant update stays until an install of it goes through

Mirrors [ADR-0124](0124-a-failed-system-install-stays-until-one-goes-through.md) and
[ADR-0148](0148-a-kept-failed-install-is-a-repair-that-installs-again.md) for hosts, within
[ADR-0164](0164-home-assistant-updates-install-one-at-a-time-with-core-os-and-supervisor-last.md).

## Context and Problem Statement

A run can end as `failed`, `not_reached` or `timeout`. The plan would call the same version `ready` again at the next
tick, and Vördr would install it again and again, each time with the same cause, and each time a failure that nobody
looked at would disappear behind the next attempt. For hosts, a failed install stays a problem until an install goes
through, and the repair lets the operator install again once the cause is fixed.

What happens to an entity whose install failed, and how does it come free?

## Decision Drivers

* A failure stays visible until something shows it is resolved.
* No automatic retry of the same version; a new version is a new chance.
* The person can let it try again in one step, where Home Assistant shows the failure.
* What clears a failure is a fact: the version the entity reports.

## Considered Options

1. Keep the failure per entity in the Store; hold that version (plan code `last_failed`); one fixable repair per entity
   whose fix lets Vördr try again in a window; cleared once the entity reports the version or is up to date, or an
   install of it ends `ok`.
2. Retry the same version in every window.
3. Hold the entity until the person clears it, also for newer versions.
4. Only the event; no hold, no repair.

## Decision Outcome

Chosen option: **1**.

* **Kept:** `failed`, `not_reached` and `timeout` keep a failure for the entity: versions, result, time; in
  `.storage/vordr.ha_updates_runs.<entry_id>`, across restarts. A later failure replaces it. `backup_failed`,
  `skipped_paused` and `skipped_not_ready` installed nothing and keep none (the failed backup has its own hold, ADR-0163).
* **Held:** while the entity offers the failed version, the plan's code is `last_failed` (after `not_allowed` and
  `major`, before the age and the window): not installed again by itself. A newer version is not held; it is tried in
  its window under every rule.
* **Repair:** `ha_update_failed_<object id>`, one per entity, with its own text for each result
  (`ha_update_failed`, `ha_update_not_reached`, `ha_update_timeout`), naming the entity, the versions and the time.
  It stays, also while a newer version waits, until the failure is cleared.
* **Fix:** confirming the repair releases the hold: the failed version may be tried again in a window, under every
  other rule; the repair goes, and comes back if that run fails again. Checked at confirmation: an entry not loaded or a
  failure already gone abort with their own reason.
* **Cleared:** once the entity reports the failed version (installed by hand, or reported late) or is up to date with
  another one, once a later install of it ends `ok`, or once it is gone from the entity registry. An entity that is
  unavailable keeps it.

Option 2 repeats a broken install every night, with a backup each time. Option 3 holds back the fix the vendor ships
for the very problem. Option 4 loses the failure with the next run, which ADR-0124 removed for hosts.

### Consequences

* Good, because a failure is seen, held and cleared by a fact, not by another attempt.
* Good, because a person decides about a retry in the place Home Assistant shows it.
* Bad, because a failure caused by something passing (the network at night) also holds the version until a newer one
  or a person comes.
* Neutral: the fix does not install now; installing now stays with Home Assistant's own install button.

### Confirmation

`ha/tests/test_ha_updates_install.py`: each kept result is a fixable repair with its text and names, held in the next
window, kept across a reload (`test_a_failed_install_is_kept_held_and_a_repair`); the fix releases it, the next window
installs, the repair and the failure go, a second release finds nothing
(`test_the_repair_lets_it_try_again_and_goes_once_it_goes_through`); a newer version is tried while the repair stays
until it goes through (`test_a_newer_version_is_tried_and_the_repair_stays_until_it_goes_through`); installed by hand
clears it (`test_installed_by_hand_clears_the_failure`). `ha/tests/test_ha_updates_install_rules.py`: the place of
`last_failed` among the codes, what a release leaves out, when a failure is cleared. `ha/tests/test_texts.py`: every
kept result has its repair text in every language with exactly the names the integration fills.
