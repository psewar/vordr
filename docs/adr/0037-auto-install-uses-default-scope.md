---
status: superseded by ADR-0059
date: 2026-10-29
decision-makers: kay
---

# Automatic runs use `default_scope`

Backfilled on 2026-10-30, decided on 2026-10-29. Sources: the operator's notes ("window and `default_scope` are
required as before"), the operator's project notes.

**Superseded by [ADR-0059](0059-own-auto-scope.md).**

## Context and Problem Statement

An automatic run ([ADR-0036](0036-automatic-install-in-rings.md)) needs a scope.

## Decision Drivers

The sources give no reason. `default_scope` already existed in every `agent.yaml`.

## Considered Options

The sources name no alternative.

## Decision Outcome

Automatic runs take the host's `install.default_scope`, the same scope as the install button in HA.

### Consequences

* Bad, because "only security updates automatically" required `default_scope: security`. Then the button in HA also
  installed only security updates, and HA no longer offered "install all". This came up when hosts were enabled,
  and led to ADR-0059.

### Confirmation

Not applicable; superseded.
