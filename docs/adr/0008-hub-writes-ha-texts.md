---
status: superseded by ADR-0027
date: 2026-10-28
decision-makers: kay; proposed by Claude in design draft v0.1
---

# The hub writes the texts for Home Assistant, in German

Backfilled on 2026-10-30, decided on 2026-10-28. Sources: `design/architecture.md` (draft v0.1, table 7.3 "next step
(text in HA)").

**Superseded by [ADR-0027](0027-protocol-2-codes-and-translations.md).**

## Context and Problem Statement

For every problem HA should show the next step in words.

## Decision Drivers

* Display names in HA in German.
* With MQTT discovery ([ADR-0009](0009-ha-via-mqtt-discovery.md)) there are no translation files on the HA side.

## Considered Options

The sources name no alternative.

## Decision Outcome

The hub sends ready-made German texts next to the codes (`next_step`, "n pending …", the package list; `hatext.go`).

### Consequences

* Good, because it was simple while HA was connected via MQTT.
* Bad, because with the own integration, repair issues mixed languages (frame in the profile language, reason in the
  system language, next step in German). This led to ADR-0027.

### Confirmation

Not applicable; superseded.
