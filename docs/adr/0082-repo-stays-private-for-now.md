---
status: accepted
date: 2026-10-30
decision-makers: kay
---

# The repo stays private for now; publishing needs testing and a review first

## Context and Problem Statement

Design 16.7 left open whether the repo becomes public and under which license. The operator is considering publishing
it, but only after more testing and only once it is certain that nothing in it tells others how the infrastructure
could be attacked.

Many files mention internal host names, most of them tests that use actual host names as examples, design examples and
ADRs; the own domain appears in an example and a link; container and VM numbers appear as well. The operations tools
live outside the repo.

## Decision Drivers

* No information that helps an attack on the infrastructure leaves the site.
* Others may profit from Vördr; HACS needs a public repo ([ADR-0025](0025-distribution-via-deploy-tool.md)).
* Accepted ADRs are not rewritten.

## Considered Options

* publish now
* stay private for good
* stay private now, publish after more testing and a review

## Decision Outcome

Chosen option: private now, publish later only after more testing and a review (the operator, 2026-10-30). The review
checks that nothing reveals how the infrastructure could be attacked: host names, domains, addresses, container
numbers, which host carries what, network layout, weaknesses and their state. The license is decided when the repo is
published.

### Consequences

* Good, because nothing leaves the site now.
* Bad, because the ADRs name hosts where a decision depends on them. Accepted ADRs are not rewritten, so a public repo
  would need a cleaned copy rather than this history; every new mention adds to that work.

### Confirmation

Not applicable until publication.
