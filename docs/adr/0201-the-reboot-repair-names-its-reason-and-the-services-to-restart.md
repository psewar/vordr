---
status: accepted
date: 2026-11-19
decision-makers: kay (2026-11-19, asked that the reboot repair name its reason and the affected services; accepted with the merge); proposed by Claude
---

# The reboot repair names its reason and the services to restart

## Context and Problem Statement

A host that needs a reboot is a repair in Home Assistant from its first report
([ADR-0132](0132-a-needed-reboot-is-a-repair-from-the-first-report.md)). Its text was the same for every reason:
"Updates take full effect only after a reboot". Since the agent finds processes still running replaced libraries
([ADR-0200](0200-the-agent-itself-finds-processes-running-replaced-libraries.md)), `libraries` is the most frequent
reason, and for it a reboot is the largest remedy: restarting the services that still run the old code is often
enough. On a hypervisor, a reboot stops every guest. The operator could not see which services they were; the view
carried the reason as a code and nothing about the processes.

## Decision Drivers

* The repair says why, in the reader's language, and names what can be done short of a reboot.
* Texts users see come from the translations; the hub sends codes and data
  ([ADR-0007](0007-fixed-codes-instead-of-error-texts.md)).
* An older hub or agent keeps working: without the data, the general text stays.
* The report stays bounded ([ADR-0195](0195-the-hub-bounds-reports-inventories-and-the-home-assistant-hello.md)).

## Considered Options

* the agent names the services, and the repair has a text per reason
* the agent names the packages behind the replaced files instead of the services
* one text with the reason as a code in a placeholder

## Decision Outcome

Chosen: the agent names the services, and the repair has a text per reason.

* **Agent:** the reboot part of the report gets `services`. Each process the library signal counts is named by its
  systemd service - the innermost `.service` of its cgroup, from the unified hierarchy or systemd's own in cgroup v1,
  a unit of a user's manager included - or by its process name where it belongs to no service (a login session,
  systemd itself). Sorted, each once, at most 32 (`MaxRebootServices`). A process that ends before it is named runs no
  old code and is left out. The list is also sent when another reason wins: restarting them is still due.
* **Hub:** `reboot.services` is bounded like the other names (at most 32, each a name text); out of bounds the list
  alone is dropped. The view carries it as `reboot.services`.
* **Integration:** the reboot repair takes the reason's own text where the view has what that text names:
  * `reboot_required_libraries` / `reboot_required_manual_libraries`: the services, and that restarting them would be
    enough;
  * `reboot_required_kernel` / `reboot_required_manual_kernel`: the running and the newest kernel, where they differ;
  * `reboot_required_manual_windows_update`: Windows Update waits for the reboot (by hand only: the Windows agent does
    not reboot);
  * every other case - `other`, a reason without its data, an older hub or agent - keeps `reboot_required` /
    `reboot_required_manual`.

  The texts with a fix share the general one's dialog and refusals; the dialog names the services and kernels as the
  host last reported them. A change of the reason or of the services makes the repair again. The sensor "Reboot
  required" shows `services` as an attribute.

Rejected:

* **Packages instead of services,** because the package behind a deleted file is not found reliably (on merged-`/usr`
  systems the package database lists `/lib/...` where the process maps `/usr/lib/...`), and the operator restarts
  services, not packages.
* **The reason as a code in one text,** because the reader would see `libraries` instead of a sentence in their
  language, and the text could not say what to restart.

### Consequences

* Good, because the repair says why and, for libraries, which services to restart instead of rebooting.
* Good, because hub, integration and agent can be updated in any order: without the data the general text stays.
* Bad, because there are five more reboot texts in every language, and two of them repeat the dialog's refusals.
* Bad, because a list cut at 32 names does not say that it was cut.

### Confirmation

`internal/source/apt/libraries_test.go`: the services and process names, each once and sorted
(`TestRebootLibrariesNamesTheServices`), the cgroup forms (`TestServiceOf`), a process that ended before its name
(`TestRebootLibrariesAProcessEndedBeforeItsName`), the bound (`TestRebootLibrariesNamesAtMostTheBound`), the list under
the kernel reason (`TestRebootLibrariesReason`). `internal/hub/bounds_test.go` and `view_test.go`: the bound of
`reboot.services` and the view. `ha/tests/test_texts.py`: every reboot text in every language with its placeholders
and the general refusals (`test_reboot_texts_per_reason`), the choice of the key (`test_reboot_key_follows_the_reason`).
`ha/tests/test_repairs.py`: the repair per reason, its dialog, and the repair made again on a change
(`test_repair_names_its_reason`).
