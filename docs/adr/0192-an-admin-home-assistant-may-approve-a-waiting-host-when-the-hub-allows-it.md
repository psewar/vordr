---
status: accepted
date: 2026-11-18
decision-makers: kay (2026-11-18, chose approval in Home Assistant over a web interface on the hub, because approving every new machine with a command at the hub is too much work for most users; accepted with the merge); proposed by Claude
---

# An admin Home Assistant may approve a waiting host when the hub allows it

Supersedes [ADR-0010](0010-administration-only-local.md) (administration and approval only locally at the hub) and
[ADR-0157](0157-a-waiting-host-is-a-repair-that-names-the-approval-command.md) (a waiting host is a repair that
names the approval command). Both hold as decided there, except where this ADR says otherwise; the parts that stay are
repeated below so that this ADR is complete.

## Context and Problem Statement

A host that reaches the hub for the first time waits for approval. Today only the hub's command line admits it:
someone with a shell on the hub host compares the fingerprint with `vordr-agent identity` on the new host and runs
`vordr-hub approve <node> --fingerprint <fp>`. Home Assistant shows the waiting host as a repair with that command
(ADR-0157), and since [ADR-0161](0161-home-assistant-installs-the-agent-once-over-ssh-and-keeps-no-credentials.md)
it installs the agent itself over SSH, after the person confirmed the host's SSH key. The last step still needs a shell
on the hub host. For most users that is the step they cannot take or do not want to take for every host.

May Home Assistant approve a waiting host, and under which conditions?

## Decision Drivers

* Setting up a host ends where it started, in Home Assistant, without a shell on the hub host.
* The fingerprint is still compared with something the host itself says, not only with what Home Assistant shows.
* Whoever takes over Home Assistant gains as little as possible; a site that wants ADR-0010's boundary keeps it
  without editing anything.
* The hub's attack surface does not grow by a second user interface with its own login.

## Considered Options

1. A web interface on the hub, linked from Home Assistant.
2. Approval from Home Assistant, off unless `hub.yaml` allows it, for clients with role `admin` only; the fix of the
   waiting repair asks for the fingerprint `vordr-agent identity` prints on the host, and "Add host" approves the key
   it read over the SSH connection whose host key the person confirmed.
3. Like 2, but always on for clients with role `admin`.
4. As today: the command at the hub only.

## Decision Outcome

Chosen option: **2**.

**At the hub:**

* `hub.yaml` key `ha_approves_hosts` (default `false`). `check-config` names it. Off, everything is as in ADR-0010.
* On, the hub names the feature `approve_host` in the status it sends a client that is approved and has role `admin`
  ([ADR-0039](0039-ha-client-roles.md)); a role change sends the status again. As with every feature (ADR-0067), a
  client sends the message only to a hub that named it.
* `approve_host {request_id, node, fingerprint}` is answered by `approve_host_result {request_id, code}`. The hub
  approves only when, decided on its register as it is at that moment: the switch is on (else `ha_approval_off`), the
  client is approved (else `client_not_approved`, and the connection ends as with `agent_download`) with role `admin`
  (else `role_forbids`), the node waits for approval (unknown: `unknown_node`; approved or rejected: `not_waiting`, so
  a rejection at the hub is never undone from Home Assistant), and the fingerprint is the one the hub holds for it
  (else `fingerprint_mismatch`). A malformed fingerprint is `bad_request`.
* The event log has `approve` with the client's name, as `vordr-hub approve` has it without one; a refusal is
  `approve_refused` with its code.
* Rejecting and forgetting hosts, and approving, rejecting and forgetting Home Assistant clients and their roles, stay
  at the hub's command line over its local socket (mode 0600 in a 0700 folder), as ADR-0010 decided. The hub has no
  web interface and refuses browser connections (`Origin`).

**In Home Assistant:**

* A waiting host is the repair `waiting_<node>` as ADR-0157 decided: severity warning; placeholders `node`,
  `fingerprint`, `os` and `command`; only for a node that is a host name and a fingerprint of 64 lower-case hex
  digits; the system as one line of code; it comes and goes with the hub's view.
* While the hub names `approve_host`, the repair is fixable and its text says so: compare on the host, then fix it
  here, or run the command at the hub. Its fix asks for the fingerprint `vordr-agent identity` printed on the host.
  Spaces and upper case are ignored; anything else than 64 hex digits is refused in the form. A fingerprint that
  differs from the one the hub saw is refused in the form as well, nothing is sent, and the text says not to approve
  and how to reject at the hub. A matching one is sent; the answer's code decides what the dialog says.
* "Add host" (ADR-0161): after `agent_installed`, while the hub names `approve_host`, the flow waits up to 60 seconds
  for the hub to list the node as waiting with exactly the fingerprint the install read on the host, then sends
  `approve_host`. The person confirmed the host's SSH key in the same flow, so that fingerprint comes over a checked
  connection: the comparison the person would make is made by construction. The result says whether the host was
  approved; otherwise it is the text of ADR-0161 and the repair remains.

Option 1 adds a login, sessions, protection against forged requests and against the strangers' texts it would show,
and a second place to look next to Home Assistant, on the one service that signs every job. Option 3 changes the
boundary of every existing site with an update. Option 4 keeps the step most users cannot take.

### Consequences

* Good, because a host installed with "Add host" is approved in the same flow, without a shell on the hub host.
* Good, because a host installed otherwise is approved from its repair, after the person read the fingerprint on the
  host itself.
* Good, because nothing changes for a site that does not set `ha_approves_hosts`.
* Bad, because a taken-over Home Assistant with role `admin` can approve any host that waits, with the fingerprint
  the hub shows it: the comparison protects against carelessness, not against Home Assistant itself. Such a host gets
  only jobs meant for it and can report what it likes about itself; it cannot take a known host's name, since a node
  keeps its key, and it cannot undo a rejection. The event log names the client that approved it.
* Bad, because the person can paste the fingerprint from the repair instead of from the host. The text asks for the
  host's own output.

### Confirmation

Hub: tests for `approve_host` accepted, and refused for each code above, the client's connection ending when it is
not approved; the feature named only with the switch on and role `admin`, and again after a role change; the
`hub.yaml` key in `hub.example.yaml` and in `check-config`. Integration: the repair fixable only with the feature; the
fix refusing a malformed and a differing fingerprint without sending anything, and showing each refusal code; "Add
host" approving after `agent_installed` with the feature, and keeping the text of ADR-0161 without it or when the host
does not show up in time; the texts in every language.
