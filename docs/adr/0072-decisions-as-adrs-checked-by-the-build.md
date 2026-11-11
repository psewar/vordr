---
status: accepted
date: 2026-10-30
decision-makers: kay (ADRs, English, the check in the build); numbering rule proposed by Claude
---

# Decisions as ADRs in English, with the index checked by the build

## Context and Problem Statement

The operator decided (2026-10-30) that every architecture and design decision is recorded as an ADR in the repo, also
backfilled for the past, and later the same day that ADRs are written in English. Two lines of work wrote ADRs in
parallel: the backfill (0001 ff.) and the package work, whose German ADRs reached `main` with placeholder numbers
0100–0107. An index of over 70 entries kept by hand drifts; a gap or a missing back-link would only be found by
chance.

## Decision Drivers

* Anyone can later see why Vördr is built the way it is.
* One language and one numbering for all ADRs.
* The operator's rule: a check that matters belongs in the tool, not in a text.
* Vördr has no CI; `build.sh` is the gate every change passes.

## Considered Options

* keep the index by hand and rely on review
* generate and check the index with a script run by `build.sh`
* a CI job on the Git hosting

## Decision Outcome

Chosen option: a script run by `build.sh`.

* ADRs live in `docs/adr/NNNN-title.md`, MADR 4.0, in English, numbered from 0001 without gaps. `docs/adr/README.md`
  holds the rules and the index.
* Numbers on branches: a branch takes the next free numbers of `main`; if two are open at the same time, the one merged
  later renumbers before its merge (proposed by Claude after the placeholder numbers reached `main`).
* `docs/adr/check_index.py`, copied from the repository of an earlier agent of the operator and adapted
  ([ADR-0012](0012-copy-parts-of-the-earlier-agent.md)), checks front matter (`status`, `date`, `decision-makers`), the
  status values, consecutive numbers, that every `superseded by` target exists and mentions the ADR it replaces, and
  that the index matches the files. `--write` regenerates the index. `build.sh` runs it first and stops on a finding.

### Consequences

* Good, because a gap, a stale index or a one-sided supersede link stops the build.
* Bad, because `build.sh` now needs `python3` (it says so and stops if it is missing).
* Bad, because the check cannot judge content: whether a reason is missing stays a matter for review.

### Confirmation

`build.sh` runs the check; a counter-check with a missing index line, a gap in the numbers and a missing back-link each
made it fail.
