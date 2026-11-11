---
status: accepted
date: 2026-10-30
decision-makers: kay
consulted: Claude (implementation, measurements)
---

# OSV data held locally at the hub instead of querying api.osv.dev

## Context and Problem Statement

Vördr should check installed pip, pipx and npm packages against known vulnerabilities and malware (OSV, including the
`MAL-` entries from OpenSSF). Where does the matching happen, and who sees the hosts' package lists while it does?

## Decision Drivers

* No host's package list should leave the own network (what is installed reveals attack surface).
* Matching must work without internet at the agent and must not depend on a foreign service.
* The hub must fit a small container (512 MB RAM and 4 GB disk).

## Considered Options

1. Every agent queries `api.osv.dev` (batch query) itself.
2. The hub queries `api.osv.dev` for all hosts.
3. The hub downloads the complete OSV dumps per ecosystem (`<ecosystem>/all.zip`) and matches locally.

## Decision Outcome

Chosen option: **option 3** (the operator, 2026-10-30). With the `osv:` section in `hub.yaml` the hub downloads the
PyPI and npm dumps daily (with ETag), builds a small index in `<state_dir>/osv` and deletes the zip again.

### Consequences

* Good, because package lists only go from the agent to its own hub; `api.osv.dev` gets no query.
* Good, because matching still works when the internet is briefly gone (with the last data, marked `stale`).
* Good, because an update plan can be checked against the same data before installing
  ([ADR-0070](0070-hub-vets-and-signs-the-update-plan.md)).
* Bad, because npm means a 220 MB download per day (unchanged: one request thanks to ETag), and +112 MB heap at the
  hub.
* Bad, because only TLS to the bucket secures the dumps; OSV does not sign them.
* Bad, because further ecosystems (Debian, Alpine for containers) cost several hundred MB more.

### Confirmation

`TestLiveDump`/`TestLiveMatching` with real dumps; counter-check against `packaging` and npm `semver` (0 differences in
1060 pairs). The hub reports the state of the data (`ok`, `stale`, `missing`) to HA.

## Pros and Cons of the Options

* **Option 1:** simple, always current; but every package list goes to a foreign service, and every host needs
  internet.
* **Option 2:** only one sender; the lists still go out.
* **Option 3:** nothing goes out, the same data for inventory and update checks; disk and memory at the hub.

## More Information

Measured: PyPI 37.5 MB → index 1.0 MB, npm 219.6 MB → index 4.6 MB; a 4 GB disk does not need to grow for it.
Implementation: `design/architecture.md` 11.2.

Merged in German as ADR-0100; renumbered and translated later.
