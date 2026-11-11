---
status: accepted
date: 2026-11-02
decision-makers: kay (2026-11-02, accepted); proposed by Claude
---

# The Windows service stops cleanly after a self-update, and the update guard starts it again

Refines [ADR-0084](0084-windows-self-update-without-code-signing.md) (how the Windows agent restarts after the swap)
and extends [ADR-0098](0098-update-guard-rolls-back-a-hung-new-agent.md) (the update guard).

## Context and Problem Statement

After the swap the old Windows agent ended with exit code 3. The service's recovery actions, set by `install-service`,
restart it: after 10 seconds, after 1 minute, and the third time `rollback.cmd` after 5 minutes. The service manager
counts every end with an error code as a failure, also when only a service that does not crash asks for its restart
(`FAILURE_ACTIONS_ON_NONCRASH_FAILURES`). It resets the count only after a day without failures (reset period
86400 seconds).

So every self-update used up one step of the chain, and the third self-update within a day reached `rollback.cmd`:
the service manager did not start the new agent but put the old one back. With three self-updates within a day, the
third one was rolled back with `failed_verification`, "did not stay up", although the new binary was fine. The
automatic retries every `retry_after_hours` would have failed the same way as long as the count stayed up, and each
failure extended it by another day.

The chain was meant for a new binary that does not start: the planned restart was its first step. That holds only
while the count is zero before the update.

## Decision Drivers

* A planned restart must not count as a failure; real failures of the new binary must still lead to `rollback.cmd`.
* Rely only on documented behaviour of the service manager. Whether changing the recovery actions resets the count is
  not documented.
* The way back for a new binary that hangs or does not start stays as it is.
* No extra service, task or process beyond what a self-update already starts.
* Fail safe: if the new way is not available, the old one still restarts the agent.

## Considered Options

1. Reset the count before the planned exit by setting the recovery actions again.
2. A shorter reset period, for example 15 minutes, long enough for the chain of three crashes.
3. Stop the service cleanly (exit code 0), and let the update guard, which runs outside the service during every
   self-update anyway, start it again.
4. A separate helper process or a scheduled task that starts the service after the stop.

## Decision Outcome

Chosen: option 3. Released with agent 0.11.1, after the live check under Confirmation.

* The update guard creates a named event for its job (`Global\vordr-update-guard-<job>`) and holds it as long as it
  runs. Before the swap the old agent waits up to 30 seconds until the event exists; without it, it refuses the swap
  (as for a guard that cannot be started at all).
* For the restart, the agent sets the event and then stops the service cleanly: it reports `SERVICE_STOPPED` with
  exit code 0. The service manager counts no failure.
* The guard waits until the service has stopped and starts it once, with the binary now in place. Then it goes on
  watching the update as before.
* The new agent's own rollback (no approved session within `confirmWindow`) restarts the same way.
* If the agent cannot reach the guard (no event), it ends with exit code 3 as before, and the recovery actions restart
  it.
* The recovery actions stay unchanged. A new binary that does not start now reaches `rollback.cmd` after three crashes
  of its own instead of the planned restart and two crashes.
* On Linux nothing changes: systemd restarts the agent after its exit and counts no failure.

Rejected:

* **Option 1,** because no documentation says that setting the recovery actions resets the count. A fix that depends
  on it could stop working with any Windows update, without a sign.
* **Option 2,** because it only makes the fault rarer: two self-updates within the period, for example a drill and
  the real release, or a retry right after a failed update, still use up the chain. A period that is too short could
  also reset the count in the middle of a real crash chain, so that `rollback.cmd` never runs.
* **Option 4,** because the update guard already runs outside the service for exactly this window. A second process or
  a scheduled task would add something to install, clean up and secure.

### Consequences

* Good, because any number of self-updates in a row restart the new agent; the failure count is left for real
  failures.
* Good, because the handshake also proves that the guard runs before the swap, not only that it was started.
* Good, because without the guard the agent falls back to the old restart instead of staying stopped.
* Bad, because the restart now depends on the guard. If the guard cannot start the service, the agent stays stopped
  until the guard's window ends and it puts the previous binary back (at most 15 minutes).
* Bad, because the change takes effect one release later: the swap is done by the agent that already runs. The update
  to the release with this change still restarts the old way and counts as a failure.
* Neutral: the event is created by LocalSystem with the default security; other users cannot set it. Setting it does
  no more than start the agent service once while an update is pending.

### Confirmation

* `TestPlannedRestartReachesOnlyItsGuard` (Windows): an agent reaches the guard of its own job only while that guard
  listens, each request arrives once, and the event goes with the guard.
* `TestGuardAnswersAPlannedRestart`: the guard starts the service once per request and goes on watching; after a
  failed start it puts the previous binary back at the end of its window.
* `TestSelfUpdateAndConfirm` and `TestSelfUpdateRollsBack`: the exit names the job of the update.
* A mutation probe turns each of these tests red when its check is removed.
* Live, before the release: a Windows host whose failure count is above zero (at three, the old restart would run
  `rollback.cmd`) runs a build with this change and updates itself; the new agent must come up and report to the hub
  without a service manager event 7031, without a logged failure and without `rollback.cmd`.
