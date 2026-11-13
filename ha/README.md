# Vördr – Home Assistant integration

English · [Deutsch](README.de.md)

`custom_components/vordr` makes Home Assistant a client of the hub (`vordr-hub`) over `/v1/ha`
([design/ha-integration.md](../design/ha-integration.md)). The hub stays the only place that approves hosts and signs
jobs; the integration shows its views and requests commands.

## Setup

1. Through HACS: add this repository as a custom repository (type Integration), install "Vördr", restart Home
   Assistant. HACS shows new versions as an update. Without HACS: copy `custom_components/vordr` to
   `/config/custom_components/vordr` and restart Home Assistant.
2. Add the integration "Vördr": address `wss://<hub>:8443/v1/ha`, the name of the client, and the CA certificate
   if you use your own CA. The integration checks reachability in stages (name, connection, TLS, endpoint, challenge)
   and names the one that fails.
3. It shows a fingerprint and a command; approve it at the hub:
   `vordr-hub approve-client <name> --fingerprint <fp>`. Then devices and entities appear.

## What it creates

| Device | Entities |
|---|---|
| "Vördr-Leitstelle" or "Vördr hub" (system language) | `binary_sensor.vordr_hub_online`, `sensor.vordr_hub_hosts_online`, `_hosts_with_problem`, `_pending_approvals`, `_connection` (diagnostic) |
| "Vördr <node>" per host | `binary_sensor.vordr_<id>_online`, `_problem`, `_reboot_required`; `sensor.vordr_<id>_last_report`, `_last_run`; with a package source `update.vordr_<id>_system`, `sensor.vordr_<id>_pending_updates`, `_security_updates`; buttons depending on capability (`_check`, `_install_security`, `_reboot`, `_agent_update`, `_install_app_<app>`); with `packages.containers` `update.vordr_<id>_containers` "Container images", reported; it installs only if agent.yaml allows Compose services (`image:<project>/<service>` in `install.allowed_scopes`), and then only the rebuild of their tag (a single service through the action `vordr.update_image`); "on" only while that install button would take something, so a newer tag or a service not allowed leaves it off; `sensor.vordr_<id>_outdated_images` "Outdated images" counts every outdated image, also those that stay manual (attributes `ready`, `installable`, `updates`); with `Debian` or `Alpine` in `osv.ecosystems` of the hub also known vulnerabilities in the images (attribute `vulnerable`; "on" only if a newer image is available to install, vulnerabilities alone do not turn it on; the summary says per image whether a newer image is ready; reports about the image's kernel do not count and are listed per image as `kernel`) |

Unknown stays unknown: a failed check makes `update.*_system` and the counters "unknown", never "up to date"; a host
without a reboot signal (container) has `reboot_required` "unknown", never "off".

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
- A failed install that the hub keeps as a problem until an install goes through shows up as the repair hint "install
  again" where Vördr may install: once the cause is fixed, "Fix" installs again, and a run that goes through clears it,
  even with nothing left to install
  ([ADR-0148](../docs/adr/0148-a-kept-failed-install-is-a-repair-that-installs-again.md)).
- Vulnerable pip/npm packages make a problem only if someone can take the fix now: by button, by refreshing the
  dependencies or by hand. If a finding waits on others (a package or a project file pins the version, there is no fix
  yet, or it is younger than the minimum age), it appears only on the sensor `_vulnerable_packages`: per finding
  `reach` and `held_by`, plus `waiting`
  ([ADR-0129](../docs/adr/0129-findings-say-where-their-fix-is.md)).

## Languages

The hub sends only codes and numbers; the texts live in `translations/*.json` (de, en; `strings.json` = `en.json`).
HA decides which language applies where:

| What | Language |
|---|---|
| Setup, errors, states (`last_run`, `hub_connection`), attribute values of `_problem` (`reason`, `next_step`, `check_failed_code`), repair hints | the language in the profile of the person reading |
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
