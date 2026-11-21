---
status: accepted
date: 2026-11-21
decision-makers: kay (2026-11-21, "Ja, PR bauen" on the proposal of a repair "stay on this branch"); proposed by Claude
---

# A release in another branch can be held from Home Assistant

Builds on [ADR-0204](0204-the-firewall-reports-what-its-own-updater-offers.md) (a release the device offers only in
another branch is pending and kept back), [ADR-0207](0207-the-hub-says-why-updates-are-held-back.md) (the hub says
why) and [ADR-0014](0014-stale-after-seven-days.md) (updates pending longer than `stale_after_days` are the problem
`stale`). Changes what counts for `stale`: a release the operator holds does not.

## Context and Problem Statement

A device such as a firewall may offer its next release only in another branch of its updates. Vördr installs only
within the configured branch; switching it is the operator's step in the device's own settings (ADR-0204, ADR-0205).
Staying on the current branch for a while is a normal decision, for instance until the new release has been tried
elsewhere. Yet the release counts as pending, and after `stale_after_days` the host has the problem `stale` and a
repair that Vördr cannot fix: Home Assistant offers only "Ignore". Ignoring hides the repair in Home Assistant alone;
the problem sensor stays on, an automation on it keeps alerting, and a newer release later is not news.

How does the operator say "stay on this branch for now" so that the problem ends, without losing sight of the release
or of the next one?

## Decision Drivers

* The decision belongs to the operator and is made where the repair shows, in Home Assistant.
* It holds exactly the release the operator saw: a newer one is news again.
* The release stays visible as pending and kept back; only the stale clock leaves it out.
* The hub decides on its own register and reports the hold as a fact; nothing on the device changes.
* An older hub or integration keeps working: the repair keeps its text and stays without a fix.

## Considered Options

* A hold at the hub for exactly the offered release, set by the repair's fix, ended by the hub when the offer changes.
* Switch the device's branch through Vördr.
* Never count a release in another branch for `stale`.
* Leave it to "Ignore" in Home Assistant.

## Decision Outcome

Chosen: a hold at the hub for exactly the offered release.

* The host's view names the release the host offers only in another branch, `other_branch` (the kept-back package of a
  source whose `kept_back_why` is `other_branch`, ADR-0207; empty where there is more than one), and a held release,
  `held_release`.
* Home Assistant sends `hold_release` with the node and that release, only to a hub whose status names the feature
  `hold_release`. The hub names it for a client approved with role `operate` or `admin`, as for the install button: it
  decides about the host's updates and changes nothing on the host.
* The hub holds the release only while the host's last report offers exactly it (`not_offered` otherwise) and keeps the
  hold in its register with the client and the time. While the held release is all that is pending, nothing counts
  as pending too long, and `stale` ends; other pending updates count as before.
* The hub ends the hold by itself once a complete report no longer offers exactly that release: a newer release in the
  other branch, another branch, or nothing pending. What is pending then counts from that report. The event log has
  `release_hold`, `release_hold_refused` and `release_hold_ended`.
* `vordr-hub unhold <node>` ends a hold by hand; the release counts again from then on. Home Assistant has no button
  to end it: the repair is gone while the hold lasts, and the hold ends by itself with any change of the offer.
* The integration makes the problem repair fixable (`hold_release`) where the problem is `stale` alone, every pending
  update is that release (`stale_other_branch`, ADR-0207), the hub names it and the feature. The fix dialog names the
  release and the two ways: switch the branch, or hold it. The count of pending updates names the held release
  (attribute `held_release`).

Rejected:
* **Switch the branch through Vördr:** a change of the device's update configuration through a new verb and the root
  job, right before a release install with its reboot. A new release line is a deliberate step, made in the device's
  own settings, and the release install waits for its test anyway (ADR-0205).
* **Never count another branch:** a new release line would never be news; the stale problem is the reminder that the
  device runs an older line, and a site that wants none can hold each release once.
* **Leave it to "Ignore":** Home Assistant only; the problem sensor and its automations stay, and a newer release is
  not shown again.

### Consequences

* Good, because staying on a branch is one confirmed step in the repair, and the problem ends with it.
* Good, because a newer release, a switched branch and an install each end the hold without anyone remembering it.
* Good, because the release stays visible: pending, kept back, and named as held.
* Bad, because ending a hold early needs the hub's command line; Home Assistant has no button for it.
* Bad, because one more message and feature in the Home Assistant protocol and one more field in the register.
* Bad, because a host that offers more than one release in other branches gets no `other_branch` and no fix. No
  supported device does today.

### Confirmation

* `internal/hub/hold_test.go`: `TestHoldRelease` (only the offered release is held, the view and the stale problem,
  refusals, the hold survives a restart), `TestHoldReleaseFollowsTheReports` (kept by the same release, other updates
  count, ended by a newer release or nothing pending, a failed check changes nothing), `TestHoldReleaseEndLogged`,
  `TestUnhold`, `TestHoldReleaseFromHomeAssistant` (roles, form of the version, event log) and
  `TestHoldReleaseOverTheHAProtocol`.
* `TestApproveHostFeature` and the other feature tests in `internal/hub`: `hold_release` for operate and admin only.
* `ha/tests/test_repairs.py`: the fix holds the named release and the repair goes; no fix without the feature, without
  the release or with another reason, following the status at once; a newer release before confirming sends nothing;
  the hub's refusals say why.
