---
status: accepted
date: 2026-10-28
decision-makers: kay (principle "report state instead of deriving it from errors")
---

# Fixed codes instead of error texts

Backfilled on 2026-10-30, decided on 2026-10-28. Sources: the operator's project notes (2026-10-28),
`design/architecture.md` sections 2 and 7, the project principle "errors are not a control signal" (`AGENTS.md`).

## Context and Problem Statement

A run can fail in many ways. HA, automations and people must tell the cases apart. Error texts from apt, dpkg or
winget are translated, change between versions and are no interface.

## Decision Drivers

* No guessing from error texts.
* Every case that is handled differently needs its own marker.

## Considered Options

* pass on and parse error texts
* fixed result codes; the error text only as an excerpt for people

## Decision Outcome

Chosen option: fixed codes. Every result is a code from a fixed list (`design/architecture.md` 7.3), with a flag
whether it is a problem. The agent only evaluates exit codes and structured output (dry run, `APT::Status-Fd`, winget
status values) and, after a failure, asks for the state directly. The error text only travels as `log_excerpt`. If a
case must be told apart, it gets its own code.

Technical names (verbs, codes, states, config keys, entity IDs) are English.

### Consequences

* Good, because HA and automations depend on stable codes.
* Good, because a new case shows up as a new code instead of hiding in a text.
* Bad, because hub and integration must know a new code before an agent sends it
  ([ADR-0049](0049-site-decision.md)).

### Confirmation

`TestCodes` in `internal/protocol`; `ha/tests/test_texts.py` (a code has the same name everywhere).

## More Information

Until protocol 2 the hub sent ready-made texts next to the codes ([ADR-0008](0008-hub-writes-ha-texts.md),
superseded by [ADR-0027](0027-protocol-2-codes-and-translations.md)).
