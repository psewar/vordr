---
status: accepted
date: 2026-10-28
decision-makers: kay (threshold of 7 days, confirmed 2026-10-29); proposed by Claude in design draft v0.1
---

# "Stale" as a catch-all state after 7 days

Backfilled on 2026-10-30, decided on 2026-10-28 (draft), threshold confirmed by the operator on 2026-10-29. Sources:
`design/architecture.md` 7.5, the operator's project notes (approval of 2026-10-29: "warning threshold 7 days").

## Context and Problem Statement

Not every reason why updates stay pending has its own code: a refused automatic run, a forgotten button, a window
that never fits.

## Decision Drivers

* No case may stay pending silently.
* No alarms for updates that have been pending for a few hours.

## Considered Options

The sources name no alternative.

## Decision Outcome

The hub sets `stale` (a problem) when updates have been pending for longer than `stale_after_days` (default 7) without
a run installing them, whatever the reason. A later change added two siblings: `reboot_pending` (a reboot needed for
more than 7 days) and `no_report` (connected, but no check for more than 26 h).

### Consequences

* Good, because it also catches cases without their own code.
* Bad, because it reports late; until then only the counters show it.

### Confirmation

`TestRebootPendingAndNoReport`; problem reasons in `internal/hub/view_test.go`.
