---
status: accepted
date: 2026-11-11
decision-makers: kay (2026-11-11, asked to make the backup before an install wait for a busy hypervisor; accepted with the merge); proposed by Claude
---

# A backup waits for a hypervisor busy with a check of its own

## Context and Problem Statement

Before an install on a guest, the hub has the hypervisor's agent back the guest up
([ADR-0096](0096-fresh-backup-right-before-the-install.md)). The agent runs one job at a time and refuses the backup
job with `rejected_busy` while it holds its own lock: during a check, and during the inventory that follows a report.
The hub waited for runs it knows of, and asked again after a busy refusal at most three times, five seconds apart, on
the assumption that a check of the hypervisor's own is over in moments.

After the agent starts, for example after its self-update, it runs a check that includes refreshing the package
lists; together with its inventory that takes longer than ten seconds. An install whose backup was asked for in that
time ended with `failed_backup` and installed nothing, although the hypervisor would have taken the backup a few
seconds later. The same can happen with a scheduled check of the hypervisor on a slow mirror.

## Decision Drivers

* A backup that the hypervisor would make moments later does not fail the install.
* The hub uses what it knows as a fact, not only the refusals: an agent that connected and has not reported yet is
  in the check after its start.
* A hypervisor that stays busy still ends the wait, with a reason, and nothing is installed.
* Every wait is bounded.

## Considered Options

* wait for the first report after the hypervisor connected, and ask again after busy refusals for a time instead of a
  number of tries
* only ask again for longer
* let the hypervisor's agent wait for its own lock instead of refusing

## Decision Outcome

Chosen: both waits in the hub.

* **Starting up:** while the hypervisor is connected and has not reported on this connection, the backup waits, as it
  waits for a run of the hypervisor's own. At most 10 minutes after the connection came; an agent whose check hangs
  then gets the backup job, and its refusals decide.
* **Busy:** a `rejected_busy` is asked again with pauses that start at 5 seconds and double up to 30 seconds, for up to
  2 minutes. Then the install ends with `failed_backup`, "<hypervisor> stayed busy with a check of its own for 2m0s",
  and nothing is installed.
* Both bounds lie within the wait of an install for its backup (5 hours, ADR-0096). The hub keeps them as settings of
  its own (poll, patience, startup wait); `hub.yaml` does not change.

Rejected:

* **Only ask again for longer,** because the hub would send backup jobs into a check it can see from the missing
  report, and every refusal is one more signed job and two more events.
* **Let the agent wait for its lock,** because a job would then sit in the agent without a bound the hub controls, and
  it needs a new agent release on every hypervisor; the agent's answer `rejected_busy` stays as it is.

### Consequences

* Good, because an install right after the hypervisor's agent restarted, or during its scheduled check, gets its
  backup instead of failing.
* Good, because the reason of a backup that did not come about says how long the hub waited.
* Bad, because a backup can now wait up to about 12 minutes for a hypervisor that just started and stays busy, before
  the install ends; the install would not have happened earlier either.

### Confirmation

`internal/agent/prebackup_e2e_test.go` with the real hub and agents: the backup waits while the hypervisor's agent
holds its lock longer than three quick tries and goes ahead afterwards (`TestBackupWaitsWhileTheHypervisorIsBusy`); a
hypervisor that stays busy ends the install with `failed_backup` and the reason
(`TestBackupGivesUpWhenTheHypervisorStaysBusy`); no backup job goes to a hypervisor before its first report after
connecting (`TestBackupWaitsForTheHypervisorsFirstReport`).
