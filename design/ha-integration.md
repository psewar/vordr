# Vördr – own Home Assistant integration instead of MQTT

English · [Deutsch](ha-integration.de.md)

> **Status:** Built (section 10). Home Assistant talks to the hub only through the integration; MQTT no longer exists
> since hub 0.6 ([ADR-0026](../docs/adr/0026-remove-mqtt.md)). Details from the build are in sections 5a, 6a and 6b;
> Home Assistant's own updates under Vördr's rules, planned and, once switched on, installed, in 6c.
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

- On setup, the integration generates its own Ed25519 key pair (stored in the config entry). Until the entry exists,
  the key waits in the integration's storage file `vordr.pending_keys` (mode 0600), per hub and client name: setting up
  again after a timeout, a closed dialog or a restart uses the same key and so continues the request the hub keeps; the
  entry takes the key over and the file forgets that hub
  ([ADR-0156](../docs/adr/0156-the-config-flow-keeps-its-key-until-the-client-is-set-up.md)).
- It is approved **only at the hub**, like a host: `vordr-hub clients` shows waiting clients,
  `vordr-hub approve-client <name> --fingerprint <fp>` approves, `reject-client` / `forget-client` revoke.
  The config flow shows the fingerprint and the command and waits until the approval is there.
- An approved HA client may:
  - read the state of all approved hosts and the list of waiting hosts (name, fingerprint, OS),
  - request commands from the fixed list (`check`, `install`, `install_security`, `install_app:<id>`, `reboot`,
    `agent_update`); the hub checks them against verbs and the host's policy as today and only then signs,
  - with role `admin`: fetch the signed agent release to install a new host (`agent_download`, 5b).
- It may not: approve, reject or forget hosts, change policies, sign by itself, see other clients, get the operator
  key from the hub (it has it from the person who sets it up, 5b).
- Several HA clients are possible (e.g. a test HA), each approved individually and revocable.

Change to the threat model (architecture.md, 5): A compromised HA can, as before, only trigger allowed jobs. The path
through any MQTT account is gone; the hub no longer needs an account at the broker.

## 5. Protocol `/v1/ha`

JSON messages over WebSocket, like the agent tunnel; `protocol: 1` in the hello.

| Direction | Message | Content |
|---|---|---|
| Hub → HA | `challenge` | nonce |
| HA → Hub | `hello` | `role: "ha"`, name, public key, signature (context `vordr/ha-hello/v1`), integration version, `protocol` |
| Hub → HA | `status` | `pending_approval` / `approved` / `rejected` with reason; again on change; `features`: what the hub accepts beyond the base protocol (`agent_download`) |
| Hub → HA | `snapshot` | hub (version, `protocol`, waiting hosts) and all hosts as `HostView`; after every approval and every connection setup |
| Hub → HA | `host` / `host_removed` | one `HostView` on every change / host forgotten or rejected |
| Hub → HA | `hub` | hub state on change (waiting hosts) |
| HA → Hub | `command` | `request_id`, `node`, `command`; `reason` for a run outside the window (architecture.md 6.4) |
| Hub → HA | `command_result` | `request_id`, `code` (`ok` = job signed and sent, waits for the running run, or is that run already, then with its `job_id`; otherwise a fixed reason such as `verb_unsupported_by_agent`, `not_allowed_by_host_policy`, `not_connected`), `job_id`; the result of the run comes later in the `HostView` (`last_run`) |
| HA → Hub | `agent_download` | `request_id`, optionally `os` and `arch` of a new host (5b); only to a hub whose `status` named `agent_download` |
| Hub → HA | `agent_download_result` | `request_id`, `code`; with `ok` the signed manifest, its signature, the hub's key and a download path per binary (5b) |
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

### 5b. The agent release for a new host

Built in the hub; the integration does not send the request yet
([ADR-0167](../docs/adr/0167-home-assistant-gets-one-time-downloads-of-the-signed-agent-release.md)). It is meant for
setting up a host from Home Assistant: Home Assistant reaches the new device once over SSH, fetches the agent there and
runs `vordr-agent install`. The hub has nothing to do with SSH and never sees the device's credentials.

- **Request:** `agent_download` with `request_id` (as with `command`; a malformed one ends the connection) and
  optionally `os` and `arch` as the manifest names the platform (`linux`/`amd64`, `arm64`, `arm`; `windows`/`amd64`),
  both or neither. Neither: every binary of the release. Only to a hub whose `status` carries `agent_download` in
  `features`; an older hub ends the connection on a message it does not know.
- **Answer** with `ok` (other codes carry only `request_id` and `code`):

  ```json
  {"t": "agent_download_result", "request_id": "add-1", "code": "ok", "version": "1.4.0",
   "manifest": "<base64 of manifest.json>", "manifest_sig": "<base64 of manifest.sig>", "hub_key": "<base64>",
   "expires_at": "2030-01-01T12:15:00Z",
   "downloads": [{"file": "vordr-agent-linux-arm64",
                  "path": "/v1/agent-download/vordr-agent-linux-arm64?token=<hex>"}]}
  ```

  `manifest` and `manifest_sig` are the exact bytes the operator signed. `hub_key` is the hub's public key that signs
  every job, in the form of `hub.public_key` in `agent.yaml` and of `--hub-key`. Each path opens its file once, until
  `expires_at` (15 minutes).
- **Codes:** `ok`, `client_not_approved` (and the hub ends the connection), `role_forbids` (only role `admin`),
  `bad_request` (only one of `os`/`arch`, or not `[a-z0-9]{1,16}`), `no_release`, `no_binary_for_platform`,
  `release_incomplete` (a binary of the manifest is missing on the hub, has another size or other bytes than its
  hash), `too_many_downloads` (8 paths exist, or 4 of this client; a path counts until it expires, used or not).
- **What the integration does with the answer**, before anything reaches the new host:
  1. It checks `manifest_sig` over `vordr/agent-manifest/v1`, a zero byte and `manifest` with the operator key (and
     the backup key) the person entered. Never with a key from the hub: the hub sends none. Without such a key, no
     install.
  2. It reads platform, `sha256` and `size` only from the manifest it checked, and takes from `downloads` only the
     `path` of that `file`.
  3. On the new host it fetches the hub's address (scheme `https`, host and port of its `wss://` address) followed by
     `path`, once, compares size and SHA-256 before the binary runs, and then calls
     `vordr-agent install --hub wss://<host>:<port>/v1/tunnel --hub-key <hub_key> --operator-key <the person's key>`.
  4. It keeps the paths out of logs, diagnostics and the config entry: each carries its token.
- **Download** `GET /v1/agent-download/<file>?token=<hex>`: the binary as `application/octet-stream` with its length.
  Every refusal has a fixed status and its code as the body, and leaves the token as it was:

  | Code | Status | When |
  |---|---|---|
  | `bad_method` | 405 | not a GET |
  | `unknown_token` | 403 | no token, a malformed or unknown one, or one that expired over an hour ago |
  | `token_expired` | 410 | the 15 minutes have passed |
  | `token_used` | 409 | its download has started already |
  | `wrong_file` | 404 | the token opens another file |
  | `client_revoked` | 403 | the client the path was issued to is no longer approved with that key and role `admin` |
  | `release_changed` | 410 | the current release no longer names the file with the hash the token was issued for |
  | `binary_unreadable` | 500 | the file on the hub cannot be read or has another size |
  | `binary_mismatch` | 500 | the file on the hub has other bytes than the manifest's hash |

  With `token_used` or `release_changed`, Home Assistant asks again; a download that broke off, or was cut off for
  reading slower than 64 KiB/s, has used its token.
- **Event log:** `agent_download_issued`, `agent_downloaded`, `agent_download_refused`, with `download_id` instead of
  the token (architecture.md 10.4).

## 6. The integration `custom_components/vordr`

- **Config flow:** address (e.g. `wss://vordr-hub.example:8443/v1/ha`), optional own CA certificate (PEM), name of the
  client. Generates the key, shows the fingerprint and the approval command, waits as a progress step (at most an
  hour). Checks reachability beforehand itself (the stages of `vordr-agent hub-check` up to the challenge; it does not
  know the hub key, so there is no stage `hub_key`) and names the failing stage. A later attempt for the same hub and
  name takes the kept key (section 4). A refusal names the client and this Home Assistant's fingerprint; `key_mismatch`
  adds `vordr-hub forget-client <name>` as the way out, for a name that belongs to no other Home Assistant.
- **Connection:** one background task per config entry, backoff 1–30 s, push instead of polling
  (`DataUpdateCoordinator` without an interval, `async_set_updated_data`). Without a connection, all entities are
  unavailable.
- **Devices:** hub device "Vördr hub", per host "Vördr <node>" with `via_device` to the hub device; "Vördr Home
  Assistant" for Home Assistant's own updates (6c), without `via_device`, since it does not need the hub.
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
- **Dependencies:** what HA brings (`aiohttp` for the WebSocket, `cryptography` for Ed25519), and `asyncssh` for
  installing the agent over SSH (6d), as a range (`>=2.24.0,<3`) that holds the version HA pins for its own
  `sftp_storage` integration. Only the install item imports it; everything else loads without it.

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
- **Install again as a repair issue** `install_again_<node>` while the hub keeps a failed install of the host
  (`failed_install`, ADR-0124) and offers the install (capability `install`)
  ([ADR-0148](../docs/adr/0148-a-kept-failed-install-is-a-repair-that-installs-again.md)):
  - its fix dialog names the code and the run's detail, from the host's state when it opens (a fixable repair has its
    text in the dialog, not as a description of its own: hassfest allows one of the two); "Fix" confirms, then sends
    the same command as the install button; on confirming it is checked again (connection, host, kept failure,
    capability), each refusal with its own reason;
  - a run that goes through clears the failure at the hub, and the repair goes with it; one that fails again keeps both;
  - if the problem consists only of the kept failed install, `problem_<node>` is omitted for it (problem sensor and
    `vordr_problem` stay); without the install offered, `problem_<node>` stays as before.
- **Waiting host as a repair issue** `waiting_<node>` while the hub lists the host as waiting for approval
  ([ADR-0157](../docs/adr/0157-a-waiting-host-is-a-repair-that-names-the-approval-command.md)):
  - not fixable, a warning: approval stays at the hub (section 4); a new host is news, not an alarm;
  - the text names the node, the system the host reports, the fingerprint the hub saw and
    `vordr-hub approve <node> --fingerprint <fp>`, and asks first to compare with `vordr-agent identity` on the host
    itself; if they differ, not to approve (`vordr-hub reject <node>`);
  - it follows the hub's own view (snapshot and `hub`): gone as soon as the host is approved, rejected or forgotten;
  - only for a node that is a host name and a fingerprint of 64 hex digits; the system is the host's own word and shows
    as one line of code, at most 64 characters, without backticks. `sensor.vordr_hub_pending_approvals` keeps listing
    every waiting host as the hub sent it.
- **Buttons** raise a refusal by the hub into the UI as an error; the fix dialogs of the reboot and install-again
  repairs end with the same text. A code the integration knows says why: `role_forbids`, `outside_window`,
  `rate_limited` with the time from `HostView.next_install`, the backup check with what is missing. Any other names its
  code (`command_refused`). A fix dialog says why only for the codes the hub can send for its command, the reboot only
  for `role_forbids`. The update entity has `INSTALL` only with the capability `install`.
- **Known exploitation** ([ADR-0168](../docs/adr/0168-known-exploitation-comes-from-the-kev-catalog-fetched-whole-at-the-hub.md)):
  the KEV fields of each finding (`known_exploited`, `kev_cves`, `kev_added`, `kev_ransomware`) pass through in the
  attribute `findings` of `_vulnerable_packages` and `_malicious_packages`; `_vulnerable_packages` has the host's count
  as `known_exploited`, `sensor.vordr_hub_vulnerability_data` the catalog's state as `kev`. Each is absent while the
  hub does not send it (no `kev:` section, or no catalog yet). The integration takes no decision from them: no repair, no
  event, no new text.

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

### 6c. Home Assistant's own updates under Vördr's rules

Home Assistant offers update entities for Core, OS, Supervisor, apps, HACS, ESPHome and device firmware, and installs
them without rules of its own. The integration plans them under Vördr's rules ([ADR-0158](../docs/adr/0158-home-assistants-own-updates-follow-vordrs-rules-in-the-integration.md)) and installs what is
ready once its options switch that on ([ADR-0162](../docs/adr/0162-home-assistants-own-updates-install-only-when-switched-on-and-a-pause-holds-them.md)).

- **Rules in the integration's options** (options flow as a menu, item "Home Assistant updates", key `ha_updates`):
  allowed kinds (`core`, `os`, `supervisor`, `app`, `hacs`, `esphome`, `firmware`, `radio`, `other`) and single update
  entities; a window (days, start, end in Home Assistant's time zone, past midnight as the agent's `install.window`); a
  minimum age in days (default 7, 1–90; device firmware and radio at least 14, see below); "Install automatically"
  (`auto_install`, off by default; options saved
  without it read as off). Closed by default: nothing chosen, nothing allowed, nothing installed. Refused with a text:
  something allowed without a day, start equal to end, a minimum age that is no whole number; the form itself refuses
  unknown choices, other domains and Vördr's own entities. The hub has no say: no field, no message.
- **Kinds from the registries,** never from entity ids: Core, OS and Supervisor by their device of the Supervisor
  integration, its other devices are apps; HACS and ESPHome by their integration; the radio adapters' firmware by
  theirs (`radio`, a kind of its own); other firmware by the device class `firmware`; the rest is `other`.
- **The plan** (`custom_components/vordr/ha_updates.py`, pure functions): per update entity with a pending or skipped
  version one code, in this order: `in_progress`, `self_updating` (the entity's `auto_update`: reported, never
  installed), `no_install` (no install feature), `not_allowed`, `major`
  ([ADR-0160](../docs/adr/0160-a-new-major-version-of-a-home-assistant-update-stays-manual.md)), `last_failed`
  (Vördr's install of this version failed and is held, [ADR-0165](../docs/adr/0165-a-failed-home-assistant-update-stays-until-an-install-of-it-goes-through.md)), `too_new`
  ([ADR-0159](../docs/adr/0159-the-minimum-age-of-a-home-assistant-update-counts-from-its-first-sight.md)),
  `outside_window`, `ready`; `skipped` for a version skipped in Home Assistant. `too_new` and `outside_window` carry the
  earliest time it would be ready. Vördr's own update entities are left out, by the registry's platform `vordr`.
  Unavailable or unknown entities are not listed.
- **First sight:** Home Assistant gives no release date, so the age counts from when the integration first saw the
  version, kept per update entity in `.storage/vordr.ha_updates.<entry_id>` (written 10 s after a change and on
  unload, removed with the entry). A newer version starts its own age; an installed one, or an entity gone from the
  registry, drops it; an unavailable entity keeps it.
- **Line** ([ADR-0179](../docs/adr/0179-a-newer-release-of-a-version-line-inherits-the-lines-age-with-two-days-of-its-own.md)):
  the major and minor number of a version that is numbers separated by dots after an optional `v` (`v1.4.2` → `1.4`,
  Core `2026.11.1` → `2026.11`); firmware numbers, commits, pre-releases and build suffixes have none. The Store also
  keeps per entity when each line it reports, installed or offered, was first seen (`lines`); a line stays while the
  entity reports a version of it, also after the install. A version of a line seen before it counts its age from the
  line's first sight but must itself be 2 days old (never more than the minimum age); the first release of a line, a
  new line and a version without a line wait their own full age.
- **Minimum age per kind** ([ADR-0180](../docs/adr/0180-device-firmware-and-the-radio-adapter-wait-at-least-14-days.md)):
  `firmware` and `radio` wait at least 14 days, or the minimum age of the options if it is longer; a fixed rule, not an
  option, which the form names in its description.
- **When it is computed:** on every state change of an update entity, when the options are saved (without a reload),
  and once a minute, since windows open and versions grow old without any change. Independent of the hub connection.
- **Sensor** `sensor.vordr_ha_updates_ready` on its own device "Vördr Home Assistant" (no `via_device`, cannot be
  removed by hand, available without the hub): state = the number of `ready`; attributes: one count per code (their
  names are the codes' texts), `min_age_days`, `min_age_days_firmware`, and `updates`, per entity `entity_id`, `kind`,
  `code`, `installed_version`, `latest_version`, `first_seen`, `line_first_seen` (where the version inherits its
  line's age), `earliest` (times in Home Assistant's time zone). `updates` stays out of the recorder
  ([ADR-0101](../docs/adr/0101-package-lists-stay-out-of-the-recorder.md)).
- **Installing** (`custom_components/vordr/ha_updates_installer.py`): only while "Install automatically" is on, the
  switch `switch.vordr_ha_updates_paused` "Pause Home Assistant updates" is off (kept across restarts), and no other
  enabled Vördr entry has installing on; the options refuse a second one (`auto_install_other_entry`) ([ADR-0162](../docs/adr/0162-home-assistants-own-updates-install-only-when-switched-on-and-a-pause-holds-them.md)).
  The attribute `installer` of the last-run sensor says which: `off`, `paused`, `other_entry`, `idle`, `running`.
- **One at a time, the system last** ([ADR-0164](../docs/adr/0164-home-assistant-updates-install-one-at-a-time-with-core-os-and-supervisor-last.md)): a run starts only when none runs, no update entity at all reports
  `in_progress` and Home Assistant's backup manager is `idle`. Order: every other kind by entity id, then
  `supervisor`, `core`, `os`; a system update only when nothing else is `ready`, and after one nothing more starts in
  that opening of the window. Vördr's own entities never. A run:

| Step | What happens | Ends with |
|---|---|---|
| Backup ([ADR-0163](../docs/adr/0163-a-backup-before-every-automatic-home-assistant-update.md)) | an entity with the `BACKUP` feature backs itself up (`backup: true` below); device firmware and radio without it need none (`exempt`, [ADR-0181](../docs/adr/0181-device-firmware-and-the-radio-adapter-install-without-a-backup-of-home-assistant.md)); for the others, Home Assistant's last successful automatic backup (the backup integration's sensor, found by platform and unique id) at most 26 h old, else `backup.create_automatic` and the sensor must show the new one within 5 min | `backup_failed` with `backup_unavailable`, `failed` or `timeout` |
| Check again | the plan computed again from the states as they are: installing on, not paused, no other entry, still `ready` for the same version, nothing busy | `skipped_paused`; `skipped_not_ready` with `target_changed`, `gone`, `auto_install_off`, `other_entry`, `busy` or the plan's code |
| Keep | the run written to `.storage/vordr.ha_updates_runs.<entry_id>` | – |
| Install | `update.install`, with `backup: true` where `BACKUP`, with `version` (the checked one) where `SPECIFIC_VERSION`; its errors caught here only | `failed`; `timeout` after 2 h |
| Verify | no longer in progress and reporting the target version (or up to date on another one than it started from) | `ok`; after 30 min, device firmware and radio after 2 h ([ADR-0183](../docs/adr/0183-device-firmware-and-the-radio-adapter-get-two-hours-to-report-their-version.md)), `timeout` while still in progress, else `not_reached` |

- **Restart:** at start, before anything else, a kept run is verified from the entity's state once Home Assistant has
  started (`restarted: true`); nothing else starts until it is decided. An entry that unloads leaves a run going (an
  install may be half done); the next start decides it from the Store.
- **After a failed backup:** no new backup for 24 h and no install of an entity that needs Home Assistant's backup
  (no `BACKUP`, not device firmware or radio) until Home Assistant shows a newer automatic backup; the repair
  `ha_updates_backup_failed` stays until then.
- **Report:** the event `vordr_ha_update_finished` with `entity_id`, `kind`, `from_version`, `to_version`,
  `installed_version`, `result`, `reason`, `backup` (`entity`, `existing`, `created`, `exempt`), `started`, `finished`,
  `restarted`. The sensor `sensor.vordr_ha_updates_last_run` on the device "Vördr Home Assistant": the phase while a
  run goes (`backing_up`, `installing`, `verifying`), then its result; the run's fields as attributes, `installer`, and
  `queue` (the ready updates in the order they would go, out of the recorder); kept across restarts.
- **Failures** ([ADR-0165](../docs/adr/0165-a-failed-home-assistant-update-stays-until-an-install-of-it-goes-through.md)): `failed`, `not_reached` and `timeout` are kept per entity in the same Store; the version
  is held (`last_failed`); a fixable repair `ha_update_failed_<object id>` with a text per result. Cleared once the
  entity reports the version or is up to date, once an install of it ends `ok`, or once it leaves the registry.
- **The repair's fix installs at once** ([ADR-0182](../docs/adr/0182-the-repairs-fix-installs-a-failed-home-assistant-update-at-once.md)):
  checked first, it starts the entity's run now, outside the window but under every other rule, with the backup and
  the check right before acting; or it aborts with a fixed code and queues nothing: `busy` (another install runs, or
  the backup manager works), `paused`, `auto_install_off`, `other_entry`, `not_ready` (the plan does not let it
  through apart from the window), `backup_held`, `no_longer_needed`, `entry_not_loaded`. The repair goes while the run
  goes; a kept result brings it back, and so does a run that does not get to install (the version is held again).
- **Not yet:** one entity of a model first (a canary across several devices of one model), a restart of Home Assistant
  after HACS updates, runs reported to the hub.

### 6d. Installing the agent on a new Linux host over SSH

Home Assistant reaches a new host once over SSH, has it fetch and check the signed agent release, and runs
`vordr-agent install` there. It keeps nothing of the SSH access; afterwards the host waits for approval at the hub
like any new host
([ADR-0161](../docs/adr/0161-home-assistant-installs-the-agent-once-over-ssh-and-keeps-no-credentials.md), with the
threat comparison). Linux with systemd and apt only.

- **Operator keys**, an item of the options menu (key `operator_keys`): the operator key and an optional backup,
  base64 Ed25519 as `vordr-release public-key` prints them. Checked (32 bytes, the backup not equal to the key and only
  with it), stored like any option: they are public. Empty removes them. Never taken from the hub.
- **The button "Add host"** on the integration's page starts the install
  ([ADR-0185](../docs/adr/0185-the-agent-install-starts-from-an-add-host-button-and-stores-no-subentry.md)): the
  config flow names the subentry type `host` (`async_get_supported_subentry_types`), and the button opens its subentry
  flow (`AddHostFlow` in `config_flow.py`), which only administrators can start. Every entry offers it. The flow stores
  nothing, no option and no subentry: the hub's registry stays the one list of hosts. It ends with an abort whose code
  has a text in `config_subentries.host.abort`, success included; what the person can correct (host, port, user,
  node, the login, sudo) shows in the form as an error (`config_subentries.host.error`). The options menu offers no
  install.
- **Steps** (the subentry flow's step ids in brackets):

| Step | What it does | Ends with |
|---|---|---|
| Start (`user`) | the SSH library installed, entry loaded, operator key stored, hub connected, its status names `agent_download`; each checked before the form shows. The form names the role the hub asks for (`vordr-hub client-role <client> --role admin`): the hub's status does not tell a client its role | `install_needs_ssh_library`, `entry_not_loaded`, `install_needs_operator_key` (names Configure → Operator keys), `hub_not_connected`, `hub_without_download` |
| Target (`user`) | host, SSH port, user (`root`), node (empty: the host's own name) | form errors; `ssh_unreachable`, `ssh_timeout`, `ssh_failed` |
| Host key (`host_key`) | fetched without logging in (`asyncssh.get_server_host_key`), shown as algorithm and SHA256 fingerprint; a menu: the same (`login`), or stop (`host_key_rejected`) | `host_key_rejected` |
| Login (`login`) | password or pasted private key with its passphrase, sudo's password (empty: the login password); the login accepts only the confirmed key, nothing goes to a `known_hosts` file | form errors `ssh_auth_failed`, `ssh_key_invalid`, `sudo_failed`, `sudo_needs_password`; `ssh_host_key_changed` |
| Probe | as the user, `key=value` facts: `uname -s`, `uname -m`, `id -u`, systemd, `apt-get`, `curl`/`wget`, `sha256sum`, `base64`, `mktemp`, `timeout`, `systemd-run`, `sudo`; then `sudo -k -n true`, else `sudo -k -S` with the password on stdin | `not_linux`, `unknown_arch`, `no_systemd`, `no_apt`, `no_download_tool`, `missing_tools`, `no_sudo`, `probe_failed` |
| Release | `agent_download` for `linux/<arch>` (`x86_64` → `amd64`, `aarch64` → `arm64`, `armv7l`/`armv8l` → `arm`); the operator's signature over `vordr/agent-manifest/v1\0` + manifest with the stored keys; the manifest read as strictly as the agent's `VerifyManifest`; file, SHA-256 and size only from it; the download path must open that file; not older than the newest release an install of this entry took (kept in `.storage/vordr.agent_release.<entry_id>`, compared as the agent's `NewerRelease`) | the hub's codes (`role_forbids` with `vordr-hub client-role <client> --role admin`, `no_release`, `too_many_downloads`, …), `hub_refused`, `manifest_signature_invalid`, `manifest_invalid`, `no_binary_for_platform`, `release_too_old`, `download_invalid` |
| Install (`install`, progress) | as root, as a transient service of its own (`systemd-run --pipe --wait --collect`), outside the login session; one script, with no `${…}` and no `$$` in it or its values, as systemd expands them in a service's command line; a private temporary folder (`mktemp -d`, umask 077, refused if nothing may run from it); download over HTTPS from the integration's host and port with the hub's CA if it has one, the URL read from a file in that folder (`curl -K`, `wget -i`), bounded by the signed size (`curl --max-filesize`, `ulimit -f` for both) and in time (`curl --max-time 900`, `timeout 900 wget`); size and SHA-256 compared before anything runs; the hub's CA to `/etc/vordr/hub-ca.pem` (compared if there, removed again after a refusal); `vordr-agent install --hub wss://<host>:<port>/v1/tunnel --hub-key <from the answer> --operator-key … [--backup-operator-key] [--node] [--ca-file]`; the folder removed by a trap in every case | `size_mismatch`, `hash_mismatch`, `download_failed`, `download_refused`, `download_too_large`, `ca_differs`, `ca_failed`, `temp_noexec`, `temp_failed`, `bad_input`; the agent's codes as `agent_<code>`, others as `agent_refused`/`agent_failed`; `agent_no_result`, `install_no_result`, `ssh_lost`, `ssh_timeout` |
| End (`result`) | the agent's last line `result=installed node=<n> fingerprint=<hex>`; an abort, also on success: no subentry is stored | `agent_installed` with node, fingerprint and version; the repair `waiting_<node>` then names the approval command, and once approved the host is a device of the entry |

- **What is read from the host:** only `key=value` fields: the probe's lines, the script's own last line
  (`step=<step> code=<code>`), and after `step=agent exit=<n>` the agent's last line. Prose is never read; a line that is
  not all fields counts as no result. Of each command at most 64 KiB of stdout is read and stderr not at all; a host
  that prints more ends the flow with `host_output_too_long`.
- **Credentials and tokens** live only in the flow object (`AgentInstall`): the login password and the key until the
  login, sudo's password until the install command is sent, the connection until the install ends. asyncssh gets the
  password and the key once, when it asks for them; the open connection keeps neither. sudo's password and the
  download path (it carries the single-use token) go over stdin, never on a command line; on the host the download
  tool reads the path from a file in the private folder. Nothing of it is in the config entry, its options, Home
  Assistant's storage, the logs (also asyncssh's debug log), the diagnostics, a form default or an exception. The flow
  drops it when it ends in any way. Closing the dialog during the login closes the connection at once and ends the
  waiting step with `install_cancelled`: the hub is not asked and nothing runs on the host. Closing it during the
  install cancels the task and closes the connection; the host finishes on its own (the install runs outside the login
  session, also where logind ends a session's processes with `KillUserProcesses=yes`) and removes its temporary folder.
- **Boundary:** `custom_components/vordr/ssh.py` is the only place that speaks SSH; it maps every failure to a code by
  its type and reads nothing from `~/.ssh`, uses no SSH agent and no default keys. `agent_install.py` holds the pure
  parts (checks, scripts, reading), `agent_installer.py` the steps and what they hold, `config_flow.py` the subentry
  flow of "Add host".
- **The client** keeps the `features` of the hub's last status and sends `agent_download` only to a hub that named it
  ([ADR-0067](../docs/adr/0067-hub-announces-features.md)); the answer goes to the one who asked, never to the
  coordinator or an entity.

### 6e. AI explanations of failed runs (optional)

When a run fails, an AI Task entity of Home Assistant that the person chose may explain it: a short text, a likely
cause and next steps from fixed lists, and a quote from the run's log as evidence. Advice only: it changes nothing
that Vördr decides, and the hub knows nothing of it
([ADR-0166](../docs/adr/0166-optional-ai-explanations-of-failed-runs-go-through-home-assistants-ai-task-and-only-advise.md)).
`custom_components/vordr/ai_explain.py` holds the pure parts (options, replacements, facts, the check of the answer)
and `Explainer`, which asks; the coordinator hands it every run that ended.

- **Item "AI explanations"** of the options menu (key `ai_explanations`): `entity_id`, an `ai_task` entity, and
  `data_ack`, the confirmation that package names and log excerpts of failed runs go to its provider. Empty: off.
  The form refuses `ai_entity_invalid` (no `ai_task` entity), `ai_data_ack_missing`, `ai_entity_missing` (not there or
  unavailable) and `ai_entity_unsupported` (cannot generate data, or AI Task's action is missing). Emptying the entity
  removes the item's options; the other items' options stay.
- **Which runs:** the result codes in `EXPLAINED`, failures whose log carries the cause. Never `ok`, a refusal
  (`rejected_*`), a decision of Vördr's rules (`precheck_version_flagged`, `precheck_version_too_new`,
  `precheck_vetting_unavailable`, `precheck_not_from_registry`) or a security signal (`precheck_hash_mismatch`,
  `failed_target_tls`, `failed_target_hostkey`). A test keeps every code in one of the two sets.
- **Steps:**

| Step | What happens | Code if not |
|---|---|---|
| Options | an entity chosen and confirmed; without one it is off: nothing is sent, no event, no attribute, and the run is taken so that switching on later does not explain it ([ADR-0184](../docs/adr/0184-with-ai-explanations-off-nothing-is-sent-and-no-event-is-fired.md)); stored options the form would refuse send nothing and are logged once | `not_configured` (off has no code) |
| Excerpt | the run has a log excerpt | `no_excerpt` |
| Entity | in the state machine and not `unavailable`, `supported_features` with generate data (1), the action `ai_task.generate_data` there; checked again right before the call | `entity_missing`, `unsupported` |
| Call | `ai_task.generate_data` through `hass.services.async_call` with `task_name` `vordr_failed_run`, `entity_id`, `instructions` and `structure`, `return_response`; at most 60 s; one question at a time | `no_answer`: a timeout, or whatever the provider raises (caught only there) |
| Answer | `data` with `explanation` (text), `likely_cause` (one value of the list), `next_steps` (values of the list), `quote` (text) | `invalid_answer` |
| Quote | part of the excerpt as it was sent, character for character, runs of white space as one space, 8 to 300 characters | `unverified`: nothing of the answer is shown |

- **What is sent:** instructions in English that name the placeholders, say that the facts are data and ask for the
  explanation in Home Assistant's language (its tag, e.g. `de`), then between the lines `BEGIN RUN FACTS` and
  `END RUN FACTS`: the result code, the verb, the package names of the run's summary (at most 50) and the end of the
  log excerpt (at most 2000 characters). A marker line inside the facts is taken out; control characters go.
- **Replaced before sending** (`redact`), in this order: the hub's URL and host → `<hub>`; the run's host and every
  other host the hub knows, case-insensitive and with the domain after it → `<host>`, `<other-host>`; names in the
  hub's domain when it has two labels or more (`*.site.example` for `hub.site.example`) → `<server>`; the host of any
  other URL, with user and password → `<server>`; IPv4 addresses (four numbers up to 255) and IPv6 addresses (eight
  groups, or fewer with one `::`) → `<ip>`, not a version like `10.0.26100.1`, a time or a MAC address. Replaced first,
  shortened then. Other words go as they are.
- **The fields asked for:** `likely_cause` one of `package_conflict`, `package_source`, `disk_space`, `package_lock`,
  `network`, `configuration`, `service`, `credentials`, `interrupted`, `other`, `unknown`; `next_steps` up to three of
  `retry_later`, `free_disk_space`, `check_package_sources`, `repair_package_manager`, `resolve_conflict_by_hand`,
  `check_service`, `check_configuration`, `check_credentials`, `check_network`, `read_run_log`. With `ok` the
  explanation is put on one line and shortened to 600 characters, and repeated steps go.
- **Result:** the event `vordr_ai_explanation` `{node, job_id, state, code, entity_id}`, with `ok` also
  `likely_cause`, `next_steps`, `explanation` and `quote`; while on, for every run of `EXPLAINED`, also when it cannot
  ask; while off, never.
  The host's `sensor.vordr_<id>_last_run` shows `ai_code`, `ai_entity_id` and with `ok` `ai_likely_cause`,
  `ai_next_steps`, `ai_explanation`, `ai_quote` while it shows that run, out of the recorder; nothing while off. Their
  values are translated like states (`state_attributes` of `last_run`).
- **Bounds:** a run is asked about once (node and job id; the last 256 are kept); an `ok` answer is kept by a digest
  of the entity and the text sent (the last 32), so the same failure on several hosts is asked about once.
- **Never:** a window, a minimum age, a scope, the policy, a button, a repair or a command. Nothing reads the free
  text.
- **Not yet:** release notes and changelogs, a daily limit, the explanation in a push (an automation on the event can
  do that).

### 6f. Notification blueprints

Four automation blueprints in `blueprints/automation/vordr/` turn the events and entities above into notifications
([ADR-0173](../docs/adr/0173-notifications-are-shipped-as-blueprints-the-user-imports.md)). The user imports them
(`ha/README.md`); the integration does not install them and its version does not change with them.

| Blueprint | Starts on | Does |
|---|---|---|
| `problem.yaml` | `vordr_problem` | `message.<language>` of the problem to a notify service; optional wait (a problem that ends or changes inside the wait is dropped), ignored reasons, a note when it ends (without a wait only) |
| `run_failed.yaml` | `vordr_run_finished`, `state` not `ok` | `message.<language>` of the result; filter by kind of run and by ignored results (`ok_reboot_required` by default) |
| `reboot_offer.yaml` | `vordr_run_finished` with `ok_reboot_required`; `vordr_problem` with `reboot_pending` | the text, and the action "Reboot now" where the host has a reboot button; a tap presses that button |
| `hub_offline.yaml` | the hub's online entity off for N minutes | a notice, and one when the connection is back |

- Inputs are selectors: the notify service (text), hosts (device selector of this integration, empty means every
  host), the language (`en`, `de`, free text; a language the integration does not ship falls back to English). No
  entity id is written into a blueprint; the reboot offer finds the host's reboot button and reboot sensor through the
  host's device.
- The action appears only where the host offers the reboot (the button exists then). A tap presses the button only if
  the sensor still says that the host needs the reboot and the button is available; otherwise it says that nothing was
  done. Each offer has its own action id and expires after the chosen hours. A blueprint never installs or reboots
  by itself.
- `mode: parallel` for the problem (waits per host) and the reboot offer (a pending tap per host); `queued` for the
  others.
- `ha/tests/test_blueprints.py` loads each blueprint with Home Assistant's blueprint schema, checks its events, event
  fields, reasons, result codes and inputs against the integration, and runs the blueprints against events of the fake
  hub with a mocked notify service.

## 7. Changes to the hub

- Endpoint `/v1/ha` next to `/v1/tunnel`; client registry `clients.json` (0600) separate from the host registry.
- CLI: `clients`, `approve-client`, `reject-client`, `forget-client`, `client-role`; admin codes as with the hosts.
- `HostView` computed in one place (`internal/hub/view.go`, 5a); `/v1/ha` is the only way to HA.
- Commands go through the same `Resolve` as the MQTT commands did before.
- `agent_download` and the download path `/v1/agent-download/` for installing a new host (5b).
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
  availability, buttons → commands, entity IDs, repair issues. The install over SSH (6d) against a fake hub and a real
  SSH server whose commands run the integration's scripts in a sandbox (`ha/tests/fake_ssh_host.py`). Installing
  Home Assistant's own updates (6c) against update entities of integrations the test plays and a played backup
  integration, including a restart in the middle. The AI explanations (6e) against a played AI Task entity and action,
  no provider. Ruff and Pyright.
- End to end: the Python test starts the real `vordr-hub` binary with a test certificate.

## 10. Stages

| Stage | Content | Done when |
|---|---|---|
| H1 (built) | Hub: `/v1/ha`, client registry and CLI, `HostView`, tests | a test client gets snapshot, pushes and command results; one that is not approved gets nothing |
| H2 (built) | Integration: config flow with approval, connection, entities, de/en, tests | all entities from section 6 in one HA test, IDs proven |
| H3 (built) | Repair issues, diagnostics, `mqtt.mode: remove`, migration (section 8) | the hub runs without MQTT, all IDs unchanged |
| H5 (built) | Protocol 2: codes instead of texts, translations (6b) | every language has every text (`tests/test_texts.py`) |
| H6 (built as a dry run) | Home Assistant's own updates under Vördr's rules: options, plan, first sight, sensor (6c) | every code tested; nothing installs |
| H7 (built, Linux) | Installing the agent on a new host over SSH: operator keys, host key, login, probe, checked release, install (6d) | every end tested against a fake hub and a real SSH server; no credential left anywhere |
| H8 (built) | Installing Home Assistant's own updates under the plan: switch, pause, backup, one at a time with the system last, check before acting, verification, restart, event, last run, repair (6c) | every result tested in Home Assistant's test harness; nothing installs until switched on |
| H9 (built) | Optional AI explanations of failed runs through AI Task: item, checks, replacements, checked quote, event, sensor attributes (6e) | every code tested against a played AI Task; nothing sent until switched on, nothing else changes |

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
5. Home Assistant's own updates: **rules in the integration, closed by default, first as a dry run**
   ([ADR-0158](../docs/adr/0158-home-assistants-own-updates-follow-vordrs-rules-in-the-integration.md)); minimum age
   from first sight, a patch release with the age of its line, firmware and radio at least 14 days
   ([ADR-0159](../docs/adr/0159-the-minimum-age-of-a-home-assistant-update-counts-from-its-first-sight.md));
   a new major version by hand ([ADR-0160](../docs/adr/0160-a-new-major-version-of-a-home-assistant-update-stays-manual.md)).
6. Setting a host up from Home Assistant: **once over SSH, credentials only in the flow's memory, the release checked
   against the operator key entered in Home Assistant and on the host before it runs, approval at the hub**
   ([ADR-0161](../docs/adr/0161-home-assistant-installs-the-agent-once-over-ssh-and-keeps-no-credentials.md));
   **started from the button "Add host", which stores nothing, not even a subentry**
   ([ADR-0185](../docs/adr/0185-the-agent-install-starts-from-an-add-host-button-and-stores-no-subentry.md)).
7. Installing Home Assistant's own updates: **only when switched on, from one entry, with a pause**
   ([ADR-0162](../docs/adr/0162-home-assistants-own-updates-install-only-when-switched-on-and-a-pause-holds-them.md)); **a backup before each**, the entity's own or Home Assistant's automatic backup at most 26 h old
   ([ADR-0163](../docs/adr/0163-a-backup-before-every-automatic-home-assistant-update.md)); **one at a time, Core, OS and Supervisor last, checked again before acting, verified by the
   version, across restarts** ([ADR-0164](../docs/adr/0164-home-assistant-updates-install-one-at-a-time-with-core-os-and-supervisor-last.md)); **a failure stays until an install of it goes through**
   ([ADR-0165](../docs/adr/0165-a-failed-home-assistant-update-stays-until-an-install-of-it-goes-through.md)).
8. AI: **optional, in the integration through Home Assistant's AI Task, for failed runs, advice only**, with the
   site's names replaced and the quote checked; the hub stays without AI and API keys
   ([ADR-0166](../docs/adr/0166-optional-ai-explanations-of-failed-runs-go-through-home-assistants-ai-task-and-only-advise.md));
   **off means silent**: no call and no event
   ([ADR-0184](../docs/adr/0184-with-ai-explanations-off-nothing-is-sent-and-no-event-is-fired.md)).
9. Home Assistant's own updates, refined: **a newer release of a line inherits the line's age, with 2 days of its own**
   ([ADR-0179](../docs/adr/0179-a-newer-release-of-a-version-line-inherits-the-lines-age-with-two-days-of-its-own.md));
   **device firmware and radio wait at least 14 days**
   ([ADR-0180](../docs/adr/0180-device-firmware-and-the-radio-adapter-wait-at-least-14-days.md)), **install without a
   backup of Home Assistant**
   ([ADR-0181](../docs/adr/0181-device-firmware-and-the-radio-adapter-install-without-a-backup-of-home-assistant.md))
   and **get 2 hours to report their version**
   ([ADR-0183](../docs/adr/0183-device-firmware-and-the-radio-adapter-get-two-hours-to-report-their-version.md)); **the
   repair's fix installs at once**
   ([ADR-0182](../docs/adr/0182-the-repairs-fix-installs-a-failed-home-assistant-update-at-once.md)).

## 12. Result of the migration

After the migration (section 8), all entity IDs stand unchanged under the platform `vordr`; only
`sensor.vordr_hub_connection` is new, no MQTT devices remain. The display names follow the system language of HA; under
MQTT they were fixed German ([ADR-0075](../docs/adr/0075-display-names-follow-system-language.md)).
