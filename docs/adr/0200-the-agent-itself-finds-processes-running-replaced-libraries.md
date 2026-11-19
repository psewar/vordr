---
status: accepted
date: 2026-11-19
decision-makers: kay (2026-11-19, chose that the agent reads the processes itself instead of relying on needrestart or only correcting the design document; accepted with the merge); proposed by Claude
---

# The agent itself finds processes running replaced libraries

## Context and Problem Statement

The design named three reboot signals for apt hosts: the running kernel against the newest installed one, the
reboot-required file of update-notifier, and `needrestart -b` with the reason `libraries`. The third was never built:
needrestart runs only after an install, for the list of services still on old libraries in the run summary. In a
container (LXC) the kernel signal does not apply - the container runs the host's kernel - and Debian keeps no
reboot-required file. So no signal answered there, and `reboot_required` stayed unknown for good (`checked` empty).

Building the signal on needrestart as described would not change that where needrestart is not installed: Debian does
not install it by default, and minimal container templates lack it. Installing it on every host brings its apt hook
into every package run, which, depending on its configuration, restarts services by itself.

Meanwhile a process that started before an update of a library or program keeps running the old code - a fixed
vulnerability included - until it restarts. That is the case `libraries` was meant for, on virtual machines as much as
in containers.

## Decision Drivers

* A container gets an answer: yes or no instead of unknown.
* No new dependency on the hosts, and nothing that changes how a package run behaves.
* "No" only when it was checked: a host whose processes the agent cannot all see stays unknown
  ([ADR-0007](0007-fixed-codes-instead-of-error-texts.md): state as a fact).
* A host answers for its own processes, not for the containers on it.

## Considered Options

* the agent reads `/proc/<pid>/maps` itself
* `needrestart -b`, with needrestart installed on the hosts
* only correct the design document

## Decision Outcome

Chosen: the agent reads the processes itself. The reboot check of the apt source gets a third signal, `libraries`:

* A process counts when a line of its `/proc/<pid>/maps` maps a file that the kernel marks `(deleted)` - it was
  replaced on disk after the process mapped it - below `/usr/lib`, `/usr/lib32`, `/usr/lib64`, `/usr/libx32`,
  `/usr/libexec`, `/usr/bin`, `/usr/sbin`, `/lib`, `/lib32`, `/lib64`, `/libx32`, `/bin` or `/sbin`. Not counted:
  `/usr/local` (not the package manager's), the locale archive (data, not code), memory and temporary files
  (`/memfd:`, `/dev/shm`, `/tmp`). This is the evidence needrestart and checkrestart read.
* Only processes in the agent's own PID namespace with the same root count. The processes of the containers on a host
  belong to the containers, and a path under another root names another file.
* The signal answers only as root and only when every running process could be read. A process that has ended or is a
  zombie (state `Z` or `X` in its `stat`) runs no code and is passed over; a running process whose links or map cannot
  be read leaves the signal without an answer.
* The first process found settles it: `required` with the reason `libraries`. The reason is the weightiest that
  applies: `kernel` before `libraries` before `other`.
* It answers in containers as well as on virtual machines and physical hosts. Windows is unchanged.

needrestart keeps its place: after an install it names the services still on old libraries in the run summary, where
it is installed.

Rejected:

* **needrestart as the signal,** because it is missing where the answer is missing, and installing it changes every
  package run through its hook.
* **Only correcting the design document,** because containers would stay unknown and a process running an old library
  would go unnoticed on every host.

### Consequences

* Good, because containers answer: `reboot_required` is on or off there instead of unknown, and the hub's count of
  hosts needing a reboot includes them
  ([ADR-0188](0188-the-hub-device-counts-the-hosts-with-updates-and-a-reboot-due.md) treated a container as a host
  with nothing to wait for).
* Good, because a process still running an old library is seen on every apt host, not only at the next reboot by
  chance.
* Good, because no package and no tool is needed on the hosts, and an install run behaves as before.
* Bad, because the signal answers yes more often than the kernel did: after a library update every long-running process
  that uses it keeps the need until it restarts. The host shows the reboot repair from its first report
  ([ADR-0132](0132-a-needed-reboot-is-a-repair-from-the-first-report.md)), and an install that replaces such a library
  ends with `ok_reboot_required`.
* Bad, because restarting the services would often be enough, and Vördr offers only the reboot. The view names no
  process or service yet; that would need a field of its own.
* Bad, because the list of paths is the agent's own: code a package installs elsewhere is not seen.

### Confirmation

`internal/source/apt/libraries_test.go`, with a fake `/proc`: a container answers
(`TestRebootLibrariesAnswerInContainers`); a replaced library makes a reboot needed, in a container or not
(`TestRebootLibrariesReplaced`); which mapped files count (`TestReplacedPackageFile`); other namespaces and roots do not
count (`TestRebootLibrariesOnlyOwnNamespaceAndRoot`); ended processes do not stop the answer
(`TestRebootLibrariesEndedProcesses`); an unreadable running process (`TestRebootLibrariesUnreadableIsNoAnswer`), no
root or no `/proc` (`TestRebootLibrariesNeedRootAndProc`) leave it unanswered; the order of the reasons
(`TestRebootLibrariesReason`).
