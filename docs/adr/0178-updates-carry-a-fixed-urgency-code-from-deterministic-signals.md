---
status: proposed
date: 2026-11-17
decision-makers: kay (2026-11-17, accepted in principle with `unknown` ranked directly below `high`, implementation later; asked on 2026-11-17 how fast an update ideally should be installed); proposed by Claude; status becomes accepted with the merge that implements it
---

# Updates carry a fixed urgency code from deterministic signals

Builds on [ADR-0007](0007-fixed-codes-instead-of-error-texts.md) (results are fixed codes) and
[ADR-0064](0064-osv-data-local-at-the-hub.md) (OSV data lives at the hub). It is the data side of the answer to "how
urgent is this update"; an AI takes no part in it
([ADR-0176](0176-an-ai-may-not-change-maintenance-windows-minimum-ages-ring-delays-or-scopes.md)).

## Context and Problem Statement

Vördr knows several things about an update, but only as the source's own word. A finding from OSV carries the
severity word of its database (`LOW` to `CRITICAL`); the CVSS vectors in the same record are not read. A system update
is marked `security` only because it comes from a security archive, with no CVE and no severity; a Windows update only
by its classification. Whether a vulnerability is known to be exploited is not known at all.

People who decide what to install first (and the automation in Home Assistant that pushes a notification) have no
single, comparable answer to "does this one hurry". Today the answer is spread over a word, an origin and a person's
memory.

## Decision Drivers

* The answer comes from checkable data and is the same every time; it cannot be changed by the text of a release.
* Results are fixed codes, not text ([ADR-0007](0007-fixed-codes-instead-of-error-texts.md)); Home Assistant shows
  them from its translations.
* The code orders and marks; it changes no window, minimum age, ring delay or scope. Where a faster path is wanted it
  needs its own decision ([ADR-0177](0177-a-fix-for-a-known-exploited-vulnerability-does-not-skip-the-ring-delay.md)
  examined and rejected a waiver of the ring delay).
* Missing data is `unknown`, never a guess, and never sorted as harmless: it ranks directly below `high`, so a gap
  cannot hide behind `normal` or `low`.
* Package lists and CVE lists stay inside the network (ADR-0064): bulk data comes down, single queries go nowhere.

## Considered Options

* Keep the sources' own words and let each client sort them (status quo).
* One fixed urgency code per finding and per pending update, derived at the hub from deterministic signals.
* Let an AI estimate the urgency. Rejected (ADR-0176): not checkable, and open to manipulation through a changelog.

## Decision Outcome

Chosen option: one fixed urgency code, derived at the hub (accepted in principle).

* **Codes, most urgent first:** `exploited`, `critical`, `high`, `unknown`, `normal`, `low`. They are fixed values in
  the protocol with translations in Home Assistant; no code is made of free text.
* **Signals and mapping:**
  * `exploited`: a CVE of the finding or update is in the CISA KEV catalogue (public, CC0), fetched whole by the hub.
  * `critical`: the source says `CRITICAL`, or the CVSS vector from OSV has network reach, no privileges, no user
    interaction and a high impact. Only a few vector features are read; no score is computed.
  * `high`: the source says `HIGH`, or the vector has a high impact without the rest; also an update that comes from a
    security archive or has the classification "security" and has no finer data.
  * `normal`: an update that is not marked as security and has no finding.
  * `low`: the source says `LOW`, or the distribution marks the fix as unimportant.
  * `unknown`: the data needed is missing (no catalogue, no vector, no CVE list for a system update). It is shown as
    such and ordered directly below `high`, before `normal`, so missing data is never sorted as harmless; it does
    not mean low.
* **Per host:** the highest code of its findings and pending updates, in the order above (`unknown` therefore
  outranks `normal` and `low`). A malicious package keeps its own, stronger
  problem ([ADR-0064](0064-osv-data-local-at-the-hub.md)).
* **Where.** The hub computes the code from data it holds, so every client sees the same one. Home Assistant shows it
  (attribute and sensor, sorted), and a user's automation may react to it. The code is a statement, not a trigger inside
  Vördr: nothing installs because of it.
* **Staged.** The catalogue and the vector first (findings of pip, npm and images, which the hub already has); the CVEs
  of system updates (Windows API, Debian OSV by source package) later, each with its own protocol change, hub before
  agent ([ADR-0049](0049-site-decision.md)).
* **Optional input.** EPSS (probability of exploitation) may raise `critical` later, once its licence is clarified;
  it is not part of this decision.

### Consequences

* Good, because one comparable, explainable code replaces a word, an origin and guesswork, and Home Assistant can sort
  and notify on it.
* Good, because nothing in it can be influenced by the text of a release.
* Neutral: until system updates name their CVEs, many system updates are `high` (security archive) or `unknown`, not
  `exploited`; the code says so instead of pretending.
* Bad, because the hub reads one more external catalogue and the vector fields of OSV; format changes need upkeep.
* Bad, because the mapping from vector to `critical` is a policy choice that people may argue with; it is fixed and
  documented so that it is at least predictable.

Decided: `unknown` ranks directly below `high`. Still open for the maintainer: the mapping of
the middle codes (what is `critical`, what is `high`).

### Confirmation

* Hub tests with fixtures: the order of the codes puts `unknown` between `high` and `normal`; each code from its signal (KEV hit by CVE or alias, vector, source word, archive origin,
  unimportant); the highest wins per host; missing catalogue or missing vector gives `unknown`, never `exploited`.
* `internal/protocol/protocol_test.go` and `ha/tests/test_texts.py` check that each code exists in every language and
  has one wording.
* A test that the code feeds no setting of window, minimum age, ring delay or scope.
