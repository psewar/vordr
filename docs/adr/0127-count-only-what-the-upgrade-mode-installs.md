---
status: accepted
date: 2026-11-06
decision-makers: kay (2026-11-06, "Alles machen", after the check of a point noted on 2026-11-05 showed it still open); proposed by Claude
---

# Count only what the upgrade mode installs

Refines [ADR-0076](0076-full-upgrade-by-default.md) (full-upgrade by default) for hosts that choose
`install.mode: upgrade`, and [ADR-0106](0106-update-entity-on-only-when-vordr-would-install.md) (update entity on
only when Vördr would install) for what that means. Everything else there stays.

## Context and Problem Statement

The apt check always simulates `apt-get full-upgrade` and counts every package in that plan as pending. A host with
`install.mode: upgrade` runs `apt-get upgrade` for scope `all`, which leaves packages that need others added or removed:
a new kernel, firmware, a changed dependency. For such a host:

* Home Assistant shows the system entity as an update while the install button would take nothing of it (ADR-0106 is
  about what Vördr would install).
* The automatic install of scope `all` starts a run in every window that changes nothing.

The ring part is solved already: a run without changes ends no canary wait (ADR-0107, after a review). The counts
were still open. The default is `full-upgrade`; this concerns sites that choose `install.mode: upgrade`.

## Decision Drivers

* The system entity is on only when the install button would take something (ADR-0106).
* No automatic run that cannot change anything.
* What is available stays visible: a kept-back kernel is still pending.
* The check stays one source of truth for the host; the hub decides nothing from text.

## Considered Options

1. Leave it, since the default is `full-upgrade`.
2. Simulate the run's own mode in the check and mark what it leaves.
3. Count only the run's mode, and drop the packages it leaves from the report.

## Decision Outcome

Chosen: option 2.

* **Agent:** with `install.mode: upgrade`, the check also simulates `apt-get -s upgrade`. Each package of the full plan
  that the upgrade plan lacks is marked `kept_back`, and the source counts them (`kept_back`). Pending and security
  counts stay as they are. With any other mode nothing more is asked. When the second simulation fails, nothing is
  marked: the counts are as before this change.
* **Hub:** for a host whose install button takes scope `all`, `installable` leaves the kept-back updates out. The host
  view names them (`kept_back`). The automatic install of scope `all` starts no run when only kept-back updates are
  pending.
* **Unchanged:**
  * scope `security`: its runs install by name and take what they need, kept back or not;
  * the rings, which go by what a run installed (ADR-0118).

Rejected:

* **Option 1,** because a policy value the agent accepts must not leave the entity on and the automation running for
  nothing.
* **Option 3,** because a pending kernel the host's mode leaves is exactly what the operator should see, and the
  security scope would lose packages it does install.

### Consequences

* Good, because on such a host the system entity goes off when the button would take nothing, and the automatic runs
  that change nothing stop.
* Good, because the kept-back updates stay visible with a count.
* Bad, because the check simulates twice on such hosts.
* Bad, because it is a new field from agent to hub: the hub must be released first, as for every such field.
* Neutral: hosts that run `full-upgrade` see no change.

### Confirmation

* `internal/source/apt`: `TestCheckMarksKeptBack`: with `upgrade`, the packages only `full-upgrade` takes are marked
  and counted, pending and security unchanged; `full-upgrade` asks nothing more; a failing second simulation marks
  nothing.
* `internal/hub`:
  * `TestKeptBackIsNotInstallable`: `installable` leaves them out for scope `all`, the view names them, the security
    button counts them;
  * `TestAutoInstallSkipsKeptBack`: no automatic run of `all` for kept-back updates only, one with an update it takes.
