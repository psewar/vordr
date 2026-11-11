---
status: accepted
date: 2026-10-30
decision-makers: kay
consulted: Claude (measurements, proposal)
---

# Soft memory limit for the hub (GOMEMLIMIT), no hard cap

## Context and Problem Statement

Since hub 0.18.0 the hub downloads the OSV dumps of PyPI and npm and builds an index of each (ADR-0064). While it
builds the npm index, the hub's resident memory peaks at about twice its level afterwards (in a measurement about
370 MB against about 175 MB), which in a small container (for example 512 MB RAM) leaves little room. The npm dump
changes practically every day, so that peak comes back daily. How do we keep the hub well inside its container?

## Decision Drivers

* The hub must never be killed for memory: it signs jobs and talks to Home Assistant.
* The index build is the only peak; the live heap with both indexes is about 112 MB.
* No slower refresh, no larger container if a setting does the job.

## Considered Options

1. Leave it as it is (Go's default garbage collector pacing).
2. A soft limit for Go's garbage collector, `GOMEMLIMIT`, in the systemd unit.
3. A hard cgroup limit (`MemoryMax=`) in the unit.
4. Give the hub's container more RAM.

## Decision Outcome

Chosen option: **2**, `Environment=GOMEMLIMIT=200MiB` in `deploy/systemd/vordr-hub.service` (the operator,
30.10.2026). The collector runs earlier once the heap approaches 200 MiB, so the build's garbage is collected instead
of piling up; it is a target, not a cap, and the process is never killed for crossing it.

### Consequences

* Good, because the measured build peak drops from 248 MB to 201 MB (development machine, both indexes, same
  4.8–5.1 s).
* Good, because it needs no code change and no larger container.
* Bad, because if the live heap ever grows past 200 MiB (much larger dumps, many more hosts), the collector runs
  very often; the hub then gets slower before it gets bigger. Raise the value in the unit then.

### Confirmation

`systemctl show vordr-hub -p Environment` on the hub shows the setting; the RSS peak during an npm index build is
measured (expected well under 300 MB).

## Pros and Cons of the Options

### 1. Leave it

* Good, because nothing changes.
* Bad, because a daily peak at a large share of a small container's RAM leaves little room for anything else.

### 2. GOMEMLIMIT (soft)

* Good, because it bounds the peak where it comes from, the collector's pacing.
* Bad, because it is invisible unless one reads the unit (hence the comment there and this ADR).

### 3. MemoryMax (hard)

* Good, because the bound is guaranteed.
* Bad, because crossing it kills the hub - exactly what must not happen.

### 4. More RAM

* Good, because it gives room for everything.
* Bad, because it treats the symptom; the peak is garbage, not live data.

## More Information

Measured on a development machine with both dumps: no limit 248 MB,
300 MiB 227 MB, 200 MiB 201 MB, 150 MiB 157 MB. 150 MiB was not chosen: too close to the live heap of about 112 MB.
`design/architecture.md` 11.2.
