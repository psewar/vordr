---
status: accepted
date: 2026-10-28
decision-makers: kay
---

# The name Vördr

Backfilled on 2026-10-30, decided on 2026-10-28. Sources: the operator's project notes (2026-10-28), `README.md`.

## Context and Problem Statement

The project needs a name for the repo, binaries, configuration and HA integration.

## Decision Drivers

* No confusion with existing projects.
* Fits an existing naming convention.

## Considered Options

* Vördr
* Gjallarhorn
* Heimdall

## Decision Outcome

Chosen option: Vördr (the operator, 2026-10-28). Technically `vordr`, binaries `vordr-agent` and `vordr-hub`.

In Norse tradition the *vörðr* is the guardian spirit that accompanies a person. Here every computer is accompanied by
its own agent.

### Consequences

* Good, because it is unique, short and works as a technical name without special characters.
* Bad, because the "ö" only appears in display names; technically it is `vordr` everywhere.

### Confirmation

No check needed.

## Pros and Cons of the Options

| Option | Why not |
|---|---|
| Gjallarhorn | too close to "Gjallar", an existing Go monitoring project |
| Heimdall | collides with the dashboard of the same name |
