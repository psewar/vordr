---
status: accepted
date: 2026-11-17
decision-makers: kay (2026-11-17, asked for an event log bounded by size and read across its files, with defaults under which an existing configuration keeps working without edits); proposed by Claude
---

# The event log is rotated by size and keeps a fixed number of files

## Context and Problem Statement

The hub's event log `events.jsonl` ([ADR-0011](0011-state-in-files.md)) is only ever appended to. On a hub that runs
for years it grows without a bound in the state folder, beside `registry.json`, whose saves fail once the disk is full.
Every `vordr-hub events` reads the whole file, however old most of it is. A record that has to last leaves the hub
anyway ([ADR-0060](0060-event-log-off-host.md)); the hub itself needs the recent past.

How does the event log stay bounded, without losing or doubling a line and without surprising an existing
installation?

## Decision Drivers

* Disk use in the state folder bounded, by a setting of the operator.
* No line lost or written twice by a rotation, also with several writers at once and with a hub that stops at any
  point of it.
* Nothing is overwritten: each step checks first that its target is free.
* An existing `hub.yaml` keeps working, and an existing installation keeps its history until its operator decides
  otherwise.
* Queries see the whole kept history in order, without reading all of it for the newest lines.
* No tool outside the hub (logrotate) that every system running the hub would need.

## Considered Options

1. Rotation by size in the hub, with a fixed number of numbered files (`events.jsonl.1` … `.N`).
2. Rotation by time (one file per day or month).
3. An external tool (logrotate): `copytruncate`, or a signal that makes the hub reopen its file.
4. No limit: rely on external copies (ADR-0060) and on deleting by hand.
5. Option 1 with the full files compressed.

## Decision Outcome

Chosen option: **1**.

* **Settings** in `hub.yaml`, section `event_log`: `max_size_mb` (default 32, 1 to 1024) and `keep` (default 8, 1 to
  100). `check-config` checks both and says how the event log keeps its history, also when it keeps everything.
* **Off without the section**, as before: deleting history is the operator's decision, and a copy of `events.jsonl`
  elsewhere (ADR-0060) may watch the file's length. `hub.example.yaml` has the section, so a new installation starts
  bounded; `event_log: {}` takes the defaults.
* **When:** a line that would take the current file past `max_size_mb` first starts a new file, under the log's lock,
  the lock every write takes. A file passes the limit only if a single line does.
* **How:** the files numbered past `keep` are deleted; the files below the first free number move one number up, the
  highest first; the current file becomes `events.jsonl.1`; a new current file is opened, and its first line is the
  event `event_log_rotated` (`previous`, `previous_size`, `keep`). A higher number is always older. Every move checks
  first that nothing is at its target; something there stops the rotation with `target_exists`. A hub that stops
  between two steps leaves the files in their order: the next start opens or creates the current file, and the next
  rotation fills a gap instead of dropping a file for it.
* **When a step fails:** the rotation stops where it is, and the hub keeps appending to the file it has. The event
  `event_log_rotation_failed` names the step's code (`check_failed`, `target_exists`, `remove_failed`,
  `rename_failed`, `reopen_failed`) and the file; the next attempt waits a minute. If the new current file cannot be
  opened, every line is still in the service log, and each new line tries to open it again.
* **Reading:** `vordr-hub events` opens the current file and the full ones under the lock, so it gets one consistent
  set, and reads them without it. It starts with the newest file and reads an older one only while that can still add
  to the answer (fewer matches than `--limit` so far, and the file does not start before `--since`). The answer is the
  same as before: the newest matching lines, oldest first.

Option 2 bounds the number of files, not their size, which follows the hosts' activity. Option 3 with
`copytruncate` loses the lines written between the copy and the truncate; a reopen signal is a second moving part, and
logrotate is not on every system the hub runs on (a container). Option 4 is what exists. Option 5 makes reading and
the external copy more involved for a gain that the limit already gives; it can follow if the files prove too large.

### Consequences

* Good, because the event log takes at most (`keep` + 1) × `max_size_mb`, and a query for the newest events reads the
  newest file.
* Good, because a rotation neither loses nor doubles a line, and a hub that stops in its middle leaves nothing broken.
* Bad, because with the section the hub deletes its oldest file: whoever needs a longer history copies the numbered
  files too (ADR-0060). A copy that raises an alarm when `events.jsonl` gets shorter sees every rotation; the first line
  of the new file says why.
* Bad, because an existing installation stays unbounded until its operator adds the section.
* Neutral: the current file is closed before it is renamed (a file that is open cannot be renamed on Windows); no
  writer is waiting meanwhile, they all wait for the lock.

### Confirmation

* `internal/hub/events_rotate_test.go`: `TestEventLogRotatesAtTheLimit` (no file past the limit, the new file starts
  with `event_log_rotated`, every line once and in order; nothing moves without rotation),
  `TestEventLogKeepsItsNumberOfFiles` (the oldest goes; a smaller `keep` takes effect with the next rotation),
  `TestReadAcrossFiles` (filters, `--limit` and `--since` across files, equal to filtering every line on disk),
  `TestEventLogConcurrentWritesAndReads` (eight writers and a reader across many rotations, under `-race`),
  `TestEventLogAfterAStopInTheMiddle` (a stop between moving the full file and opening the new one; a stop between
  two moves leaves a gap that the next rotation fills), `TestEventLogRotationNeverOverwrites` (`target_exists`, the
  foreign file untouched, one attempt a minute, going on once the place is free),
  `TestEventLogWhenTheNewFileCannotBeOpened` (lines in the service log, reading without a current file, the file
  opens again), `TestParseConfigEventLog` (off, defaults, limits, unknown key, the example's section).
* `cmd/vordr-hub/events_test.go`: `TestEventLogSummary` (what `check-config` says, on and off).
