---
status: accepted
date: 2026-11-02
decision-makers: kay (2026-11-03, accepted); proposed by Claude
---

# Refresh indirect dependencies with findings within the ranges their dependents allow

Extends the package update by button ([ADR-0069](0069-package-updates-by-button-only.md)). Its safeguards stay as they
are: minimum age ([ADR-0068](0068-minimum-age-for-target-versions.md)), the plan vetted and signed by the hub
([ADR-0070](0070-hub-vets-and-signs-the-update-plan.md)), no code during the install
([ADR-0071](0071-no-code-during-install.md)).

## Context and Problem Statement

Vördr updates the packages a location has installed for their own sake (`direct`). Their dependencies move only as a
side effect: when a direct package gets a newer version, pip or npm resolve its dependencies anew. The findings,
however, are mostly in dependencies, and many of them stay there although a fixed version exists:

* In a Node.js project or a Node-RED user directory the lock file keeps every dependency where it is. Updating a direct
  package changes only what that update needs.
* A global npm package or a pip environment whose direct packages are all at their newest version is not touched at
  all, even when newer versions of its dependencies would fit its declared ranges.

In a typical Node-RED user directory, `npm audit` finds a fix within the declared ranges for most of the vulnerable
packages; a few need a new major version of the direct package that pulls them in. A global installation of the same
kind can have dozens of dependencies with a newer version within range.

How does Vördr bring those fixes in without giving up the safeguards of the package update?

## Decision Drivers

* A fix that the dependents already allow should not wait for their next release.
* Every version that arrives passes the same safeguards as today: minimum age, a plan vetted by the hub, no install
  scripts, a check after the install.
* The change stays as small as the finding needs: every new version is a new chance for a malicious release.
* Never outside the ranges the dependents declare. Whether a dependent works with a version it does not allow is its
  maintainer's call, not the operator's.
* By button only, as every package update.
* Works for pip as well as npm, and for locations with and without a lock file.

## Considered Options

1. Leave it as it is: dependencies move only with their direct package.
2. Refresh the dependencies that have a finding, each to the newest version within the ranges of the packages that
   depend on it and older than the minimum age. A separate action per location, through the existing pipeline: plan,
   vet, install exactly the plan, check.
3. As option 2, but refresh every dependency within range, with or without a finding.
4. The package managers' own fix commands (`npm audit fix`, `pip-audit --fix`).
5. Force fixed versions beyond the declared ranges (npm `overrides`, pip constraints).

## Decision Outcome

Chosen: option 2, built in steps. Step 1 (npm folders with a lock file) comes with agent 0.12.0 and integration
0.19.0; global npm and pip environments follow as their own steps.

* **What:** a dependency of the location (not `direct`) with a vulnerable or malicious finding in the hub's OSV data.
  The hub names them through the signed vet it already gives for plans (ADR-0070): the agent offers the dependencies it
  has installed there, the answer flags those with findings. So the agent works only from its own inventory and the
  hub's current data, and no list of names has to travel in the job.
* **Target:** the newest version the manager chooses within all declared ranges, resolved as of
  `now - min_age_days`.
  * If the planned version still has a finding, that dependency is left out and the plan is made once more without
    it. A flagged entry that the update brings along (not one of the named dependencies) stops the run before anything
    changes.
  * If no allowed version is newer, the dependency is not touched. The run's result names it with the packages that
    pin it ("needs a new release of `<dependent>`"), instead of a failed run.
* **How, per kind of location:**
  * npm with a lock file (project, Node-RED user directory), step 1: `npm update <names> --package-lock-only` with the
    same `--before` as an update, in a copy of `package.json` and the lock. The plan is what the new lock has that the
    old one had not. The install is the same `npm update <names>` in place, with the same `--before` and
    `--ignore-scripts` unless allowed, so npm resolves it the same way; then every entry of the plan must be installed.
    `npm ci` with the vetted lock was considered: it reinstalls the whole tree and leaves the folder empty if it fails
    halfway.
  * global npm (no lock file), a later step: the dependent is installed again at its installed version with
    `--before`. npm cannot limit that to single dependencies, so the plan is everything new in the fresh tree, as for an
    update of a global package today. The summary says so.
  * pip environments, a later step: `pip install --dry-run --report` with the dependents pinned at their installed
    versions and the dependencies named with `--upgrade`; the agent checks each planned version's age as today.
    Installed with `--no-deps` and every version of the plan pinned, then `pip check`.
* **Trigger:** the action `vordr.refresh_dependencies` per location in Home Assistant (target the location's update
  entity, with `reason` for outside the window). The location's own install button does not do it, so its plan stays
  readable.
* **Job:** the scope `deps:<location>` beside `pkg:<location>`. An agent reports per location that it can refresh
  there (`deps` in the inventory); the hub offers the command only for those, and the agent checks again. Older agents
  report nothing and get nothing.

Rejected:

* **Option 1,** because those fixes would wait for releases of the dependents that may not come soon.
* **Option 3,** because it brings many new versions without a finding behind them: on a global installation, dozens
  at once. That is more risk of a malicious release than the findings justify. Global npm does it anyway, which
  is accepted only because npm leaves no other way there.
* **Option 4,** because these commands decide by advisory with their own data, honour neither the minimum age nor the
  hub's signed vet, and run install scripts unless told not to. pip has no equivalent in the base tools.
* **Option 5,** because it overrules the dependents' declared ranges. Findings that need a major version stay
  visible as such until the dependent releases one.

### Consequences

* Good, because most findings in dependencies can be fixed at once, under the same safeguards as any package update.
* Good, because a finding that cannot be fixed within range is named with its reason, not a refusal.
* Bad, because there is a second kind of package update per location, with its own action and scope.
* Bad, because a global npm package will refresh its whole tree, not only the dependencies with findings.
* Bad, because a version within range can still break a dependent that declared its range too wide. The check after
  the install and the `health:` checks catch only what they look at; the previous state is in the run log and the
  backup.
* Neutral: the reasons why a dependency stayed are in the run's result for now, not yet as a field in the host view.

### Confirmation

* `TestRefreshDepsMovesOnlyWhatHasFindings`: only the dependencies the hub flags are named, one whose newest allowed
  release is flagged too is left out and the plan made again, one that no release moves is named with what pins it,
  the install runs without scripts and only for the remaining names.
* `TestRefreshDepsStopsOnAFlaggedNewcomer`: a flagged entry that the update brings along stops the run before
  anything is installed. `TestRefreshDepsOnlyWhereItCan` and `TestPackageRunGates`: global npm and folders without a
  lock file are refused, as is a location outside `packages.update`.
* `TestVetAndUpdatable` (hub): the vet answers for a `deps:` run, and `refresh_deps:` is resolved only for a location
  whose agent reported `deps`. `test_refresh_dependencies` (integration): nothing goes out without that capability.
* Before a live run, a dry run with the new binary (`vordr-agent packages --plan-deps`) on a location with
  dependencies with findings: the plan shows those that move within their ranges, each one left is named with the
  dependent that pins it, and the lock file stays unchanged.
