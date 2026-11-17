---
status: proposed
date: 2026-11-17
decision-makers: kay (2026-11-17, accepted in principle, implementation later; asked on 2026-11-17 for a new machine to join with one command); proposed by Claude; status becomes accepted with the merge that implements it
---

# A host can join with a one-time token issued at the hub

Refines [ADR-0010](0010-administration-only-local.md): admission stays local at the hub, but it can be given in advance
and bound to an intent. Issuing a token from Home Assistant is not part of this decision.

## Context and Problem Statement

A new host joins in two steps that depend on each other. The agent starts, connects and waits as `pending_approval`;
then someone at the hub runs `vordr-hub approve <node> --fingerprint <fp>` after comparing the fingerprint
(ADR-0010). The installer prints that command at the end, but whoever installs the agent has to switch to the hub, and
the hub holds a list of unknown senders in the meantime (at most `max_pending`).

Home Assistant cannot admit hosts, by design (ADR-0010; [ADR-0039](0039-ha-client-roles.md)). A way to admit a host
before it connects, bound to what the administrator intends, removes the second step without handing Home Assistant a
new power.

## Decision Drivers

* Admission stays a decision of someone with access to the hub's admin socket, the same trust as `approve`.
* A secret that can admit a host never crosses the network in the clear and is useless once used or expired.
* What someone can do who sees the command or the token is small, bounded and visible in the event log.
* The hub cannot enforce a host's local policy ([ADR-0006](0006-local-policy-decides.md)), so the token does not carry
  one.
* Names with a special role in `hub.yaml` (the hypervisor for backups, the canaries, hosts without a backup) must not be
  claimable by a token that is not issued at the admin socket.
* Failures are reported as fixed codes ([ADR-0007](0007-fixed-codes-instead-of-error-texts.md)); a refused token says
  why.

## Considered Options

* Keep approval by fingerprint after the connection (status quo).
* A one-time token issued at the admin socket, with a proof the agent computes from it.
* A shared enrolment password for all new hosts.
* Approve waiting hosts from Home Assistant. Not chosen here: a compromised Home Assistant could admit any host that
  knocks, and the fingerprint comparison would be theatre, because Home Assistant would supply both sides.

## Decision Outcome

Chosen option: a one-time token issued at the admin socket.

* **Issue.** `vordr-hub enroll <node>` creates a token `<id>.<secret>`. `id` (8 bytes, hex) is for lookup and the
  event log; `secret` (32 bytes, base64url) is kept by the hub in `enrollments.json` in the state folder, mode `0600`,
  like `hub.key`.
* **Proof, not password.** The agent sends in its signed hello
  `HMAC-SHA256(secret, "vordr/enroll/v1" 0 nonce 0 node 0 public_key)`. The secret never leaves the host. A proof that
  is captured or forwarded is good only for that key and that challenge. The field exists only for a hub that announced
  the feature ([ADR-0067](0067-hub-announces-features.md)); an agent refuses before sending against a hub without it.
* **Bound to the node name.** The token admits exactly that name; another name gets `node_mismatch`. A name the
  registry already knows gets `node_known` (a reinstall means `forget` first, as today).
* **Lifetime and single use.** Default 30 minutes, at most 24 hours at the admin socket. The first valid proof
  consumes the token, atomically under the registry's lock: of two simultaneous hellos exactly one wins; the other
  gets `used`.
* **Limits.** At most 5 open tokens (`enrollment.max_open`). A wrong proof is counted and logged but does not lock the
  token (256 bits cannot be guessed, and a lock would only be a lever for denial of service). Unknown ids are logged at
  most once per address and minute. Joined hosts are never `pending`, so `max_pending` is untouched.
* **Reserved names.** Names that `hub.yaml` gives a role are refused for a token with `node_reserved`; they can only be
  admitted with `approve` at the admin socket.
* **Event log.** `enroll_issued`, `enroll_revoked`, `enrolled` (node, id, fingerprint, remote address, agent version)
  and `enroll_refused` with a fixed reason (`unknown_token`, `expired`, `used`, `node_mismatch`, `bad_proof`,
  `node_known`, `node_reserved`, `key_mismatch`, `enrollment_off`). The secret is never logged. The registry entry
  remembers how the host joined.
* **Policy template is recorded, not enforced.** If the agent's reported policy differs from the template named at
  issue, the hub logs `enroll_policy_differs`.
* **Home Assistant later, separately.** Issuing tokens from Home Assistant is a separate decision with its own grant:
  the role `admin` is not enough; it needs an explicit per-client allowance, so no existing admin client gains the
  power silently on a hub update. It needs its own ADR with a threat analysis (a compromised Home Assistant could
  then show a forged command to an administrator).

What someone who sees the command or token can do: within its lifetime and before use, join with their own key under
that one name; the intended machine then fails with `used` and a hint to run `forget`. After use or expiry: nothing.

### Consequences

* Good, because setting up a host needs no second visit to the hub and no fingerprint comparison, while admission is
  still a local act of the hub's administrator.
* Good, because the secret never travels and a token is single use, bound to a name and short-lived.
* Neutral: tokens persist in a file with the hub's other secrets; the hub's file access is already the trust boundary.
* Bad, because the hub needs a new protocol field and a new state file; hub is updated before agents
  ([ADR-0049](0049-site-decision.md)).
* Bad, because someone who sees a valid command can win a race for that one name within the lifetime. It is visible as
  `used`.

### Confirmation

* Hub tests: valid proof admits the host, consumes the token and logs `enrolled`; refused: unknown, expired, used,
  wrong name, wrong proof, known name, reserved name, too many open tokens, revoked, feature off; two simultaneous
  hellos, one winner.
* Agent test: against a hub without the feature the agent refuses before sending.
* A test that the secret appears in no event, no view and no diagnostic output.
* `check-config` accepts the `enrollment` section of `hub.example.yaml` and refuses an invalid one.
