---
status: accepted
date: 2026-11-11
decision-makers: kay (2026-11-11, asked to make Vördr ready for GitHub; accepted with the merge); proposed by Claude
---

# The neutral check also stops the wording of an operations log

Extends [ADR-0099](0099-the-repository-is-site-neutral-checked-by-the-build.md).

## Context and Problem Statement

ADR-0099 made the build stop on known site terms, private addresses, real domains and paths outside the repository. A
review before publishing found that this caught names but not content. Texts still read as the log of one
installation: runs with their date, what was observed on which kind of host, results of a pilot, sources such as
internal reports, and facts about the rollout in the `decision-makers` line. Test fixtures had been renamed rather than
replaced. No list of site terms can catch that, and a second review by hand does not last.

## Decision Drivers

* A check that matters belongs in the tool ([ADR-0072](0072-decisions-as-adrs-checked-by-the-build.md)).
* Few false hits: ordinary design text and test data must pass.
* What the rule cannot judge stays with review.

## Considered Options

* review by hand before each publication
* generic rules for the typical wording of an operations log

## Decision Outcome

Chosen: generic rules in `tools/check_neutral.py`, with their own tests (`tools/check_neutral_test.py`, run by
`build.sh`).

* Observations of runs: phrases such as `live probe`, `seen on the host`, `seen on the canary`, `in the pilot`,
  `first real`, `real hosts`.
* Dated observations: `on`, `since`, `until` or `as of` followed by a date, and the German `am`, `seit`, `bis`, `vom`
  followed by a day and month. Front matter, `Backfilled on … decided on …` lines and quoted data (fixture timestamps)
  stay allowed.
* Private sources and the operator's home: `morning report`, `night order`, `operations report`, `(private)`,
  `home network`, `in the house`, `at home` and their German counterparts.
* In an ADR's `decision-makers:` line: words that record operations (`hosts`, `since`, `rolled`, `rollout`, `live`,
  `night`, `morning`, `report`). The line keeps names, roles and the decision date.
* WSL paths in UNC form (the `wsl.localhost` and `wsl$` shares) join the mount paths of ADR-0099.
* An ADR with `scope: site` ([ADR-0133](0133-site-decisions-stay-with-the-site.md)) is exempt from these wording
  rules, since it is published only as a stub.

### Consequences

* Good, because the usual ways an operations log creeps into design text stop the build.
* Bad, because the rules know phrases, not meaning: a real version tied to one device or a count of hosts still needs
  review. Fixtures must be synthetic by habit; the tests are written that way.
* Bad, because a quoted phrase passes, so data in quotes is not judged.

### Confirmation

`tools/check_neutral_test.py` covers each rule with a case it stops and ordinary text it lets pass. `build.sh` runs the
tests and the check.
