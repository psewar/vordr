# Vördr – own Home Assistant integration instead of MQTT

English · [Deutsch](ha-integration.de.md)

> **Status:** Built (section 10). Home Assistant talks to the hub only through the integration; MQTT no longer exists
> since hub 0.6 ([ADR-0026](../docs/adr/0026-remove-mqtt.md)). Details from the build are in sections 5a, 6a and 6b.
> Complements [architecture.md](architecture.md), section 8 (HA entities). Technical names are English, display names
> come from the integration's translations (de/en, they follow the system language of HA).

## 1. Reason

Hubs up to 0.5 reported to Home Assistant through MQTT discovery. This showed
([ADR-0023](../docs/adr/0023-own-ha-integration.md)):

1. **The devices are listed under "MQTT"** among all the other MQTT devices instead of under an integration of their
   own. Devices of the same name from other integrations could only be told apart by a prefix in the name.
2. **An MQTT update entity cannot show "unknown".** HA checks `latest_version` as text and discards the whole message
   on `null`. The workaround is "unavailable". An own `UpdateEntity` knows `latest_version = None` and shows "unknown".
3. **Commands over MQTT are not authenticated.** Without a broker ACL, any account of the broker can write to
   `vordr/host/+/cmd`. Hub and agent do check every command against verbs and policy, but as soon as Vördr installs,
   a broker ACL would be mandatory – and it affects all accounts of the broker.

The firewall is not a reason: agents do not speak MQTT, a host only needs the way to the hub.

## 2. Decision: the hub stays its own service

Rationale as an ADR: [ADR-0022](../docs/adr/0022-hub-stays-own-service.md).

| | A: integration as a client of the hub (chosen) | B: hub as part of HA |
|---|---|---|
| Signing key | stays in the hub; HA can only ask | would sit in HA; a compromised HA signs jobs for all hosts |
| Reachability | agents reach only the hub | every network would need a path to HA |
| HA restart (updates!) | disconnects only the integration; agents stay connected | disconnects all tunnels |
| Approval of new hosts | only at the hub, as today | in HA |
| Effort | endpoint in the hub + integration | rebuild the hub in Python |

## 3. Overview

```
vordr-agent ──wss /v1/tunnel──▶ vordr-hub ◀──wss /v1/ha── HA integration "vordr" (in Home Assistant)
 (per host)                     (hub,                     devices "Vördr <node>", entities,
                                 signs)                   buttons -> commands
```

Both endpoints run on the same port (8443) with the same certificate. The connection goes out from HA; the hub needs no
path to HA. MQTT is gone entirely (section 8).

## 4. Trust and rights

- On setup, the integration generates its own Ed25519 key pair (stored in the config entry).
- It is approved **only at the hub**, like a host: `vordr-hub clients` shows waiting clients,
  `vordr-hub approve-client <name> --fingerprint <fp>` approves, `reject-client` / `forget-client` revoke.
  The config flow shows the fingerprint and the command and waits until the approval is there.
- An approved HA client may:
  - read the state of all approved hosts and the list of waiting hosts (name, fingerprint, OS),
  - request commands from the fixed list (`check`, `install`, `install_security`, `install_app:<id>`, `reboot`,
    `agent_update`); the hub checks them against verbs and the host's policy as today and only then signs.
- It may not: approve, reject or forget hosts, change policies, sign by itself, see other clients.
- Several HA clients are possible (e.g. a test HA), each approved individually and revocable.

Change to the threat model (architecture.md, 5): A compromised HA can, as before, only trigger allowed jobs. The path
through any MQTT account is gone; the hub no longer needs an account at the broker.

## 5. Protocol `/v1/ha`

JSON messages over WebSocket, like the agent tunnel; `protocol: 1` in the hello.

| Direction | Message | Content |
|---|---|---|
| Hub → HA | `challenge` | nonce |
| HA → Hub | `hello` | `role: "ha"`, name, public key, signature (context `vordr/ha-hello/v1`), integration version, `protocol` |
| Hub → HA | `status` | `pending_approval` / `approved` / `rejected` with reason; again on change |
| Hub → HA | `snapshot` | hub (version, `protocol`, waiting hosts) and all hosts as `HostView`; after every approval and every connection setup |
| Hub → HA | `host` / `host_removed` | one `HostView` on every change / host forgotten or rejected |
| Hub → HA | `hub` | hub state on change (waiting hosts) |
| HA → Hub | `command` | `request_id`, `node`, `command`; `reason` for a run outside the window (architecture.md 6.4) |
| Hub → HA | `command_result` | `request_id`, `code` (`ok` = job signed and sent, waits for the running run, or is that run already, then with its `job_id`; otherwise a fixed reason such as `verb_unsupported_by_agent`, `not_allowed_by_host_policy`, `not_connected`), `job_id`; the result of the run comes later in the `HostView` (`last_run`) |
| both | `heartbeat` | every 30 s; 90 s of silence = connection dead |

`HostView` is the transport-neutral version of the former MQTT state: device (name, model, agent version), `online`,
`problem` (reasons, next step), `pending`/`security`, `system` (installed, newest version or `null`), reboot need
(`true`/`false`/`null` with reason, packages, answered signals), `last_report`, `last_run` and `capabilities` (which
buttons there may be: verb announced **and** allowed by the policy). `null` may appear here – it is our protocol, and
the integration turns it into `None`.

If the integration cannot handle the hub's `protocol` version, it does not connect and reports this as a repair issue
("hub too old/too new") instead of working halfway.

### 5a. Settled while building H1

- **Types** in `internal/wire/ha.go` (messages) and `internal/wire/view.go` (`HostView`, `HubView`). The views are
  computed in one place (`internal/hub/view.go`).
- **Rejected before the registry** (`status` `rejected`, connection closed): hello with a wrong signature, `role` ≠
  `ha`, name not `^[a-z0-9][a-z0-9.-]{0,62}$` (`bad_hello`); a different `protocol` version (`protocol_mismatch`). An
  agent hello on `/v1/ha` and an HA hello on `/v1/tunnel` both fail as `bad_hello`.
- **Rejected in the registry:** a different key under a known name (`key_mismatch`), a rejected client (`rejected`),
  more than 5 waiting clients (`too_many_pending`).
- **Waiting client:** gets only its `status` and answers to heartbeats. A `command` from it is answered with
  `client_not_approved`. It is decided at every command after the registry, as it is right now (state and key), not
  by the status at login.
- **Approval during the connection:** `status` `approved`, then `snapshot`. Rejecting sends `status` `rejected` and
  disconnects; forgetting disconnects.
- **`command`:** `request_id` `^[A-Za-z0-9_-]{1,64}$`, `node` like a host name, `command` at most 80 characters.
  Otherwise the hub disconnects (protocol violation, in the event log) instead of guessing. Answer codes:
  `ok` (with `job_id`), `client_not_approved`, `unknown_node`, `not_approved`, `bad_request`,
  `verb_unsupported_by_agent`, `not_allowed_by_host_policy`, `not_connected`, `send_failed`. With `reason`
  ([ADR-0087](../docs/adr/0087-runs-outside-the-window-with-a-reason.md)) additionally `bad_reason` (empty, over 200
  bytes, control characters) and `outside_window_unsupported` (the agent does not know it yet); `bad_request` when the
  verb has no window (`check`, `agent_update`). An invalid reason does not disconnect, it is input from people.
  `outside_window`: an install without a reason while the window is closed, for an agent that accepts no reasons;
  rejected without a backup and without a job. Otherwise the hub itself gives an install without a reason outside the
  window one ([ADR-0109](../docs/adr/0109-the-install-button-installs-at-once.md)).
  `rate_limited`: an install of the system packages or of an app while the agent's minimum interval since its last run
  is still running; rejected without a backup and without a job. `HostView.next_install` says from when it works again
  ([ADR-0110](../docs/adr/0110-the-hub-checks-the-minimum-interval-before-the-backup.md)).
- **Pushes:** on every change and at the latest every 30 s (the offline limit and the age of pending updates expire
  without an event). Only what changed compared with the last thing sent is sent; a host that is no longer approved
  comes as `host_removed`.
- **`HostView`:** `last_report` `null` means "never reported". Whether package entities exist is then open (neither
  create nor remove). With a report, they exist exactly when `system` is set. `capabilities` offers exactly what
  `Resolve` accepts. The only exception: `install_security` has no button of its own when security is already the
  default scope of the update entity. A test checks this for all 96 combinations of verbs and policies.

## 6. The integration `custom_components/vordr`

- **Config flow:** address (e.g. `wss://vordr-hub.example:8443/v1/ha`), optional own CA certificate (PEM), name of the
  client. Generates the key, shows the fingerprint and the approval command, waits as a progress step. Checks
  reachability beforehand itself (same stages as `vordr-agent hub-check`) and names the failing stage.
- **Connection:** one background task per config entry, backoff 1–30 s, push instead of polling
  (`DataUpdateCoordinator` without an interval, `async_set_updated_data`). Without a connection, all entities are
  unavailable.
- **Devices:** hub device "Vördr hub", per host "Vördr <node>" with `via_device` to the hub device.
- **Entities** (same meaning as architecture.md 8.1/8.2, now native):

| Entity | Platform | New compared with MQTT |
|---|---|---|
| `update.vordr_<id>_system` | `UpdateEntity` | `latest_version = None` → **unknown**; `INSTALL` only with the capability; `in_progress`, progress |
| `sensor.vordr_<id>_pending_updates`, `_security_updates` | `SensorEntity` | – |
| `sensor.vordr_<id>_last_report` | `SensorEntity` (timestamp) | – |
| `sensor.vordr_<id>_last_run` | `SensorEntity` (enum) | – |
| `binary_sensor.vordr_<id>_online`, `_problem`, `_reboot_required` | `BinarySensorEntity` | `None` → unknown without a template workaround. A PC that has announced that it sleeps (`power` in the host view, hub from 0.15.0) is not online and still not a problem; the integration does not show `power` as a state of its own yet |
| `button.vordr_<id>_check`, `_install_security`, `_reboot` | `ButtonEntity` | command authenticated through the own connection |
| `sensor.vordr_hub_*`, `binary_sensor.vordr_hub_online` | hub device | – |

- **Entity IDs stay** `*.vordr_<id>_<object>`, so that dashboards, automations and blueprints that name
  `update.vordr_*` (such as an auto-update blueprint that is to leave them out,
  [ADR-0028](../docs/adr/0028-site-decision.md)) keep fitting. The way: set the `entity_id` as a
  suggestion before adding, proven by a test against the HA version (6a).
- **Repair issues** (section 11): one issue per host with a problem, with the reason and the next step; it disappears
  as soon as the problem is gone.
- **Diagnostics download:** connection, versions, host list – without keys.
- **Translations:** en and de for the config flow, entities and issues.
- **Dependencies:** only what HA brings (`aiohttp` for the WebSocket, `cryptography` for Ed25519).

### 6a. Settled while building H2

- **Entity IDs** are set explicitly by the integration (`self.entity_id` before adding); HA 2026.10 takes them over as
  long as they are free. Proven in the test (`test_entity_ids_as_mqtt_had_them`).
- **Unique IDs** `<node>:<key>` or `hub:<key>`; devices `("vordr", "host:<node>")` under the hub device
  (`via_device_id`, since HA 2026.10; `via_device` and `async_get_device(identifiers=…)` are deprecated there).
- **Entities come and go with the view:** it is decided only after a snapshot (complete view). Without a report
  (`last_report` null), package entities stay open; buttons follow `capabilities`; removal goes through the entity
  registry, so that leftovers from before a restart disappear too. Hosts that the hub no longer has lose their device.
- **Additional entity** `sensor.vordr_hub_connection` (diagnostic): `connecting`, `unreachable`, `pending_approval`,
  `approved`, `rejected` – the state as a fact, not guessed from errors.
- **Events for automations:** `vordr_problem` `{node, active, reason, reasons, check_failed_code, message}` on a new,
  changed or ended problem (after an HA restart once for each one that still exists); `vordr_run_finished`
  `{node, job_id, verb, scope, state, message}` (since H5, see 6b).
- **Repair issues** `problem_<node>` per host problem, `protocol_mismatch` and `client_rejected` for the connection.
- **Reboot as a repair issue** `reboot_<node>` from the first report "Reboot required" (only a clear yes) until the host
  needs none ([ADR-0132](../docs/adr/0132-a-needed-reboot-is-a-repair-from-the-first-report.md)):
  - with "Fix" where the hub offers the reboot (capability `reboot`): confirm, then the same command as the reboot
    button; on confirming it is checked again (connection, host, need, capability), each refusal with its own reason;
    otherwise "restart by hand" (`reboot_required_manual`);
  - warning, from the hub's `reboot_pending` an error; if the problem consists only of `reboot_pending`, `problem_<node>`
    is omitted for it (problem sensor and `vordr_problem` stay);
  - after an accepted "Fix" it stays away until the reboot run ends, at most 30 minutes.
- **Buttons** raise a refusal by the hub into the UI as an error with a code (`command_refused`); the update entity
  has `INSTALL` only with the capability `install`.

### 6b. Languages (H5, protocol 2)

Before H5, the hub sent finished German texts (`next_step`, `latest_version` "n pending …", the package list). A repair
issue therefore mixed languages: frame in the profile language, reason in the system language, next step in German.
Since H5, the hub sends only codes and numbers, and the integration writes every text from its translation files
([ADR-0027](../docs/adr/0027-protocol-2-codes-and-translations.md)):

- **Protocol 2:** `problem` without `next_step`; `last_run` without `next_step`; `system` with `packages` (at most 30,
  in the order of the report) instead of `latest_version`/`release_summary`; `hub.limits` with the thresholds
  (`offline_after_minutes`, `stale_after_days`, `reboot_pending_after_days`, `no_report_after_hours`). The hub rejects
  an integration with protocol 1 (repair issue `protocol_mismatch`).
- **What the frontend translates** (profile language): repair issues – one text of its own per problem key
  `problem_<key>` with the thresholds as placeholders –, states and the attribute values of `_problem` (`reason`,
  `next_step`, `check_failed_code`; all three codes).
- **What the backend writes** (system language, like the names): `latest_version` and `release_summary` of the update
  entity, the device name of the hub. Their texts are in the section `common` of the translation files, the one Home
  Assistant keeps for texts of the integration's own
  ([ADR-0145](../docs/adr/0145-the-integrations-own-texts-live-in-the-common-section.md)).
- **Problem key:** the first reason; for `check_failed` and failed runs the result code, because the next step depends
  on it; an unknown code becomes `unknown`.
- **Events** carry `message` in every supplied language, so that an automation chooses the language of its recipient,
  independent of the system language.
- `tests/test_texts.py` keeps the files together: every language has every text, every issue text contains the next
  step of its key, a code is called the same everywhere. It also checks what hassfest, Home Assistant's own check,
  requires: only sections Home Assistant knows, and no text that looks like an HTML tag.

## 7. Changes to the hub

- Endpoint `/v1/ha` next to `/v1/tunnel`; client registry `clients.json` (0600) separate from the host registry.
- CLI: `clients`, `approve-client`, `reject-client`, `forget-client`, `client-role`; admin codes as with the hosts.
- `HostView` computed in one place (`internal/hub/view.go`, 5a); `/v1/ha` is the only way to HA.
- Commands go through the same `Resolve` as the MQTT commands did before.
- For the migration, up to hub 0.5 there was `mqtt.mode: remove`: connect once, delete all own retained topics,
  disconnect.

## 8. Migration

Applies only to an installation that still runs a hub up to 0.5 with MQTT. A new installation sets up the integration
right away (`ha/README.md`).

1. Roll out the hub with `/v1/ha` (0.5); MQTT keeps running.
2. Put the integration into `/config/custom_components/vordr`, restart HA.
3. `mqtt.mode: remove`: the MQTT entities disappear, their IDs become free.
4. Set up the integration, approve it at the hub: devices and entities appear under the same IDs.
   Gap: a few minutes without vordr entities.
5. Hub 0.6 or newer without an `mqtt` section in `hub.yaml`; remove the hub's MQTT account at the broker and its
   password file.
6. Cross-check: all IDs present, the button "Check now" produces a run.

## 9. Testing

- Hub (Go): approval of the client, client cannot approve hosts, snapshot and push, commands with all reasons for
  refusal, revocation disconnects; mutation test on the rights check.
- Integration (Python): `pytest-homeassistant-custom-component` under Linux, with the HA version that
  `ha/requirements_test.txt` fixes. Config flow including waiting for approval, all entities, `None` → unknown,
  availability, buttons → commands, entity IDs, repair issues. Ruff and Pyright.
- End to end: the Python test starts the real `vordr-hub` binary with a test certificate.

## 10. Stages

| Stage | Content | Done when |
|---|---|---|
| H1 (built) | Hub: `/v1/ha`, client registry and CLI, `HostView`, tests | a test client gets snapshot, pushes and command results; one that is not approved gets nothing |
| H2 (built) | Integration: config flow with approval, connection, entities, de/en, tests | all entities from section 6 in one HA test, IDs proven |
| H3 (built) | Repair issues, diagnostics, `mqtt.mode: remove`, migration (section 8) | the hub runs without MQTT, all IDs unchanged |
| H5 (built) | Protocol 2: codes instead of texts, translations (6b) | every language has every text (`tests/test_texts.py`) |

## 11. Decisions

1. Repair issues per host problem: **yes** ([ADR-0024](../docs/adr/0024-repair-issues.md)).
2. Device name "Vördr <node>" also under an own integration: **yes**, the device list is global
   ([ADR-0021](../docs/adr/0021-device-name-vordr-node.md)).
3. Distribution: **through HACS** as `vordr.zip` on every release of the public repository; the integration lives in
   `custom_components/vordr/` at the root of the repository, because HACS checks that layout before it downloads
   anything ([ADR-0144](../docs/adr/0144-the-integration-lives-at-the-repository-root.md), supersedes
   [ADR-0136](../docs/adr/0136-integration-via-hacs-as-a-zip-release.md) and through it
   [ADR-0025](../docs/adr/0025-distribution-via-deploy-tool.md)); copying it by hand to
   `/config/custom_components/vordr` still works (`ha/README.md`).
4. **Remove MQTT from the code entirely** after the migration ([ADR-0026](../docs/adr/0026-remove-mqtt.md)).

## 12. Result of the migration

After the migration (section 8), all entity IDs stand unchanged under the platform `vordr`; only
`sensor.vordr_hub_connection` is new, no MQTT devices remain. The display names follow the system language of HA; under
MQTT they were fixed German ([ADR-0075](../docs/adr/0075-display-names-follow-system-language.md)).
