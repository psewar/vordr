---
status: accepted
date: 2026-10-29
decision-makers: kay
---

# Brand: the rune shield with Algiz

Backfilled on 2026-10-30, decided on 2026-10-29. Sources: `README.md`, the operator's project notes.

## Context and Problem Statement

Vördr had no image of its own. Home Assistant showed a placeholder for the integration and its devices.

## Decision Drivers

* Recognisable at 16 px.
* Fits the name ([ADR-0004](0004-name-vordr.md)).

## Considered Options

Five drafts; the sources only name the first.

## Decision Outcome

Chosen option: draft 1, the "rune shield": a shield with the rune Algiz (ᛉ), the rune of protection; teal and amber.
`assets/brand/render.py` draws SVG sources and the PNGs that HA (from 2026.3) reads from
`custom_components/vordr/brand/`, from one geometry.

### Consequences

* Good, because one source produces all sizes and the light and dark variants.
* Bad, because HA caches images; changes only show after a restart and some time.

### Confirmation

`render.py` produces the sizes HA requires; after a rollout, the images HA serves are compared byte by byte with the
rendered files.
