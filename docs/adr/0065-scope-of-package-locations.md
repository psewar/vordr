---
status: accepted
date: 2026-10-30
decision-makers: kay (scope), Claude (splitting into steps)
---

# Scope of package locations: global, Node-RED, projects and venvs; containers later

## Context and Problem Statement

Which packages does Vördr check besides those of apt and winget, and in which order is it built?

## Decision Drivers

* Supply-chain attacks come through language packages that apt and winget do not see.
* No false findings: what a distribution package manager manages has backported fixes.
* A PR should stay reviewable.

## Considered Options

1. Only globally installed packages.
2. Global, Node-RED, project folders and venvs per host from `agent.yaml`, plus container images (the operator,
   2026-10-30).
3. Like 2, but also `/usr/lib/python3/dist-packages`.

## Decision Outcome

Chosen option: **option 2** (the operator). The agent detects known locations itself (npm global, pip outside the
distribution, `pip --user`, pipx, Node-RED, Linux and Windows); `packages.locations` names further ones.
`/usr/lib/python3/dist-packages` is **not** checked: apt manages it and already reports it, and OSV's PyPI ranges do
not know Debian's backported fixes. **Container images** follow as a separate step (11.5); Claude proposed the split,
because OSV data for Debian/Alpine and updating Compose services are a topic of their own each.

### Consequences

* Good, because Node-RED nodes, global CLIs and service venvs are covered without counting apt packages twice.
* Bad, because containers have no package check for now.
* Bad, because project folders are only covered when `agent.yaml` names them.

### Confirmation

Tests `TestDetectAndIDs`, `TestConfiguredGlob`; a dry run on Linux and Windows hosts.

## More Information

`design/architecture.md` 11.2, 11.5.

Merged in German as ADR-0101; renumbered and translated later.
