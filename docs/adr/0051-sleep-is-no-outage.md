---
status: accepted
date: 2026-10-30
decision-makers: kay (principle "report state instead of deriving it from errors", `AGENTS.md`)
---

# Sleep is no outage: a PC announces sleep and shutdown

Backfilled on 2026-10-30, decided on 2026-10-30. Sources: `design/architecture.md` 11.1 ("Schlafen ist kein
Ausfall").

## Context and Problem Statement

A Windows PC sleeps or is off without anything being broken. After 10 minutes the hub would report `offline`, and HA
would raise an alarm every time the PC sleeps.

## Decision Drivers

* Report state instead of guessing it from a dropped connection.
* A crash or a lost network must still count as `offline`.

## Considered Options

* guess sleep from the dropped connection (for example a longer threshold for PCs)
* the PC announces it

## Decision Outcome

Chosen option: the PC announces it. The Windows service receives the power notice from Windows (`PBT_APMSUSPEND`,
shutdown) and sends the message `power` (`sleeping`, `shutdown`) before the connection drops. Until the PC reports
again, the missing connection is not `offline`; the host view carries `power`. Without a notice the rule stays as
before.

### Consequences

* Good, because PCs can be managed without an alarm whenever they sleep.
* Bad, because hubs before 0.15.0 do not know `power` and disconnect the agent; the hub must be rolled out first
  ([ADR-0049](0049-site-decision.md)).

### Confirmation

`TestSleepingHostIsNotOffline`, `TestPowerNoticeOverTheTunnel`, `test_sleeping_pc_is_away_not_a_problem`.
