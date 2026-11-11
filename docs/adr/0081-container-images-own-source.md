---
status: accepted
date: 2026-10-30
decision-makers: kay ("2. Go")
---

# Container images: Vördr's own source, updates2mqtt rejected

## Context and Problem Statement

Containers carry their own packages that apt and winget do not see. Design 16.6 left open whether Vördr gets its own
source for container images or uses updates2mqtt, and ADR-0065 moved container images to a later step.

## Decision Drivers

* The same safeguards and the same view in HA as for every other source.
* No MQTT again ([ADR-0026](0026-remove-mqtt.md)).
* Updating a running service is risky; reporting is not.

## Considered Options

* updates2mqtt
* Vördr's own source (design 11.5)

## Decision Outcome

Chosen option: Vördr's own source (the operator, 2026-10-30). First it only reports: images with a newer version and
known vulnerabilities in the packages of an image (OSV, like [ADR-0064](0064-osv-data-local-at-the-hub.md)). Updating
by button comes later and gets its own ADR. updates2mqtt is rejected: it would bring MQTT back, covers Docker only, and
has no backup check, health checks or canaries.

### Consequences

* Good, because containers appear in HA like every other source, with fixed codes.
* Bad, because OSV data for Debian and Alpine images costs several hundred MB more at the hub.
* Bad, because it has to be built; it belongs to the package work that built 0064–0071.

### Confirmation

Not applicable until the source is built.
