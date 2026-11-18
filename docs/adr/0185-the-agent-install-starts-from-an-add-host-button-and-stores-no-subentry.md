---
status: proposed
date: 2026-11-18
decision-makers: kay (2026-11-18, asked why adding a device sits in the configure dialog and decided to rebuild it as an "Add host" button); proposed by Claude
---

# The agent install starts from an "Add host" button and stores no subentry

Refines [ADR-0161](0161-home-assistant-installs-the-agent-once-over-ssh-and-keeps-no-credentials.md): everything it
decides about the install itself stays, with its threat comparison: what is checked, held, run on the host and read
from it. What changes is where the install starts and how it ends in Home Assistant.

## Context and Problem Statement

ADR-0161 put the one-time SSH install of the agent into the integration's options, as an item of the menu next to the
settings ("Configure → Install the agent on a Linux host (SSH)"). The options hold an entry's settings; adding a device
is no setting, and people look for it where Home Assistant offers to add things: a button on the integration's page.
The maintainer asked why adding a device sits in the configure dialog, and decided that it becomes such a button.

Home Assistant offers these buttons through config subentries: an integration names subentry types, the integration's
page shows "Add …" for each, and the button starts a subentry flow of that entry. A subentry flow usually ends by
storing a subentry under the entry, which the page then lists.

Where does the install start, and what does it leave behind in Home Assistant?

## Decision Drivers

* Adding a host is found where Home Assistant offers to add things, not among the settings.
* The install keeps every check of ADR-0161 as it is: the host key confirmed before any credential, the probe, the
  signed manifest, size and hash on the host before anything runs, the cancellation when the dialog closes, the
  credentials only in memory, the output caps, `systemd-run`, the codes and their texts.
* One source of which hosts exist: the hub's registry, where a new host waits for approval
  ([ADR-0010](0010-administration-only-local.md),
  [ADR-0157](0157-a-waiting-host-is-a-repair-that-names-the-approval-command.md)) and from which it shows in Home
  Assistant once approved.
* What cannot run says why at the start, each reason with its own code and text
  ([ADR-0007](0007-fixed-codes-instead-of-error-texts.md)).
* Only Home Assistant's administrators can start it, as before.

## Considered Options

1. Keep the install as an item of the options menu, as ADR-0161 has it.
2. A subentry type `host` whose flow is the install; it ends with an abort that carries the outcome, success included,
   and stores nothing.
3. As 2, but on success the flow stores a subentry for the host (title the node, data node and fingerprint).
4. An action (service) with the host and the credentials as fields.

## Decision Outcome

Chosen option: **2**.

* **The button.** The config flow names one subentry type, `host` (`async_get_supported_subentry_types`); the
  integration's page shows "Add host" ("Host hinzufügen") at the hub's entry. Every entry offers it; whether it can run
  now is what its first step checks. Home Assistant lets only administrators start a subentry flow, as it does for the
  options.
* **The flow** is the install of ADR-0161, step for step, moved out of the options flow: the target (`user`), the host
  key (`host_key`: log in, or stop), the credentials (`login`), the install as progress (`install`), the end
  (`result`). The same `AgentInstall` holds what it held; the flow's `async_remove` drops it however the flow ends, and
  Home Assistant cancels the install's task when the dialog closes, as it did for the options flow.
* **At the start**, before anything is entered, each missing precondition ends the flow with its own code and text:
  the SSH library not installed (`install_needs_ssh_library`), the entry not loaded (`entry_not_loaded`), no operator
  key (`install_needs_operator_key`, whose text names where to enter it: Configure → Operator keys), the hub not
  connected (`hub_not_connected`), the hub without `agent_download` (`hub_without_download`). The client's role is not
  checked there: the hub's status names its features, not the client's role
  ([ADR-0039](0039-ha-client-roles.md)), so Home Assistant does not know the role before it asks the hub for the
  release. The first form names the requirement and the command (`vordr-hub client-role <client> --role admin`); the
  hub's refusal `role_forbids` stays the check, after the probe and before anything runs on the host.
* **The end stores nothing.** Success and every failure end with an abort whose text is the outcome; on success the
  agent's version, node and fingerprint, that the host waits for approval at the hub, that the repair "Host … waits for
  approval" will name the command, and that the host shows as a device once approved. Home Assistant allows this
  cleanly: a subentry flow adds a subentry only when it creates an entry, never when it aborts, and Home Assistant's own
  subentry flows end with an abort too, at the start (`entry_not_loaded`) and after a reconfigure
  (`reconfigure_successful`).
* **The options** keep the settings only: Home Assistant's updates, the operator keys and the AI explanations. The
  operator keys stay there: they are a setting of the entry, public, and checked against every release.
* **Texts** are under `config_subentries.host` in `strings.json` and the translations, in the parts Home Assistant's
  schema has for a subentry flow (`entry_type`, `initiate_flow.user`, `step`, `progress`, `error`, `abort`); the
  operator keys' errors stay under `options.error`.

### Why not the others

* **Option 1, the options menu:** it works, but it hides adding a device among the settings; the maintainer decided
  against it. It was also the odd item of that menu: every other item saves the entry's options, the install never
  does.
* **Option 3, a stored subentry per host:** Home Assistant would keep a second list of hosts next to the hub's
  registry. The two drift apart as soon as a host is rejected, forgotten or renamed at the hub, set up by hand, or
  installed again: the page would list hosts the hub does not have and miss hosts it has. Deleting such a subentry
  would look as if it removed the host, which it cannot: approving and forgetting stay at the hub (ADR-0010). And it
  would be the one thing the install leaves behind about a host, also about one that never joins. The hosts the hub
  has show in Home Assistant anyway, as devices from the hub's view.
* **Option 4, an action with fields:** the credentials would travel in an action's data, which Home Assistant logs,
  shows in traces and may record; there is no step to confirm the host key before credentials are sent; and there is
  no dialog to show the fingerprint at the end.

### Consequences

* Good, because adding a host is a button on the integration's page, where Home Assistant offers to add things, and
  the options are settings only.
* Good, because what the install checks, holds and runs does not change: the same code, moved, driven by the same
  tests through the subentry flow.
* Good, because the hub stays the one list of hosts; the button leaves nothing behind in Home Assistant apart from
  what ADR-0161 already keeps (the newest release an install took).
* Bad, because success shows as an abort: Home Assistant closes it the same way as a failure, so the text has to say
  that it worked. The page lists nothing under "Host"; hosts show as devices once approved, not as subentries.
* Bad, because the role is still known only when the hub answers, after the login and the probe. A field with the
  client's role in the hub's status would let the first step check it; that is a change of the hub and its protocol,
  not made here.
* Neutral: the texts carry a label for the subentry type ("Host"), which Home Assistant shows only above stored
  subentries, so never.

### Confirmation

`ha/tests/test_agent_install.py` starts the install as Home Assistant's frontend does, as the subentry flow
`(entry_id, "host")` of the hub's entry, and keeps every test ADR-0161 names on it: success with a password and sudo
(with `curl` and `wget`) and as root with a key, the host key not confirmed and changed after confirming, the login
refusals, the probe's and the hub's refusals, the release checks, size and hash on the host, the bounded download,
the CA, the agent's refusals and failures, closing the dialog during the login and during the install, the bounded
output, and no credential or token in the logs, the entry, its options, the diagnostics or the storage. New:
`test_the_options_menu_no_longer_offers_the_install` (the menu is `ha_updates`, `operator_keys`, `ai_explanations`, and
its old step is refused), `test_the_button_add_host_is_registered` (every entry offers `host`, without reconfigure),
`test_install_explains_why_it_cannot_run` (the entry not loaded, no operator key, a hub without the feature, no hub, no
SSH library: each with its own code, before anything is asked, nothing stored, no flow left), and after a successful
install, with a password and with a key, `assert_nothing_stored` (no subentry in the entry or in what Home Assistant
writes of it, no other entry, no flow left, nothing of the host in the storage).
`ha/tests/test_agent_install_parts.py` checks that every end and every form error has its text under
`config_subentries.host` with exactly its values, that the steps and the button have theirs, that the start form names
the role command, and that the options keep only the operator keys' errors. `ha/tests/test_texts.py` checks the flows'
texts against the parts Home Assistant's schema allows, a subentry flow's included. A mutation probe (the host key step
skipped, a host key menu without the stop, a subentry stored at the end, nothing cleared when the dialog closes, no
check of the preconditions, of the entry's state or of the SSH library, the install still in the options menu, the
button not registered, the credentials not cleared after the login, a prefilled password field, the button without
its label) makes at least one of these tests fail.
