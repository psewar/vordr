---
status: accepted
date: 2026-10-30
decision-makers: kay (approval of the expansion on 2026-10-30)
---

# Certificate and PBS token are copied in; the hub container has no secret access

Backfilled on 2026-10-30, decided on 2026-10-30. Sources: the operator's notes (expansion plan of 2026-10-30), the
operator's project notes.

## Context and Problem Statement

The TLS certificate renews via DNS-01 ([ADR-0020](0020-certificate-from-lets-encrypt.md)), and the PBS token comes
from a secret store. In the hub's own container ([ADR-0054](0054-hub-in-own-container.md)) both would need credentials
for the secret store and the DNS provider.

## Decision Drivers

* The hub container should hold no credentials for the secret store or the DNS provider. The source states this as
  the outcome; it gives no further reason.

## Considered Options

* give the hub container access to secret store and DNS
* renew on another host and copy the files

## Decision Outcome

Chosen option: renew on another host and copy. The identities for the secret store and the DNS provider stay on that
host. A daily timer outside the container copies certificate and token to the hub container only when they changed,
after checking validity, name and key. A failure of the timer is reported to HA.

### Consequences

* Good, because a compromised hub container exposes no secret-store or DNS credentials.
* Bad, because there is one more moving part; a renewal reaches the hub up to a day later.

### Confirmation

The timer checks before copying and reports failures to HA.
