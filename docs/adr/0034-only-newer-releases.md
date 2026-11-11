---
status: accepted
date: 2026-10-29
decision-makers: kay ("implement the proposals on your own")
---

# Agents only take newer releases

Backfilled on 2026-10-30, decided on 2026-10-29. Sources: a security review in the operator's notes,
`design/architecture.md` 10.4 ("Nur aufwärts").

## Context and Problem Statement

Every manifest the operator ever signed stays valid. Whoever takes over the hub could send an old manifest again and
bring back an old agent with a known bug.

## Decision Drivers

* No downgrade through a compromised hub.
* Hand-built test versions should still accept any release.

## Considered Options

The sources name no alternative.

## Decision Outcome

An agent on a release (`X.Y.Z`) only takes a newer one, compared as numbers (0.7.9 < 0.7.10). Otherwise it refuses
with `rejected_invalid_job`, reason `older_release`; HA shows that as a problem. The distribution never offers an older
release to a newer agent. A hand-built agent (date or suffix in the version) accepts any release.

### Consequences

* Good, because downgrading through the hub is impossible.
* Bad, because bringing back old code means publishing it under a new version.

### Confirmation

`TestNewerRelease`, `TestReleaseBackupKeyAndNoDowngrade`; counter-check: without the check exactly the three downgrade
cases fail.
