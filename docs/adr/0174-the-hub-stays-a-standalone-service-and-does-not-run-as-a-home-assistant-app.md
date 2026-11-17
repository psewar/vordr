---
status: rejected
date: 2026-11-17
decision-makers: kay (2026-11-17, rejected for now after asking whether the hub can be installed from the Home Assistant UI); proposed by Claude
---

# The hub stays a standalone service and does not run as a Home Assistant app

Examines a deviation from [ADR-0022](0022-hub-stays-own-service.md) (drivers 1 and 4) and a refinement of
[ADR-0010](0010-administration-only-local.md) for one mode of operation, and rejects it; ADR-0022 stands. Home
Assistant calls what used to be add-ons "apps"; this ADR uses the new name.

## Context and Problem Statement

Installing the hub means a Linux machine, a binary, a state folder, a certificate and a unit file. Many sites already
run Home Assistant OS or a supervised installation and have no other machine for the hub. Home Assistant can install
and run a container as an app from a repository the user adds in the UI, announce it to an integration (Supervisor
discovery), and back its data up with the rest of the system. The hub fits that shape: a static binary without a
database ([ADR-0011](0011-state-in-files.md)), one TCP port, a state folder, a certificate.

[ADR-0022](0022-hub-stays-own-service.md) keeps the hub out of Home Assistant on purpose: whoever takes over Home
Assistant must not be able to sign jobs for every host. As an app the hub runs under the Supervisor. Home Assistant
Core, with every custom integration inside it, and every Home Assistant administrator can reach `hub.key` (a backup,
the protection mode, a terminal app). In app mode "Home Assistant administrator" therefore equals "hub administrator",
and the roles of [ADR-0039](0039-ha-client-roles.md) protect against mistakes, not against a compromised Home Assistant.
The question is whether Vördr offers this mode at all, and on what terms.

What stays true in app mode: the operator key is never on the hub ([ADR-0032](0032-self-update-signed-by-operator.md),
[ADR-0139](0139-the-release-signer-is-part-of-the-repository.md)), so a taken-over hub still cannot ship agent code;
each agent's local policy keeps the last word ([ADR-0006](0006-local-policy-decides.md)); jobs stay signed.

## Decision Drivers

* A site that has only Home Assistant should be able to run the whole of Vördr without a second machine.
* The stronger separation of [ADR-0022](0022-hub-stays-own-service.md) must stay available and stay the reference;
  nobody may believe the app is as separated as a standalone hub.
* The app brings no new way to reach a host: the hub's own rules (approval, signing, policy at the agent) are unchanged.
* The repository stays site-neutral ([ADR-0099](0099-the-repository-is-site-neutral-checked-by-the-build.md)) and
  keeps one release path for the integration ([ADR-0136](0136-integration-via-hacs-as-a-zip-release.md),
  [ADR-0144](0144-the-integration-lives-at-the-repository-root.md)).
* The app does not need rights beyond its own container (no host network, no Supervisor API write access).

## Considered Options

* No app mode: the hub is always standalone (status quo). **Chosen.**
* An opt-in app mode in the same repository (examined below, rejected for now): `repository.yaml` and an app folder next to the integration, a prebuilt
  multi-architecture image.
* An opt-in app mode in a thin repository of its own (rejected for now with the one above), with the image built from the main repository.
* A hub inside the integration itself (Home Assistant Core signs). Rejected on its face: it ends the separation
  completely.

## Decision Outcome

Chosen option: no app mode; the hub stays a standalone service. Rejected for now (the maintainer, 2026-11-17): the boundary of
[ADR-0022](0022-hub-stays-own-service.md) is worth more than the convenience. In app mode a compromised Home
Assistant is a compromised hub, and the roles of ADR-0039 would protect against mistakes only. The one-time SSH
install from Home Assistant removes most of the onboarding friction without moving the hub. A later ADR may revisit
this, for example when the pairing and approval questions below are settled.

The analysis below is kept as the record of what the app mode would have been. It is the examined option, not the
decision.

Examined option: an opt-in app mode in the same repository. The standalone hub would stay the reference and the
default in all documents; the app would be described as a convenience for sites where Home Assistant administrator
and operator are the same people, with its trade-off stated in the first paragraph of its documentation.

* **Form.** `repository.yaml` at the root and an app folder (`vordr_hub/`) with `config.yaml`, a Dockerfile and its
  documents. A prebuilt image for `amd64` and `aarch64` is published by the repository's CI; the Supervisor never
  builds it. The image is built before the version in `config.yaml` rises, a tag is never overwritten, and building the
  image creates no GitHub release (releases belong to the integration, ADR-0144). The app version is the hub version.
* **Rights.** Only the container's own rights: port mapping for the agents' port, `/data` for the state, `/ssl` and a
  config folder read as needed. No `host_network`, no `hassio_api`, no `homeassistant_api`.
* **Discovery.** The app announces its internal address, its CA and the hub's fingerprint to Home Assistant through
  Supervisor discovery; the integration offers the setup, and the user confirms. Discovery never creates an entry by
  itself.
* **Certificate.** Agents reach the hub through the mapped port with a certificate from `/ssl` or from a CA the app
  creates in `/data`. The integration reaches it on an internal listener that is not mapped, with a certificate of the
  app's CA for the internal name; the integration keeps requiring `wss` and checking the name.
* **Approvals.** The hub still admits hosts and clients only locally ([ADR-0010](0010-administration-only-local.md)).
  In an app there is no shell, so the admin commands are delivered over the app's standard input
  (`hassio.app_stdin`, Home Assistant administrators only) into the same admin logic, with the same fingerprint
  comparison. No new command goes into the `/v1/ha` protocol.
* **Operator key.** Never in the app. A "create the key" option is refused. Release files that the operator signs
  elsewhere are copied into the app's config folder; agents verify them against their own `agent.yaml`.
* **Out of scope here, each its own ADR if wanted:** pairing the integration by a secret in the discovery message,
  approving hosts from Home Assistant's UI, a sender allowlist in the hub as a substitute for the host firewall of
  [ADR-0056](0056-firewall-8443.md), and a read-only status page through ingress.

### Consequences

Of the examined option, which is not built:

* Good, because a site with only Home Assistant gets hub, agents and integration from the UI, with backups handled by
  Home Assistant.
* Good, because the signed-job and operator-key guarantees against the network and against a taken-over hub stay as
  they are.
* Bad, because a compromised Home Assistant is a compromised hub in app mode: it can sign jobs within every host's
  policy and read the hub state. Documents and the app's first paragraph say so.
* Bad, because there is no host firewall in front of the mapped port; hellos without approval stay `pending` (at most
  `max_pending`), but the port is open on the whole network.
* Bad, because a second publication path (images) runs next to `vordr.zip`, and the repository must not carry another
  `config.yaml` that the Supervisor would take for an app.
* Neutral: small devices with an SD card need the OSV data off by default (ADR-0064, ADR-0073).

Questions that were open when it was examined: whether `hub.key` is part of Home Assistant's backup (restore works without
changing any `agent.yaml`, but the key then lies wherever backups lie) or is excluded and backed up separately; the
default certificate mode; whether the image is built by CI in the public repository or pushed by the operator; how an
existing hub moves into the app. Two assumptions are unchecked until tried on a test installation: that discovery
reaches a custom integration like a core one, and that standard input reaches the hub without an init system.

### Confirmation

* A check in `./build.sh` fails when the repository holds a `config.yaml` or `config.json` outside the app folder.
* Tests of the app mode: options accepted and refused (with the state and the next step), certificate names, discovery
  against a fake Supervisor, an admin command over standard input accepted and refused; in the integration, discovery
  to confirmation and approval, and abort for an entry that exists or an incomplete message.
* A test that the app never writes or asks for an operator private key, and that the hub refuses to start in app
  mode without a state folder it can write.
