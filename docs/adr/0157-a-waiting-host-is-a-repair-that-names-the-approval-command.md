---
status: accepted
date: 2026-11-17
decision-makers: kay (2026-11-17, asked for setup to become simpler and allowed the proposals to be built; accepted with the merge); proposed by Claude
---

# A waiting host is a repair that names the approval command

Refines [ADR-0024](0024-repair-issues.md) (one repair per host problem) for hosts that wait for approval, within
[ADR-0010](0010-administration-only-local.md) (approval only at the hub). The hub, the agent and the protocol stay as
they are: the hub's own view already lists the waiting hosts with their node, the fingerprint it saw and the system
they report.

## Context and Problem Statement

A host that reaches the hub for the first time waits for approval. The operator compares its fingerprint with
`vordr-agent identity` on the host and runs `vordr-hub approve <node> --fingerprint <fp>` at the hub. Home Assistant
already receives the list of waiting hosts, but only counts it in `sensor.vordr_hub_pending_approvals` and keeps the
list in that sensor's attributes. Nothing tells anyone that a host waits or what to do; the operator learns it from
the last lines of `install-agent.sh` on the host or from `vordr-hub list`. A host under a name nobody expects shows
only there as well.

Where does Home Assistant show a waiting host, and how far does it go?

## Decision Drivers

* A waiting host shows where Home Assistant lists things to do, with what the step at the hub needs.
* Approval stays at the hub, and the fingerprint is compared with the host's own output, not with something Home
  Assistant supplies on both sides (ADR-0010: whoever takes over Home Assistant must not be able to admit hosts).
* A new host is news, not an alarm.
* A host that is not approved is a stranger: what it says about itself must not shape the page beyond plain text.

## Considered Options

1. A repair per waiting host that is not fixable and names the fingerprint the hub saw and the approval command.
2. A fixable repair whose fix approves the host from Home Assistant.
3. Like 1, but the command without the fingerprint, to be filled in from the host's `vordr-agent identity`.
4. A persistent notification or an event per new waiting host.
5. Leave it to the sensor.

## Decision Outcome

Chosen option: **1**.

* **Repair** `waiting_<node>` (text `waiting_host`), not fixable, severity warning — the lowest Home Assistant has.
  Placeholders: `node`, `fingerprint`, `os` and `command`, which is `vordr-hub approve <node> --fingerprint <fp>`.
* **Text in this order:** run `vordr-agent identity` on the host itself and compare; only if both fingerprints are the
  same, run the command at the hub; if they differ, do not approve, `vordr-hub reject <node>` keeps the other key out.
* **Comes and goes with the hub's view:** created from every snapshot and every push of the hub's own view; gone as soon
  as the hub no longer lists the host as waiting — approved, rejected or forgotten. After a reconnect the snapshot
  settles what was decided in between.
* **Only what a command can carry as it is:** an entry whose node is no host name (`^[a-z0-9][a-z0-9.-]{0,62}$`) or
  whose fingerprint is not 64 lower-case hex digits gets no repair; the sensor keeps listing it.
* **The system is the stranger's word:** shown as code, on one line, backticks replaced, at most 64 characters, so it
  can carry neither a link nor formatting onto the repairs page; `?` when the host names none.

Option 2 gives a compromised Home Assistant what ADR-0010 keeps from it, and the comparison would happen in Home
Assistant, which then supplies both the fingerprint it shows and the confirmation it sends. Option 3 forces the
comparison by construction, but the common case is an operator who just installed the host: `install-agent.sh` prints
the same command from the host's side, and two complete lines are easier to compare than one to assemble; the hub
refuses an approval whose fingerprint differs from the key it holds, so the pasted command can only admit the key the
hub saw. Option 4 is gone once dismissed and does not go away when the host is approved. Option 5 is today's state.

### Consequences

* Good, because a waiting host shows in the repairs list with the step at the hub, and goes by itself once decided.
* Good, because a host under an unexpected name becomes visible where people look, not only at the hub's shell.
* Good, because nothing changes at the hub, the agent or the protocol; Home Assistant gains no right.
* Bad, because whoever reaches the hub can place up to `max_pending` warnings in Home Assistant (name and system
  constrained as above). The same hosts were already in the sensor.
* Bad, because a ready command invites pasting without comparing. The text asks for the comparison first; the hub's
  fingerprint check limits a careless approval to the key the hub saw, as with `vordr-hub list` before.
* Neutral: `sensor.vordr_hub_pending_approvals` stays as it is.

### Confirmation

`ha/tests/test_repairs.py`: the repair with its placeholders and severity, none for an approved host or an entry a
command cannot carry, the system cut to one line of code; gone on approval, on rejection or removal, and after a
reconnect whose snapshot no longer lists the host. `ha/tests/test_texts.py`: the text in every language with exactly
these placeholders, the comparison before the command, and the command as `vordr-hub`'s usage names it.
`ha/tests/test_e2e_real_hub.py`: against the real hub and agent, the repair shows the fingerprint `vordr-agent
identity` prints, its command run at the hub admits the host, and the repair goes.
