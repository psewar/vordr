---
status: accepted
date: 2026-11-19
decision-makers: kay (2026-11-19, asked to fix the text of the stale repair; accepted with the merge); proposed by Claude
---

# The stale repair names only a way that exists

## Context and Problem Statement

The hub marks a host `stale` once updates have been pending longer than `stale_after_days`, whatever the reason
([ADR-0014](0014-stale-after-seven-days.md)). The repair in Home Assistant said: "Install the updates, by hand or with
the button." On two kinds of hosts no button takes them:

* a host that allows no install through Vördr (neither capability `install` nor `install_security`), such as a device
  that Vördr only reads through a proxy instance;
* a host that takes installs through Vördr, but whose policy takes none of the pending updates: it allows only security
  updates and none of them is one - Docker's own repository is no security source
  ([ADR-0077](0077-hold-docker-packages.md)) - or its `install.mode` holds them back
  ([ADR-0127](0127-count-only-what-the-upgrade-mode-installs.md)).

There the update entity is off ([ADR-0106](0106-update-entity-on-only-when-vordr-would-install.md)), so the button the
text names is not shown. The reboot texts already made the same distinction (`reboot_pending_manual`).

## Decision Drivers

* A repair names a next step the operator can take on that host.
* Pending updates still reach someone who decides (ADR-0014, ADR-0077): the problem itself stays.
* No change to the hub or the protocol: the hub already sends `installable` and the capabilities.

## Considered Options

* text variants chosen from what the host lets Vördr install
* no `stale` for updates the host's policy excludes
* keep one text

## Decision Outcome

Chosen: text variants. The integration picks the key of a `stale` problem from the host's view:

* `stale` while the install button takes some of the pending updates (`installable` above zero), and when the hub sends
  no count (an older hub);
* `stale_policy` when `installable` is zero but the host offers an install (capability `install` or
  `install_security`): next step "install them by hand, or widen the install section of the host's agent.yaml";
* `stale_manual` when it offers none: next step "install the updates by hand - this host does not allow installs
  through Vördr".

The same key gives the repair, the problem sensor's `next_step` and the message of `vordr_problem`. When the key
changes while the problem stays the same, the repair is made again; this holds for the reboot variants too, which
before kept their old text until the next reconnect. `vordr_problem` does not fire for such a change: the problem did
not change.

Rejected:

* **No `stale` for excluded updates,** because ADR-0014 and ADR-0077 want pending updates to reach someone who decides;
  a host whose policy leaves out Docker's updates would then never say so.
* **One text,** because it sends the operator to a button that is not there.

### Consequences

* Good, because each repair says what can be done on that host and why there is no button.
* Good, because a repair follows a change of the host's capabilities at once, the reboot texts included.
* Bad, because there are two more texts in every language.
* Bad, because a set the button takes only in part keeps "by hand or with the button".

### Confirmation

`ha/tests/test_texts.py`: the choice of the key from capabilities and `installable`
(`test_stale_key_follows_what_vordr_installs`), every language with both texts and their next step
(`test_problem_texts_complete_and_consistent`). `ha/tests/test_entities.py`: repair and problem sensor follow the host
without a new `vordr_problem` (`test_stale_hint_names_a_way_that_exists`), and the reboot repair follows the
capability (`test_reboot_hint_follows_the_capability`).
