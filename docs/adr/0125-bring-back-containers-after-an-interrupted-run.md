---
status: accepted
date: 2026-11-06
decision-makers: kay (2026-11-06, "Alles machen", after the check of a point noted on 2026-11-05 showed it still open); proposed by Claude
---

# Bring back containers after an interrupted run

Refines [ADR-0061](0061-restart-containers-after-install.md) (the agent starts containers again after an install) for
a run that is cut short. Everything else there stays.

## Context and Problem Statement

Before an install of the system packages, the agent notes which containers with a restart policy run, and after the
run it starts those that do not (ADR-0061): a Docker upgrade restarts the daemon, and not every such container comes
back on its own.

The list lived only in the agent's memory. When the agent stops during the run (`systemctl restart vordr-agent`, the
host shutting down, a crash), the run ends as `interrupted` and the agent skips the restore. At its next start it
marks the journal `interrupted` and does nothing else. A Docker upgrade that was under way leaves its containers down
until someone notices. The point had been noted earlier and was checked again: still open.

## Decision Drivers

* A container that ran before an install and should run must run after it, also when the run was cut short.
* The agent learns at its next start that a run was cut short, from the run journal (design 10.2).
* Containers someone stopped on purpose stay alone, as in ADR-0061.
* No change to hub, protocol or integration.

## Considered Options

1. Leave it: an interrupted run reports `interrupted`, and the operator starts what is down.
2. Restore while the agent stops.
3. Keep the list in the run journal; the agent's next start restores, before it reports.

## Decision Outcome

Chosen: option 3.

* **Journal:** the containers noted before a system install are written into the run journal with its start
  (`containers`), before the first change. Runs that do not guard containers (pip/npm, images) write none.
* **Next start:** when the journal holds a run without a result, the agent first starts again those of its containers
  that do not run, the same way as after a run (it waits for the daemon at most as long, leaves running and restarting
  ones alone). Only then does it connect.
* **Result:** `interrupted` as before; its summary lists the containers started again (`restarted`), and its detail
  names those that could not be started.
* A journal whose run has a result is left alone: that run restored its containers itself.

Rejected:

* **Option 1,** because an interrupted Docker upgrade is exactly when containers stay down, and nothing would tell the
  operator which.
* **Option 2,** because a stopping agent has little time (systemd's stop timeout), the daemon may be restarting for
  minutes, and a host that shuts down has none at all.

### Consequences

* Good, because a cut-short run no longer leaves containers down.
* Good, because the result says what was brought back.
* Bad, because after an interrupted run the agent's start can wait for the Docker daemon, up to the same two minutes as
  after a run, before it connects.
* Bad, because an agent stopped for good brings nothing back until it starts again.
* Neutral: a journal written by an agent before this change holds no list; such an interrupted run restores nothing,
  as before.

### Confirmation

* `internal/agent`:
  * `TestInstallStartsContainersAgain`: the run's journal holds the containers it noted.
  * `TestInterruptedRunStartsContainersAgain`: after a run cut short, the next start starts the container that is down
    (also while the daemon comes up late), leaves the running one alone, names the gone one in the detail, and the
    interrupted result lists the started one; a journal whose run ended starts nothing.
