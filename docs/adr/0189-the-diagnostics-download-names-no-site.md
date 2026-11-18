---
status: accepted
date: 2026-11-18
decision-makers: kay (2026-11-18, accepted: the repair issues' ids stay as they are, renaming them needs a migration of existing repairs); proposed by Claude
---

# The diagnostics download names no site

## Context and Problem Statement

Home Assistant offers a diagnostics download for every integration, and people attach it to issues, also public
ones in the repository's tracker. Vördr's download kept the keys out, but carried everything else as the hub sends it:
the hub's address, this Home Assistant's name at the hub, every host's name (as keys, in device names, in the hub's
waiting list), the ids of the pip/npm locations (`pip-user-<user>`, `node-red-<user>`), their paths on the hosts,
container image repositories with their registry, excerpts of run logs (with addresses and names), log files, and free
text: the reason given for a run outside its window, the reason a host is exempt from backups. That is a map of the
site, and the security findings next to it say where it is weak.

The repository itself keeps every site out ([ADR-0099](0099-the-repository-is-site-neutral-checked-by-the-build.md));
the download is how a site's data most easily lands next to it.

What does the download keep, and what does it hide?

## Decision Drivers

* What is needed to diagnose stays: the structure of the host views, counts, codes, states, versions, package names,
  fingerprints.
* Nothing that names or locates the site: names, addresses, paths, registries, user names, free text from people.
* The download stays readable: which host is which within one download must stay clear.
* A field name stays a field name, whatever a host is called.

## Considered Options

1. As before: the keys redacted, everything else as the hub sends it.
2. Redact the fields that name the site with Home Assistant's `async_redact_data`, host names included.
3. Names replaced by stable placeholders wherever they stand on their own; fields that hold a path, a registry, a log
   or free text redacted; the hub's address with a reserved host name.

## Decision Outcome

Chosen option: **3**.

* **Placeholders.** Host names, also of hosts waiting for approval, become `host-1`, `host-2` … and the ids of the
  pip/npm locations `location-1` … in the order of the names; this Home Assistant's name at the hub becomes `client`.
  They are replaced in values, also as words inside a text and in any case, but not inside longer names (a host called
  `agent` leaves `vordr-agent` alone; a dot ends a name, so `web-01.example.org` loses its `web-01`). As keys, only the
  host list's own keys and location ids are replaced: a host called `system` leaves the field `system` alone.
* **Redacted text.** `path`, `image`, `log_excerpt`, `log_file`, `outside_window` (the reason of a run outside its
  window) and `exempt` (why a host has no backup), wherever they hold text. `before_install`, the hypervisor that backs
  a guest up, becomes its placeholder if it is one of the hosts, and is redacted otherwise. Codes stay, also a run's
  `reason`, which is a code.
* **The hub's address** keeps scheme, port and path, with `hub.invalid` as its host and without user or password. The
  keys stay redacted as before.
* **What Home Assistant adds** is outside the integration: Home Assistant puts the integration's repair issues into
  the download with their ids, and Vördr's issue ids name the host (`problem_<node>`, `reboot_<node>`, …). A person who
  shares the download in public still has to look at that list; ids without the host's name are a change of their own,
  since existing repairs are found by their ids.

### Consequences

* Good, because the part the integration writes can be attached to a public issue as it is.
* Good, because the structure, the counts and the codes stay, and the same placeholder means the same host or location
  throughout.
* Bad, because the repair issues' ids, which Home Assistant adds, still name the hosts.
* Bad, because a host name that also stands on its own in a value that does not mean the host (a host called `check`
  in a value "check") is replaced there too; over-hiding is the safe side.
* Bad, because a problem that depends on a path, an image name or the wording of a log cannot be seen in the download;
  the person can still give that part by hand. Names of containers, Compose services and apps stay: they say what
  runs, which a diagnosis needs, not where.
* Neutral: option 2 would leave host names as keys of the host list (`async_redact_data` redacts values only) and
  lose which host is which.

### Confirmation

* `ha/tests/test_entities.py::test_diagnostics_name_no_site`: hosts with every kind of site data (a run with a code,
  free text, a log excerpt with an address and the host's name in capitals, and a log file; a location id with a user
  name and its path; an image repository; a backup exemption and a hypervisor outside the hub; a host waiting for
  approval; a host whose name is part of `vordr-agent`; a host named like a field). None of the names, addresses,
  paths, the registry or the free text is in the download; the placeholders are stable; field names, counts, codes
  and package names stay.
* `test_the_hub_address_keeps_no_credentials`: user and password leave the hub's address.

## More Information

* Design: [design/ha-integration.md](../../design/ha-integration.md), "Diagnostics download".
* [ADR-0099](0099-the-repository-is-site-neutral-checked-by-the-build.md): the repository is site-neutral.
