---
status: accepted
date: 2026-10-30
decision-makers: kay (minimum age, 7 days, adjustable), Claude (details)
---

# Minimum age for the target versions of pip and npm updates

## Context and Problem Statement

Supply-chain attacks mostly come as a malicious new version of a popular package: `chalk` 5.6.1 and `debug` 4.4.2 on
2025-09-08, `axios` 1.14.1 in April 2026. Such versions are usually discovered and pulled within hours to days. Which
version may a Vördr update install?

## Decision Drivers

* A fresh release is the biggest risk; a few days of distance cost little.
* The dependencies of an update can be fresh too.
* No silent switch-off (the operator's rule: exceptions only explicitly).

## Considered Options

1. Always the newest version.
2. The newest version that has been public for at least `min_age_days` ("candidate"), for the package and every
   dependency.
3. Only versions someone approved by hand.

## Decision Outcome

Chosen option: **option 2** (the operator, 2026-10-30), `packages.min_age_days` in `agent.yaml`, default 7, allowed
1–90, cannot be switched off. Details:

* The age of a PyPI release counts from its **newest** file: wheels can be uploaded to an old release later.
* Yanked and deprecated releases and pre-releases never qualify; on npm nothing above the `latest` tag.
* Dependencies: npm resolves with `--before <now − minimum age>`; for pip the agent checks every version of the plan
  itself ([ADR-0070](0070-hub-vets-and-signs-the-update-plan.md)).

### Consequences

* Good, because a malicious new version has days to be discovered before Vördr even offers it. In a first check the
  candidate was older than the newest version for about a quarter of the outdated packages.
* Bad, because security fixes only arrive after the minimum age; whoever needs them at once updates by hand.
* Bad, because a package with very frequent releases often has no candidate at all (`precheck_version_too_new`).

### Confirmation

`TestPick`, `TestOutdated`, mutation probe ("no minimum age", "age from the first file", "young pip dependency
accepted", "npm without --before").

## More Information

`design/architecture.md` 11.3, 11.4.

Merged in German as ADR-0104; later renumbered and translated.
