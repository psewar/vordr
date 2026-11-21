---
status: accepted
date: 2026-11-21
decision-makers: kay (2026-11-21, "Ja, PR bauen" on the proposal to give the firewall its own repair text); proposed by Claude
---

# The hub says why updates are held back

Builds on [ADR-0127](0127-count-only-what-the-upgrade-mode-installs.md) (what apt's install mode upgrade leaves) and
[ADR-0204](0204-the-firewall-reports-what-its-own-updater-offers.md) (a release in another branch), which both count
an update as kept back, and on [ADR-0197](0197-the-stale-repair-names-only-a-way-that-exists.md) (the stale repair
names only a way that exists). Changes how the integration chooses the text of ADR-0204's `stale_kept_back`.

## Context and Problem Statement

An update is kept back for one of two reasons: the host's `install.mode upgrade` leaves it, since it needs packages
added or removed (ADR-0127), or a device offers it only in another branch of its updates, which the operator switches
in the device's own settings (ADR-0204). The hub counts both as `kept_back` and sends only that count. Where every
pending update is kept back and they stay pending for longer than the stale limit, the integration cannot tell the two
apart, and its repair `stale_kept_back` names both steps: "switch the device's update branch, or set install.mode
full-upgrade". On a firewall the second half names a setting the device does not have; on a Debian host the first half
names a branch that does not exist. Neither repair can be fixed through Vördr, so Home Assistant shows only "Ignore",
and the text is all a reader has to decide by.

How does the repair name the one step that brings the updates?

## Decision Drivers

* State as a fact, as a fixed code ([ADR-0007](0007-fixed-codes-instead-of-error-texts.md)): the integration does not
  guess the reason from the host's model, its source name or the package's name.
* The source that holds an update back knows why; nobody after it should have to derive it.
* An older agent or hub keeps working and keeps the text that names both steps.
* The texts stay in the integration, in every language; the hub sends codes.

## Considered Options

* A reason per source report (`kept_back_why`), which the hub passes on in the host's view.
* A reason per pending package.
* The integration derives the reason from what it already has: the host's model or the name of the source.
* Only rewrite the common text.

## Decision Outcome

Chosen: a reason per source report, passed on by the hub.

* A source that counts kept-back updates names why, as a code: `install_mode` (apt with `install.mode upgrade`,
  ADR-0127) or `other_branch` (a proxy instance whose driver offers a release only in another branch, ADR-0204). A
  source holds its updates back for one reason. The field is `kept_back_why` in the report and empty where nothing is
  kept back.
* The hub bounds it like every key of a report; one that is not a key is dropped, and the count stays.
* The host's view carries `kept_back_why` next to `kept_back`: the reason every source that holds updates back names.
  Where one of them names none (an agent before this decision) or two name different ones, the view names none.
* The integration chooses the stale text by it: `stale_other_branch` names only the device's branch and says that
  ignoring the repair is right while the device stays on its branch; `stale_install_mode` names only `install.mode
  full-upgrade` or an install by hand. Without a reason, or with one it does not know, it keeps `stale_kept_back`,
  which names both.
* `vordr-hub show` names the reason after the count.

Rejected:
* **A reason per package:** finer than any source needs today, and a field on every pending package of every report
  for a case that only the count decides.
* **Derive it in the integration:** the host's model and the source's name are text for people and names, not facts
  about holding back; a new appliance or a renamed source would get the wrong step without anyone seeing it.
* **Only rewrite the common text:** it still has to name both steps, so every reader keeps reading one that does not
  apply.

### Consequences

* Good, because the repair on a firewall names only its branch, and on a Debian host only its install mode.
* Good, because the reason is stated where it is known, once, and every later reader takes it as it is.
* Good, because the hub reads reports leniently and the integration falls back to the common text: hub, integration
  and agent can be rolled out in any order. The specific text appears once all three have it.
* Bad, because one more field in the report, the view and the bounds.
* Bad, because a host whose sources hold back for different reasons gets the common text. No supported host has two
  such sources today.

### Confirmation

* `TestKeptBackWhy` in `internal/hub`: the view names the one reason its sources share, none where one names none or
  two differ, and none where nothing is held back.
* `TestEveryFieldOfTheMessagesIsBounded` and `TestTheHubBoundsEveryFieldOfAReport` in `internal/hub`: the reason is
  bounded like a key and dropped alone.
* `TestCheckMarksKeptBack` in `internal/source/apt` and `TestTargetCheckerKeptBackOffer` in `internal/agent`: apt
  names `install_mode`, the proxy instance `other_branch`, and neither names a reason where nothing is kept back.
* `ha/tests/test_texts.py` (`test_stale_key_names_the_one_step_the_hub_says`) and `ha/tests/test_entities.py`: the
  repair and the problem sensor's `next_step` follow the reason, and without a known one stay `stale_kept_back`.
