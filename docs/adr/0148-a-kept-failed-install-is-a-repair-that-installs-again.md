---
status: accepted
date: 2026-11-11
decision-makers: kay (2026-11-11, chose a button in the repair; accepted with the merge); proposed by Claude
---

# A kept failed install is a repair that installs again

## Context and Problem Statement

A failed install of the system packages stays the host's problem until an install goes through
([ADR-0124](0124-a-failed-system-install-stays-until-one-goes-through.md)). To clear it after fixing the cause by hand,
the operator installs again: a run that goes through, even with nothing left to install, clears it. The automatic rings
and the agent rollout follow the problem, so a canary's kept failure holds them.

In Home Assistant that step had no place. The problem's repair is not fixable and says to decide by hand. The install
button of the update entity shows only while updates are pending, and after a fix by hand nothing is pending any more.
What remained was the action `vordr.install_outside_window`, which an operator has to know of.

## Decision Drivers

* The operator clears a kept failure where Home Assistant shows it, in one step.
* The clearing still proves the fix, as ADR-0124 wants: an install that goes through, not an acknowledgement.
* One repair per cause, as the reboot repair does it
  ([ADR-0132](0132-a-needed-reboot-is-a-repair-from-the-first-report.md)).
* No change to the hub or the protocol.

## Considered Options

* a repair of its own that installs again
* make the problem's repair fixable for every problem code
* clear a kept failure from a precheck once the host reports nothing pending

## Decision Outcome

Chosen: a repair of its own, `install_again_<node>`.

* It exists while the hub keeps a failed install for the host (`failed_install` in the host's view) and offers the
  install there (capability `install`). It names the failed run's code and detail.
* Its fix confirms, checks again (connection, host, kept failure, capability; each refusal with its own reason) and
  sends the same command as the install button. A run that goes through clears the failure at the hub, and the repair
  goes with it; a run that fails again keeps both.
* If the problem consists only of the kept failed install, the problem's own repair is left out, as for the
  long-pending reboot; the problem sensor and the event `vordr_problem` stay. Another reason keeps its own repair.
  Without the install offered, the problem's repair stays as before.

Rejected:

* **Fixable problem repairs for every code,** because Home Assistant takes a fix flow's texts from the repair's
  translation key, so each of the 37 problem texts would need the same dialog texts in every language.
* **Clear a precheck failure when nothing is pending,** because the hub would decide on the host's report what
  ADR-0124 leaves to a run that proves it; and a failure kept on a canary would vanish without anyone looking at it.

### Consequences

* Good, because a kept failure on a canary no longer needs a hidden action to clear.
* Good, because the repair tells what failed and with which detail.
* Bad, because the fix starts an install even outside the host's window, as the install button does; on a host whose
  cause is not fixed it fails again.

### Confirmation

`ha/tests/test_repairs.py`: the kept failed install is one fixable repair with code and detail, and the problem's
repair stays without the install offered or with another reason
(`test_kept_failed_install_is_one_fixable_repair`); the fix sends the install and the repair goes with the failure
(`test_fix_installs_again_and_goes_with_the_failure`); changes between opening and confirming abort without a command,
a refusal of the hub keeps the repair (`test_install_again_checks_again_at_confirmation`).
