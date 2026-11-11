---
status: accepted
date: 2026-10-29
decision-makers: kay ("yes, build the fix")
---

# The same command as the running run joins it instead of waiting

Backfilled on 2026-10-30, decided on 2026-10-29. Sources: `design/architecture.md` 12, `design/ha-integration.md` 5
(`command_result`).

Extends [ADR-0043](0043-queue-per-host.md).

## Context and Problem Statement

Two taps on "reboot now" can arrive for the same host. The queue holds the second reboot. After the first reboot it
starts, and the agent refuses it (`rejected_rate_limited`, at most one reboot per hour). The result is an alarm and an
AI push for a command that has nothing left to do.

## Decision Drivers

* A request that is already being fulfilled is no error.

## Considered Options

The sources name no alternative.

## Decision Outcome

An HA command equal to the running run (verb **and** scope) is not queued. The answer is `ok` with the `job_id` of the
running run; the event log records `command_already_running`. A different scope of the same verb still waits. The check
sits after the role check and before the backup check, because joining changes nothing on the host.

### Consequences

* Good, because double taps cause no alarms.
* Bad, because none known.

### Confirmation

`TestSameCommandAsTheRunningRunJoinsIt`; counter-check: without the check the test fails.
