---
status: accepted
date: 2026-11-04
decision-makers: kay (2026-11-04, chose this among the variants against notification fatigue; accepted once released); proposed by Claude
---

# The ring delay counts per update, and a clean canary run ends it

Refines the rings of [ADR-0036](0036-automatic-install-in-rings.md) (`design/architecture.md` 10.3).

## Context and Problem Statement

Under ADR-0036, hosts after the canaries install automatically only updates that have been pending for `delay_hours`
(default 24), and only while every canary is free of problems. The time was measured with `pending_since`: since when
anything at all has been pending on the host, reset only when nothing is pending any more.

That had two effects:

* An update that appears some hours before the window opens is pending for less than 24 hours then. It waits for the
  window a day later, although the canaries installed cleanly in the first.
* A host that always has something pending keeps an old `pending_since`. A host whose policy installs only security
  updates while other updates wait there is such a host. A new security update on it is due at once, before any
  canary has met it. The rings do not protect exactly the hosts with a backlog.

## Decision Drivers

* Every update meets the canaries, or at least the delay, before the later rings install it.
* When the canaries installed cleanly, the later rings follow in the same window instead of the next one.
* When the canaries had nothing to install, the time-based delay still holds: a broken update is often withdrawn
  upstream within a day.
* It survives a hub restart (the register, not memory), and needs no new switch.

## Considered Options

1. Leave it.
2. Only a shorter `delay_hours`.
3. A first-seen time per pending update. A later-ring host is due when each pending update of its automatic scope has
   either been pending `delay_hours`, or a canary has finished a clean install run that started after the update was
   first seen.
4. As 3, but a canary counts only for the same package in the same version.

## Decision Outcome

Chosen: option 3, since hub 0.22.0.

* The register keeps, per host, the pending updates of its last complete report: source, package, target version,
  whether it is a security update, and when it was first reported. A known update keeps its time from report to
  report; a new target version is a new update. A source that reports a count without a package list counts as one
  update for that source. A report whose sources failed changes nothing (as for `pending_since`).
* The register keeps, per host, when its last clean system install started: verb `install`, scope `security` or
  `all`, result `ok` or `ok_reboot_required`, whoever started it.
* A host after the canaries is due when every pending update in its automatic scope is either pending for
  `delay_hours`, or first seen no later than the start of a canary's last clean install. As before, every canary must
  be free of problems, and the window, the backup check and one attempt per window apply.
* Canaries are unchanged: they install as soon as their window opens.
* After the upgrade, a host has no per-update list until its next report; the updates of that report count as first
  seen then. The delay starts anew once, never shorter.

Rejected:

* **Option 1,** because of the two effects above.
* **Option 2,** because the backlog hosts would still skip the canaries, and every update would get a shorter
  delay, also those that no canary met.
* **Option 4,** because canaries seldom run the same packages as the other hosts (another distribution, another role).
  Almost nothing would match, and everything would wait for the delay as today. A clean canary run shows that the
  window's install path works (agent, package manager, backup, health checks). Where no canary ran cleanly after an
  update appeared, that update still waits for the delay.

### Consequences

* Good, because an update that a canary met in a clean run reaches the later rings in the same window, minutes after
  the canary finished.
* Good, because a host with a backlog no longer installs a new update before the canaries.
* Bad, because a canary's clean run says little about packages it does not have: those now get as little as an hour
  instead of 24. A regression that shows later is caught by the health checks, the backup before the install
  ([ADR-0096](0096-fresh-backup-right-before-the-install.md)) and the problem alarm, not by the canary.
* Bad, because on a host where new updates keep arriving and no canary runs in between, the install waits until one
  batch is old enough. The problem `stale` points at it if it lasts.
* Neutral: the register grows by the pending list of each host, dozens of short entries.

### Confirmation

* `TestAutoInstallRings` (unchanged): after `delay_hours` the later ring is due, a failed canary run stops it.
* `TestAutoInstallCleanCanaryRunEndsTheDelay`: a clean canary install that started after the updates were first seen
  makes the later ring due within the window; a failed one or one that started before does not.
* `TestAutoInstallNewUpdateWaitsOnABacklogHost`: on a host with an old backlog, a new update keeps the host waiting.
* `TestPendingUpdatesKeepTheirFirstSeen` (register): times survive reports, a new version is new, a failed report
  changes nothing, nothing pending clears the list.
* A live check after the upgrade to this hub release: the register fills each host's list with its first report
  after the restart, every update first seen then, and no clean install time yet. A host that has not reported since
  keeps an empty list, and no host becomes due early. A clean canary run that ends the delay shows in the hub log as
  `auto_install_started` with ring `later` within the same window, and the register's `clean_install_at` gives the
  canary run it followed.
