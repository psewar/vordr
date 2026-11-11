---
status: accepted
date: 2026-10-31
decision-makers: kay (2026-10-31, "Root cron job" and "Random password", both as recommended); proposed by Claude
---

# The firewall command stays unprivileged; a root job works a checked queue

## Context and Problem Statement

[ADR-0091](0091-firmware-for-devices-without-internet.md) gives the proxy agent one forced SSH command on the firewall
(pfSense): read the version and update facts, read `config.xml` as a backup, and open a short update window for a
listed device through a runtime pf table. Three facts shape how:

* `pfctl` needs `/dev/pf`, which is `root:proxy 0660`; the forced command runs as the unprivileged user `vordr-gna`.
* Where `config.xml` is readable by every local user, the backup needs no privilege.
* Where the firewall's SSH also accepts passwords, a password login of the forced-command user would reach a shell:
  the forced command binds only the key.

How does the forced command reach pf, and how is the password route closed?

## Decision Drivers

* The proxy gains no root on the firewall, and nothing beyond the three calls.
* Whatever the proxy sends is checked again by a part it cannot change.
* An opening closes by itself, also when the proxy is gone.
* No new package on the firewall.

## Considered Options

* The forced command runs a root helper through sudo (package `pfSense-pkg-sudo`, one NOPASSWD rule).
* `vordr-gna` joins the group `proxy`, so `pfctl` works directly.
* The forced command stays unprivileged and only queues a request; a root job run by cron every minute checks each
  request again and does the pf work.

## Decision Outcome

Chosen: the third option (the operator, 2026-10-31). The scripts live in `deploy/pfsense/`.

* `/usr/local/sbin/vordr-gna` (root:wheel 0755) runs as `vordr-gna`: `version`, `backup`, `window status`,
  `window open <address> <minutes>`, `window close <address>`. It never evaluates the caller's words. Answers are
  `key=value` lines; a refusal says `state=refused` and `reason=<code>`, and the exit code tells the kind (2 unknown
  call, 3 not allowed, 4 not ready).
* `/usr/local/sbin/vordr-gna-root` (root:wheel 0700) runs from the firewall's cron every minute under `lockf`.
  * It works the queue `/var/db/vordr-gna-queue` (owned by `vordr-gna`, 0700) and treats it as untrusted input:
    plain files only, the first 100 bytes, exact words, the address on the list `/usr/local/etc/vordr-gna-allow`,
    1 to 30 minutes. It never logs a request's content.
  * Closing happens in one place: the pf table `vordr_update_window` holds only what its state knows, whether closed on
    request, expired, or the state was lost; `pfctl -T expire 1800` is a hard limit on top.
  * Once an hour it writes the update facts from the local package catalog, without network; pfSense refreshes the
    catalog in its own scheduled job.
* `vordr-gna` gets a random password that is stored nowhere, which closes the password login. The key keeps its
  `from=` option with the proxy's address.
* The rule that lets table entries reach their vendor's hosts comes with the first driver that needs it. Until then an
  entry in the table has no effect.

Rejected: sudo, because it adds a package and a sudoers entry, and the proxy would start a root process directly. The
group `proxy`, because it gives full pf access (rules, states, flushing) to a user the proxy controls.

### Consequences

* Good, because the proxy holds no privilege on the firewall, and the root side checks everything again.
* Good, because an opening closes within a minute of its end even if the proxy never calls again.
* Bad, because a window opens or closes up to a minute after the request; the driver has to wait for it.
* Bad, because root reads files an unprivileged user writes. It reads little and strictly, but it is a boundary to
  keep in mind.
* Bad, because the scripts live outside the pfSense configuration; after a firewall upgrade the install tool has to
  confirm they are still there. The cron entry is part of the configuration and survives.

### Confirmation

* Tests in `deploy/pfsense` run both scripts with `/bin/sh` against stubs: refusals, the root job checking again,
  expiry without any further call, symlinks in and as the queue, table and state kept together, update facts.
  A mutation probe catches all 27 of 27 changes.
* On the firewall after installing: `version` and `backup` answer, `sh` and an unlisted address are refused, and an
  opened entry is gone after its end without any further call.
