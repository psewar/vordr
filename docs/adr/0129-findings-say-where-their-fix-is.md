---
status: accepted
date: 2026-11-06
decision-makers: kay (2026-11-06, "Ja, bauen", after the check of two package problems that Home Assistant could not resolve); proposed by Claude
---

# Findings say where their fix is

Refines the problem `vulnerable_package` (design 11.2, [ADR-0064](0064-osv-data-local-at-the-hub.md)) and the
dependency refresh ([ADR-0104](0104-refresh-indirect-dependencies-within-their-ranges.md)): a finding says whether something on
the host can fix it now, and only those findings make the problem. Malicious packages are unchanged.

## Context and Problem Statement

Every vulnerable pip or npm package sets the host's problem and raises a repair in Home Assistant. Often nobody can
act on it, for example:

* In a Node-RED installation, a finding can be held by a Node-RED node that pins the vulnerable version exactly,
  though a fixed release has long existed; others by Node-RED itself, which pins the versions in its newest release;
  others sit inside the npm that Node-RED brings along as a bundle, some fixed only in a new major version. A fresh
  resolution of the whole tree with the minimum age then gives exactly the installed tree.
* On a developer PC, the versions in project environments come from the projects: a lock file, a requirements file
  with an upper bound decided on purpose, a test package that pins a whole framework. Only a finding in a plain
  site-packages folder can be updated by hand.

Home Assistant offers no button for such findings, and none would help. The repair stays for weeks. Ignoring it
in Home Assistant is no way out: there is one repair per host, so it would hide every later finding as well.

The agent reads what holds a package already: the dependencies in `package.json` and `METADATA` of everything installed
next to it, and the project files next to a virtual environment. The hub knows the version that fixes a finding
(`fixed_in`) and what an update could reach (candidate, fix, dependency refresh).

## Decision Drivers

* A repair asks for something the operator can do now: in Home Assistant, or by hand on the host.
* A finding that waits for someone else stays visible, with what it waits for.
* The decision rests on facts the agent reports, not on a resolution against a registry at every inventory.
* Wrongly calling a finding "waiting" is worse than wrongly calling it actionable: when in doubt, the finding counts.

## Considered Options

1. Leave it: every finding makes the problem.
2. Let the operator acknowledge findings in Home Assistant until a fix is reachable.
3. The agent reports what holds each package, the hub decides per finding whether a fix is reachable, and only those
   make the problem.
4. Resolve the tree against the registry at each inventory to see what an update would reach.

## Decision Outcome

Chosen: option 3.

* **Agent: holds.** Each package gets `holds`, what keeps it below a version, at most two:
  * the tightest range among the packages that depend on it in the location (npm: `dependencies` and
    `optionalDependencies` of the installed copy the dependency resolves to; Python: `Requires-Dist` without an
    environment marker or extra), as `{by: "<name>@<version>", range}`;
  * for a virtual environment in a project folder (`.venv` or `venv`), the project's files: an exact version in
    `uv.lock`, `poetry.lock`, `pdm.lock` or `Pipfile.lock`, a specifier in a `requirements*.txt`, as
    `{by: "<file>", range}`;
  * npm: the package it came bundled in, when it sits below a package with `bundleDependencies`, as
    `{by: "<name>@<version>", bundled: true}`.
  Only ranges with an upper bound are kept; a range the agent cannot read is left out.
* **Hub: reach.** Each vulnerable finding gets `reach`:
  * `update`: a direct package whose candidate or fix (ADR-0111) reaches `fixed_in`, in a location Home Assistant may
    update;
  * `update_via`: a hold keeps it below the fix, but a direct package that holds it, or the package it came with, has
    an update ready in such a location;
  * `refresh`: nothing holds it below the fix and the location's dependencies can be refreshed (ADR-0104);
  * `by_hand`: nothing on the host holds it below the fix, but Vördr does not or may not update it there;
  * `held`: a hold keeps it below `fixed_in` (`held_by` names it), and nothing above it has an update ready;
  * `no_fix`: no release fixes every advisory;
  * `too_new`: the fix is released but younger than the minimum age.
  A hold keeps a package below `fixed_in` when its range admits no version at or above it, or when it is a bundle.
* **Problem.** `vulnerable_package` counts only findings with `update`, `update_via`, `refresh` or `by_hand`. The view
  reports how many wait (`waiting`); the repair names the actionable ones.
* **Home Assistant.** The sensor of vulnerable packages shows every finding with `reach` and `held_by`, and `waiting`.
* **Unchanged:** malicious packages always make `malicious_package`; the counts of vulnerable packages stay complete.

Rejected:

* **Option 1,** because a repair nobody can act on is ignored, and with one repair per host that hides the next one.
* **Option 2,** because an acknowledgement does not know when the situation changes: a new Node-RED release that fixes
  the finding would stay silent until someone looks again.
* **Option 4,** because it would send every name in the tree to the registry each day, take minutes on large trees, and
  still not cover lock files and bundles.

### Consequences

* Good, because hosts like those above lose their repair, except for findings a person can update by hand.
* Good, because a finding comes back by itself when what held it has an update ready (`update_via`) or the hold goes.
* Good, because the reason a finding waits is visible as data: which package, which range, which file.
* Bad, because a finding without any fixed release no longer raises a repair; removing the package would be the
  answer, and Vördr cannot tell when that is possible. It stays on the sensor.
* Bad, because the inventory grows by at most two holds per package, and the hub must be released before the agent
  (a new field).
* Neutral: a range or file the agent cannot read holds nothing, so the finding keeps making the problem as before.

### Confirmation

* `internal/versions`: `TestUpperBound` for npm ranges (`^`, `~`, partial and x-ranges, hyphens, unions, comparators)
  and PEP 440 specifiers (`==`, `==.*`, `~=`, `<`, `<=`), and what cannot be read.
* `internal/inventory`: `TestNpmHolds` (the copy a dependency resolves to, the tightest range, bundles),
  `TestPythonHolds` (`Requires-Dist`, markers left out, lock and requirements files of the project).
* `internal/hub`: `TestFindingReach` for each reach, and that only actionable findings make the problem.
* `ha/tests`: the sensor shows `reach`, `held_by` and `waiting`; the repair names only actionable findings.
