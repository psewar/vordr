---
status: accepted
date: 2026-11-17
decision-makers: kay (2026-11-17, decided after the merge of ADR-0165 that the fix installs at once, as in ADR-0148); proposed by Claude
---

# The repair's fix installs a failed Home Assistant update at once

Refines [ADR-0165](0165-a-failed-home-assistant-update-stays-until-an-install-of-it-goes-through.md): its repair
`ha_update_failed_<object id>` stays, and so does everything else it decides, but the fix no longer lets Vördr try
again in the next window; it installs at once, as [ADR-0148](0148-a-kept-failed-install-is-a-repair-that-installs-again.md)
does for hosts.

## Context and Problem Statement

A failed install of one of Home Assistant's own updates is kept, holds its version and is a fixable repair. Its fix
released the hold so that the next window would try the version again. A person who has just fixed the cause and
confirms the repair then waits for the window, often a week, and the outcome comes when nobody looks. For hosts, the
fix of a kept failed install sends the install at once.

What does confirming the repair do?

## Decision Drivers

* The person who confirms the fix is there now and wants to see the outcome now.
* Every rule but the window still holds: installing switched on, not paused, one entry, allowed, not a major
  version, old enough, the backup before it where one is needed, the check right before acting.
* One install at a time ([ADR-0164](0164-home-assistant-updates-install-one-at-a-time-with-core-os-and-supervisor-last.md)).
* Nothing is queued silently: if it cannot install now, the fix says why with a fixed code.
* Nothing installs later behind the person's back.

## Considered Options

1. The fix releases the hold; the next window tries again (as before).
2. The fix installs at once, outside the window, under every other rule, and refuses with a code when it cannot.
3. The fix queues the install for the moment the other install ends.

## Decision Outcome

Chosen option: **2**.

* **Checked first** (`async_install_now`), each refusal a fixed code the fix flow aborts with, with a text in every
  language: `no_longer_needed` (the failure is gone), `busy` (another install runs, Vördr's or anyone's, or Home
  Assistant's backup manager is working), `paused`, `auto_install_off`, `other_entry`, `not_ready` (the plan does not
  let the entity through apart from the window: not allowed, major, too new, skipped, gone), `backup_held` (it needs
  Home Assistant's backup, which failed lately, ADR-0163); `entry_not_loaded` stays as before. Nothing is queued.
* **Then at once:** the run starts as the one run, for what the entity offers now, with the backup before it where
  needed and the check right before `update.install`; the window is the only rule it does not wait for. The run is
  marked as the fix's; Core, OS and Supervisor do not wait for the end of the other kinds.
* **The repair** goes while the run goes. `ok` clears the failure (ADR-0165). A kept result (`failed`,
  `not_reached`, `timeout`) keeps a new failure and the repair comes back. A run that does not get to install
  (`skipped_*`, `backup_failed`) holds the version again and the repair comes back: the next window does not try it by
  itself. A released failure found at start without its run (Home Assistant stopped while the fix prepared) is held
  again too.

Option 1 makes the person wait for a window to see whether the fix worked, and installs later when nobody looks.
Option 3 installs at a moment nobody chose and hides a queue behind a confirmed dialog.

### Consequences

* Good, because the fix behaves like the host's fix: confirm, and the install runs now.
* Good, because the one-at-a-time rule and every other rule stay; a refusal names its reason.
* Bad, because a Core, OS or Supervisor update confirmed outside the window restarts Home Assistant or the machine at
  that moment; the person who confirms chose that moment.
* Bad, because a fix that is refused for `busy` has to be confirmed again afterwards.

### Confirmation

`ha/tests/test_ha_updates_install.py`: confirmed outside the window, the fix installs at once and the repair and the
failure go (`test_the_repairs_fix_installs_at_once_and_the_repair_goes_once_it_goes_through`); a fix that fails again
brings the repair back; while another install runs the flow aborts `busy` and nothing starts afterwards, then
`paused`, `auto_install_off`, `other_entry` and `not_ready`, the repair staying each time
(`test_the_fix_says_why_it_cannot_install_now_and_queues_nothing`); `backup_held`; a fix whose backup fails holds the
version again, the next window does not install it, and a released failure without its run is held at the next
start (`test_a_fix_that_does_not_get_to_install_holds_the_version_again`). `ha/tests/test_texts.py`: every refusal has
its text for every kept result in every language.
