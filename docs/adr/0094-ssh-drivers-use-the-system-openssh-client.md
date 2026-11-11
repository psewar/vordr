---
status: accepted
date: 2026-10-31
decision-makers: kay (2026-11-01, accepted); proposed by Claude
---

# SSH drivers use the system's OpenSSH client with a pinned host key

## Context and Problem Statement

The proxy agent's pfSense driver reaches the firewall only through the forced command of an SSH key
([ADR-0091](0091-firmware-for-devices-without-internet.md),
[ADR-0093](0093-firewall-command-unprivileged-root-job-works-a-checked-queue.md)). Go's standard library has no SSH
client. The module cache used for builds holds no `golang.org/x/crypto`, and builds run with `GOPROXY=off`; adding it
would be the project's first third-party cryptography dependency. The Linux hosts the proxy agent runs on ship an
OpenSSH client.

How does the driver speak SSH, and how does it know it talks to the firewall?

## Decision Drivers

* Trust only what is pinned: a different host key must stop the driver, never be learned.
* Codes decided on facts, never on error texts ([ADR-0007](0007-fixed-codes-instead-of-error-texts.md)).
* No new third-party dependency for a single driver if the system already has a maintained client.
* The private key stays readable by the instance only, and on disk as briefly as possible.

## Considered Options

* `golang.org/x/crypto/ssh` as a new module.
* The system's OpenSSH client, run with a fixed set of options and a known_hosts file holding only the pinned key.
* A web API on the firewall instead of SSH (a pfSense API package).

## Decision Outcome

Proposed: the system's OpenSSH client.

* agent.yaml pins the host key: `target.host_key: "<type> <base64>"` from `ssh-keyscan`. The address carries the
  user: `ssh://<user>@<host>[:port]`. `server_name`, `fingerprint` and `ca_file` are refused for SSH drivers, and
  `host_key` for HTTPS drivers.
* The credentials file holds `KEY=<base64 of the OpenSSH private key>`. For each call the driver writes it to a
  0600 file in a fresh 0700 temporary directory and removes it afterwards.
* Before `ssh` the driver establishes two facts:
  * The address answers on its port; otherwise `precheck_repo_unreachable`.
  * `ssh-keyscan` shows the pinned key; otherwise the new code `failed_target_hostkey`, and `ssh` does not run at all.
* After those two facts, `ssh` exiting with 255 can only be the login, so it is `failed_target_auth`. Any other exit
  is the forced command's refusal, and its own `reason=` goes into the detail.
* `ssh` runs with:
  * `-F /dev/null`, `BatchMode=yes`, `IdentitiesOnly=yes` and `IdentityAgent=none`;
  * `StrictHostKeyChecking=yes`, a `UserKnownHostsFile` with only the pinned key, and `GlobalKnownHostsFile=/dev/null`.

Rejected:
* **x/crypto/ssh:** a new cryptography dependency fetched into an otherwise offline build, for one driver.
* **A web API on the firewall:** an extra package with its own login on the device that guards the network, when
  ADR-0093 already gives the narrowest possible access.

### Consequences

* Good, because host keys are compared, never learned, and a mismatch stops before any login.
* Good, because the codes stay distinct without reading OpenSSH's messages.
* Bad, because the container needs `openssh-client`; the driver reports `rejected_unsupported` when it is missing.
* Bad, because there is a short time window between the `ssh-keyscan` check and the login. During it, `ssh` itself
  still refuses any other key through `StrictHostKeyChecking`.
* Bad, because a network failure in that window counts as `failed_target_auth`. The detail carries ssh's text for
  people.
* Bad, because a host key rotation on the firewall needs `target.host_key` updated by hand. That is intended.

### Confirmation

* Tests in `internal/target` run the driver against stand-ins for `ssh` and `ssh-keyscan` and a real listening port.
  They cover the options and the key file (0600, removed after the call), known_hosts with exactly the pinned key, and
  every code.
* A mutation probe (each change must fail a test) catches 21 of 21 changes.
* On the firewall: a live report of the instance shows the firmware and the outdated packages, and a backup is stored.
