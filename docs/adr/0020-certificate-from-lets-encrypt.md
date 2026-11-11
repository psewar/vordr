---
status: accepted
date: 2026-10-28
decision-makers: kay (approval of the plan)
---

# TLS certificate of the hub from Let's Encrypt, not from an internal PKI

Backfilled on 2026-10-30, decided on 2026-10-28. Sources: the operator's notes (plan of 2026-10-28), the operator's
project notes.

## Context and Problem Statement

Agents and HA check the hub's certificate. There are two common ways to get one: acme.sh with Let's Encrypt (DNS-01)
and an internal PKI.

## Decision Drivers

* Every agent, especially on Linux, must accept the chain.

## Considered Options

* Let's Encrypt via acme.sh (DNS-01)
* certificate from an internal PKI

## Decision Outcome

Chosen option: Let's Encrypt via acme.sh. Certificates of an internal PKI can end at a root that the Linux trust
stores do not hold; Linux agents refuse them. Whoever wants to trust their own CA sets `hub.ca_file` in `agent.yaml`.
The hub reloads a renewed certificate without a restart.

### Consequences

* Good, because it works with the system roots of every host.
* Bad, because renewal needs DNS access. Since the hub runs in its own container, the certificate is therefore renewed
  on a host that has that access and copied over ([ADR-0055](0055-files-copied-no-secret-access.md)).

### Confirmation

`vordr-agent hub-check` checks the TLS chain on every host as its own stage; `install-agent.sh` runs it.
