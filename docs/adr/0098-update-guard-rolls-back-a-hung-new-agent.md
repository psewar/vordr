---
status: accepted
date: 2026-10-31
decision-makers: kay (2026-10-31, asked to close the gap; 2026-11-01, accepted); proposed by Claude
---

# An update guard outside the new binary rolls back a new agent that hangs

Extends [ADR-0032](0032-self-update-signed-by-operator.md) and
[ADR-0084](0084-windows-self-update-without-code-signing.md). Their way back stays as it is; this adds one for the case
they do not cover.

## Context and Problem Statement

After a self-update swap there are three ways back to the previous binary:

1. The new agent itself: no approved hub session within 10 minutes, it puts the old binary back and exits.
2. Linux: after four failed starts the unit fails, and `OnFailure` runs `rollback.sh`.
3. Windows: on the third failure the service's recovery runs `rollback.cmd`.

All three need the new binary to do something: run its own timer, or end. A new agent that hangs does neither, before
it starts its timer or in a way that stops the timer. systemd and the service manager see a running agent, and nothing
puts the old binary back. The host stays without a working agent until someone notices it offline at the hub and
steps in by hand. Found while building the rollback drill, which tests only a new agent that crashes.

## Decision Drivers

* The way back must not depend on the binary it protects against: only the old binary is known to work.
* It reaches the self-update through the hub, like every agent fix. Unit files, `rollback.sh` and `rollback.cmd` change
  only through `install-agent.sh` or `install-service` by hand, on every host.
* The paths that work today stay first. The new one acts only where none of them did.
* The agent that comes back reports what happened as a fact, not as text someone has to interpret.

## Considered Options

* An update guard: before the swap, the old agent starts a process outside its own unit or service. If the update is
  still pending after a window, the guard puts the old binary back and restarts the agent.
* A systemd watchdog (`Type=notify`, `WatchdogSec`): the agent pings systemd from its main loop. Without the pings
  systemd kills it, and the restarts lead to `OnFailure`.
* Leave the gap and rely on the hub: an agent that stays away after an update shows as `timeout` and offline, and the
  operator steps in by hand.

## Decision Outcome

Chosen: the update guard.

* **Start.** The old agent writes the marker `update.json`, then starts the guard, then swaps. If the guard cannot be
  started, there is no swap: the job ends `failed_package_manager`, "update guard: …".
  * On Linux the guard is a transient unit, `vordr-agent-update-guard.service`, started by `systemd-run` with
    `Type=exec` and `--collect`. `Type=exec` makes `systemd-run` return only after the guard has started the binary,
    which is still the old one before the swap. Self-update on Linux now needs `systemd-run`; without it the agent does
    not announce `agent_update`.
  * On Windows the guard is a process detached from the service, so the service manager does not end it with the
    agent. `CreateProcess` maps the image before it returns.
* **Watch.** `vordr-agent update-guard --job <id> --exe <path>` checks every 10 seconds whether the marker still exists.
  It ends as soon as the marker is gone: the new agent confirmed, or the old one reported. It reads the marker only once,
  at the end, and on Windows with `FILE_SHARE_DELETE`. With an ordinary open, the agent's removal of the marker (its
  confirmation) failed with a sharing violation, and the guard would then have rolled back a confirmed agent. A marker
  of another job at the end means its own update was settled long ago.
* **Act.** After 15 minutes (`confirmWindow` plus 5 minutes) the guard acts, but only if the marker is still this job's
  and `<path>.old` is still there; otherwise one of the three ways above was faster. The guard then:
  1. puts the old binary back;
  2. writes into the marker who did it and why (`rolled_back`);
  3. restarts the agent. On Linux it sends SIGKILL to the unit's main process, then `reset-failed` and `restart`. On
     Windows it ends the service process and starts the service, unless the service's recovery already started it.
* **Report.** The old agent reports `failed_verification` as before, with the reason from the marker: "hung: not
  confirmed within 15m0s, the update guard put 0.x.y back". The new agent's own rollback now writes its reason as well
  ("no approved session within 10m0s").

Rejected:

* **The watchdog,** because it needs `Type=notify` and `WatchdogSec` in the unit file. That file changes only by hand,
  and a unit with `WatchdogSec` kills every agent that does not ping yet. It also depends on the new binary: a binary
  whose ping loop runs while the rest hangs keeps the watchdog quiet. And it has no Windows counterpart.
* **Leaving the gap,** because the hub only shows the failure; nobody puts the agent back. The canaries are the hosts
  that run a release first.

### Consequences

* Good, because a new agent that hangs, starts something else, or never reaches its own timer is rolled back within
  15 minutes. The hub waits 30 minutes for the result (design 10.4), so the old agent's report arrives in time.
* Good, because the guard is the old binary, started before the swap: what it does was proven by the release before.
* Good, because the reasons for a rollback are now facts in the marker and in the job's detail.
* Bad, because the guard takes effect one release late. The agent being replaced starts it, so an update away from a
  release with the guard is the first one it protects.
* Bad, because the guard does not survive a reboot of the host within the 15 minutes. A new agent that hangs after such
  a reboot is not rolled back. The new agent's own window still runs from its start.
* Bad, because the guard's restart ends a hung agent the hard way, as a crash would. An unconfirmed agent had no
  approved session and so started no install run, but a run it got anyway would be cut short.
* Bad, because on Windows the new agent's own rollback may fail to write its reason into the marker. Replacing a file
  fails at the moment the guard checks it, so the report then gives the general reason. The confirmation (removing the
  marker) is not affected.
* Neutral: on Linux the guard logs to its own unit's journal (`journalctl -u vordr-agent-update-guard`), on Windows to
  `agent.log`.
* Assumed: the service manager runs the Windows service in no job object that ends its children with it. Checking that
  needs an elevated prompt; the live check below shows it.

### Confirmation

Tests:

* `TestGuardRollsBackAHungUpdate` checks that the guard acts after the window.
* `TestGuardEndsWhenTheUpdateSettles` checks that it ends untouched once the marker is gone.
* `TestGuardLeavesWhatIsNotItsToDo` covers another job, an old binary already put back, and an unreadable marker.
* `TestHungUpdateRolledBackByTheGuard` runs update, guard and the old agent's report end to end.
* `TestSelfUpdateNeedsTheGuard` checks that there is no swap without the guard.
* `TestArmGuardStartsATransientUnit` checks the `systemd-run` command line.
* `TestSelfUpdateAndConfirm` checks that the guard starts once, from the old binary.
* `TestGuardDoesNotBlockTheConfirmation` removes the marker while the guard checks it without pause, and while it reads
  it. It runs natively on Windows as well, where it caught the sharing violation.

A mutation probe (each change must fail a test) found all 9 changes on Linux, among them "no guard before the swap",
"no wait for the window" and "no `Type=exec`". It also found the one on Windows: reading the marker without
`FILE_SHARE_DELETE`.

Live check: a variant of the rollback drill whose new agent hangs instead of crashing, on a canary and on a Windows
host, once a release with the guard runs there.
