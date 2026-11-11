---
status: rejected
date: 2026-10-31
decision-makers: kay (2026-10-31, chose the PBS backup right before the install, ADR-0096, after this examination); proposed by Claude
---

# Synology Active Backup for Business for the backup before installs

## Context and Problem Statement

The operator asked whether the agent should back up through Synology Active Backup for Business (ABB) on a Synology
NAS before updates. The idea: the backup would lie outside the host, could be restored after a failure, and ABB's
snapshots might make it fast. Examined, read only: DSM's web API, Synology's specification and release notes, and the
Proxmox forum.

## Decision Drivers

* It covers the kinds of hosts Vördr installs on, for example hypervisor guests (VMs and containers), ARM
  single-board computers, Windows PCs and the hypervisor itself.
* A backup can be started on demand by a closed, checkable call that needs few rights.
* It stays supported.

## Considered Options

* ABB on a Synology NAS.
* Proxmox Backup Server, which already backs up the guests ([ADR-0096](0096-fresh-backup-right-before-the-install.md)).

## Decision Outcome

ABB is rejected. What the examination found:

* **Proxmox:** ABB backs up VMware and Hyper-V, not Proxmox VE; Synology supports Proxmox only in its separate
  ActiveProtect appliances. ABB's Linux agent needs a kernel module, so it cannot run inside a container. On the
  hypervisor itself it is unsupported, and the Proxmox forum reports problems with LVM-thin after a restore.
* **ARM hosts (for example Raspberry Pis):** the Linux agent exists for x86-64 only.
* **Windows PCs:** ABB requires decrypted volumes, which rules out a system drive with BitLocker. ReFS (Dev Drives) is
  not supported.
* **NAS:** ABB 3.x needs DSM 7.2. A NAS that cannot move beyond DSM 7.1 stays on ABB 2.7.1 from February 2025, with
  Linux kernels up to 6.8 and Debian up to 12.
* **Starting a backup:** only through DSM's undocumented web API (`SYNO.ActiveBackup.Task`, `backup_task_run`). That
  needs an ABB administration account, which can also delete backup versions.
* **Already off the host:** PBS writes its datastore outside the hypervisor, so its backups already lie outside it.

Revisit if a host without BitLocker needs a backup that PBS cannot make, or where ABB supports the kernels and distributions in use.

### Consequences

* Good, because the guests keep one backup system (PBS), and Vördr needs no administration account on a NAS.
* Bad, because hosts PBS cannot back up, such as Windows PCs, stay `without` in hub.yaml and get no backup before
  installs.
* Bad, because this choice adds no second copy of the backups on another storage system. That is a question of its
  own, not decided here.

### Confirmation

Sources:
* Synology's ABB specification: https://www.synology.com/en-global/dsm/7.2/software_spec/abb
* ABB release notes, where 2.7.1 is the last version for DSM 7.0/7.1:
  https://www.synology.com/api/releaseNote/findChangeLog?identify=ActiveBackup&lang=en-us
* The ABB agent on a Proxmox host: https://forum.proxmox.com/threads/active-backup-for-business-agent.163433/
* ActiveProtect with Proxmox support: https://www.it-connect.tech/synology-apm-2-0-expands-to-proxmox-nutanix-and-more/

The DSM version, ABB version and ABB tasks of a NAS can be read through DSM's web API, read only.
