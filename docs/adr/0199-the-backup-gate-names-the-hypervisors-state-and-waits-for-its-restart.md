---
status: accepted
date: 2026-11-19
decision-makers: kay (2026-11-19, asked to fix the refused install while the hypervisor restarts; accepted with the merge); proposed by Claude
---

# The backup gate names the hypervisor's state and waits for its restart

Refines [ADR-0096](0096-fresh-backup-right-before-the-install.md), whose hub refuses a guest's install with
`backup_host_unavailable` when the hypervisor's agent is not there.

## Context and Problem Statement

Before the install of a guest that gets a fresh backup first, the hub checks whether the hypervisor's agent can take the
backup job: known, approved, offering the verb `backup`, connected. ADR-0096 answers each of these four states with the
one code `backup_host_unavailable`, and the text in Home Assistant lists them all: "offline, not approved, or without
guest_backup".

Two things follow:

* **"Update all" in Home Assistant refuses guests at random.** It sends the agent updates and the installs of all hosts
  within a second. The hypervisor's agent swaps its binary and is gone for half a minute; a guest install whose check
  falls into that half minute is refused, one that comes a moment earlier goes through. The hub knows better: the
  hypervisor's run is its own agent update, the agent said it restarts itself (phase `rebooting`), and the hub waits for
  the agent that starts next. The install's run already waits for a running hypervisor and for its first report after a
  start before it asks for the backup
  ([ADR-0147](0147-a-backup-waits-for-a-hypervisor-busy-with-a-check-of-its-own.md)); only the check before the run
  did not.
* **The refusal does not say which state it is.** The hub knows it (the four cases are distinct codes inside the hub),
  but throws it away. The operator has to guess from three possibilities or read the event log to find out that the
  hypervisor was merely restarting.

## Decision Drivers

* State is reported as a fact, not left to be guessed from a text that names several causes.
* A guest install that can be done is not refused because of a moment the hub itself caused and waits out anyway.
* A hypervisor that really cannot make the backup still stops the install before anything starts, so that nothing waits
  for hours for a backup that will not come.
* An older integration keeps working with a newer hub, and the other way round.

## Considered Options

* one code per state, and a hypervisor restarting for a run of the hub's counts as there
* one code, with the state as an extra field of the command result
* let every install with a hypervisor that is merely not connected through, and let the run fail if it does not come back
* keep it as it is

## Decision Outcome

Chosen: one code per state, and a hypervisor restarting for a run of the hub's counts as there.

* The hub refuses with `backup_host_unknown` (before_install names a host it does not know), `backup_host_not_approved`,
  `backup_host_no_guest_backup` (its agent does not offer `backup`: no `guest_backup` in its `agent.yaml`, or an agent
  from before ADR-0096) or `backup_host_offline` (not connected). It no longer sends `backup_host_unavailable`. The
  same codes answer `vordr-hub backup <node>` and stand in the event log of a refused command and of a skipped
  automatic install.
* A hypervisor that is not connected while its last run has no result yet and its agent's last word on that run was
  phase `rebooting` (an agent update or a reboot sent by the hub) counts as there. The install starts in phase
  `backing_up`; its run waits for the hypervisor's run and for the first report of the agent that starts next, as for
  any run of the hypervisor (ADR-0147), and then asks for the backup. If the agent does not come back, the backup job
  cannot be sent, the run ends with `failed_backup`, and nothing is installed.
* The integration has a text for each code that names the hypervisor and the state, and keeps the text of
  `backup_host_unavailable` for an older hub.

Rejected:

* **A field with the state,** because the codes of the backup check are already one code per case
  (`backup_stale`, `backup_missing`, `backup_unreachable`, `backup_unmapped`), and a field would have to travel
  through every caller of a command in the integration, which hands on the code alone.
* **Letting every not connected hypervisor through,** because an install would then wait in `backing_up` for a
  hypervisor that is switched off, up to the bound of the wait, and end as a failed run instead of a refusal the
  operator sees at once.
* **Keeping it,** because "update all" would go on refusing guests depending on which of two messages arrived first.

### Consequences

* Good, because "update all" installs the guests of a hypervisor whose agent it updates at the same time.
* Good, because the refusal says which state keeps the hypervisor from the backup, in Home Assistant, in the
  command-line answer and in the event log.
* Bad, because an integration older than this change shows the new codes as an unknown refusal with the code ("the hub
  did not run the command (backup_host_offline)") until it is updated; the rollout order hub, integration keeps that
  window short.
* Bad, because a hypervisor whose agent update hangs holds its guests' installs in `backing_up` until that run times
  out, instead of refusing them at once.

### Confirmation

`internal/hub/backuphost_test.go`: each state of the hypervisor gives its code, a hypervisor restarting for its agent
update passes, one whose run is over or that never said it restarts is offline (`TestBackupHostStates`); the install
button on a guest while guest and hypervisor update their agents waits in the queue, and a hypervisor that is offline
is refused with `backup_host_offline` in the event log (`TestInstallWhileTheHypervisorRestarts`). Both fail without the
exception for a restarting hypervisor. `internal/agent/prebackup_e2e_test.go`: an unknown hypervisor gives
`backup_host_unknown` (`TestHypervisorAway`). `ha/tests/test_texts.py`: every backup refusal has its text and names
the hypervisor (`test_backup_refusals_have_their_texts`); `ha/tests/test_entities.py`: the install button raises the
text of each state (`test_backup_host_refusal_names_its_state`).
