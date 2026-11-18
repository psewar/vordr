---
status: accepted
date: 2026-11-18
decision-makers: kay (2026-11-18, chose to mark cancel as not built instead of building it); proposed by Claude
---

# The cancel verb is not built until a run needs stopping

Refines [ADR-0005](0005-closed-verbs-without-shell.md) (closed verbs without a shell), which lists `cancel` among the
verbs, and [ADR-0029](0029-runs-survive-disconnects.md) (runs survive disconnects), which says when `cancel` may still
stop a run. Both stay as decided; this ADR records that `cancel` exists in the protocol only.

## Context and Problem Statement

`cancel` with a `target` job id is defined in the protocol (`internal/protocol`: the verb, the check that it names a
target, and the final code `aborted`), and the design documents describe it: it would stop a run until the package
installation begins. Neither side carries it out. The hub has no command and no button that sends it, and an agent
answers a signed `cancel` job with `rejected_unsupported`, as it answers any verb it does not carry out. The documents
did not say so, so a reader had to find out from the code.

Build `cancel`, or say that it is not built?

## Decision Drivers

* The documents say what Vördr does, not what it once meant to do.
* Every verb that is built is a path that can change a host; it is built when a run needs it.
* The protocol stays as it is, so that a later `cancel` needs no protocol change.

## Considered Options

1. Mark `cancel` as not built in the documents; keep it in the protocol.
2. Build it: a hub command and a Home Assistant button, the agent stopping a run before its installation begins, the
   state `aborted`.
3. Remove it from the protocol.

## Decision Outcome

Chosen option: **1**. `design/architecture.md` (the table of verbs and the table of run results) and `README.md` say
that `cancel` and `aborted` are defined but not built, and link this ADR. The protocol keeps the verb, its check and
the code. An agent keeps answering it with `rejected_unsupported`; the hub sends none.

Option 2 adds a path that changes what a host does for a case nobody has met yet: a run waits for its window or its
backup without a cancel, and an install, once it starts, runs to the end anyway (ADR-0029). Option 3 would need a
protocol change later, for no gain now.

### Consequences

* Good, because the documents match the code.
* Good, because nothing changes on the hub, the agent or the protocol.
* Bad, because a queued or waiting run cannot be withdrawn; it ends as it would have, or with the agent's refusal.
* Neutral: building `cancel` later is a new ADR that supersedes this one.

### Confirmation

`tools/check_docs.py` checks both languages of the design document and the links; the agent's answer to a verb it
does not carry out is tested in `internal/agent` (`rejected_unsupported`).
