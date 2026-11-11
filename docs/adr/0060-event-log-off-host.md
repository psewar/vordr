---
status: accepted
date: 2026-10-30
decision-makers: kay (approval of the expansion)
---

# The event log also leaves the hub: file copy and syslog

Backfilled on 2026-10-30, decided on 2026-10-30. Sources: the operator's notes (analysis and expansion plan of
2026-10-30), the operator's project notes.

## Context and Problem Statement

The hub's event log (`events.jsonl`, [ADR-0011](0011-state-in-files.md)) only lives on the hub host. Whoever takes over
or loses the hub also takes or loses the record of what happened.

## Decision Drivers

* A record that survives the hub.
* Searchable if possible.

## Considered Options

* hourly file copy to a NAS
* syslog to a log server (searchable)
* both

## Decision Outcome

Chosen option: both, in two steps.

* First the file copy, which needs no log server: a timer outside the hub container copies `events.jsonl` every hour
  to a NAS (root only), keeps one state per day for 400 days, keeps the old state and raises an alarm if the file gets
  shorter, and reports failures to HA.
* Then the hub container also sends the events via rsyslog over TCP with octet-counted framing (with newline framing
  a log server can report "Invalid frame header" and drop everything). The history is delivered afterwards. The file
  copy keeps running.

### Consequences

* Good, because the record survives the hub and is searchable.
* Bad, because there are two paths to watch.

### Confirmation

The timer reports failures to HA; a live check shows the complete history in the log server.
