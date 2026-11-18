# Vördr – Home Assistant integration

English · [Deutsch](README.de.md)

`custom_components/vordr` makes Home Assistant a client of the hub (`vordr-hub`) over `/v1/ha`
([design/ha-integration.md](../design/ha-integration.md)). The hub signs every job and approves hosts; only where its
`hub.yaml` sets `ha_approves_hosts` may a client with role `admin` approve a waiting host from Home Assistant
([ADR-0192](../docs/adr/0192-an-admin-home-assistant-may-approve-a-waiting-host-when-the-hub-allows-it.md)). The
integration shows the hub's views and requests commands.

## Setup

1. Through HACS, which Home Assistant shows as Marketplace: search for "Vördr", install it and restart Home
   Assistant. HACS shows new versions as an update. If "Vördr" is not listed, first add this repository as a custom
   repository (type Integration). Without HACS: copy `custom_components/vordr` to `/config/custom_components/vordr`
   and restart Home Assistant.
2. Add the integration "Vördr": address `wss://<hub>:8443/v1/ha`, the name of the client, and the CA certificate
   if you use your own CA. The integration checks reachability in stages (name, connection, TLS, endpoint, challenge)
   and names the one that fails.
3. It shows a fingerprint and a command; approve it at the hub:
   `vordr-hub approve-client <name> --fingerprint <fp>`. Then devices and entities appear. The dialog waits at most an
   hour and may be closed: adding the integration again with the same name continues the request with the same key,
   also after a restart of Home Assistant
   ([ADR-0156](../docs/adr/0156-the-config-flow-keeps-its-key-until-the-client-is-set-up.md)).

## What it creates

| Device | Entities |
|---|---|
| "Vördr-Leitstelle" or "Vördr hub" (system language) | `binary_sensor.vordr_hub_online`, `sensor.vordr_hub_hosts_online`, `_hosts_with_problem`, `_hosts_with_updates`, `_hosts_with_security_updates`, `_hosts_needing_reboot`, `_pending_approvals`, `_connection` (diagnostic) |
| "Vördr Home Assistant" | `sensor.vordr_ha_updates_ready` "Updates ready": Home Assistant's own updates that Vördr's rules let through now; `sensor.vordr_ha_updates_last_run` "Last run": what Vördr installed last and how it ended; `switch.vordr_ha_updates_paused` "Pause Home Assistant updates". Nothing installs until you switch it on (see below) |
| "Vördr <node>" per host | `binary_sensor.vordr_<id>_online`, `_problem`, `_reboot_required`; `sensor.vordr_<id>_last_report`, `_last_run`; with a package source `update.vordr_<id>_system`, `sensor.vordr_<id>_pending_updates`, `_security_updates`; buttons depending on capability (`_check`, `_install_security`, `_reboot`, `_agent_update`, `_install_app_<app>`); with `packages.containers` `update.vordr_<id>_containers` "Container images", reported; it installs only if agent.yaml allows Compose services (`image:<project>/<service>` in `install.allowed_scopes`), and then only the rebuild of their tag (a single service through the action `vordr.update_image`); "on" only while that install button would take something, so a newer tag or a service not allowed leaves it off; `sensor.vordr_<id>_outdated_images` "Outdated images" counts every outdated image, also those that stay manual (attributes `ready`, `installable`, `updates`); with `Debian` or `Alpine` in `osv.ecosystems` of the hub also known vulnerabilities in the images (attribute `vulnerable`; "on" only if a newer image is available to install, vulnerabilities alone do not turn it on; the summary says per image whether a newer image is ready; reports about the image's kernel do not count and are listed per image as `kernel`) |

Unknown stays unknown: a failed check makes `update.*_system` and the counters "unknown", never "up to date"; a host
without a reboot signal (container) has `reboot_required` "unknown", never "off".

**Is everything up to date?** The hub device counts over all hosts: `sensor.vordr_hub_hosts_with_updates` (system
updates, outdated images or pip/npm packages), `_hosts_with_security_updates` and `_hosts_needing_reboot`, each with the
attribute `hosts`. A host whose system check failed or that has not reported yet is listed in the attribute `unknown`
and is not counted; images and packages count where the host compares them. "0" with an empty `unknown` means every host
was checked and has nothing to update, e.g. as a template condition
`{{ is_state('sensor.vordr_hub_hosts_with_updates', '0') and not state_attr('sensor.vordr_hub_hosts_with_updates', 'unknown') }}`.

**For automations:**
- Event `vordr_problem` `{node, active, reason, reasons, check_failed_code, message}` for every new, changed or ended
  problem; after an HA restart once for each one that still exists.
- Event `vordr_run_finished` `{node, job_id, verb, scope, state, message, summary}` when a run ends; for a run that
  touched packages, `summary` says what it did (`package_count`, `packages`, `seconds`, `log_file` on the host,
  `outdated_services`), otherwise `{}`.
- `message` is the text of the repair hint in every shipped language, e.g. `{{ trigger.event.data.message.en }}`;
  empty if there is nothing to say (problem ended, run `ok`).
- One repair hint per host problem; it disappears with the problem.
- If a host needs a reboot, that shows up among the repair hints from the first report until it no longer needs one.
  Where Vördr may reboot, "Fix" starts the reboot (like the reboot button); otherwise the hint says to reboot by hand.
  After `reboot_pending_after_days` of the hub (default 7) it becomes an error and replaces the problem hint
  `reboot_pending`
  ([ADR-0132](../docs/adr/0132-a-needed-reboot-is-a-repair-from-the-first-report.md)).
- Outside a host's reboot window, the reboot button and "Fix" send nothing and say from when the reboot runs; the
  button shows the window and the time as attributes (`reboot_window`, `next_reboot`). On a host with
  `reboot.window_only` a reason does not help either; on any other the action `vordr.press_outside_window` with a
  reason reboots at once
  ([ADR-0187](../docs/adr/0187-the-agent-reports-its-reboot-window-and-home-assistant-says-when-a-reboot-can-run.md)).
- A failed install that the hub keeps as a problem until an install goes through shows up as the repair hint "install
  again" where Vördr may install: once the cause is fixed, "Fix" installs again, and a run that goes through clears it,
  even with nothing left to install
  ([ADR-0148](../docs/adr/0148-a-kept-failed-install-is-a-repair-that-installs-again.md)).
- A host waiting for approval at the hub shows up as a repair hint (a warning) with the fingerprint the hub saw and the
  command for the hub, `vordr-hub approve <node> --fingerprint <fp>`. Compare the fingerprint with
  `vordr-agent identity` on the host first. The hint goes once the host is approved, rejected or forgotten
  ([ADR-0157](../docs/adr/0157-a-waiting-host-is-a-repair-that-names-the-approval-command.md)). Where the hub's
  `hub.yaml` sets `ha_approves_hosts: true` and this Home Assistant has role `admin`
  (`vordr-hub client-role <name> --role admin`), the hub names `approve_host` in its status and the hint can be fixed
  here: "Fix" asks for the fingerprint `vordr-agent identity` printed on the host (spaces and upper case do not
  matter), refuses one that differs from the hub's without sending anything, and otherwise asks the hub to approve the
  host. Without that, Home Assistant cannot approve; rejecting and forgetting stay at the hub either way
  ([ADR-0192](../docs/adr/0192-an-admin-home-assistant-may-approve-a-waiting-host-when-the-hub-allows-it.md)).
- Vulnerable pip/npm packages make a problem only if someone can take the fix now: by button, by refreshing the
  dependencies or by hand. If a finding waits on others (a package or a project file pins the version, there is no fix
  yet, or it is younger than the minimum age), it appears only on the sensor `_vulnerable_packages`: per finding
  `reach` and `held_by`, plus `waiting`
  ([ADR-0129](../docs/adr/0129-findings-say-where-their-fix-is.md)).
- With a `kev:` section in the hub's `hub.yaml`, a finding whose CVE is in CISA's catalog of Known Exploited
  Vulnerabilities carries `known_exploited: true`, `kev_cves`, `kev_added` (when CISA added it) and, for use in
  ransomware campaigns, `kev_ransomware: true`; such findings come right after malware. `_vulnerable_packages` counts
  them per host in the attribute `known_exploited`, and `sensor.vordr_hub_vulnerability_data` shows the catalog's state
  in `kev` (`stale`: older than `stale_hours`, newer entries may be missing). The marks change no problem and no timing
  ([ADR-0168](../docs/adr/0168-known-exploitation-comes-from-the-kev-catalog-fetched-whole-at-the-hub.md)); an
  automation can use them, e.g. `{{ state_attr('sensor.vordr_<id>_vulnerable_packages', 'known_exploited') }}`.
- Event `vordr_ai_explanation` with the optional AI explanation of a failed run (see "AI explanations of failed
  runs").

## Home Assistant's own updates

Home Assistant can install its own updates – Core, OS, apps, HACS, ESPHome, device firmware – but without rules. Under
*Settings → Devices & services → Vördr → Configure → Home Assistant updates* you set Vördr's rules for them
([ADR-0158](../docs/adr/0158-home-assistants-own-updates-follow-vordrs-rules-in-the-integration.md)):

- **Allowed kinds** (Core, OS, Supervisor, apps, HACS, ESPHome, device firmware, radio adapter firmware, other) and
  single update entities. Nothing chosen, nothing allowed.
- **Window:** days, start and end in Home Assistant's time zone; past midnight it belongs to the day it starts on.
- **Minimum age:** 7 days by default (1–90), counted from when Vördr first saw the version, since Home Assistant gives
  no release date
  ([ADR-0159](../docs/adr/0159-the-minimum-age-of-a-home-assistant-update-counts-from-its-first-sight.md)). What is
  pending when this version of the integration first runs starts its age then. A newer release of the same line (the
  same first two numbers, such as 2026.11.2 after 2026.11.1) counts from when Vördr first saw the line, but must itself
  be 2 days old
  ([ADR-0179](../docs/adr/0179-a-newer-release-of-a-version-line-inherits-the-lines-age-with-two-days-of-its-own.md)).
  Device firmware and the radio adapter's firmware wait at least 14 days
  ([ADR-0180](../docs/adr/0180-device-firmware-and-the-radio-adapter-wait-at-least-14-days.md)).
- **Install automatically:** off by default. Until you switch it on, Vördr only plans.

**It installs only once you switch it on.** The sensor `sensor.vordr_ha_updates_ready` (device "Vördr Home
Assistant") counts the updates the rules let through now, also while installing is off. Its attributes say how many
there are per reason, and per update entity why not and from when (`updates`: `code`, `earliest`, `first_seen`). Never
touched: Vördr's own entities, a version skipped in Home Assistant (`skipped`), an update that installs itself
(`self_updating`), and a new major version, which stays
manual (`major`, [ADR-0160](../docs/adr/0160-a-new-major-version-of-a-home-assistant-update-stays-manual.md)). The
sensor works without the hub.

**Installing** ("Install automatically" in the same form, off by default; [ADR-0162](../docs/adr/0162-home-assistants-own-updates-install-only-when-switched-on-and-a-pause-holds-them.md)):

- One update at a time, inside the window, only what is `ready` at that moment: the plan is checked again right
  before each install. Other kinds first; Core, OS and Supervisor last, one of them per opening of the window, since
  they restart Home Assistant or the machine ([ADR-0164](../docs/adr/0164-home-assistant-updates-install-one-at-a-time-with-core-os-and-supervisor-last.md)).
- A backup before each: `update.install` with `backup` where the entity can back itself up (Core, OS, apps); for the
  others (HACS, ESPHome, the Supervisor) Home Assistant's last automatic backup must be at most 26 hours old,
  or Vördr makes one with `backup.create_automatic`. No backup, no install ([ADR-0163](../docs/adr/0163-a-backup-before-every-automatic-home-assistant-update.md)).
  Device firmware and the radio adapter need none: a backup of Home Assistant cannot roll a device back
  ([ADR-0181](../docs/adr/0181-device-firmware-and-the-radio-adapter-install-without-a-backup-of-home-assistant.md)).
- The switch `switch.vordr_ha_updates_paused` "Pause Home Assistant updates" holds every install that has not started
  yet, also across restarts. Only one Vördr entry (one per hub) may install.
- After the install Vördr waits until the entity reports the new version: 30 minutes, device firmware and the radio
  adapter 2 hours
  ([ADR-0183](../docs/adr/0183-device-firmware-and-the-radio-adapter-get-two-hours-to-report-their-version.md)). A Core
  or OS update restarts Home Assistant: the run is kept, and its outcome is checked first after the restart.
- The sensor `sensor.vordr_ha_updates_last_run` "Last run" shows the phase, then the result (`ok`, `failed`,
  `not_reached`, `timeout`, `backup_failed`, `skipped_paused`, `skipped_not_ready`), the versions, the backup and what
  the installer does now (`installer`), with the ready updates in their order (`queue`).
- Event `vordr_ha_update_finished` `{entity_id, kind, from_version, to_version, installed_version, result, reason,
  backup, started, finished, restarted}` when a run ends.
- A failed install (`failed`, `not_reached`, `timeout`) is a repair hint and is not tried again until a newer version
  comes, until you install it by hand, or until you confirm the hint: then Vördr installs it at once, outside the
  window but under all its other rules. If another install is running, installing is paused or off, or a rule stops it,
  the hint says why and nothing is queued
  ([ADR-0182](../docs/adr/0182-the-repairs-fix-installs-a-failed-home-assistant-update-at-once.md)). The hint goes
  once an install of it goes through ([ADR-0165](../docs/adr/0165-a-failed-home-assistant-update-stays-until-an-install-of-it-goes-through.md)). A failed backup is a hint of its own until Home Assistant has a
  newer automatic backup.
- Not yet: one device of a model first before the others of that model (a later change).

## Installing the agent on a new Linux host

Home Assistant can set up a new host itself: it reaches it once over SSH, has it fetch the signed agent release from
the hub, checks it, and runs `vordr-agent install` there. Nothing of the SSH access is stored
([ADR-0161](../docs/adr/0161-home-assistant-installs-the-agent-once-over-ssh-and-keeps-no-credentials.md)), and
neither is the host: the hub keeps the list of hosts, and the host shows in Home Assistant once it is approved there
([ADR-0185](../docs/adr/0185-the-agent-install-starts-from-an-add-host-button-and-stores-no-subentry.md)). For
Linux with systemd and apt (Debian, Ubuntu, Raspberry Pi OS) on x86_64, aarch64 or armv7.

What it needs:

- a hub that hands out the agent release (its status names `agent_download`), with a signed release on it
  (`deploy/README.md`, "Signing agent releases");
- this Home Assistant approved at the hub with role `admin`: `vordr-hub client-role <name> --role admin`;
- the operator key (and its backup, if any), entered once under *Settings → Devices & services → Vördr → Configure →
  Operator keys*, as `vordr-release public-key` prints them. Home Assistant checks every release against them and
  never takes a key from the hub;
- on the host: SSH, `root` or a user with `sudo`, `curl` or `wget`, and the hub reachable at the address this
  integration uses;
- optionally, for the approval in the same dialog: `ha_approves_hosts: true` in the hub's `hub.yaml`.

Then press *Add host* on the integration's page (*Settings → Devices & services → Vördr*; with several hubs, at the
entry of the hub the host should join). If something it needs is missing, the dialog says what before it asks for
anything: the integration not running, no operator key, the hub not connected or not handing out the release, the SSH
library missing. Otherwise:

1. host, SSH port, user and node name (empty: the host's own name);
2. the host key the server presents: compare it with the host's own on its console
   (the dialog names the file, e.g. `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub`; it asks for ED25519 first) and
   confirm only if they are the same;
3. a password or a private key (with its passphrase), and sudo's password if it differs from the login password. Use a
   key or a password made for this if you can: Home Assistant holds the login's password and key in memory until it is
   logged in, sudo's password until the install starts, and stores them nowhere. Closing the dialog while it logs in
   stops everything: nothing runs on the host;
4. Home Assistant checks the host (Linux, processor, systemd, apt, the tools, sudo), fetches the release, checks its
   signature (and that it is not older than one an earlier install took), and the host, as a service of its own
   (`systemd-run`) that finishes even if the connection ends, downloads the binary into a private temporary folder,
   compares its size and SHA-256 with the signed manifest and only then runs `vordr-agent install`. The folder goes in
   every case.

At the end the dialog shows the agent's fingerprint. Where the hub names `approve_host` (above), the dialog first waits
up to a minute for the hub to list the new host as waiting with exactly that fingerprint (under the node name you
entered, if you entered one), and then approves it: the
fingerprint came over the SSH connection whose host key you confirmed, so the comparison is made by construction
([ADR-0192](../docs/adr/0192-an-admin-home-assistant-may-approve-a-waiting-host-when-the-hub-allows-it.md)). Its
end then says that the host is approved, or, if the hub refused, its code. Otherwise, or if the host does not show up
in time, the host waits for approval at the hub; as soon as the hub lists it, the repair "Host … waits for approval"
shows the command. Compare the fingerprint, then approve at the hub (or fix the repair, where the hub allows it); the
host then shows as a device of the integration. The button stores no entry of its own for the host, so the
integration's page lists none. Every other end names what happened on the host and what to do next. A host with a policy of its own is still set up
by hand (`deploy/README.md`); the agent installed this way checks and reports, and installs nothing until its
`agent.yaml` allows it.

## AI explanations of failed runs

Optional and off by default
([ADR-0166](../docs/adr/0166-optional-ai-explanations-of-failed-runs-go-through-home-assistants-ai-task-and-only-advise.md)).
When a run on a host fails, an AI Task entity you choose explains it: a few sentences in Home Assistant's language, a
likely cause and up to three next steps from fixed lists, and a quote from the run's log excerpt as evidence. It only
advises: it never changes windows, minimum ages, scopes, buttons or repairs, and the hub knows nothing of it.

It needs an AI Task entity that can generate data, from any integration that offers one (a local model or a cloud
service). Then *Settings → Devices & services → Vördr → Configure → AI explanations of failed runs*: choose the entity
and confirm that package names and log excerpts of failed runs may go to its provider. Empty the entity to switch it
off.

**What goes to the provider**, per failed run: the result code, the action, the package names and the end of the log
excerpt (at most 2000 characters). Before that, the hub's address, the names of the hosts the hub knows (with their
domain), names in the hub's domain, the server of every URL with its user and password, and IP addresses are replaced
by placeholders; the other words of the excerpt go as they are. A provider that is a cloud service sees them, also
behind an integration for local models: check where the entity sends its data.

**Which runs:** failures whose log tells the cause (package manager, check after the update, verification, disk space,
package source, backup, timeout, interruption). Not: runs that ended `ok`, refusals, the decisions of Vördr's rules
(too new, flagged, not from the registry) and security signals (a hash, certificate or host key that does not match):
an AI does not explain those away.

**The result:**

- Event `vordr_ai_explanation` `{node, job_id, state, code, entity_id}`; with `code` `ok` also `likely_cause`,
  `next_steps`, `explanation` and `quote`. While switched on it comes for every such run, also when no answer can be
  had, so an automation that waits for it does not wait in vain; a push can add `{{ trigger.event.data.explanation }}`
  when `code` is `ok`. While off, nothing is sent and no event comes
  ([ADR-0184](../docs/adr/0184-with-ai-explanations-off-nothing-is-sent-and-no-event-is-fired.md)).
- The host's sensor "Last run" shows `ai_code`, `ai_entity_id` and with `ok` the fields (`ai_likely_cause`,
  `ai_next_steps`, `ai_explanation`, `ai_quote`) while it shows that run; out of the recorder.
- `code`: `ok`; `unverified` (the quote is not in the log as it was sent: nothing of the answer is shown);
  `invalid_answer` (a field missing or a value not in its list); `no_answer` (none within 60 seconds, or the provider
  failed); `no_excerpt` (the run has no log excerpt); `unsupported` (the entity cannot generate data, or AI Task is not
  set up); `entity_missing` (the entity is gone or unavailable); `not_configured` (the stored options are not valid).

Only `likely_cause` and `next_steps` come from fixed lists: base an automation on those, never on the text. The
explanation is AI text, not checked beyond its quote being in the log, and a log can be crafted to make a model say
anything. One question at a time, each run once; the same failure on several hosts is asked about once.

## Blueprints

Four automation blueprints in [`blueprints/automation/vordr/`](../blueprints/automation/vordr/) turn the events and
entities above into notifications
([ADR-0173](../docs/adr/0173-notifications-are-shipped-as-blueprints-the-user-imports.md),
[design](../design/ha-integration.md)). They only notify; none installs or reboots by itself.

- `problem.yaml`: a host's problem begins or changes, with reason and next step; optional wait, ignored reasons, a note
  when it ends.
- `run_failed.yaml`: a run ends with a result other than `ok`.
- `reboot_offer.yaml`: a host needs a reboot; where Vördr may reboot it, the notification has the action "Reboot now",
  which presses the host's reboot button when you tap it (Companion app); while the host's reboot window is closed,
  the tap says from when instead.
- `hub_offline.yaml`: the hub's online entity is off for N minutes.

Import: *Settings → Automations & scenes → Blueprints → Import blueprint*, with the address of the file in the
repository, for example `https://github.com/<owner>/<repo>/blob/main/blueprints/automation/vordr/problem.yaml`
([Home Assistant: using blueprints](https://www.home-assistant.io/docs/automation/using_blueprints/)); or copy the
files to `/config/blueprints/automation/vordr/`. Then create an automation from the blueprint and pick the notify
service, for example `notify.mobile_app_<device>`. Home Assistant needs 2024.10 or newer for them.

## Languages

The hub sends only codes and numbers; the texts live in `translations/*.json` (de, en; `strings.json` = `en.json`).
HA decides which language applies where:

| What | Language |
|---|---|
| Setup and options, errors, states (`last_run`, `hub_connection`, `ha_updates_last_run`), attribute values of `_problem` (`reason`, `next_step`, `check_failed_code`) and of `_last_run` (`ai_code`, `ai_likely_cause`, `ai_next_steps`), the reasons of `ha_updates_ready`, repair hints | the language in the profile of the person reading |
| The text of an AI explanation (`explanation`) | the system language of HA, which the model is asked to write in |
| Entity and device names, `latest_version` and summary of the update entity | the system language of HA (HA sets names at startup) |
| `message` in the events | all shipped languages; the automation chooses |

Another language is another file in `translations/`; `tests/test_texts.py` checks that every file has every text.

## Tests

The code is in `custom_components/vordr/` at the root of the repository, where HACS looks for it; this folder holds
the tests and their requirements ([ADR-0144](../docs/adr/0144-the-integration-lives-at-the-repository-root.md)). On
Linux with `pytest-homeassistant-custom-component`; which HA version the tests use is set by the pinned versions in
`requirements_test.txt`. Run from the root of the repository:

```bash
uv venv --python 3.14 ~/vordr-ha-venv
uv pip install --python ~/vordr-ha-venv/bin/python -r ha/requirements_test.txt
~/vordr-ha-venv/bin/ruff check custom_components ha && ~/vordr-ha-venv/bin/ruff format --check custom_components ha \
  && ~/vordr-ha-venv/bin/python -m pytest -q
```

`tests/test_e2e_real_hub.py` starts the real binaries from `../dist` (run `./build.sh` first): the hub, an agent that
checks this machine, and the integration – approval by CLI, button through to signed job, revocation.
