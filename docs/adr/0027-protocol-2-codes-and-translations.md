---
status: accepted
date: 2026-10-29
decision-makers: kay ("yes, build H5")
---

# Protocol 2: the hub sends codes, the integration writes the texts

Backfilled on 2026-10-30, decided on 2026-10-29. Sources: `design/ha-integration.md` 6b, the operator's project notes.

Supersedes [ADR-0008](0008-hub-writes-ha-texts.md).

## Context and Problem Statement

The hub sent ready-made German texts. A repair issue therefore mixed languages: frame in the profile language, reason
in the system language, next step in German. On an HA instance with English as system language, English and German
were jumbled.

## Decision Drivers

* Everyone reads HA in their own language.
* Fixed codes as the interface ([ADR-0007](0007-fixed-codes-instead-of-error-texts.md)).

## Considered Options

* texts at the hub, per language
* only codes and numbers from the hub, texts from the integration's translation files

## Decision Outcome

Chosen option: only codes and numbers (protocol 2).

* `problem` and `last_run` without `next_step`; `system` with `packages` instead of ready-made version texts;
  `hub.limits` with the thresholds.
* The integration writes every text from `translations/`. Repair issues per problem key, states and attribute values
  are translated by the frontend (profile language). Names and version texts follow the system language; that is a
  limit of HA.
* Events carry `message` in every language, so an automation can pick the language of its recipient.
* The hub refuses an integration with protocol 1 (`protocol_mismatch`).

### Consequences

* Good, because languages no longer mix, and new languages need no change in the hub.
* Bad, because hub and integration must be rolled out together when the protocol changes.
* Bad, because display names are no longer always German.

### Confirmation

`TestViewsCarryCodesOnly`; `ha/tests/test_texts.py` (every language has every text, every issue contains its next
step, a code has the same name everywhere); `test_protocol_mismatch`.
