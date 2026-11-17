---
status: accepted
date: 2026-11-17
decision-makers: kay (2026-11-17, decided after the merge of ADR-0166); proposed by Claude
---

# With AI explanations off, nothing is sent and no event is fired

Refines [ADR-0166](0166-optional-ai-explanations-of-failed-runs-go-through-home-assistants-ai-task-and-only-advise.md):
everything it decides for AI explanations that are switched on stays; what changes is that, while they are off, the
event `vordr_ai_explanation` is not fired either, and its code `disabled` is gone.

## Context and Problem Statement

ADR-0166 fires `vordr_ai_explanation` for every failed run, also while the feature is off, with the code `disabled`,
so that an automation never waits for an answer that does not come. But an automation that waits for that event
exists only where the feature is on. Off, which is the default, every failed run fired an event about a feature
nobody uses, into the event log and to every listener.

What happens while AI explanations are off?

## Decision Drivers

* Off by default means off: no call, no event, no attribute; a failed run is as it was before the feature.
* An automation that waits for an answer while the feature is on still gets an event for every failed run, with the
  code why there is no answer.
* Stored options that are not valid are not the same as off: someone chose an entity.

## Considered Options

1. Fire the event while off, with `disabled` (as before).
2. While off, nothing is fired and nothing is called; `disabled` is removed. `not_configured` stays for stored options
   that are not valid.
3. As 2, and no event for `not_configured` either.

## Decision Outcome

Chosen option: **2**.

* **Off** (no item, or no entity chosen): no call, no event, no attribute on the last-run sensor. It is the state
  `off` of the options (`entity_from_options`), not a code of the event.
* **Runs while off** are taken all the same: switched on later, it explains the runs that end from then on, not one
  that ended while it was off.
* **On:** as ADR-0166, an event for every failed run of the set, with `ok` or the code why not.
* **`not_configured`:** stored options that the form would refuse (an entity without the confirmation, edited by hand)
  send nothing, fire the event with `not_configured`, show it on the sensor and are logged once, as before.
* **Codes:** `ok`, `unverified`, `invalid_answer`, `no_answer`, `no_excerpt`, `unsupported`, `entity_missing`,
  `not_configured`.

Option 1 fires an event for a feature that is off. Option 3 would hide that options someone set are not valid: an
automation waiting for an answer would wait for nothing.

### Consequences

* Good, because with the feature off nothing of it shows, as before it existed.
* Bad, because an automation that listened for `disabled` gets nothing now; it has to be written for the feature
  switched on.

### Confirmation

`ha/tests/test_ai_explain.py`: with no item, an empty item or no entity, a failed run fires the run event and no AI
event, makes no call and sets no attribute; switched on, the same run is not explained, the next one is
(`test_off_by_default_nothing_is_sent_and_nothing_fired`); options that are not valid fire `not_configured`; off is
no code of the event. `ha/tests/test_texts.py`: every code has its text in every language, and `disabled` has none.
