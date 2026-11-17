---
status: accepted
date: 2026-11-17
decision-makers: kay (2026-11-17, asked for Home Assistant's own updates to follow Vördr's rules, first as a plan that installs nothing; accepted with the merge); proposed by Claude
---

# A new major version of a Home Assistant update stays manual

Takes the test of [ADR-0052](0052-major-version-jumps.md) (winget: major jumps are marked) to Home Assistant's own
updates, within [ADR-0158](0158-home-assistants-own-updates-follow-vordrs-rules-in-the-integration.md), and makes it
a hold instead of a mark. A decision of its own beside the minimum age
([ADR-0159](0159-the-minimum-age-of-a-home-assistant-update-counts-from-its-first-sight.md)): that one answers when a
version is old enough, this one which steps need a person, and either may change without the other.

## Context and Problem Statement

A new major version of an app, a HACS integration or a device's firmware is where breaking changes come: a changed
configuration, a removed feature, a migration that cannot be undone. Age does not make such a step safe; reading its
notes does. ADR-0052 only marks major jumps from winget and leaves blocking to a pin.

What do Vördr's rules for Home Assistant's updates do with a major version?

## Decision Drivers

* A step with breaking changes gets a person's look.
* No false alarms: Core, Supervisor and ESPHome number by calendar ("2026.11.0").
* The same test as the agent's.

## Considered Options

1. A major version stays manual: its own code, never ready; the person installs it with Home Assistant's own button.
2. Mark it only, as ADR-0052, and let the rules install it.
3. A setting per kind.

## Decision Outcome

Chosen option: **1**.

* **Test:** the installed and the offered version begin with different numbers, both below 1000, as
  `internal/source` `MajorJump`. One leading `v` is ignored (tags such as `v2.1.0`). A version without a leading number
  is no jump (firmware shown as `0x…`); calendar versions are none either.
* **Code:** `major`, without an earliest time. It comes after `not_allowed`: an allowed entity's major version is
  `major` whatever its age or the window.
* Not switchable in this stage.

Option 2 lets an unattended run take the step that most needs a look. Option 3 adds a setting before anyone has
asked for an exception; it can come as a decision of its own.

### Consequences

* Good, because big steps wait for a person, while patches and minor versions follow the rules.
* Bad, because every major version of Home Assistant OS and of HACS repositories that publish majors often needs a
  press of the button.
* Bad, because a breaking change in a minor version passes; the test reads numbers, not notes.
* Neutral: calendar versions of Core, Supervisor and ESPHome never count as major.

### Confirmation

`ha/tests/test_ha_updates_plan.py`: `test_major_jump` (14 cases: numbers, `v` tags, calendar versions, hex firmware,
missing versions), `test_a_major_version_stays_manual`, and the order of the codes. A mutation probe ("major
ignored", "calendar versions as jumps", "`v` not stripped") makes these tests fail.
