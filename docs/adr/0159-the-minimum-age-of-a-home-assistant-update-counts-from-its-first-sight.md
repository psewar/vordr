---
status: accepted
date: 2026-11-17
decision-makers: kay (2026-11-17, asked for Home Assistant's own updates to follow Vördr's rules, first as a plan that installs nothing; accepted with the merge); proposed by Claude
---

# The minimum age of a Home Assistant update counts from its first sight

Carries the minimum age of [ADR-0068](0068-minimum-age-for-target-versions.md) over to Home Assistant's own updates,
within [ADR-0158](0158-home-assistants-own-updates-follow-vordrs-rules-in-the-integration.md).

## Context and Problem Statement

A fresh release carries the most risk: a regression nobody has hit yet, or, for repositories that HACS installs, a
release that turns out to be malicious. ADR-0068 lets pip and npm updates take only versions public for some days,
counted from the registry's release date. Home Assistant's update entities give no release date: only the installed
and the offered version, a release URL and a summary. HACS offers a new tag as soon as it exists.

How is the age of an offered version counted?

## Decision Drivers

* The age is a fact, not read from text or a web page.
* It survives a restart of Home Assistant, also while integrations are still loading.
* Unknown means young, never old.
* The same default and range as ADR-0068, and no silent switch-off.

## Considered Options

1. From the integration's first sight of a version, per update entity, kept in a Store.
2. From a release date fetched through the release URL (for example a repository's release API).
3. From when the entity's state last changed.
4. No minimum age.

## Decision Outcome

Chosen option: **1**.

* **First sight:** per update entity, the offered version and when the integration first saw it, kept in
  `.storage/vordr.ha_updates.<entry_id>`. A new offered version replaces it and starts its own age; an installed
  version, or an entity removed from the entity registry, drops it; an entity that is unavailable or unknown keeps it.
* **Unknown counts as now:** a version without a first sight starts its age when it is seen. When the feature starts,
  every pending update starts its age then.
* **Minimum age:** in the options, default 7 days, 1 to 90, whole days, one for all kinds; it cannot be switched off.
* **Code:** `too_new` with the earliest time it would be ready: the first time in the window from first sight plus
  the minimum age on.
* **Writing:** 10 seconds after a change and when the entry unloads. A first sight lost to a crash in between only
  starts the age again, later. The Store goes with the entry.

Option 2 needs the network, a different source per integration, rate limits and parsing; most firmware has no such
source. Option 3 changes with every attribute and starts again with every restart. Option 4 leaves the day-one risk
that ADR-0068 removed for packages.

### Consequences

* Good, because it works for every integration, without network or parsing.
* Good, because it errs on the late side.
* Bad, because the age is the age in this Home Assistant, not the release's: a version first seen now waits the full
  minimum age, however old it is, and so does everything pending when the feature starts.
* Bad, because a quick series of patch releases (x.y.0, x.y.1, x.y.2) starts the age again each time and may never
  become old enough. Whether a newer patch of the same major and minor version inherits the age is open; not built.
* Bad, because one minimum age holds for all kinds; a longer one for firmware is open.

### Confirmation

`ha/tests/test_ha_updates_plan.py`: the age boundary (exactly the minimum age is old enough, a minute less is not),
the earliest time in and outside the window, a first sight of another version or none counting as now, the first
sights kept, replaced and dropped. `ha/tests/test_ha_updates.py`: the Store across an unload and a new setup (the
version keeps its first sight and becomes ready), a newer version starting its own age, an unavailable entity keeping
it, a removed one losing it, a deleted entry removing the Store, stored entries that are none left out.
