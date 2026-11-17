---
status: accepted
date: 2026-11-17
decision-makers: kay (2026-11-17, asked for Home Assistant's own updates to follow Vördr's rules, first as a plan that installs nothing; accepted with the merge); proposed by Claude
---

# Home Assistant's own updates follow Vördr's rules in the integration, closed by default

Builds on [ADR-0023](0023-own-ha-integration.md) (Home Assistant is a client of the hub; neither hub nor agents talk
to Home Assistant) and keeps [ADR-0013](0013-scope-limits.md) (no agent on Home Assistant OS or on devices with vendor
firmware). The age and major-version rules are [ADR-0159](0159-the-minimum-age-of-a-home-assistant-update-counts-from-its-first-sight.md)
and [ADR-0160](0160-a-new-major-version-of-a-home-assistant-update-stays-manual.md).

## Context and Problem Statement

Home Assistant offers update entities for itself and for much of a smart home: Core, OS, Supervisor, apps (add-ons),
HACS, ESPHome, Zigbee, Matter and Z-Wave firmware, and more. Many of them can install from Home Assistant. Vördr does
not reach them: those systems get no agent, and the hub never speaks to Home Assistant. What Home Assistant itself
offers is a button per entity, "update all" without a backup, an app's own auto-update, or an automation that installs
everything in a window. None of these has a minimum age, a hold on major versions or a report per update, and
Vördr's own update entities have to be kept out by their names ([ADR-0028](0028-site-decision.md)).
`design/architecture.md` 8.5 leaves this open.

Where do Vördr's rules for these updates live, who opens them, and what is the starting point?

## Decision Drivers

* Whoever takes over the hub must not be able to make Home Assistant install anything.
* Closed by default, like the agent's local policy: nothing is allowed until a person allows it, where it acts.
* What a person or the entity decided in Home Assistant stays decided: a skipped version, an entity that installs its
  updates itself (one installer per device, as in [ADR-0092](0092-mesh-keeps-its-vendor-self-update.md)).
* Kinds come from facts in the registries, not from entity ids.
* The rules are testable without Home Assistant.
* A first stage shows what it would do before anything does it.

## Considered Options

1. The integration holds the rules as its options (options flow, for Home Assistant's administrators); it computes the
   plan from Home Assistant's own states and registries; first as a dry run.
2. The hub holds the rules and asks Home Assistant to install, through a new message on `/v1/ha`.
3. An automation or blueprint with the rules in templates.
4. Only Home Assistant's own means: the auto-update of apps and devices, the install button.

## Decision Outcome

Chosen option: **1**.

* **Where:** in the integration. Its options flow is a menu; its first item, "Home Assistant updates", holds the rules
  under the key `ha_updates` of the entry's options. Only Home Assistant's administrators reach options. No field or
  message of the hub opens or widens them, the plan does not read the hub's state, and it keeps running without a
  connection to the hub.
* **Closed by default:** without a chosen kind or entity, every update is `not_allowed`. Once anything is allowed, the
  window needs a day; start equal to end is refused, as for the agent's `install.window`. Options stored by hand or by
  another version that fail the same check count as closed, and the log says so.
* **What may be allowed:** kinds, told by the registries: `core`, `os` and `supervisor` by their device of the
  Supervisor integration (`hassio`), any other of its devices is an `app`; `hacs` and `esphome` by their integration;
  `radio` for the firmware of the Zigbee or Thread radio adapter (its own kind, so that allowing device firmware does
  not flash the network's coordinator); `firmware` by the device class; `other`. Besides the kinds, single update
  entities.
* **Window:** days, start and end in Home Assistant's time zone, read as the agent reads `install.window`: a window past
  midnight belongs to the day it starts on.
* **Never by Vördr**, each with a fixed code: Vördr's own update entities are left out of the plan, told by the
  registry's platform `vordr`, not by `update.vordr_*` (HACS's entity for this integration has such a name and stays
  choosable); `skipped`, a version the person skipped in Home Assistant; `self_updating`, an entity whose
  `auto_update` is on: reported, never installed, and Vördr does not switch it off; `in_progress`; `no_install`, an
  entity without the install feature.
* **Order of the codes:** `in_progress`, `self_updating`, `no_install`, `not_allowed`, `major`, `too_new`,
  `outside_window`, `ready` — first what Home Assistant or the entity decided, then the options, then the version, its
  age and the window. `too_new` and `outside_window` carry the earliest time the update would be ready.
* **Report:** `sensor.vordr_ha_updates_ready` on its own device "Vördr Home Assistant", not below the hub, since it does
  not need the hub and stays available without it. State: how many are ready. Attributes: how many per code (their
  names are the codes' texts, in the reader's language), the minimum age, and the list `updates` per entity (kind,
  code, versions, first sight, earliest time), which stays out of the recorder
  ([ADR-0101](0101-package-lists-stay-out-of-the-recorder.md)).
* **Dry run:** nothing installs. Installing, the backup before it and one entity of a model first are decisions of
  their own.

Option 2 turns the hub into a remote control of Home Assistant: a taken hub could install there, and the hub would have
to see entities that are none of its business. Option 3 cannot keep state that a later stage needs (first sight, a
run across a restart of Home Assistant) in a form tests reach, and its rules sit in an automation that a re-import
overwrites, as the local patch of ADR-0028 shows. Option 4 has no common window, no minimum age and no rule for major
versions; it stays where a person chose it, and Vördr reports it as `self_updating`.

### Consequences

* Good, because the smart home gets the hosts' ideas — window, minimum age, major versions by hand — visible before
  anything installs.
* Good, because a taken hub gains nothing: the rules are opened in Home Assistant by its administrators.
* Good, because the plan is a pure function and every code is tested on its own.
* Bad, because rules live in a second place: `agent.yaml` for hosts, the integration's options for Home Assistant.
* Bad, because there is one entry per hub: with more than one hub, each entry plans the same update entities with its
  own options. The install stage has to settle which one acts.
* Bad, because kinds depend on the names of integrations (the Supervisor's devices, HACS's platform names, the radio
  adapters' integrations). A renamed integration falls to `other`, which stays closed unless chosen.
* Neutral: ADR-0013 stays; Home Assistant OS still gets no agent.

### Confirmation

`ha/tests/test_ha_updates_plan.py`: every code, their order, the window (also past midnight), closed by default, a
single entity besides the kinds, Vördr's own entities left out by platform and an entity of another platform with a
similar name kept, the kinds from the registries, the check of the options with each refusal.
`ha/tests/test_ha_updates.py`: the facts from states and registries, the sensor's state, counts and list, the list
out of the recorder, its own device that cannot be removed and stays available without the hub, the plan following
the options at once and a minute's tick, invalid stored options allowing nothing, the options menu, its closed
defaults, saving that keeps other items' options, refusals with a text and by the form. `ha/tests/test_texts.py`:
every code, kind, day, field and error has its text in every language. A mutation probe (each weakening of the plan,
the tracker, the sensor alone) makes at least one of these tests fail.
