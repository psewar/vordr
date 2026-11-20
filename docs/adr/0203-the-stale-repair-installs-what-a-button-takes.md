---
status: accepted
date: 2026-11-20
decision-makers: kay (2026-11-20, asked that repairs he cannot fix in the user interface become fixable there; accepted with the merge); proposed by Claude
---

# The stale repair installs what a button takes

Extends [ADR-0197](0197-the-stale-repair-names-only-a-way-that-exists.md), whose text variants stay.

## Context and Problem Statement

A host whose updates have been pending longer than `stale_after_days` has the problem `stale`
([ADR-0014](0014-stale-after-seven-days.md)), shown as the repair `problem_<node>`. Where the install button takes
some of those updates, the repair says "install the updates, by hand or with the button", but cannot act itself: the
operator has to leave the repair, find the host's update entity and install there. The repairs for a needed reboot
([ADR-0132](0132-a-needed-reboot-is-a-repair-from-the-first-report.md)) and a kept failed install
([ADR-0148](0148-a-kept-failed-install-is-a-repair-that-installs-again.md)) already have a fix that sends the
button's command.

Where no button takes them, ADR-0197 names the way that exists: install by hand, or widen the host's policy. The
policy lives in the host's `agent.yaml` and stays closed by default; Home Assistant cannot widen it.

## Decision Drivers

* A repair that names a button offers what the button does.
* No new way around the host's policy: the fix sends nothing a button could not send.
* The problem still reaches someone who decides (ADR-0014): it ends only when the updates are installed.
* No change to the hub or the protocol: the hub already sends `installable` and the capabilities
  ([ADR-0106](0106-update-entity-on-only-when-vordr-would-install.md)).

## Considered Options

* a fix for the `stale` repair that sends the command of the button whose updates the hub counts as installable
* a fix that installs every pending update, also those the policy leaves out
* a fix that defers the problem without installing
* no fix: the text names the button

## Decision Outcome

Chosen: the fix sends the button's command.

* `problem_<node>` is fixable, with the text `stale_install`, while the problem is `stale` alone, the hub sends
  `installable` above zero, and the hub offers the button those updates belong to: the install button (capability
  `install`), or the button for security updates where it is the only one (`install_security`). ADR-0106 counts
  `installable` for the same button.
* Its dialog names how many of the pending updates the button takes, out of how many. "Fix" sends `install` or
  `install_security` without a reason, like the button; the hub gives it the reason outside the window where the agent
  takes it ([ADR-0109](0109-the-install-button-installs-at-once.md)). Window, minimum interval and backup check hold it
  as they hold the button, and their refusals have the button's texts.
* At confirmation the flow checks again: the connection, the host, a problem that is still `stale`, and a button that
  still takes some of the updates; each has its own reason, and nothing is sent.
* A run that installs them ends the problem, and the repair goes with it. Updates no button takes keep it.
* Unchanged: the problem sensor's `next_step` and `vordr_problem` keep the `stale` text; without the count (an older
  hub), with another reason besides `stale`, or where no button takes the updates, the repair keeps its ADR-0197 text
  without a fix.

Rejected:

* **Installing every pending update,** because the host's policy decides what Vördr installs; a repair in Home
  Assistant that widened it would make the policy in `agent.yaml` meaningless. Where the policy should take more, it
  is widened on the host.
* **Deferring without installing,** because it only hides the problem; Home Assistant's own "Ignore" already does that
  for the person who wants it.
* **No fix,** because it sends the operator to look for a button the repair could press.

### Consequences

* Good, because a repair that names a button can be fixed where it is shown.
* Good, because the fix cannot do more than a button: the policy, the window, the minimum interval and the backup check
  stay in force.
* Bad, because a repair whose updates the button takes only in part stays after the fix, with the rest pending; its
  dialog says so.
* Bad, because another fixable text in every language.

### Confirmation

`ha/tests/test_repairs.py`: the fixable repair and its dialog (`test_stale_repair_installs_what_the_button_takes`),
the security button where it is the only one, no fix without the count, without a button, with another reason or with
nothing the button takes (`test_stale_repair_without_a_fix`), the check at confirmation and the refusals.
`ha/tests/test_entities.py`: the repair follows the count and the capabilities without a new `vordr_problem`
(`test_stale_hint_names_a_way_that_exists`). `ha/tests/test_texts.py`: the texts in every language
(`test_stale_install_texts`, `test_repair_refusals_have_their_texts`).
