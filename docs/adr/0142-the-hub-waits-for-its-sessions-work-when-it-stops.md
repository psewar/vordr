---
status: accepted
date: 2026-11-11
decision-makers: kay (2026-11-11, asked to fix the failing checks before the first release; accepted with the merge); proposed by Claude
---

# The hub waits for its sessions' work when it stops

## Context and Problem Statement

The hub handles each agent and Home Assistant connection in its own goroutine and starts further goroutines for the
work a session causes: waiting for a job's result, starting the next job of a queue, answering an admin request. Many
of them write the register, and every save syncs the file and its directory to disk
([ADR-0011](0011-state-in-files.md)).

Nothing waited for these goroutines. On SIGTERM the hub closed its HTTP server and exited, whatever was being stored.
The end-to-end tests had the same gap: a test ended, its agents disconnected, and the testing package removed the hub's
directory while the hub was still storing a report or a job's result. On a fast disk the save finished first; on a disk
where a sync takes milliseconds the removal found a new file and the test failed with "directory not empty".

## Decision Drivers

* A stop (and the end of a test) does not cut a save short or leave a temporary file behind.
* The tests are deterministic on slow disks too.
* No change for agents: a stopping hub looks to them like any lost connection.

## Considered Options

* the server counts its connections and the goroutines it starts, and `Close` ends the sessions and waits for them
* the tests wait or retry the removal of the hub's directory
* every goroutine of the hub takes a context that a stop cancels

## Decision Outcome

Chosen: `Server.Close(ctx)` (`internal/hub/close.go`).

* Every tunnel and Home Assistant connection is counted while its handler runs. Every goroutine the server starts
  goes through one helper that counts it; starting the next job of a queue counts under the same lock.
* `Close` marks the server as stopping, closes every connection, and waits until everything counted has ended, at most
  until `ctx` ends; it reports whether all of it ended. From then on a new connection is closed without a session and
  no new work starts.
* The waits that outlast a session (a job's result, the backup before an install, the agent that starts next) end when
  `Close` begins, with the admin code `hub_stopping`, and record nothing: the run stays open in the register, and the
  agent's next hello settles it ([ADR-0114](0114-agent-names-its-run-and-the-hub-acknowledges-results.md)), as after
  any hub restart. Recording `interrupted` or `timeout` there would be a guess; the install may well go on.
* A job that would be waited for once the hub is stopping is not waited for either. The queue lives in memory and
  ends with the hub, as before; its settle time before the next job ends with `Close` too.
* `vordr-hub run` on SIGTERM or SIGINT: close the HTTP server and the admin socket, then `Close` with 10 s.
* The test hubs call `Close` before their directory is removed.

Rejected:

* **Tests wait or retry the removal,** because it hides the race instead of ending it, and the hub itself would still
  exit in the middle of a save.
* **A context for every goroutine,** because the work already ends when its session ends: closing the connections ends
  the reads, and the waits for job results end with the session. A context through every call would be a larger change
  for the same result.

### Consequences

* Good, because a stop no longer interrupts a save, and the end-to-end tests no longer depend on disk speed.
* Good, because the counting is in one place (`open`/`release`, `spawn`); a new goroutine of the server uses `spawn`.
* Bad, because a stop takes as long as the work in flight, up to 10 s.
* Bad, because a goroutine started with a bare `go` is not waited for; nothing but review keeps new code on `spawn`.

### Confirmation

`TestCloseWaitsForTheSaveASessionStarted` holds a session's save in its sync and checks that `Close` returns only after
it; `TestCloseEndsTheWaitForAJobWithoutARecord` checks that a wait for a result ends with `hub_stopping` and leaves the
run open; `TestAfterCloseNothingNewStarts` checks that no connection and no work starts afterwards. The end-to-end tests in
`internal/agent` run with the race detector in `./build.sh`, also on the hosted CI runners
([ADR-0137](0137-checks-run-in-github-actions.md)).
