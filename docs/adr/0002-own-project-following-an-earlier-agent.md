---
status: accepted
date: 2026-10-28
decision-makers: kay (direction); details from design draft v0.1
---

# Own project following the pattern of an earlier agent

Backfilled on 2026-10-30, decided on 2026-10-28. Sources: the operator's project notes (2026-10-28),
`design/architecture.md` sections 3 and 4.

## Context and Problem Statement

An earlier agent project has a proven pattern: Ed25519 identity with approval, an outgoing tunnel to a hub,
signed self-update with a canary. Vördr needs the same. But Vördr jobs act as root.

## Decision Drivers

* No open port on managed hosts.
* A job must be genuine even if someone redirects DNS or the network.
* A new host only gets in with explicit approval.
* Reuse what is proven.

## Considered Options

* Vördr as part of the earlier project
* own project following the pattern of the earlier agent

## Decision Outcome

Chosen option: own project following the pattern of the earlier agent. The operator: not inside the earlier project,
because the topic is unrelated.

* The agent creates its own Ed25519 identity and logs in with a challenge (context `vordr/hello/v1`).
* A new host waits as `pending_approval` until it is approved at the hub with its fingerprint. Its key is fixed from
  the first hello; another key under the same name stays out (`key_mismatch`) until the host is removed with `forget`.
* The agent only connects outward (WebSocket over TLS, `/v1/tunnel`).
* Stricter than the earlier agent: the hub signs every job (context `vordr/job/v1`, valid for at most 5 minutes,
  replay protection). The agent checks it against the hub key pinned in its `agent.yaml`.

Why sign jobs although the tunnel is authenticated: in the earlier project only the agent proves who it is. Here the
agent must know that a root job comes from its hub.

### Consequences

* Good, because whoever redirects the network cannot forge jobs.
* Good, because no port is open on the hosts.
* Bad, because a new hub key means changing every `agent.yaml`.
* Bad, because approval is manual work with fingerprints.

### Confirmation

* Tests in `internal/protocol` (`TestVerifyJobRejects`, `TestVerifyJobRejectsReplayUntilExpiry`),
  `TestEndToEndApprovalReportAndCheckJob`, `TestHubRefusesBadHelloSignature`.
* Mutation probes when it was built (4 of 4 and 9 of 9 caught).

## More Information

Related: [ADR-0010](0010-administration-only-local.md) (approval only at the hub),
[ADR-0012](0012-copy-parts-of-the-earlier-agent.md) (copy code instead of sharing it),
[ADR-0032](0032-self-update-signed-by-operator.md) (self-update).
