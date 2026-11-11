---
status: superseded by ADR-0058
date: 2026-10-29
decision-makers: kay
---

# One host as the canary

Backfilled on 2026-10-30, decided on 2026-10-29. Sources: the operator's project notes and the operator's notes.

**Superseded by [ADR-0058](0058-site-decision.md).**

## Context and Problem Statement

Self-update ([ADR-0032](0032-self-update-signed-by-operator.md)) and automatic installs
([ADR-0036](0036-automatic-install-in-rings.md)) need a first ring.

## Decision Drivers

The sources give no reason for the host chosen, other than that it was the first host allowed to `install`.

## Considered Options

The sources name no alternative.

## Decision Outcome

`agent_update.canary` and `auto_install.canary` in `hub.yaml` = that one host.

### Consequences

* Good, because self-updates reach one host first.
* Bad (the reason it was replaced), because the canary host has a special role and runs a different distribution than
  most other hosts. An update there does not hit what it hits elsewhere.

### Confirmation

Not applicable; superseded.
