---
status: accepted
date: 2026-11-11
decision-makers: kay (2026-11-11, chose MIT as for the operator's other repositories); proposed by Claude
---

# MIT license

Settles the license left open by [ADR-0082](0082-repo-stays-private-for-now.md).

## Context and Problem Statement

Vördr is to be published. Without a license nobody else may use, change or pass on the code. Home Assistant
integrations are mostly published under MIT or Apache 2.0; the operator's other public repositories use MIT.

## Decision Drivers

* Others may use and adapt Vördr for their own sites without legal hurdles.
* The same terms as the operator's other projects.
* Simple: one short license file, no notice files to keep.

## Considered Options

* MIT
* Apache 2.0
* a copyleft license (GPL 3.0)

## Decision Outcome

Chosen: MIT (the operator, 2026-11-11). `LICENSE` in the root holds the MIT text with the copyright line of the
operator's account; the Go module, the Home Assistant integration and the tools fall under it.

Rejected:

* **Apache 2.0,** because its patent grant brings nothing here, and its notice rules add work.
* **GPL 3.0,** because it would keep others from embedding parts in projects under other licenses, which is not the
  aim.

### Consequences

* Good, because anyone can use Vördr, also in their own projects.
* Bad, because changes made by others need not come back.

### Confirmation

`LICENSE` exists in the root and is the MIT text.
