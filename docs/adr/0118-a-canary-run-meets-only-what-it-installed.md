---
status: accepted
date: 2026-11-05
decision-makers: kay (2026-11-05, asked to build it); proposed by Claude after the review of ADR-0116
---

# A canary run meets only what it installed

## Context and Problem Statement

[ADR-0107](0107-ring-delay-per-update-ended-by-a-clean-canary-run.md) lets a later ring install an update before
`delay_hours` once it has "met a canary". That means a canary's clean system install started after the update was
first seen. A clean install is an `install` with scope `security` or `all`, `ok`, that changed packages.

That counts every pending update as met, also the ones the canary's run did not install:

* **Outside its scope:** a canary's security run counts for the non-security updates it never touched. A later host
  with `auto_scope: all` then installs them without any canary having run them.
* **Held back:** since [ADR-0116](0116-security-runs-stay-within-security-archives-and-the-release.md), a security
  run holds back fixes that need packages from other sources. They stay pending at the canary, and still count as
  met. The review of ADR-0116 found this.
* **Kept back by apt:** with `install.mode: upgrade`, packages that need new dependencies stay pending.

## Decision Drivers

* "Met a canary" should mean the canary installed it, as far as the hub can tell.
* No protocol change: the hub already gets each canary's pending updates in its reports.
* Fail safe: when the hub cannot tell yet, the update waits for the delay, as without a canary.

## Considered Options

1. **What the canary still reports pending after its run:** an update the canary still has pending once the run is
   over was not installed by that run.
2. **The package names the run reports in its summary:** an update is met only if the canary's run names it. This is
   exact for what was installed, but an update the canary never had (another release, another package set) would never
   meet a canary. That changes ADR-0107 far beyond the gap.
3. **Count a security run only for security updates:** this closes "outside its scope", but not held back or kept back.
4. **Leave it as it is.**

## Decision Outcome

Chosen: option 1.

* **When a canary's run counts:** its last clean install counts only once the hub has a complete report from that
  canary received after the run ended. Agents send one right after a run.
  * Before that report the run counts for nothing. That holds also when the canary had nothing pending before it.
  * The register keeps when the clean install ended (`clean_install_end`) and when the complete report behind the
    pending list was received (`pending_at`).
* **What it left behind:** every update that report still has pending, by source and package name, whenever it was
  first seen.
  * One that reached the canary's list only in that report was not installed by the run either.
  * Nor was one whose target version changed in between.
  * Versions differ between releases, so they are not compared.
* An update of a later host has met a canary when, for at least one canary whose run counts:
  * that canary's clean install started no earlier than the update was first seen; and
  * the canary did not leave an update of the same source and package behind.
* **Unchanged:** the delay, the problem check of each canary, what a clean install is, and an update the canary never
  had pending (it still counts as met by the canary's clean run, as in ADR-0107).

Decided while building, after an independent review (2026-11-05): the first version took as left behind only the
canary's updates first seen before the run started. The review showed three ways that version counted an update as
met although the canary still had it pending:
* the update first reached the canary in the report after the run;
* the canary had nothing pending before the run;
* the update's target version changed.

Hence a report after the run, and everything in it.

### Consequences

* Good: a canary's security run no longer counts for updates outside its scope, nor for held-back fixes. Neither does
  an `upgrade` run for what apt kept back.
* Good: no protocol change; the register keeps two more times.
* Bad: a later host can wait until the next check after the canary's run (the 5-minute tick), instead of starting the
  moment the run ends.
* Bad, once: a register from before this ADR has no end time for the canaries' last clean installs. They count from
  each canary's next clean install on, and until then the delay holds.
* Neutral: an update the canary never had (a package it does not have installed) still counts as met by any clean
  install after it was seen. That is ADR-0107's choice, kept: the canary showed that installing in that window works.

### Confirmation

* `internal/hub/ringclock_test.go`:
  * `TestCanaryRunMeetsOnlyWhatItInstalled`:
    * before the canary reports after its run, nothing is met;
    * a security run that held back one fix: neither that fix nor the updates outside the scope are met;
    * after a run that installed everything, everything is.
  * `TestSoakedWithTwoCanaries`:
    * held back at one canary, installed at the other: met;
    * a run before the update was seen: not met;
    * after the delay: always.
  * `TestLastCleanRun`:
    * no count before a report after the run, or without an end time;
    * after the report, everything in it is left behind, also what was first seen after the run;
    * sources without package names count as one.
  * `TestHeldBackUpdateFirstReportedAfterTheRun`: the review's two cases.
  * The existing tests of ADR-0107 now report the canary's state after its run, as agents do.
* Mutation probes. Each of these turns a named test red:
  * what was left behind ignored;
  * only one canary considered;
  * the time condition dropped;
  * no report after the run required;
  * `pending_at` not kept, with or without pending updates;
  * the end not kept;
  * only updates seen before the run left behind (the first version).
