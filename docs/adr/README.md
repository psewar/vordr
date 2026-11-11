# Architecture Decision Records

Why Vördr is built the way it is. The design documents in `design/` describe *how* it works; the ADRs here say *why*.
Format: [MADR 4.0](https://adr.github.io/madr/), in English.

## Rules

- One decision per ADR: `docs/adr/NNNN-title-in-kebab-case.md`, numbered from `0001` without gaps.
- Front matter: `status`, `date` (the day of the decision), `decision-makers`.
- Status: `proposed` → `accepted` or `rejected`; later `deprecated` or `superseded by ADR-NNNN`.
- Accepted ADRs are not rewritten. A changed decision gets a new ADR that supersedes the old one; the old one only
  changes its status. Both link to each other. Typos and links may be fixed, and site details redacted
  ([ADR-0099](0099-the-repository-is-site-neutral-checked-by-the-build.md)): the decision and its reasons stay.
- The ADR comes in the same PR as the change, or before it as `proposed`. The PR description names it.
- ADRs 0001–0063 were backfilled on 2026-10-30 from the design documents, the history of pull requests and the
  operator's notes. Each says when it was decided and from which source.
- ADRs 0064–0071 came with the pip/npm package work, first in German under other numbers; they were renumbered,
  translated to English and set to `accepted` with their merge; each says so at the end.
- A decision about one site's own operation carries `scope: site` in its front matter and is published only as a stub
  ([ADR-0133](0133-site-decisions-stay-with-the-site.md)).

## Numbers on branches

A branch that adds ADRs takes the next free numbers of `main`. If two branches are open at the same time, the one that
is merged later renumbers its ADRs to the next free numbers before its merge: file names, this index, cross-links and
`superseded by` lines. Placeholder numbers (for example `01xx`) never reach `main`.

## Index

Generated from the files by `python3 docs/adr/check_index.py --write`. `build.sh` runs the same script without
`--write` and stops when the index, the numbers, the front matter or a supersede link is off
([ADR-0072](0072-decisions-as-adrs-checked-by-the-build.md)).

<!-- adr-index:begin -->
| ADR | Title | Status | Date |
|---|---|---|---|
| [0001](0001-own-agent-instead-of-patchmon.md) | Own update agent instead of PatchMon | accepted | 2026-10-28 |
| [0002](0002-own-project-following-an-earlier-agent.md) | Own project following the pattern of an earlier agent | accepted | 2026-10-28 |
| [0003](0003-go.md) | Go for agent and hub | accepted | 2026-10-28 |
| [0004](0004-name-vordr.md) | The name Vördr | accepted | 2026-10-28 |
| [0005](0005-closed-verbs-without-shell.md) | Closed set of verbs, no shell | accepted | 2026-10-28 |
| [0006](0006-local-policy-decides.md) | The local policy on the host has the last word | superseded by ADR-0087 | 2026-10-28 |
| [0007](0007-fixed-codes-instead-of-error-texts.md) | Fixed codes instead of error texts | accepted | 2026-10-28 |
| [0008](0008-hub-writes-ha-texts.md) | The hub writes the texts for Home Assistant, in German | superseded by ADR-0027 | 2026-10-28 |
| [0009](0009-ha-via-mqtt-discovery.md) | Home Assistant via MQTT discovery from the hub, agents without MQTT | superseded by ADR-0023 | 2026-10-28 |
| [0010](0010-administration-only-local.md) | Administration and approval only locally at the hub | accepted | 2026-10-28 |
| [0011](0011-state-in-files.md) | Hub state in files, no database | accepted | 2026-10-28 |
| [0012](0012-copy-parts-of-the-earlier-agent.md) | Copy parts of the earlier agent instead of a shared library | accepted | 2026-10-28 |
| [0013](0013-scope-limits.md) | Scope limits: what Vördr does not do, and who gets no agent | accepted | 2026-10-28 |
| [0014](0014-stale-after-seven-days.md) | "Stale" as a catch-all state after 7 days | accepted | 2026-10-28 |
| [0015](0015-windows-service-localsystem.md) | Windows agent as a service under LocalSystem | accepted | 2026-10-28 |
| [0016](0016-site-decision.md) | A decision about the operation of one site | accepted | 2026-10-28 |
| [0017](0017-entity-ids-with-prefix.md) | Entity IDs with the prefix `vordr_`, unchanged across every rebuild | accepted | 2026-10-28 |
| [0018](0018-agent-root-hub-own-user.md) | Agent as root, hub as its own user with tight sandboxing | accepted | 2026-10-28 |
| [0019](0019-site-decision.md) | A decision about the operation of one site | superseded by ADR-0054 | 2026-10-28 |
| [0020](0020-certificate-from-lets-encrypt.md) | TLS certificate of the hub from Let's Encrypt, not from an internal PKI | accepted | 2026-10-28 |
| [0021](0021-device-name-vordr-node.md) | Device name `Vördr <node>` | accepted | 2026-10-29 |
| [0022](0022-hub-stays-own-service.md) | The hub stays a service of its own and keeps the signing key | accepted | 2026-10-29 |
| [0023](0023-own-ha-integration.md) | Own HA integration as an approved client over `/v1/ha` instead of MQTT | accepted | 2026-10-29 |
| [0024](0024-repair-issues.md) | One repair issue per host problem | accepted | 2026-10-29 |
| [0025](0025-distribution-via-deploy-tool.md) | Ship the integration with a deploy tool, not via HACS | superseded by ADR-0136 | 2026-10-29 |
| [0026](0026-remove-mqtt.md) | Remove MQTT from the code after the switch | accepted | 2026-10-29 |
| [0027](0027-protocol-2-codes-and-translations.md) | Protocol 2: the hub sends codes, the integration writes the texts | accepted | 2026-10-29 |
| [0028](0028-site-decision.md) | A decision about the operation of one site | accepted | 2026-10-29 |
| [0029](0029-runs-survive-disconnects.md) | A run does not belong to its connection, and dpkg is never stopped halfway | accepted | 2026-10-29 |
| [0030](0030-backup-required.md) | A fresh backup before every install, checked at the hub | accepted | 2026-10-29 |
| [0031](0031-install-mode-full-upgrade.md) | `install.mode`: `full-upgrade` selectable per host, not the default | superseded by ADR-0076 | 2026-10-29 |
| [0032](0032-self-update-signed-by-operator.md) | Self-update: signed by the operator, distributed by the hub, in rings | accepted | 2026-10-29 |
| [0033](0033-one-host-as-the-canary.md) | One host as the canary | superseded by ADR-0058 | 2026-10-29 |
| [0034](0034-only-newer-releases.md) | Agents only take newer releases | accepted | 2026-10-29 |
| [0035](0035-backup-operator-key.md) | Backup operator key, secret part only with the operator | accepted | 2026-10-29 |
| [0036](0036-automatic-install-in-rings.md) | The hub starts installs by itself, in rings | accepted | 2026-10-29 |
| [0037](0037-auto-install-uses-default-scope.md) | Automatic runs use `default_scope` | superseded by ADR-0059 | 2026-10-29 |
| [0038](0038-health-checks-after-install.md) | Health checks after an install (`health:`), result `failed_health` | accepted | 2026-10-29 |
| [0039](0039-ha-client-roles.md) | Roles for Home Assistant clients: observe, operate, admin | accepted | 2026-10-29 |
| [0040](0040-brand-rune-shield.md) | Brand: the rune shield with Algiz | accepted | 2026-10-29 |
| [0041](0041-reboot-with-window-and-proof.md) | Reboot on request, only inside the window, with proof | superseded by ADR-0087 | 2026-10-29 |
| [0042](0042-replaced-packages-are-no-removal.md) | A package the run replaces is no removal | accepted | 2026-10-29 |
| [0043](0043-queue-per-host.md) | Commands for a busy host wait in a queue at the hub | accepted | 2026-10-29 |
| [0044](0044-same-command-joins-running-run.md) | The same command as the running run joins it instead of waiting | accepted | 2026-10-29 |
| [0045](0045-site-decision.md) | A decision about the operation of one site | accepted | 2026-10-29 |
| [0046](0046-site-decision.md) | A decision about the operation of one site | superseded by ADR-0078 | 2026-10-29 |
| [0047](0047-site-decision.md) | A decision about the operation of one site | accepted | 2026-10-29 |
| [0048](0048-winget-before-windows-update.md) | On Windows, winget first; Windows Update later | accepted | 2026-10-29 |
| [0049](0049-site-decision.md) | A decision about the operation of one site | accepted | 2026-10-30 |
| [0050](0050-winget-via-powershell-module.md) | winget through Microsoft's PowerShell module, not the CLI table | accepted | 2026-10-30 |
| [0051](0051-sleep-is-no-outage.md) | Sleep is no outage: a PC announces sleep and shutdown | accepted | 2026-10-30 |
| [0052](0052-major-version-jumps.md) | Mark major version jumps from winget in Home Assistant | accepted | 2026-10-30 |
| [0053](0053-powershell-7-for-the-service.md) | PowerShell 7 for the Windows service | accepted | 2026-10-30 |
| [0054](0054-hub-in-own-container.md) | The hub in a container of its own | accepted | 2026-10-30 |
| [0055](0055-files-copied-no-secret-access.md) | Certificate and PBS token are copied in; the hub container has no secret access | accepted | 2026-10-30 |
| [0056](0056-firewall-8443.md) | Port 8443 of the hub only for known senders | accepted | 2026-10-30 |
| [0057](0057-site-decision.md) | A decision about the operation of one site | accepted | 2026-10-30 |
| [0058](0058-site-decision.md) | A decision about the operation of one site | superseded by ADR-0080 | 2026-10-30 |
| [0059](0059-own-auto-scope.md) | Automatic runs have their own scope (`install.auto_scope`) | accepted | 2026-10-30 |
| [0060](0060-event-log-off-host.md) | The event log also leaves the hub: file copy and syslog | accepted | 2026-10-30 |
| [0061](0061-restart-containers-after-install.md) | The agent starts containers again after an install | accepted | 2026-10-30 |
| [0062](0062-docker-live-restore.md) | Docker `live-restore` to keep containers running during Docker updates | rejected | 2026-10-30 |
| [0063](0063-winget-skip-unknown-versions.md) | winget: leave out packages with an unknown installed version | accepted | 2026-10-30 |
| [0064](0064-osv-data-local-at-the-hub.md) | OSV data held locally at the hub instead of querying api.osv.dev | accepted | 2026-10-30 |
| [0065](0065-scope-of-package-locations.md) | Scope of package locations: global, Node-RED, projects and venvs; containers later | accepted | 2026-10-30 |
| [0066](0066-inventory-from-metadata.md) | Read the inventory from metadata, start no package manager | accepted | 2026-10-30 |
| [0067](0067-hub-announces-features.md) | New message types only after a feature the hub announces | accepted | 2026-10-30 |
| [0068](0068-minimum-age-for-target-versions.md) | Minimum age for the target versions of pip and npm updates | accepted | 2026-10-30 |
| [0069](0069-package-updates-by-button-only.md) | pip and npm updates only by button, never automatically | accepted | 2026-10-30 |
| [0070](0070-hub-vets-and-signs-the-update-plan.md) | The hub vets the whole update plan and signs its answer | accepted | 2026-10-30 |
| [0071](0071-no-code-during-install.md) | No code during install: npm without scripts, pip wheels only | accepted | 2026-10-30 |
| [0072](0072-decisions-as-adrs-checked-by-the-build.md) | Decisions as ADRs in English, with the index checked by the build | accepted | 2026-10-30 |
| [0073](0073-soft-memory-limit-for-the-hub.md) | Soft memory limit for the hub (GOMEMLIMIT), no hard cap | accepted | 2026-10-30 |
| [0074](0074-refuse-auto-local-until-built.md) | `install.auto: local` is refused until it is built | accepted | 2026-10-30 |
| [0075](0075-display-names-follow-system-language.md) | Display names follow Home Assistant's system language | accepted | 2026-10-30 |
| [0076](0076-full-upgrade-by-default.md) | `full-upgrade` is the default for scope `all` | accepted | 2026-10-30 |
| [0077](0077-hold-docker-packages.md) | Hold Docker packages back during updates | rejected | 2026-10-30 |
| [0078](0078-site-decision.md) | A decision about the operation of one site | accepted | 2026-10-30 |
| [0079](0079-detect-packages-in-msix-app-containers.md) | Detect pip and npm packages inside MSIX app containers | accepted | 2026-10-30 |
| [0080](0080-security-updates-everywhere-unattended-upgrades-off.md) | Automatic security updates on every Linux host, unattended-upgrades off | superseded by ADR-0088 | 2026-10-30 |
| [0081](0081-container-images-own-source.md) | Container images: Vördr's own source, updates2mqtt rejected | accepted | 2026-10-30 |
| [0082](0082-repo-stays-private-for-now.md) | The repo stays private for now; publishing needs testing and a review first | accepted | 2026-10-30 |
| [0083](0083-snapshot-before-install.md) | A snapshot of the guest before every install | rejected | 2026-10-30 |
| [0084](0084-windows-self-update-without-code-signing.md) | Self-update on Windows, without code signing | accepted | 2026-10-30 |
| [0085](0085-keep-osv-data-when-a-dump-is-implausible.md) | Keep the OSV data when a new dump is empty or far smaller | accepted | 2026-10-30 |
| [0086](0086-windows-update-source.md) | Windows Update as a source on Windows, installed by button together with winget | accepted | 2026-10-30 |
| [0087](0087-runs-outside-the-window-with-a-reason.md) | A person may start a run outside the window, with a reason | superseded by ADR-0109 | 2026-10-30 |
| [0088](0088-hypervisor-updates-by-button-only.md) | The hypervisor gets updates by button only, and never a reboot through Vördr | superseded by ADR-0108 | 2026-10-30 |
| [0089](0089-appliances-through-a-proxy-agent.md) | Appliances through a proxy agent, not through the hub | accepted | 2026-10-30 |
| [0090](0090-appliance-installs-need-a-fresh-config-backup.md) | Appliance installs need a fresh configuration backup, taken and checked by the proxy | accepted | 2026-10-30 |
| [0091](0091-firmware-for-devices-without-internet.md) | Firmware for devices without internet: the proxy fetches it, otherwise a short, narrow opening | accepted | 2026-10-30 |
| [0092](0092-mesh-keeps-its-vendor-self-update.md) | A mesh Wi-Fi keeps its vendor self-update; Vördr reports and backs up | accepted | 2026-10-30 |
| [0093](0093-firewall-command-unprivileged-root-job-works-a-checked-queue.md) | The firewall command stays unprivileged; a root job works a checked queue | accepted | 2026-10-31 |
| [0094](0094-ssh-drivers-use-the-system-openssh-client.md) | SSH drivers use the system's OpenSSH client with a pinned host key | accepted | 2026-10-31 |
| [0095](0095-install-mode-fact-reports-the-mode-the-host-runs.md) | The fact `install_mode` reports the mode the host runs | accepted | 2026-10-31 |
| [0096](0096-fresh-backup-right-before-the-install.md) | A fresh backup right before every install of a hypervisor guest, made by the hypervisor's agent | accepted | 2026-10-31 |
| [0097](0097-synology-active-backup-for-business-for-backups-before-installs.md) | Synology Active Backup for Business for the backup before installs | rejected | 2026-10-31 |
| [0098](0098-update-guard-rolls-back-a-hung-new-agent.md) | An update guard outside the new binary rolls back a new agent that hangs | accepted | 2026-10-31 |
| [0099](0099-the-repository-is-site-neutral-checked-by-the-build.md) | The repository is site-neutral, checked by the build | accepted | 2026-11-01 |
| [0100](0100-publish-as-a-new-repository-with-one-start-commit.md) | Publish as a new repository with one start commit | accepted | 2026-11-01 |
| [0101](0101-package-lists-stay-out-of-the-recorder.md) | Package lists stay in the state but out of the recorder | accepted | 2026-11-02 |
| [0102](0102-offer-an-agent-release-only-where-it-has-a-binary.md) | Offer an agent release only to hosts it has a binary for | accepted | 2026-11-02 |
| [0103](0103-windows-service-restarts-cleanly-after-a-self-update.md) | The Windows service stops cleanly after a self-update, and the update guard starts it again | accepted | 2026-11-02 |
| [0104](0104-refresh-indirect-dependencies-within-their-ranges.md) | Refresh indirect dependencies with findings within the ranges their dependents allow | accepted | 2026-11-02 |
| [0105](0105-global-npm-plan-leaves-out-what-is-installed.md) | The plan of a global npm update leaves out versions the location already has | accepted | 2026-11-03 |
| [0106](0106-update-entity-on-only-when-vordr-would-install.md) | An update entity is on only when Vördr would install something | accepted | 2026-11-04 |
| [0107](0107-ring-delay-per-update-ended-by-a-clean-canary-run.md) | The ring delay counts per update, and a clean canary run ends it | accepted | 2026-11-04 |
| [0108](0108-automatic-installs-take-every-update-on-linux-guests.md) | Automatic installs take every pending update on Linux guests, not only security updates | accepted | 2026-11-04 |
| [0109](0109-the-install-button-installs-at-once.md) | The install button in Home Assistant installs at once, also outside the window | accepted | 2026-11-04 |
| [0110](0110-the-hub-checks-the-minimum-interval-before-the-backup.md) | The hub checks the minimum interval before it has a guest backed up | proposed | 2026-11-04 |
| [0111](0111-shorter-minimum-age-for-security-fixes.md) | A shorter minimum age for releases that fix a known vulnerability | proposed | 2026-11-04 |
| [0112](0112-windows-time-zone-through-the-cldr-table.md) | The Windows agent reports its time zone through the CLDR table | proposed | 2026-11-05 |
| [0113](0113-report-outdated-container-images.md) | Report outdated container images: same tag rebuilt, or a newer tag of the same form | accepted | 2026-11-05 |
| [0114](0114-agent-names-its-run-and-the-hub-acknowledges-results.md) | The agent names its running job in the hello, and the hub acknowledges final results | accepted | 2026-11-05 |
| [0115](0115-recognize-images-compose-built-on-the-host.md) | Recognize images Compose built on the host | accepted | 2026-11-05 |
| [0116](0116-security-runs-stay-within-security-archives-and-the-release.md) | Security runs stay within the security archives and the host's release | accepted | 2026-11-05 |
| [0117](0117-compare-pinned-images-under-their-tag.md) | Compare pinned images under their tag | accepted | 2026-11-05 |
| [0118](0118-a-canary-run-meets-only-what-it-installed.md) | A canary run meets only what it installed | accepted | 2026-11-05 |
| [0119](0119-update-container-images-by-button.md) | Update container images by button | accepted | 2026-11-05 |
| [0120](0120-known-vulnerabilities-inside-container-images.md) | Known vulnerabilities inside container images | accepted | 2026-11-05 |
| [0121](0121-read-tag-lists-of-up-to-60000-tags.md) | Read tag lists of up to 60000 tags | accepted | 2026-11-06 |
| [0122](0122-kernel-advisories-in-images-do-not-count.md) | The kernel's advisories in images do not count | accepted | 2026-11-06 |
| [0123](0123-stopped-containers-of-the-project-do-not-share-the-tag.md) | Stopped containers of the same Compose project do not share the tag | accepted | 2026-11-06 |
| [0124](0124-a-failed-system-install-stays-until-one-goes-through.md) | A failed system install stays until one goes through | accepted | 2026-11-06 |
| [0125](0125-bring-back-containers-after-an-interrupted-run.md) | Bring back containers after an interrupted run | accepted | 2026-11-06 |
| [0126](0126-say-why-an-image-comparison-got-no-answer.md) | Say why an image comparison got no answer | accepted | 2026-11-06 |
| [0127](0127-count-only-what-the-upgrade-mode-installs.md) | Count only what the upgrade mode installs | accepted | 2026-11-06 |
| [0128](0128-check-npm-tarball-origins-and-let-npm-12-fetch-them.md) | Check npm tarball origins and let npm 12 fetch them | accepted | 2026-11-06 |
| [0129](0129-findings-say-where-their-fix-is.md) | Findings say where their fix is | accepted | 2026-11-06 |
| [0130](0130-image-entity-needs-attention-only-with-a-newer-image.md) | The image entity needs attention only when a newer image exists | accepted | 2026-11-07 |
| [0131](0131-name-the-newest-tag-of-the-running-major-version.md) | Name the newest tag of the running major version beside a new one | accepted | 2026-11-07 |
| [0132](0132-a-needed-reboot-is-a-repair-from-the-first-report.md) | A needed reboot is a repair from the first report | accepted | 2026-11-08 |
| [0133](0133-site-decisions-stay-with-the-site.md) | Decisions about one site's operation stay with that site; the public tree carries a stub | accepted | 2026-11-11 |
| [0134](0134-the-neutral-check-also-stops-operations-wording.md) | The neutral check also stops the wording of an operations log | accepted | 2026-11-11 |
| [0135](0135-mit-license.md) | MIT license | accepted | 2026-11-11 |
| [0136](0136-integration-via-hacs-as-a-zip-release.md) | The integration ships through HACS as a zip release of the public repository | superseded by ADR-0144 | 2026-11-11 |
| [0137](0137-checks-run-in-github-actions.md) | The checks run in GitHub Actions on the public repository | accepted | 2026-11-11 |
| [0138](0138-vulnerabilities-are-reported-privately-on-github.md) | Vulnerabilities are reported privately through GitHub | accepted | 2026-11-11 |
| [0139](0139-the-release-signer-is-part-of-the-repository.md) | The release signer is part of the repository | accepted | 2026-11-11 |
| [0140](0140-work-stays-private-the-public-repository-gets-snapshots.md) | Work stays in the private repository; the public one gets checked snapshots | accepted | 2026-11-11 |
| [0141](0141-readme-in-english-with-a-german-version.md) | The README is English, with a German version beside it | superseded by ADR-0143 | 2026-11-11 |
| [0142](0142-the-hub-waits-for-its-sessions-work-when-it-stops.md) | The hub waits for its sessions' work when it stops | accepted | 2026-11-11 |
| [0143](0143-every-document-in-english-and-german.md) | Every document exists in English and German | accepted | 2026-11-11 |
| [0144](0144-the-integration-lives-at-the-repository-root.md) | The integration lives at the repository root, where HACS looks for it | accepted | 2026-11-11 |
| [0145](0145-the-integrations-own-texts-live-in-the-common-section.md) | The integration's own texts live in the `common` section, and its translations pass hassfest | accepted | 2026-11-11 |
<!-- adr-index:end -->
