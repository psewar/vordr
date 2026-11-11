---
status: accepted
date: 2026-10-30
decision-makers: kay ("then please build both")
---

# Windows Update as a source on Windows, installed by button together with winget

Follows [ADR-0048](0048-winget-before-windows-update.md), which put winget first and Windows Update later.

## Context and Problem Statement

On Windows the agent reported and upgraded only the programs winget manages. Windows' own updates (cumulative
updates, .NET, Defender definitions) did not appear in Vördr: Home Assistant could not show that a Windows PC lacked
security updates, while it does so for every Linux host. The design listed the source `winupdate` as "later"; the
result code `failed_windows_update` already existed. A Windows PC could have a .NET security update and a Defender
definition update pending that Vördr did not show.

## Decision Drivers

* Home Assistant shows every pending update of a Windows PC, with security updates counted as on Linux.
* Fixed codes and facts instead of error texts ([ADR-0007](0007-fixed-codes-instead-of-error-texts.md)): Windows
  Update's own result codes and HRESULTs, never its messages.
* Windows' own automatic updates keep running; Vördr must not fight them.
* One install button per host, as for apt; hub and Home Assistant stay unchanged.
* Feature upgrades and drivers are no routine updates.

## Considered Options

* winget only, as before
* Windows Update reported, but not installed by Vördr
* Windows Update reported and installed by button, with one installer that combines both Windows sources

## Decision Outcome

Chosen option: report and install by button, with a combined installer (the operator, 2026-10-30).

* **Check:** the Windows Update Agent over COM (`Microsoft.Update.Session`) in PowerShell, like winget. Search
  `IsInstalled=0 and IsHidden=0 and Type='Software'`, so no drivers. Every update is a package: its name is the KB
  number (`KB5000001`), without one the update id; `to` carries the title, since Windows updates have no version;
  `security` means the classification "Security Updates".
* **Left out of the count,** each as a number in a fact:
  * feature upgrades (classification "Upgrades"): a new Windows version is no update of this one;
  * definition updates (classification "Definition Updates", Defender's security intelligence): Defender fetches them
    itself several times a day. Counted, one would be pending at almost every check, keep the host from ever being up
    to date and set `stale`;
  * optional updates (`BrowseOnly`, for example preview cumulative updates): Windows does not install them by itself,
    so they would stay pending for weeks and set `stale`;
  * updates that are installed and wait for a reboot: installing them again changes nothing.
  Classifications are matched by their fixed ids, not by their names, which Windows may translate.
* **Facts:** `install_context` as for winget; `windows_update_search` (`ok`, `failed`, `no_answer`); with `failed` the
  HRESULT as `windows_update_hresult`; the version of the Windows Update Agent. A failed search is `check_failed` with
  `failed_windows_update`, or `failed_package_manager` when PowerShell gave no answer at all.
* **Install by button only.** Windows gets no automatic installs, as before
  ([ADR-0080](0080-security-updates-everywhere-unattended-upgrades-off.md)), although Windows Update now classifies.
  The agent enforces it: on Windows, `check-config` and the agent refuse `install.auto: hub`, so the rule does not
  depend on how `agent.yaml` is written.
* **Scopes and the combined installer:** `security` installs updates of the classification "Security Updates" with
  Windows Update only, since winget does not classify. `all` installs everything the check counts with Windows Update,
  then runs winget. `app:<id>` stays winget's alone. Both parts of `all` run, whatever the first ended with, because
  they change different things. The run has one journal. Its result is the first code that is not `ok`, with the
  packages, details and logs of both parts, and `ok_reboot_required` if either part needs a reboot.
* **Install run of Windows Update:**
  * prechecks: not elevated (neither administrator nor LocalSystem) → `rejected_unsupported`; Windows Installer busy
    (the mutex winget checks too) or `IUpdateInstaller.IsBusy` → `rejected_busy`; less than 2 GiB free →
    `precheck_disk_space`, as for winget;
  * a fresh search makes the plan; nothing applicable is `ok` with nothing planned;
  * download (`IUpdateDownloader`) and installation (`IUpdateInstaller`) one update after the other, one JSON line per
    update with its `OperationResultCode`, `HResult` and `RebootRequired`;
  * 2 (succeeded) and 3 (succeeded with errors) are `ok`; 4 (failed), 5 (aborted), any other code and a COM call that
    throws are `failed_windows_update` with the HRESULTs in the detail; an update Windows no longer offers was
    installed meanwhile and counts as done;
  * verification: another search; an update of the plan still pending and not waiting for a reboot is
    `failed_verification`.
* **License terms:** the agent accepts the license terms (`AcceptEula`) of the updates in its plan, as a person does
  who clicks "Install" in Settings; the button in Home Assistant is the operator's consent. Each acceptance is in the
  run log. The other option, leaving such updates out, would keep them pending forever with nobody to accept them.
  Feature upgrades, which carry the Windows license, are never in a plan.
* **Reboot** stays manual; Windows has no reboot verb yet. The registry keys of Windows Update and Component Based
  Servicing stay the source of `reboot_required`.

### Consequences

* Good, because Home Assistant shows Windows' own pending updates and how many are security updates, and a PC that
  lacks them is noticed like a Linux host, including `stale`.
* Good, because one button installs the system and the programs, and a failure names Windows Update's HRESULT.
* Bad, because downloads are large (cumulative updates, full Defender packages) and a run can take long.
  `install.timeout_min` bounds the whole run: if Windows Update uses it up, the winget part ends with `timeout`.
* Bad, because most Windows updates need a reboot, which stays a manual step.
* Bad, because Windows' own automatic updates keep running. They may install an update before the run (it then
  counts as done) or hold the installer (`rejected_busy`).
* Bad, because Vördr does not notice when Defender stops fetching its definitions; it only counts the pending ones in
  the fact `windows_update_definitions`.
* Bad, because a failing Windows Update search marks the whole host `check_failed`, even when winget answered: the hub
  merges all sources of a host.
* Neutral, because optional updates are not reported; installing them stays a step in Settings.

### Confirmation

Unit tests with a fake PowerShell (`internal/source/winupdate`): parsing, security flag, the updates left out, every
result code mapping, busy, not elevated, verification. `internal/agent/wininstall_test.go`: security runs Windows
Update only, all runs both in order, `app:` never reaches Windows Update, the first failure wins, the reboot flag, one
journal. `TestAutoHubIsRefusedOnWindows` (`internal/policy`): `install.auto: hub` is an issue on Windows, not on Linux.
Counter-checks: mapping result code 4 to `ok` makes `TestInstallResults` fail; counting definition updates makes
`TestCheck` fail; allowing `hub` on Windows makes `TestAutoHubIsRefusedOnWindows` fail. `TestWinUpdateLive`
(`VORDR_WINUPDATE_LIVE=1`) runs the real search script read-only and refuses to reach the install script; on
a Windows PC it lists the pending updates and leaves definition updates out.
