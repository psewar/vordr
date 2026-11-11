---
status: proposed
date: 2026-11-04
decision-makers: kay (2026-11-04, asked whether 7 days are too long now that vulnerabilities are exploited within days; chose 2 days with a floor of 1 and the normal age for the rest of the plan, "Ja, so umsetzen"); proposed by Claude
---

# A shorter minimum age for releases that fix a known vulnerability

Refines [ADR-0068](0068-minimum-age-for-target-versions.md) (minimum age of pip and npm target versions). Its rule
stays for every other update.

## Context and Problem Statement

A pip or npm update installs only releases that have been public for `packages.min_age_days` (default 7), the package
and every dependency in its plan (ADR-0068). The minimum age protects against a malicious new release, which is
usually found and pulled within hours to days.

It also delays the other case. When the installed version has a known vulnerability (an OSV advisory) and the fix has
just been released, every day of waiting is a day with the hole open. Exploits now follow the publication of a
vulnerability within days, sometimes hours, and tools that write them have become cheap. A fix released on a Tuesday
is offered the Tuesday after.

The minimum age concerns only pip and npm. Updates of the system packages (apt, Windows Update, winget) have none:
security updates go out in the next automatic window ([ADR-0108](0108-automatic-installs-take-every-update-on-linux-guests.md)).

## Decision Drivers

* A release that fixes a vulnerability of the installed version should arrive in days, not a week.
* A malicious release that only claims to be a fix must still be caught. Malware reports reach OSV hours, sometimes a
  day, after the release.
* Only the hub has the OSV data. The agent decides about versions only within its own policy
  ([ADR-0006](0006-local-policy-decides.md)).
* Everything else in the plan keeps the normal minimum age: a fix must not let fresh dependencies in.
* No silent switch: the short age is a policy value with a floor, like the normal one.

## Considered Options

1. Leave it: 7 days for everything.
2. A lower `min_age_days` for everything, for example 3.
3. A second, shorter age for releases that fix a finding. The hub decides from OSV which packages qualify, and the
   agent applies the short age to exactly those, the normal age to the rest of the plan.
4. The same, but the agent decides alone.
5. No minimum age at all for fixes.

## Decision Outcome

Chosen: option 3.

* **Policy:** `packages.min_age_days_security` in `agent.yaml`, default 2, allowed from 1 to `min_age_days`. It cannot
  be switched off. The inventory announces it next to `min_age_days`.
* **Agent, outdated packages (design 11.3):** besides `candidate` (the normal age), it reports `security_candidate` for
  each outdated direct package: the newest release public for `min_age_days_security`, only when it is newer than
  `candidate`.
* **Hub:** a security candidate is a fix when the installed version has at least one OSV advisory that the security
  candidate is not affected by, and the security candidate itself has no OSV entry (malware or vulnerability). The
  view's `UpdateView.fix` names that version. Home Assistant counts it as ready and marks it as a security fix in the
  release summary.
* **Install:** the command stays `install_pkg:<location>[/<name>]`. The hub signs into the job which packages may take
  the short age (`security_fix`: the names whose fix the view shows). The agent applies `min_age_days_security` to
  exactly those, `min_age_days` to everything else in the plan:
  * pip: it checks every planned version itself, as now;
  * npm: it resolves with `--before` the short age, then checks each new version of the resolved plan. One that is not
    a named package and is younger than the normal age stops the run with `precheck_version_too_new`, naming it.
  * npm's `--before` holds for the whole resolution. A job for a whole location that has fixes and other packages runs
    in two parts in one run (added before acceptance, after a review): first the fixes alone at the short
    age, then the rest at the normal age, planned from what the first part left. Otherwise the other packages' new
    dependencies would resolve as young as the fixes, fail the normal age, and stop the whole job. The run journal is
    written once; when the second part fails, the run says that the fixes went in.
  * The hub vets the whole plan against OSV as before ([ADR-0070](0070-hub-vets-and-signs-the-update-plan.md)): a
    malware entry that has appeared since stops it.
* **Dependency refresh** ([ADR-0104](0104-refresh-indirect-dependencies-within-their-ranges.md)): the
  dependencies the hub names there have findings by definition. A release of them that fixes a finding takes the short
  age by the same rule; everything else in the plan keeps the normal one. Added after the same review:
  * `npm update <name>` moves every copy of the name in the tree. Only a release that takes the place of an installed
    copy with a finding may be that young, i.e. the same path in the lock, for a name the hub named. A copy without a
    finding, and one npm moves to another path, keeps the normal age; if its new release is younger, the refresh stops
    with `precheck_version_too_new`.
  * Named fixes and other dependencies with findings are refreshed in two parts, like an update.
* **Unchanged:** updates of pip and npm packages stay by button ([ADR-0069](0069-package-updates-by-button-only.md));
  automatic runs do not take them. An agent that does not announce `min_age_days_security` gets no `security_fix` in
  its jobs and keeps the normal age everywhere.
* **Rollout:** hub first (new inventory fields, new job field only for agents that announce it), then agents, then the
  integration for the text.

Rejected:

* **Option 1,** because a fix waits a week while exploits come within days.
* **Option 2,** because it would shorten the protection for every update, also the many without any security reason.
  The short age is worth its risk only where the installed version is known to be vulnerable.
* **Option 4,** because the agent has no OSV data. Each agent would have to fetch and match the whole database itself,
  which the hub does once for the fleet (design 11.2).
* **Option 5,** because a release that claims to fix a vulnerability is a good disguise for a malicious one. With no
  age at all, the malware report could only arrive after the install.

### Consequences

* Good, because a fix for a known vulnerability is offered after 2 days instead of 7.
* Good, because the protection of ADR-0068 stays for everything else, also for the fix's own dependencies.
* Bad, because a malicious release published as a fix for an open advisory has only 2 days to be found. The OSV check
  of the whole plan at install time and the normal age for its dependencies remain the safeguards.
* Bad, because the trust model widens slightly: a compromised hub could have the agent take a named package at the
  short age instead of the normal one. It still cannot pick versions, go below the agent's floor, or skip the vet of
  the plan.
* Bad, because it is a protocol change in agent, hub and integration.
* Neutral: comparable tools draw the same line. Dependabot's cooldown, for example, does not delay security updates.

### Confirmation

* `internal/policy`: `TestMinAgeDaysSecurity` (default 2, never above `min_age_days`) and the refusals of a negative or
  too long value.
* `internal/inventory`:
  * `TestOutdatedSecurityCandidate`: only when newer than the candidate, none without a shorter age;
  * `TestPipSecurityFix`: a named fix takes the younger release, a young dependency stops it, nothing changes
    unnamed or without a shorter age;
  * `TestNpmSecurityFix`: `--before` moves to the shorter age only with a fix, and a young dependency in the resolved
    plan stops the run;
  * `TestNpmFixAndOtherPackageInOneJob`: a fix and another package in one job: two parts, the other package's new
    dependency at the normal age;
  * `TestDepsFixOnlyWhereTheCopyHasAFinding`: a refresh takes a young release only in place of the copy with the
    finding.
* `internal/protocol`: `TestSecurityFixParameters` (only with pkg and deps scopes, names, none twice, the scope's
  package only, at most 200).
* `internal/hub`:
  * `TestSecurityFixesFromOSV`: a fix only when the installed version has a finding and the security candidate is
    clean; the names for pkg and deps scopes; none for an agent without the shorter age;
  * `TestHAJobCarriesTheFixes`: the job of a Home Assistant command carries them and passes the agent's check.
* `ha/tests`: `test_security_fix_counts_as_ready`.
* After the rollout: a job with `security_fix` in the hub's `job_sent` event, and a fix installed before its release
  was 7 days old.
