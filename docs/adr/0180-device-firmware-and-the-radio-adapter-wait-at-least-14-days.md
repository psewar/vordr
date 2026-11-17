---
status: accepted
date: 2026-11-17
decision-makers: kay (2026-11-17, decided on the open question of ADR-0159 after its merge, a fixed longer minimum age for device firmware and radio; the form of it chosen by Claude); proposed by Claude
---

# Device firmware and the radio adapter wait at least 14 days

Refines [ADR-0159](0159-the-minimum-age-of-a-home-assistant-update-counts-from-its-first-sight.md), which has one
minimum age for all kinds and left a longer one for firmware open. The kinds are those of
[ADR-0158](0158-home-assistants-own-updates-follow-vordrs-rules-in-the-integration.md).

## Context and Problem Statement

Device firmware (Zigbee, Matter, Z-Wave and other devices, kind `firmware`) and the firmware of the radio adapter that
carries the Zigbee or Thread network (kind `radio`) are the hardest updates to undo: no backup of Home Assistant reaches
into a device, a vendor rarely offers a way back, and a broken radio adapter takes the whole network with it. They
waited the same minimum age as an app or a HACS card.

How long do they wait, and how does the options form show it?

## Decision Drivers

* What is hardest to undo waits longest.
* The general minimum age of the options stays, with its default, range and form field.
* The options form stays simple and shows every rule that holds.
* Existing options keep working without edits.

## Considered Options

1. One minimum age for all kinds (as before).
2. A number field per kind in the options form, 14 days by default for `firmware` and `radio`.
3. One more number field for device firmware and radio together, 14 days by default.
4. A fixed rule: `firmware` and `radio` wait at least 14 days, or the general minimum age if it is longer.

## Decision Outcome

Chosen option: **4**, because it is the simpler one that the form can still show clearly.

* **Rule:** the minimum age of the kinds `firmware` and `radio` is `FIRMWARE_MIN_AGE_DAYS` (14) days, or the minimum age
  of the options if that is longer (`Policy.min_age_for`). Every other kind keeps the minimum age of the options. It
  applies wherever the age applies, also to the age a newer release inherits from its line
  ([ADR-0179](0179-a-newer-release-of-a-version-line-inherits-the-lines-age-with-two-days-of-its-own.md)).
* **Shown:** the options form names the 14 days in its description, from the same constant; the sensor
  `sensor.vordr_ha_updates_ready` has the attribute `min_age_days_firmware` next to `min_age_days`.
* **Not an option:** no field, no stored key; options saved before stay valid and mean what they meant, plus this rule.

Option 1 keeps the most dangerous updates at the age of the least dangerous. Option 2 would be nine fields for a rule
that two kinds need, and a field below the general age would undo the reason for it. Option 3 raises the question
why not for every kind, needs its own check and text, and a value below the general age would again undo the reason.

### Consequences

* Good, because device firmware and the radio adapter wait at least two weeks, the longest of all kinds.
* Good, because the form keeps one number and still says the rule, and stored options need no migration.
* Bad, because 14 days is fixed: a site that wants it shorter for its devices cannot have it; one that wants it longer
  raises the general minimum age, which then holds for all kinds.

### Confirmation

`ha/tests/test_ha_updates_plan.py`: `firmware` and `radio` are too new at 13 days and ready at 14, an app at 7; a
longer option holds for them too; a firmware line inherits only after 14 days. `ha/tests/test_ha_updates.py`: a
firmware entity's earliest time is 14 days after its first sight and it becomes ready then; `min_age_days_firmware`
is 14 with an option of 7 or 3. `ha/tests/test_ha_updates_install.py`: firmware is still too new where other kinds
install. `ha/tests/test_texts.py`: the form's description names the number in every language, and the attribute
has its name.
