---
status: accepted
date: 2026-11-17
decision-makers: proposed by Claude, for the maintainer to accept; kay (2026-11-17, merged)
---

# Notifications are shipped as blueprints that the user imports

## Context and Problem Statement

The integration fires `vordr_problem` and `vordr_run_finished` and keeps repair hints, but turning them into a message
on a phone is left to every operator: each writes the same four automations by hand (a problem, a failed run, a
reboot that needs a person, a hub that is away) and gets the details wrong in the same ways (a notification for a
problem that ends a minute later, a reboot button without a check that it is still wanted, texts in one language).
`design/architecture.md` 8.5 listed a blueprint as not built.

Home Assistant offers automation blueprints for exactly this: a file with inputs, imported from a URL or copied into
`<config>/blueprints/automation/<folder>/`
([blueprint schema](https://www.home-assistant.io/docs/blueprint/schema/),
[using blueprints and importing from a URL](https://www.home-assistant.io/docs/automation/using_blueprints/)). A
custom integration cannot install blueprints by itself, and HACS ships only the integration folder.

## Decision Drivers

* A blueprint must never install or reboot anything without a person's action; the hub and the host's policy keep the
  last word (ADR-0039, ADR-0087).
* Nothing site-specific: no entity ids, no host names (ADR-0099); hosts and targets are inputs.
* German and English users both get their own language, although a blueprint's own texts cannot be translated by Home
  Assistant.
* The blueprints must not drift away from the integration's events, fields and entities.

## Considered Options

1. Blueprints in the repository (`blueprints/automation/vordr/`), imported by the user from their URL, tested against
   the integration.
2. Automations created by the integration itself (a config-flow option).
3. A notify platform or notifications sent by the integration.
4. Documentation with YAML snippets only.

## Decision Outcome

Chosen option: **1**.

* **Where:** `blueprints/automation/vordr/*.yaml` at the root of the repository, the layout Home Assistant's import
  from a repository file expects. They are versioned with the repository, not with the integration: a blueprint does
  not change the integration's version. The README of the integration says how to import them.
* **Four blueprints:** a host problem (`vordr_problem`, optionally after a wait and with a note when it ends), a failed
  run (`vordr_run_finished` with a result that is not `ok`), a reboot offer, and a hub that is unreachable (the hub's
  online entity off for a number of minutes).
* **Inputs are selectors:** the notify service as text (`notify.<name>`), the hosts as a device selector filtered to
  this integration, the language, the delays; the hub's online entity is a selector, never a default. No entity id is
  written into a blueprint.
* **Language:** the events carry the problem text in every shipped language (`message.<lang>`). A blueprint takes the
  language as an input and falls back to English; its few own texts (title lines, the button) are kept in the
  blueprint for `en` and `de` and fall back the same way.
* **Mode:** `parallel` for the problem and the reboot offer, whose runs wait per host and must not cancel one another
  (a restart would drop the wait of another host); `queued` for the instant notifications.
* **The reboot offer is a notification with an action, and the tap is the action.** The notification carries the action
  "Reboot now" only where the host has a reboot button (the hub offers it: capability and local policy); the button and
  the host's reboot sensor are found through the host's device. A tap presses the same button as a person in the user
  interface, and only if the host still needs the reboot and still offers it at the time of the tap; each offer has its
  own action id and expires after a number of hours. The hub checks the command again, as for any press (role, window,
  idle check).
* **The problem wait** drops a problem that ends or changes inside the wait (a change starts its own wait). Without
  a wait it can tell when the problem ends; with a wait it does not, since a blueprint keeps no memory of what it
  announced.
* **Not decided here:** a device-based notify target (the Companion app's device action), a blueprint for other
  integrations' updates that are stuck, and translations of the blueprints beyond `en` and `de`.

## Consequences

* Good, because an operator gets the four notifications that matter in a few clicks, with the reboot behind a tap
  and not behind a rule.
* Good, because the tests keep the blueprints in step with the integration: a wrong event name, field, reason or
  result code fails the build.
* Neutral: the notify service is typed, not picked; a typo shows when the blueprint first fires.
* Bad, because a new shipped language reaches the blueprints' own few texts only when they are extended; the problem and
  run texts come with the language automatically.
* Bad, because an import copies the file: a later version of a blueprint reaches a user only when they import it again.

## Confirmation

`ha/tests/test_blueprints.py`: every blueprint loads with Home Assistant's own blueprint schema; its inputs, events,
event fields, reasons and result codes exist in the integration (a blueprint with a wrong event name, field or code
fails, and a blueprint with a wrong event name stays silent live); the problem blueprint is run against real events of
the fake hub in two languages, with a host filter, an ignored reason, a wait that ends or changes, and the end note;
the failed-run blueprint notifies only a failure of a chosen kind; the reboot offer sends no action for a host without
the button, sends nothing and presses nothing before a tap, presses the host's reboot button after a tap, and does
nothing after the offer has expired or when the host no longer needs the reboot; the hub blueprint waits, notifies, and
tells when the connection is back.
