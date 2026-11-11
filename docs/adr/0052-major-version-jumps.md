---
status: accepted
date: 2026-10-30
decision-makers: kay
---

# Mark major version jumps from winget in Home Assistant

Backfilled on 2026-10-30, decided on 2026-10-30. Sources: `design/architecture.md` 11.1 ("Hauptversionssprünge").

## Context and Problem Statement

On a Windows PC "update all" can lift programs, such as a runtime or a backup client, to a new major version without
anyone seeing it. A major jump of a backup agent is exactly what one does not want in an unattended run without a look.

## Decision Drivers

* Major jumps should be visible before installing.
* No false alarms.

## Considered Options

* mark jumps for all sources
* mark jumps from winget only
* block jumps

## Decision Outcome

Chosen option: mark, for winget only.

* A package whose version starts with a different number carries `major`. Calendar versions (first number 1000 or
  more) and versions without a leading number are not marked.
* The hub passes it on; HA writes "new major version" into the update summary.
* apt marks nothing: a distribution's stable updates stay within their major version, and date versions (such as
  tzdata) would only give false alarms.
* Whoever does not want a jump pins the package (`winget pin add --id <id> --blocking`); Vördr respects pins.

### Consequences

* Good, because jumps are visible in HA before the button is pressed.
* Bad, because the mark does not stop anything; blocking is the pin's job.

### Confirmation

`TestMajorJump` (11 cases).
