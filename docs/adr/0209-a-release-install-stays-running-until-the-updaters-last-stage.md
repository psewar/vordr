---
status: proposed
date: 2026-11-21
decision-makers: proposed by Claude (2026-11-21) after kay asked for a pull request on the gap; accepting is kay's
---

# A release install stays running until the updater's last stage

Changes when the root job of [ADR-0205](0205-the-firewall-installs-through-its-own-updater-by-button.md) reports a
release install as ended. How the install is asked for, run and verified stays as ADR-0205 decided.

## Context and Problem Statement

ADR-0205 runs a release install with `pfSense-upgrade -y` and expects the updater to reboot the firewall while the
root job still runs it: the install stays `running`, the first root job after the reboot notes `rebooted`, and the
agent verifies against update facts read in the new boot.

pfSense's updater does not work that way. It takes a release in stages across reboots. After the first stage it notes
the next one as the annotation `next_stage` of the kernel package, starts the reboot in the background (ten seconds
later) and ends with 0. The later stages run while the firewall boots, and the last one deletes the note. A major
version takes one more reboot between stages.

The root job takes the 0 as the end and writes `install_state=ok`. The agent verifies at once, against facts read
before the reboot, in which the release's packages already count as installed, and reports the run as ok. The firewall
then reboots after the run has ended. A firewall that does not come back from that reboot, or a later stage that
fails, still stands as a good install.

How does the root job know that a release install is not over when the updater ends?

## Decision Drivers

* Ok only once the firewall runs the new release, verified against facts of the boot after the last stage.
* A decision on facts the device states, never on the updater's text
  ([ADR-0007](0007-fixed-codes-instead-of-error-texts.md)).
* The vendor's updater runs as the vendor runs it, with its own reboots (ADR-0205).
* The agent stays as it is: it already waits through `running` and a firewall that does not answer, and verifies on
  `rebooted`.

## Considered Options

* The root job reads the updater's own note, the annotation `next_stage` on the kernel package.
* The root job reads the updater's log for its announcement of the reboot.
* The agent waits for a boot other than the one the install began in before it verifies a release.
* The root job runs the updater with `-R` (no reboot) and reboots the firewall itself after the end.

## Decision Outcome

Chosen: the root job reads the updater's own note.

* After `pfSense-upgrade` ends with 0 for a release, the root job asks the package manager whether a kernel package
  (`pfSense-kernel-*`) carries the annotation `next_stage` (`pkg annotate -q -S <package> next_stage`). If one does,
  the install stays `running`; the reboot the updater started ends this boot.
* An install left `running` by an earlier run stays `running` while the updater holds its lock, as before, or while a
  release install's note is there. Only then does the rule of ADR-0205 apply: `rebooted` in another boot, `failed`
  with `interrupted` in the same one.
* Only a release waits on the note. An add-on install never reboots, and a note that a broken release upgrade left
  does not hold it.
* The agent is unchanged. It sees `running`, then a firewall that does not answer (phase `rebooting`), then
  `rebooted`, and verifies against the facts the root job reads in the boot after the last stage.

Rejected:
* **The updater's log:** its announcement is text for people, and its wording may change with any release of the
  updater.
* **A new boot before verifying:** the agent would verify after the first reboot although a later stage is still to
  come; between the stages of a major version the updater holds no lock. The note covers every stage.
* **No reboot by the updater:** it runs the vendor's updater in a way it calls dangerous, and the stages after the
  reboot remain the updater's anyway.

### Consequences

* Good, because a release install is ok only after the firewall came back with the new release, and the verify reads
  facts of that boot.
* Good, because the decision rests on the state the updater keeps for its own stages.
* Good, because agents of any version handle it: they already wait through `running` and `rebooted`.
* Bad, because a later stage that fails leaves the note, and the install stays `running` until the agent's run ends
  with its timeout (`install.timeout_min`). The firewall then needs people at its console or interface, as it would
  anyway.
* Bad, because the run now lasts as long as the reboots; `install.timeout_min` has to cover them.

### Confirmation

* `TestRootJobWaitsForTheUpdatersLastStage` in `deploy/pfsense`: a release whose updater ends 0 with the note stays
  `running`; the next run in the same boot without the updater's lock keeps it `running`; after the reboot, with the
  note gone, it is `rebooted` and the facts are read again. An add-on install with a note left is `ok`.
* The stand-in for `pkg` answers `annotate -q -S` from a file per package, the stand-in for `pfSense-upgrade` writes the
  note for a staged release.
