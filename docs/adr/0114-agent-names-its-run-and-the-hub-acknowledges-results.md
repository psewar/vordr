---
status: accepted
date: 2026-11-05
decision-makers: kay (2026-11-05, asked to build it); proposed by Claude after a review of the hub's run lifecycle and the agent's run journal
---

# The agent names its running job in the hello, and the hub acknowledges final results

## Context and Problem Statement

Hub and agent each guess the other's view of a run. A review found two places where the guess goes wrong.

1. **Interrupted runs count as finished.**
   * When the tunnel breaks, the hub records the run as `interrupted`. The queue, the rollout and automatic installs
     treat that as the end of the run.
   * A queued job can then reach an agent that still runs the install. The agent answers `rejected_busy`, which
     becomes the host's last run.
   * The install's real result arrives later from the run journal and is dropped, because its job ID no longer
     matches.
   * Keeping `interrupted` as running until the job's timeout would stall the host for up to 25 h when the run is
     really gone (the agent restarted before its journal, or a check whose answer was lost).
2. **A result counts as reported once it is written to the socket.**
   * The journal marks a run's result as reported when the write succeeded. A write only reaches the TCP buffer.
   * When the hub host loses power, or the path drops without a reset, the agent notices only after its idle
     timeout (90 s).
   * A result written in that time is never sent again. The hub keeps the run as `interrupted` for ever.

Both are the same gap: neither side states what it knows about a run, so the other side works it out from a broken
connection ([ADR-0007](0007-fixed-codes-instead-of-error-texts.md): state as facts).

## Decision Drivers

* Report state as a fact, do not infer it from a disconnect.
* A run's result reaches the hub exactly once, also across hub restarts and silent connection losses.
* A host is never blocked longer than its run really takes.
* Old agents and old hubs keep working: the new fields are announced like other features
  ([ADR-0067](0067-hub-announces-features.md)).

## Considered Options

1. **Handshake in both directions.** The hello carries the job the agent runs now (`running`: job ID, verb, phase)
   and the job whose result its journal has not had acknowledged (`unacknowledged`). The hub acknowledges each final
   status with a signed-for-that-host `ack` (job ID). The journal marks a result as reported only on that `ack`.
2. **Only the hub side:** keep `interrupted` as running until the timeout.
3. **Only the agent side:** resend every final status after each reconnect for a while.

## Decision Outcome

Proposed: option 1.

* **Hello:**
  * `running` names the job the agent executes now, if any.
  * `unacknowledged` names the run in its journal whose result the hub has not acknowledged.
  * After the hello, the agent sends that result again.
* **Hub, on the hello:**
  * The host's last run is the job `running` names: it stays running, whatever the hub recorded at the disconnect.
    The queue, rollout and automatic installs wait for it.
  * If the hub's last run is unsettled or `interrupted`, and the agent names neither it as running nor its result
    as unacknowledged, the agent has no such run. The hub settles it as `interrupted` for good, and the host is free
    at once, without waiting for the timeout.
* **Hub, on a final status:** it stores the result, then sends `ack` with the job ID. A second copy of the same result
  is acknowledged again and changes nothing.
* **Agent:** the journal's `Reported` becomes `Acknowledged`, set only on the `ack`. The `ack` is only accepted from
  the hub's session and only for the job in its journal.
* **Compatibility:**
  * The hub announces the feature `ack`. An agent sends `running`/`unacknowledged` and waits for `ack` only from such
    a hub; with an older hub it behaves as today.
  * An older agent sends neither, and the hub keeps today's behaviour for it.
  * Rollout: hub first, then agents.

Rejected:

* **Option 2,** because it stalls a host for up to a day after a lost run, which is what the bounded check in the
  queue avoids today.
* **Option 3,** because the agent still cannot know when to stop, and the hub must tell duplicates apart anyway.

### Consequences

* Good, because a broken tunnel no longer lets a second job run into a running install, and the install's result
  is never dropped.
* Good, because a result survives a power loss of the hub host.
* Good, because a host whose run is gone is free right at its next hello, instead of after the timeout.
* Bad, because it is a protocol change in both directions, with a new message type.
* Neutral: the agent's journal format grows by one field; an older journal reads as not acknowledged and is sent once
  more.

### Decided while building (2026-11-05)

The proposal left these open or got them wrong; the build settled them.

* **The hub announces `ack` in its challenge, not only in its status.** The hello comes before the status, and an
  older hub reads the hello strictly and refuses a field it does not know. So the agent must know before its hello.
  The status announces `ack` as well, like every other feature.
* **`runs` is one object with two lists of job IDs.** Verb and phase are not needed: the hub knows them from its own
  record of the run. The object being present is what marks an agent that takes acks; an agent with nothing to name
  sends empty lists.
* **The ack is not signed.** It carries no authority: a forged ack could only make the agent stop resending a result,
  and whoever can forge it can also drop the result. It is accepted only over the session and only for the job in
  the journal or the reboot marker.
* **The reboot marker uses the ack too; the self-update marker does not.** The update guard takes the removal of the
  self-update marker as the confirmation of the update (ADR-0098), which must not wait for the hub. Both are named
  as held results in the hello, so the hub does not end a self-update or a reboot as lost. Until the host goes down,
  a reboot is named as running, and the agent does not report its own pending reboot as failed.
* **The old connection can end after the new hello.** The hub notices a dead connection only after its idle
  timeout, so the agent's new hello may come first. The request waiting for the job then keeps waiting for the
  result over the new session (event `job_continues`) instead of recording `interrupted`. The claim ends with the
  session that made it.
* **Events:** `run_continues` (an interrupted run runs again), `run_lost` (an open run the agent does not have),
  `job_status_again` (a second copy of a stored result).
* **After an independent review of the build (same day):**
  * An install still waiting for its guest's backup (`backing_up`) has not been sent, so the guest's hello cannot
    name it. It is left as it is.
  * Every job the hello names as running keeps the host busy until its result comes, also one that is no run of the
    host's own, like a hypervisor's backup for a guest. Before, only the host's last run counted, and a queued command
    could reach a hypervisor that was still backing up. Known limit: the install that waited for such a backup is
    still refused when the hypervisor's tunnel breaks, because the hub decides at the disconnect. The backup completes,
    and nothing is installed.
  * A final status without a journal that finds no session is kept in memory. It is named in the next hello and sent
    on the next approved session. Otherwise a job that finished between a hello naming it as running and the new
    session would have left the hub waiting until the job's timeout.
  * Only the hub's own guess at a disconnect, which has no detail, becomes "the agent reported no such run". An
    interrupted run with a detail of its own keeps it.
  * A request whose old connection ends after the result already came over the new session returns that result. It
    never records `interrupted` over it.
  * The hub acknowledges only once the result is stored. A second copy is known by its code and detail, so the agent's
    own interrupted result replaces the hub's guess, and a copy of it changes nothing.

### Confirmation

* e2e tests with a real hub and agent (`internal/agent/handshake_e2e_test.go`):
  * the tunnel breaks during an install; a queued command waits; the result arrives after the reconnect and is kept;
  * the ack for a result is lost; the agent sends it again, and the hub stores it once;
  * the hub had the run open (restarted during it) and the agent holds the result; it arrives;
  * the hub had a run open the agent does not have; the hub frees the host at the hello.
* Compatibility tests: an old agent with a new hub (no ack), and a new agent with an old hub (no `runs` in the hello,
  a sent result counts as reported).
* Hub tests for each decision (`internal/hub/handshake_test.go`, `handshake_review_test.go`), an old agent's login,
  and 22 mutation probes: each decision reverted turns its test red.
