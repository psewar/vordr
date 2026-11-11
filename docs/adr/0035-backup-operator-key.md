---
status: accepted
date: 2026-10-29
decision-makers: kay (created the key)
---

# Backup operator key, secret part only with the operator

Backfilled on 2026-10-30, decided on 2026-10-29. Sources: `design/architecture.md` 10.4 ("Ersatzschlüssel"), the
operator's project notes.

## Context and Problem Statement

All agents trust exactly one operator key ([ADR-0032](0032-self-update-signed-by-operator.md)). If it is lost or
compromised, no new release reaches a host without visiting every device.

## Decision Drivers

* A way out without visiting every device.
* The backup must not live where the main key lives.

## Considered Options

The sources name no alternative.

## Decision Outcome

A second public key `agent_update.backup_public_key` sits in every `agent.yaml` and in `hub.yaml`. The secret part is
kept apart from the main key and its backup copy; the operator created it and keeps it. Releases signed with the
backup key reach every host.

### Consequences

* Good, because losing the main key does not mean visiting every device.
* Bad, because agents only understand the key from 0.7.3; older ones refuse the unknown line. It can therefore go into
  an `agent.yaml` only once that host runs 0.7.3 or later.

### Confirmation

`TestSelfUpdateTakesTheBackupKey`, `TestReleaseBackupKeyAndNoDowngrade`, `TestValidHubAndAgentUpdate`.
