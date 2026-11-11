---
status: superseded by ADR-0136
date: 2026-10-29
decision-makers: kay
---

# Ship the integration with a deploy tool, not via HACS

Backfilled on 2026-10-30, decided on 2026-10-29. Source: `design/ha-integration.md` section 11, decision 3.

## Context and Problem Statement

The integration `custom_components/vordr` has to get into Home Assistant. The usual way is HACS with a public GitHub
repo.

## Decision Drivers

* The repo is private.

## Considered Options

* HACS via GitHub
* own deploy tool from the private repo

## Decision Outcome

Chosen option: own deploy tool (outside the repository) with checks, a partial backup of the HA
configuration, a checksum per file and a look into the log after the restart. GitHub with HACS only if the repo
becomes public.

### Consequences

* Good, because nothing has to be published.
* Bad, because HA shows no update for the integration; a new version only comes through the tool and takes effect
  after an HA restart.

### Confirmation

After copying, the tool compares checksums and reads the HA log.

## More Information

Whether the repo becomes public is open (`design/architecture.md` 16.7).
