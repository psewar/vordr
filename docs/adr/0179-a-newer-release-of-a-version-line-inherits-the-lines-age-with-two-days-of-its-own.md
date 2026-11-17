---
status: accepted
date: 2026-11-17
decision-makers: kay (2026-11-17, decided on the open question of ADR-0159 after its merge); proposed by Claude
---

# A newer release of a version line inherits the line's age, with two days of its own

Refines [ADR-0159](0159-the-minimum-age-of-a-home-assistant-update-counts-from-its-first-sight.md), which counts the
minimum age of a Home Assistant update from the integration's first sight of the version and left open whether a
newer patch release inherits an age. ADR-0159 stays as it is for every version this ADR does not name.

## Context and Problem Statement

Home Assistant's update entities give no release date, so the age of a version is the time since the integration
first saw it offered. A project that publishes a quick series of patch releases (x.y.0, x.y.1, x.y.2, a few days
apart) starts that age again with each of them: the newest is never old enough, although the line x.y has been out
for weeks and its first release has long run here. The risk of a fresh release sits mostly in a new line (new
features, changed behaviour); a patch release of an established line mostly fixes it.

How old is a newer release of a line the integration has already seen?

## Decision Drivers

* The age stays a fact the integration has seen, not read from a web page or a changelog.
* Unknown means young: a version whose line cannot be told waits its own full age.
* A new line (a minor or major step) waits the full minimum age, as before.
* A release that is withdrawn within hours is not installed: the patch release keeps a short age of its own.
* The line must be read from what the update entities show: `v1.4.2` (HACS tags), `1.4.2`, Core `2026.11.1`,
  OS `16.3`, firmware numbers such as `0x01020304`, commits, pre-releases.

## Considered Options

1. Every version waits its own age (as before).
2. A newer release of a line counts its minimum age from the integration's first sight of that line (major.minor), but
   must itself be at least 2 days old.
3. As 2, without an age of its own.
4. A shorter minimum age for every patch release, counted from its own first sight.

## Decision Outcome

Chosen option: **2**.

* **Line:** the major and minor number of a version that is numbers separated by dots, at least two, after one optional
  `v`: `v1.4.2` and `1.4.2` are line `1.4`, `2026.11.1` is `2026.11`, `16.3` is `16.3`, `1.02.3` is `1.2`. Anything
  else has no line: firmware numbers, commits, pre-releases such as `2026.12.0b1`, build suffixes, a single number,
  digits other than ASCII. A version without a line inherits nothing.
* **First sight of a line:** per update entity, each line it reports, installed or offered, with the time the
  integration first saw it, kept in the same Store as the first sights (`lines` in
  `.storage/vordr.ha_updates.<entry_id>`). A line stays while the entity reports a version of it, also once it is
  installed; a line the entity no longer reports, or an entity gone from the registry, drops it; an unavailable or
  unknown entity keeps its lines. Stored entries that are not valid are left out (their age starts again).
* **Inherited age:** a version whose line was first seen before the version itself is old enough once the line is as
  old as the minimum age and the version itself is `PATCH_MIN_AGE_DAYS` (2) days old, like the agent's
  `min_age_days_security` ([ADR-0111](0111-shorter-minimum-age-for-security-fixes.md)); never more than the minimum
  age. The first release of a line, a new line and a version without a line wait their own full age.
* **Report:** the sensor's list `updates` carries `line_first_seen` where a version inherits; `too_new` carries the
  earliest time from the inherited age. The options form names the 2 days in its description, from the constant the
  plan uses.

Option 1 keeps a line that publishes patches every few days from ever becoming old enough. Option 3 installs a patch
release minutes after it appears, before anyone could withdraw it. Option 4 lets a patch release of a brand-new line
through after a short age, where the risk is largest.

### Consequences

* Good, because a patch series of an established line becomes ready after 2 days, and new lines still wait in full.
* Good, because the line is a fact of what the entity reported here; nothing is fetched or parsed but the version.
* Bad, because the line is read from the version's form: a project whose minor number means a break, or whose
  versions are dates with the day in the second place, gets a line that is too wide. The 2-day bound and the major
  rule ([ADR-0160](0160-a-new-major-version-of-a-home-assistant-update-stays-manual.md)) still hold for it.
* Bad, because lines installed when this version of the integration first runs count from then, not from their
  release.

### Confirmation

`ha/tests/test_ha_updates_plan.py`: the line of each version format and the ones without a line; a patch release
ready by its line; the 2-day bound (a minute short, exactly, unseen, never above the minimum age); a line itself too
young; a new line starting fresh; versions without a line waiting their own age; the lines kept, dropped, counted
from installed versions, and left alone for Vördr's own entities. `ha/tests/test_ha_updates.py`: across an unload
and a new setup a patch release becomes ready after 2 days and a new line waits the full age, the Store's `lines`,
stored entries that are not valid left out. `ha/tests/test_texts.py`: the form's description names the number in
every language.
