---
status: accepted
date: 2026-10-29
decision-makers: kay (like the earlier agent: canary, then all)
---

# Self-update: signed by the operator, distributed by the hub, in rings

Backfilled on 2026-10-30, decided on 2026-10-29. Sources: `design/architecture.md` 4 and 10.4, the operator's project
notes.

## Context and Problem Statement

An update on a host can break the agent itself. A fixed agent should then reach all hosts through the hub, without
visiting each device. But whoever takes over the hub must not be able to slip in an agent of their own.

## Decision Drivers

* The authenticity of an agent binary does not depend on the hub.
* A broken agent must not hit all hosts at once.
* An agent that does not come up returns to the old version by itself.

## Considered Options

The sources name no alternative to the way of an earlier agent project.

## Decision Outcome

Chosen option: the way of the earlier agent.

* The operator signs a manifest (version, per platform file, SHA-256, size; context `vordr/agent-manifest/v1`). The
  key lives with the operator's release tool on the operator's own machine, never on the hub; a backup copy is kept in
  a secret store. The tool never generates it anew.
* The hub distributes manifest and binaries (one-time token, 15 minutes) and checks the signature only to know the
  valid version.
* The agent checks the manifest against the key in **its own** `agent.yaml`, then hash and size, lets the new binary
  answer `version` and `check-config`, swaps it in and restarts. If the new version does not confirm within 10 minutes,
  it puts the old one back; if it does not start at all, an `OnFailure` unit does that.
* Rings: canaries first, the others after `canary_clean_hours` (24) without a problem; a failed attempt is retried
  after 6 h.

### Consequences

* Good, because a compromised hub cannot distribute its own agent.
* Bad, because a change to the self-update path only takes effect with the update after it, since the running agent
  does the swap.
* Bad, because it is Linux only. On Windows, replacing the running .exe and Authenticode are missing.
* Bad, because the rollback only runs when an update fails, so it is exercised mainly by tests.

### Confirmation

`TestSelfUpdateAndConfirm`, `TestSelfUpdateRollsBack`, `TestSelfUpdateRefuses`, `TestReleaseNeedsTheOperatorsSignature`,
`TestRolloutRings`, `TestReleaseDownloadNeedsAnOpenToken`; a live check: a canary host updates itself over several
releases in a row.

## More Information

Extended by [ADR-0034](0034-only-newer-releases.md) and [ADR-0035](0035-backup-operator-key.md). Canaries:
[ADR-0033](0033-one-host-as-the-canary.md), superseded by [ADR-0058](0058-site-decision.md).
