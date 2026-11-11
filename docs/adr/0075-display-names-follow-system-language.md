---
status: accepted
date: 2026-10-30
decision-makers: kay
---

# Display names follow Home Assistant's system language

## Context and Problem Statement

Under MQTT discovery the hub set German display names. Since the own integration
([ADR-0023](0023-own-ha-integration.md)) entity and device names come from the integration's translations, and HA
resolves names only in its system language, not per person. On an instance with system language English the names
therefore became English. Whether they should be fixed to German had stayed open; the README and the design
still said "display names in German".

## Decision Drivers

* Names should come from the translation files like every other text
  ([ADR-0027](0027-protocol-2-codes-and-translations.md)).
* HA offers names in one language per installation only.

## Considered Options

* fixed German names, whatever the system language
* names in HA's system language (German or English)

## Decision Outcome

Chosen option: names follow HA's system language (the operator, 2026-10-30: "following the system language is
right"). The README and the design say so now.

### Consequences

* Good, because names, version texts and translated states come from one place and match the instance.
* Bad, because on an instance with English as system language the names are English, also for German-speaking users;
  states, repair issues and attributes still follow each person's profile language.

### Confirmation

`test_system_language_words_the_backend` and `ha/tests/test_texts.py` (every language has every text).
