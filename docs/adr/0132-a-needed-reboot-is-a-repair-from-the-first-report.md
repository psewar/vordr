---
status: accepted
date: 2026-11-08
decision-makers: kay (2026-11-08, after needed reboots had gone unnoticed); details proposed by Claude
---

# A needed reboot is a repair from the first report

Refines [ADR-0024](0024-repair-issues.md) (repair issues per host problem) for the reboot a host needs, and how it
meets the `reboot_pending` problem of [ADR-0014](0014-stale-after-seven-days.md). The hub, the agent and the protocol
stay as they are: the host view already says whether a reboot is required, since when, and whether the hub offers the
reboot ([ADR-0041](0041-reboot-with-window-and-proof.md)).

## Context and Problem Statement

After an install that needs a reboot, Home Assistant shows it in two places: the binary sensor "reboot required" and
the run-finished event, from which a site can send a one-time notification. Neither stays in view: the sensor is one
of many entities, and a notification that is swiped away is gone. Only after `reboot_pending_after_days` (7) does the
hub call it a problem, which brings a repair and an alarm. Until then a host can run for days on a kernel or libraries
that are installed but not loaded, and nobody sees it.

Where should a needed reboot remind people, and how does that fit the problem after 7 days?

## Decision Drivers

* A needed reboot stays visible until it is done, without anyone having to remember a notification.
* Where Vördr may reboot the host, the reminder can do it; where not, it says so.
* No alarm for a reboot needed for a few hours (ADR-0014 stays: the problem and its alarm come after 7 days).
* One entry per matter: the same reboot must not show as two repairs.

## Considered Options

1. A repair `reboot_<node>` from the first report, fixable where the hub offers the reboot.
2. A persistent notification or a recurring push, built by each site as an automation.
3. Lower `reboot_pending_after_days`, so the existing problem repair comes sooner.

## Decision Outcome

Chosen option: **1**. The integration keeps a repair `reboot_<node>` for as long as the host reports "reboot required"
(only a definite yes; unknown is no claim). Where the host's capabilities include `reboot`, the repair is fixable: its
fix asks for confirmation and then sends the same command as the host's reboot button. Otherwise it says to reboot by
hand. It goes away by itself once the host reports that it no longer needs a reboot.

* **Severity:** a warning; an error once the hub reports the problem `reboot_pending`.
* **One repair:** while the host's problem consists of nothing but `reboot_pending` and the host reports "reboot
  required", the problem's own repair is left out, because the reboot repair carries it and can also act. The problem
  sensor and the `vordr_problem` event, and so any alarm built on them, stay unchanged. A problem with other reasons
  keeps its repair.
* **After the fix:** the hub accepted the reboot, Home Assistant closes the repair, and the integration keeps it away
  while the host still reports the reboot as it goes down: until the reboot run finishes, at most 30 minutes. If the host
  still needs a reboot after that, the repair comes back.
* **Checked again at confirmation:** a repair may be older than the host's state. The fix refuses, each case with its
  own reason and without a command, when the hub is not connected, the host is gone, it no longer needs a reboot, or the
  reboot is no longer offered; a refusal by the hub shows its code.

Option 2 leaves every site to build the same thing and still loses the reminder to a swiped notification. Option 3
turns a routine reboot into an alarm and breaks the driver "no alarm after a few hours".

### Consequences

* Good, because the repairs list shows every host waiting for a reboot, and one click reboots it where that is allowed.
* Good, because nothing changes at the hub or the agent; the fix uses the existing command and its checks (window,
  role, policy).
* Bad, because the repairs list now also carries a routine matter. It stays a warning until the hub's limit is reached.
* Neutral: a site's own notification on `vordr_run_finished` keeps working; it is no longer the only reminder.

### Confirmation

`ha/tests/test_repairs.py`: the repair from the first report, fixable or manual by capability, gone when the reboot is
no longer needed or the host is removed; one repair for the long-pending reboot and a separate one for other reasons;
the fix sends the reboot, keeps the repair away until the reboot ran, and refuses with its own reasons.

## More Information

* Texts: `issues.reboot_required` (with the fix flow) and `issues.reboot_required_manual` in every language.
* Design: `design/ha-integration.md`, section 6a.
