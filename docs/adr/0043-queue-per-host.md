---
status: accepted
date: 2026-10-29
decision-makers: kay ("go")
---

# Commands for a busy host wait in a queue at the hub

Backfilled on 2026-10-30, decided on 2026-10-29. Sources: `design/architecture.md` 12 ("Warteschlange je Host").

## Context and Problem Statement

An agent runs one job after another. "Update all" in HA presses several buttons of a host in the same second. While a
host runs its agent update, the install ends with `rejected_busy`, and its updates stay pending.

## Decision Drivers

* A command pressed in HA should not get lost because the host is busy.
* No pile-up of commands.

## Considered Options

* refuse with `rejected_busy` (as before)
* wait in a queue at the hub

## Decision Outcome

Chosen option: queue.

* An HA command for a host with a running run waits at the hub instead of being sent.
* The same command waits only once; at most 3 wait (`queue_full`).
* 30 s after a run ends, the hub starts the next one. An agent that has just restarted checks first.
* A run older than its job may last does not block the queue.
* The host view shows the queue (`queued`); the backup check applies at the time of the command.

### Consequences

* Good, because "update all" works.
* Bad, because a waiting command can end up running the same thing twice; see
  [ADR-0044](0044-same-command-joins-running-run.md).

### Confirmation

`TestCommandWaitsForTheRunningRun`, `TestQueuedJobStartsWhenTheRunEnds`, `TestStaleRunDoesNotBlock`; a live check:
"update all" runs agent update and install one after another on each host.
