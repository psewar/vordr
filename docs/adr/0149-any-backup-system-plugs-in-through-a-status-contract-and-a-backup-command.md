---
status: proposed
date: 2026-11-11
decision-makers: kay (to decide; asked on 2026-11-11 whether the backup interface can become more general, for example for Synology); proposed by Claude
---

# Any backup system plugs in through a read-only status contract and a backup command

Refines [ADR-0030](0030-backup-required.md) (a backup before every install, checked at the hub) and
[ADR-0096](0096-fresh-backup-right-before-the-install.md) (a fresh backup right before the install, made by the
hypervisor's agent). ADR-0096 already keeps the flow independent of the backup system and names the two places that are
not: how the agent makes the backup and how the hub reads it. This ADR proposes interfaces for exactly those two places.
Proposed only: nothing in the code changes until the operator decides, and the open questions at the end are part of
that decision.

## Context and Problem Statement

The backup requirement only works with Proxmox Backup Server (PBS) today:

* **Hub, reading:** `internal/hub/backup.go` asks the PBS API (`/admin/datastore/<datastore>/groups`) with a token
  that has `DatastoreAudit` only. `hub.yaml` has `pbs_url`, `datastore`, `fingerprint` and `token_file` directly under
  `backup`, and every host maps to a PBS group (`vm/<id>`, `ct/<id>`, `host/<name>`).
* **Agent, making:** `internal/agent/guestbackup.go` runs `vzdump` on a Proxmox VE host, and the policy
  (`internal/policy/policy.go`, `guest_backup`) accepts only a `pbs:` storage.

The repository is public. An operator without PBS (with a NAS such as Synology, with restic or borg, with a product
such as Veeam) can only list every host as `without`, and then installs have no way back that Vördr checks. Hosts
that are not hypervisor guests (single-board computers, Windows PCs) are `without` even where PBS is in use, unless
something outside Vördr backs them up to a `host/` group.

How can an operator attach the backup system they have, without the hub gaining rights it does not have today, and
without changing the flow, the codes or the split of rights of ADR-0030 and ADR-0096?

## Decision Drivers

* An operator without PBS can use the age check (ADR-0030) and the fresh backup before the install (ADR-0096).
* The hub only reads. It gets no right to make, restore, prune or delete backups, and it holds as few secrets as
  possible: ideally the time of a backup, not a key that opens the backups.
* Vördr never deletes or prunes a backup.
* After a fresh backup the hub checks the backup store itself. The one who made the backup does not also confirm it.
* The local policy decides what a host does ([ADR-0006](0006-local-policy-decides.md), as amended by
  [ADR-0087](0087-runs-outside-the-window-with-a-reason.md)). The hub's signed job names only the guest.
* No shell, and no decision on a program's output text ([ADR-0007](0007-fixed-codes-instead-of-error-texts.md)).
* An existing `hub.yaml` and `agent.yaml` keep working without edits.
* A further backup system adds no code to the hub or the agent.

## Considered Options

1. Keep PBS as the only backup system; everything else stays `without`.
2. Vendor adapters in the core: the hub reads each backup system itself (the DSM web API, a restic or borg
   repository, a product's REST API), and the agent knows each product's backup command.
3. Two narrow interfaces, each with a `kind`: a read-only **backup store** at the hub (`pbs` as today, or a generic
   **status contract**) and a **backup maker** at the agent (`vzdump` as today, or a fixed **command**). PBS and
   vzdump stay built in.
4. The agent reports the time of its backup, and the hub trusts that report without reading any store, as it does for
   an appliance's configuration backup ([ADR-0090](0090-appliance-installs-need-a-fresh-config-backup.md)).
5. The operator's backup system pushes its backup times to a new endpoint of the hub.

## Decision Outcome

Proposed: option 3. Option 4 is open question 3, and the other parts the operator decides are open questions 1, 2
and 4 below.

### What stays the same

* The flow of ADR-0096: phase `backing_up`, the signed job `backup`, waiting for the result, the check in the store
  against the moment the hub asked (one minute for clock skew), reuse of a backup younger than 15 min, one backup at a
  time, waiting for a busy hypervisor
  ([ADR-0147](0147-a-backup-waits-for-a-hypervisor-busy-with-a-check-of-its-own.md)), no fallback to the age check.
* The codes: `backup_stale`, `backup_missing`, `backup_unreachable`, `backup_unmapped`, `backup_host_unavailable`,
  `failed_backup`, `timeout`, `rejected_busy`, `rejected_rate_limited`. Home Assistant needs no new text.
* The hub only reads. Vördr never prunes or deletes. A failed backup stops the install.
* On the agent: at most one backup per guest per 10 min, the agent's lock held like an install, a backup not cut short
  when the agent stops, the whole output in the run log on the host.

### Hub: the backup store

`backup.store` in `hub.yaml` names one store and its `kind`. The rest of the `backup` section (`max_age_hours`,
`guests`, `before_install`) is unchanged. A guest's `group` becomes the host's ID in that store; for `pbs` it keeps
its pattern (`vm/<id>`, `ct/<id>`, `host/<name>`).

* **`kind: pbs`**: the PBS API as today, with the same keys (`url`, `datastore`, `fingerprint`, `token_file`).
* **`kind: status`**: a document in a fixed format that a small adapter of the operator writes. The adapter runs where
  the backup system is (a scheduled task on the NAS, a hook after a restic or borg run, a script on the backup
  server) and reads the backup system's own records. The hub needs no account on the backup system and no key to its
  backups; it learns one time per host. The hub fetches the document in one of two ways:
  * `url`: HTTPS `GET`, the certificate pinned by its SHA-256 fingerprint as for PBS, an optional bearer token from
    `token_file`, no redirects, a size limit. A static file served by any web server is enough.
  * `file`: an absolute path on the hub's host that the backup system or the adapter delivers (a mount, a copy).

The document:

```json
{
  "format": "vordr-backup-status/1",
  "generated_at": "2026-11-11T19:50:00Z",
  "backups": {
    "vm/301": "2026-11-11T19:45:12Z",
    "host/pi-a": "2026-11-11T02:10:00Z"
  }
}
```

* Each value is the start time of the newest backup of that ID that **completed successfully**, in RFC 3339 with a
  zone. Failed or partial attempts are not listed. The start time matches what PBS reports and what the check after a
  fresh backup compares with.
* IDs follow a fixed pattern that cannot become an option or a path: letters, digits, `.`, `_`, `-` and `/`, starting
  with a letter or digit, without `..`.
* The hub decides on fields, never on text:
  * `generated_at` older than 30 min, or in the future beyond the allowed clock skew: `backup_unreachable` (the same
    grace as for a PBS that does not answer).
  * A document that does not parse, has an unknown `format` or breaks the ID pattern: `backup_unreachable`, and the
    hub logs why.
  * An ID that is missing: `backup_missing`. Older than `max_age_hours`: `backup_stale`.
* After a fresh backup (ADR-0096) the hub reads the store again until it shows a backup from no earlier than the moment
  it asked, for at most `status_wait_min` (proposed default 10). Otherwise the run ends `failed_backup`, with the reason
  in its detail. A status that is refreshed only on a schedule therefore delays the install; an adapter that runs right
  after each backup does not.

**Compatibility of `hub.yaml`:** the keys `pbs_url`, `datastore`, `fingerprint` and `token_file` directly under
`backup` stay valid as the short form of `store: {kind: pbs, ...}`. `check-config` refuses a file that has both forms.
No configuration has to be migrated; the example file and the documentation show the long form.

### Agent: the backup maker

`guest_backup` in `agent.yaml` gets a `kind`. Without one, the existing form (`storage`, `timeout_min`) means `vzdump`.

* **`kind: vzdump`**: as today. Proposed widening: besides a `pbs:` storage, any Proxmox VE storage whose `content`
  includes `backup` (for example an NFS or SMB share on a NAS), as long as the hub reads that target through a store
  (a `status` adapter that lists the dump files). Such a backup is a full copy each time, not incremental, so it takes
  longer and uses more space than a backup to PBS.
* **`kind: command`**: a fixed program and its arguments in `agent.yaml`, run without a shell:

  ```yaml
  guest_backup:
    kind: command
    argv: ["/usr/local/sbin/backup-guest", "--quiet"]  # the guest's ID follows as the last argument
    guests: ["vm-web", "vm-db"]                         # the only IDs this agent backs up
    timeout_min: 60
  ```

  * The hub's signed job names only the guest. The agent appends that ID as one separate argument and runs nothing
    else. It refuses an ID that is not in its own `guests` list (`failed_backup`, "not a guest of this host"), so the
    hub cannot make it back up something else.
  * Exit code 0 means the backup was made; anything else is `failed_backup` with the exit code in the detail, and the
    output goes to the run log. The agent never reads the output to decide anything.
  * The job's ID is in the environment (`VORDR_JOB_ID`), so that the command can put it into the backup's notes.
  * `check-config` checks that `argv[0]` is an absolute path to an executable that only root (or the administrators on
    Windows) can change, and that `guests` is not empty. On Windows it refuses `.bat` and `.cmd` as `argv[0]`, because
    `cmd.exe` parses their command line again by its own rules; a PowerShell script runs through `pwsh.exe` with
    `-NoProfile -File <script>` as its first arguments.
  * The protocol's guest pattern widens from `vm/<id>` and `ct/<id>` to the store's ID pattern. `kind: vzdump` keeps
    accepting only `vm/<id>` and `ct/<id>`.

**Never prunes, only by contract:** with `vzdump` the agent passes `--remove 0` and Vördr can say it never prunes.
With `command` it cannot enforce that: the program is the operator's. The documentation and the example state the
contract (add one backup, never prune, never delete, never restore), and `check-config` cannot verify it. The hub's
signed job still cannot change what runs; only the host's own `agent.yaml` can, and the hub cannot write it.

Combinations that this allows, as examples: a PBS store with a command that runs `proxmox-backup-client` into a
`host/` group; a status adapter on a NAS with vzdump to a share on that NAS; a status adapter after restic or borg with
a command that starts restic or borg.

### Optional: a host backs itself up before its own install

For hosts that are no guest of a hypervisor (single-board computers, Windows PCs, the hypervisor itself), a
`self_backup` section in `agent.yaml` (`argv`, `timeout_min`; no `guests`) would let the host's own agent make the
backup. The hub runs the same flow with the host itself as the backup maker: a `backup` job without a guest, then the
check in the store, then the install. Whether to do this at all is open question 2. Caveats:

* A backup taken from inside a running system is at best crash-consistent unless the system can snapshot itself
  (Volume Shadow Copy on Windows, LVM or btrfs snapshots on Linux); a database may need its own dump step in the
  command.
* The hub makes one backup at a time today because one hypervisor makes them all. With several backup makers, the
  hub would keep one at a time per maker instead.

### Rejected

* Option 1, because every operator without PBS loses the way back that ADR-0030 and ADR-0096 are about.
* Option 2 (unless open question 1 decides otherwise), because:
  * each backup system would put its own credentials on the hub, and they open more than a time: the password of an
    encrypted restic or borg repository decrypts every backup in it, and the DSM account that can start an ABB task can
    also delete backup versions (ADR-0097);
  * the core would carry code and tests for products it cannot test without them, some through undocumented APIs
    (ABB per [ADR-0097](0097-synology-active-backup-for-business-for-backups-before-installs.md));
  * a further backup system would still mean a change to the hub and a release.
* Option 4 as the general rule, because the check after the backup would no longer be independent: a backup command
  that exits 0 without having made a backup would pass. It remains open as an explicit exception (open question 3).
* Option 5, because the hub would get a new inbound endpoint whose callers change its state, with authentication of
  its own. With a file or a pull the hub stays the reader.

### Consequences

* Good, because any backup system can serve ADR-0030 and ADR-0096 with a small adapter, and the hub needs no account
  on it.
* Good, because the hub holds one time per host, not a key to the backups.
* Good, because operators with PBS change nothing; the short form keeps their `hub.yaml` valid.
* Good, because the Synology question of ADR-0097 leaves the core: the adapter runs on the NAS with local rights, and
  which DSM rights a remote reader would need only matters if open question 1 chooses vendor adapters.
* Bad, because the operator writes and maintains the adapter. The hub checks its format and freshness, not its truth:
  an adapter that lists failed attempts, or echoes the backup command instead of reading the backup system's records,
  makes the check worthless. The documentation has to say this plainly and give an example adapter.
* Bad, because with `command` "never prunes" is a documented contract, not an enforced property, and the program runs
  with the agent's rights (root, or SYSTEM on Windows).
* Bad, because a status that is refreshed on a schedule delays installs after a fresh backup by up to
  `status_wait_min`, and a status that stays old blocks installs (`backup_unreachable`), which fails safe.
* Bad, because the protocol, the policy, `check-config` and both example files change, and an agent with
  `kind: command` needs a release that knows it. Rollout order as in ADR-0096: integration (no new texts expected),
  hub, agent release, `agent.yaml`, then `hub.yaml`.
* Neutral: a `vzdump` backup to a share instead of PBS is a full copy per install.

### Confirmation

Once implemented:

* The existing backup tests pass unchanged with the short form of `hub.yaml`.
* Hub, `kind: status`: fresh, stale and missing IDs; `generated_at` too old and in the future; a document that does not
  parse, an unknown `format`, an ID breaking the pattern, a pinned certificate that does not match, a redirect, a
  document above the size limit; the wait after a fresh backup ending in `failed_backup`. `TestBackupConfigIssues`
  refuses both forms at once, a relative `file` and a `url` that is not HTTPS.
* Agent, `kind: command`: runs without a shell with the guest as the last argument; refuses an ID outside `guests`;
  exit codes other than 0 give `failed_backup`; timeout; minimum gap and lock as for vzdump. `check-config` refuses a
  relative or writable `argv[0]`, an empty `guests`, and `.bat` or `.cmd` on Windows. The protocol refuses a guest ID
  starting with `-` or containing `..`.
* End to end: `TestInstallWaitsForAFreshBackup` and `TestNoFreshBackupNoInstall` also with a status-file store and a
  command maker.
* A mutation probe as in ADR-0096: no check in the store after the backup, the guest passed through a shell, the
  `guests` list ignored, a stale `generated_at` accepted.

## More Information

### Open questions for the operator

1. **A generic contract or vendor adapters in the core?** Proposed: the generic status contract and the backup
   command only. PBS and vzdump stay built in because they exist and PBS needs no more than `DatastoreAudit`. Example
   adapters (for example for a NAS, for restic) as scripts in the documentation, not as code of the hub.
2. **Self-backup for hosts that are no guests: yes or no?** Proposed: yes, but as a second step once the store and
   the command exist, because it reuses both and the flow. Without it, such hosts stay `without`.
3. **May a backup the agent reports count where the hub cannot read the store?** ADR-0090 already trusts the proxy's
   reported time of an appliance's configuration backup. Proposed: no for `before_install`; the independent check in
   the store stays mandatory. If wanted, only as an explicit exception per host with its reason shown in Home
   Assistant, like `without`, never as a silent switch.
4. **One store per hub, or several named stores?** A site may have PBS for its guests and something else for the
   other hosts. Proposed: start with one store, and allow `backup.stores` with a `store` per guest later if a site
   needs both, without changing the short form.

### Not verified: reading Synology's backup state remotely

ADR-0097 found that starting an Active Backup for Business (ABB) task needs an ABB administration account through an
undocumented web API. For reading only: Synology's ABB specification names delegated package permissions as an
alternative to administrator rights, and its help describes a delegation that can view backup task details. Whether
DSM's web API honours such a view-only delegation, and what Hyper Backup offers, is not verified. With the status
contract this does not matter, because the adapter runs on the NAS. It matters only if open question 1 chooses vendor
adapters in the core.

Sources:
* Synology's ABB specification: https://www.synology.com/en-global/dsm/7.2/software_spec/abb
* ABB settings and delegation in Synology's help: https://kb.synology.com/en-ca/DSM/help/ActiveBackup/activebackup_business_settings?version=6
