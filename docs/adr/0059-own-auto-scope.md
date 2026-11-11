---
status: accepted
date: 2026-10-30
decision-makers: kay
---

# Automatic runs have their own scope (`install.auto_scope`)

Backfilled on 2026-10-30, decided on 2026-10-30. Sources: `design/architecture.md` 10.3.

Supersedes [ADR-0037](0037-auto-install-uses-default-scope.md).

## Context and Problem Statement

The operator chose automatic security updates ([ADR-0058](0058-site-decision.md)).
Until then automatic runs used `default_scope`. "Only security automatically" would have limited the HA button to
security too, and "install all" would have disappeared from HA.

## Decision Drivers

* The automation installs security only; the button still installs everything.

## Considered Options

* keep `default_scope` for both
* a separate scope for automatic runs

## Decision Outcome

Chosen option: `install.auto_scope` (`security` or `all`). Without it, `default_scope` applies as before; agents before
0.8.2 do not report the field. `check-config` checks that the value is known and in `allowed_scopes`.

### Consequences

* Good, because button and automation can differ.
* Bad, because it is a new protocol field: hub 0.16.0 before agent 0.8.2
  ([ADR-0049](0049-site-decision.md)).

### Confirmation

`TestAutoInstallOwnScope`, `TestSummaryAnnouncesAutoScope`, cases in `TestIssues`.
