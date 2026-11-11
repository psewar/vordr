---
status: accepted
date: 2026-10-29
decision-makers: kay ("implement the proposals")
---

# The hub starts installs by itself, in rings

Backfilled on 2026-10-30, decided on 2026-10-29. Sources: `design/architecture.md` 10.3.

## Context and Problem Statement

Without automatic installs, security updates wait until someone presses the button. Pending updates pile up on the
hosts, and the `stale` alarms follow.

## Decision Drivers

* Security updates should arrive without anyone pressing a button.
* A bad update must not hit every host at once.
* The window stays the host's decision ([ADR-0006](0006-local-policy-decides.md)).

## Considered Options

* `install.auto: hub` – the hub plans runs in rings
* `install.auto: local` – the agent triggers by itself in its window, even without the hub
* `install.auto: off` – button only

## Decision Outcome

Chosen option: `hub`. `local` is in the draft and accepted by the policy, but nothing triggers a run
for it.

* Both sides must want it: the agent says `install.auto: hub`, and the hub has an `auto_install` section.
* The agent reports its window and time zone in the hello. The hub plans within that window instead of collecting
  `rejected_outside_window`; the agent still checks the window itself.
* Every 5 minutes per host: inside the window, connected, no open run, something to do. At most one attempt per host
  in 20 hours. The backup check comes first; without a fresh backup there is only the event `auto_install_skipped`.
* Rings: canaries install as soon as their window opens. The others only install updates pending for `delay_hours`
  (default 24) and only while every canary is free of problems.
* The run carries `by: auto`.

### Consequences

* Good, because the same update reaches the canary first.
* Bad, because nothing installs while the hub is down (HA then sees the hub offline).
* Bad, because it only takes effect once hosts and hub opt in and canaries are chosen
  ([ADR-0058](0058-site-decision.md)).

### Confirmation

`TestAutoInstallRings`, `TestAutoInstallOnlyWhenItMakesSense`, `TestAutoInstallWindowInTheHostsZone`,
`TestAutoInstallReasons`, `TestSummaryAnnouncesWindowAndZone`.

## More Information

The scope of automatic runs: [ADR-0037](0037-auto-install-uses-default-scope.md), superseded by
[ADR-0059](0059-own-auto-scope.md).
