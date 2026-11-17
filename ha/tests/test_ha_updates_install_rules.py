"""The rules of installing Home Assistant's own updates as pure functions (ADR-0162 – ADR-0165, ADR-0181, ADR-0183):
the install switch in the options, the held version after a failed install and its place among the codes, when a kept
failure is cleared, the order (one at a time, Supervisor, Core, OS last, one system update per opening of the window),
which kinds need Home Assistant's backup, how long a run waits for the version, the window's opening, when an install
counts as reached, and when Home Assistant's backup is fresh enough."""

from __future__ import annotations

from dataclasses import replace
from datetime import datetime, time, timedelta, timezone
from typing import Any

import pytest

from custom_components.vordr import ha_updates as hu
from custom_components.vordr.ha_updates import Facts, Failure, Planned, Policy, Seen, plan

TZ = timezone(timedelta(hours=1))


def at(day: int, hour: int, minute: int = 0) -> datetime:
    """Day 0 is a Monday."""
    return datetime(2026, 11, 16 + day, hour, minute, tzinfo=TZ)


MON = at(0, 3)
RULES = Policy(
    kinds=frozenset({"app", "core", "os", "supervisor", "hacs"}),
    days=frozenset({"mon"}),
    start=time(2, 0),
    end=time(5, 0),
    min_age=timedelta(days=7),
    auto_install=True,
)


def fact(entity_id: str = "update.example_app", **over: Any) -> Facts:
    base: dict[str, Any] = {
        "entity_id": entity_id,
        "platform": "hassio",
        "kind": "app",
        "state": "on",
        "installed": "1.0.0",
        "latest": "1.1.0",
        "installable": True,
        "backup": True,
    }
    return Facts(**{**base, **over})


def planned(entity_id: str, kind: str = "app", code: str = hu.READY) -> Planned:
    return Planned(entity_id, kind, code, "1.0.0", "1.1.0")


def old(*facts: Facts) -> dict[str, Seen]:
    return {f.entity_id: Seen(f.latest or "", MON - timedelta(days=8)) for f in facts}


# --- the switch in the options


def test_installing_is_off_unless_switched_on() -> None:
    raw = {"kinds": ["core"], "entities": [], "days": ["mon"], "start": "02:00", "end": "05:00", "min_age_days": 7}
    assert hu.policy_from_options({"ha_updates": raw}).auto_install is False  # saved before it existed: off
    assert hu.check_options(raw)[0]["auto_install"] is False
    on = hu.policy_from_options({"ha_updates": {**raw, "auto_install": True}})
    assert on is not None and on.auto_install is True
    assert hu.check_options({**raw, "auto_install": True})[0]["auto_install"] is True
    assert Policy().auto_install is False and hu.CLOSED.auto_install is False


# --- a failed install holds its version


def test_a_failed_version_is_held_after_the_rules_of_the_options() -> None:
    f = fact()
    assert plan([f], old(f), RULES, MON, {f.entity_id: "1.1.0"})[0].code == hu.LAST_FAILED
    # Another version offered: tried again under every rule.
    newer = replace(f, latest="1.2.0")
    assert plan([newer], old(newer), RULES, MON, {f.entity_id: "1.1.0"})[0].code == hu.READY
    # Not allowed or a major version say so first: what the person decides comes before the hold.
    assert plan([f], old(f), replace(RULES, kinds=frozenset()), MON, {f.entity_id: "1.1.0"})[0].code == "not_allowed"
    major = replace(f, latest="2.0.0")
    assert plan([major], old(major), RULES, MON, {f.entity_id: "2.0.0"})[0].code == hu.MAJOR
    # The hold comes before age and window: a held version is not "waiting".
    assert plan([f], {}, RULES, at(0, 6), {f.entity_id: "1.1.0"})[0].code == hu.LAST_FAILED
    assert plan([f], old(f), RULES, MON)[0].code == hu.READY  # nothing held


def test_held_leaves_out_what_a_person_released() -> None:
    failures = {
        "update.a": Failure("update.a", "1.0", "1.1", hu.FAILED, MON),
        "update.b": Failure("update.b", "1.0", "1.2", hu.NOT_REACHED, MON, released=True),
    }
    assert hu.held(failures) == {"update.a": "1.1"}


def test_a_kept_failure_stays_until_an_install_goes_through() -> None:
    kept = {"update.example_app": Failure("update.example_app", "1.0.0", "1.1.0", hu.FAILED, MON)}
    registered = {"update.example_app"}
    # Still on the old version, offered the same or a newer one, unavailable, or not there but registered: stays.
    for f in (fact(), fact(latest="1.2.0"), fact(state="unavailable", installed=None, latest=None)):
        assert hu.clear_failures(kept, [f], registered) == kept, f
    assert hu.clear_failures(kept, [], registered) == kept
    assert hu.clear_failures(kept, [fact(state="off", installed="1.0.0", skipped="1.1.0")], registered) == kept
    # Reached the version (by hand, or late), or up to date with a later one: gone.
    assert hu.clear_failures(kept, [fact(installed="1.1.0", latest="1.2.0")], registered) == {}
    assert hu.clear_failures(kept, [fact(state="off", installed="1.2.0", latest="1.2.0")], registered) == {}
    assert hu.clear_failures(kept, [], set()) == {}  # gone from the registry


# --- the order


def test_one_at_a_time_and_the_system_last() -> None:
    items = [
        planned("update.os", "os"),
        planned("update.core", "core"),
        planned("update.zz_card", "hacs"),
        planned("update.supervisor", "supervisor"),
        planned("update.aa_app"),
        planned("update.waiting", code=hu.TOO_NEW),
    ]
    assert [p.entity_id for p in hu.install_order(items)] == [
        "update.aa_app",
        "update.zz_card",
        "update.supervisor",
        "update.core",
        "update.os",
    ]
    facts = {p.entity_id: fact(p.entity_id, kind=p.kind) for p in items}
    assert hu.next_install(items, facts, False, False).entity_id == "update.aa_app"
    # A system update only when nothing else is ready.
    system = [p for p in items if p.kind in hu.SYSTEM_KINDS]
    assert hu.next_install(system, facts, False, False).entity_id == "update.supervisor"
    # After a system update in this opening of the window: nothing more in it.
    assert hu.next_install(items, facts, True, False) is None
    assert hu.next_install(system, facts, True, False) is None


def test_nothing_starts_while_anything_installs() -> None:
    items = [planned("update.app"), planned("update.other", code=hu.IN_PROGRESS)]
    facts = {p.entity_id: fact(p.entity_id) for p in items}
    assert hu.next_install(items, facts, False, False) is None
    assert hu.next_install(items[:1], facts, False, False).entity_id == "update.app"


def test_after_a_failed_backup_only_entities_with_their_own_backup() -> None:
    items = [planned("update.a_card", "hacs"), planned("update.b_app")]
    facts = {"update.a_card": fact("update.a_card", kind="hacs", backup=False), "update.b_app": fact("update.b_app")}
    assert hu.next_install(items, facts, False, False).entity_id == "update.a_card"
    assert hu.next_install(items, facts, False, True).entity_id == "update.b_app"
    assert hu.next_install(items[:1], facts, False, True) is None


def test_device_firmware_and_radio_need_no_backup_of_home_assistant() -> None:
    """ADR-0181: a backup of Home Assistant cannot roll back a device's firmware; so they also go on after a failed
    backup. ESPHome, HACS and the others keep the rule; an entity with its own backup never needs Home Assistant's."""
    assert not hu.needs_ha_backup(fact(kind="firmware", platform="zha", backup=False))
    assert not hu.needs_ha_backup(fact(kind="radio", platform="homeassistant_sky_connect", backup=False))
    for kind in ("core", "os", "supervisor", "app", "hacs", "esphome", "other"):
        assert hu.needs_ha_backup(fact(kind=kind, backup=False)), kind
        assert not hu.needs_ha_backup(fact(kind=kind, backup=True)), kind
    items = [planned("update.a_card", "hacs"), planned("update.b_bulb", "firmware")]
    facts = {
        "update.a_card": fact("update.a_card", kind="hacs", platform="hacs", backup=False),
        "update.b_bulb": fact("update.b_bulb", kind="firmware", platform="zha", backup=False),
    }
    assert hu.next_install(items, facts, False, False).entity_id == "update.a_card"
    assert hu.next_install(items, facts, False, True).entity_id == "update.b_bulb"


def test_the_verification_timeout_per_kind() -> None:
    """ADR-0183: device firmware and radio 2 hours, all others 30 minutes."""
    assert hu.verify_timeout("firmware") == hu.verify_timeout("radio") == timedelta(hours=2)
    for kind in ("core", "os", "supervisor", "app", "hacs", "esphome", "other"):
        assert hu.verify_timeout(kind) == timedelta(minutes=30), kind


def test_never_vordrs_own_and_never_without_facts() -> None:
    items = [planned("update.vordr_golf_system", "other"), planned("update.unknown")]
    facts = {"update.vordr_golf_system": fact("update.vordr_golf_system", platform="vordr", kind="other")}
    assert hu.next_install(items, facts, False, False) is None


# --- the window's opening, reached, fresh


@pytest.mark.parametrize(
    ("now", "start"),
    [
        (at(0, 2), at(0, 2)),
        (at(0, 4, 59), at(0, 2)),
        (at(0, 5), None),
        (at(0, 1), None),
        (at(1, 3), None),  # Tuesday: not a window day
    ],
)
def test_the_opening_of_the_window(now: datetime, start: datetime | None) -> None:
    assert hu.window_start(now, RULES) == start


def test_the_opening_of_a_window_past_midnight_belongs_to_its_day() -> None:
    night = replace(RULES, start=time(23, 0), end=time(2, 0))
    assert hu.window_start(at(0, 23, 30), night) == at(0, 23)
    assert hu.window_start(at(1, 1, 30), night) == at(0, 23)  # Tuesday morning: Monday's window
    assert hu.window_start(at(1, 23, 30), night) is None  # Tuesday's own: not a window day
    assert hu.window_start(at(0, 1, 30), night) is None  # Monday morning: Sunday's, not chosen


@pytest.mark.parametrize(
    ("installed", "latest", "ok"),
    [
        ("1.1.0", "1.1.0", True),
        ("1.1.0", "1.2.0", True),  # the target, though a newer one is offered by now
        ("1.2.0", "1.2.0", True),  # up to date with a newer one
        ("1.0.0", "1.1.0", False),
        ("1.0.0", "1.0.0", False),  # still the old one, which calls itself up to date
        (None, "1.1.0", False),
    ],
)
def test_reached(installed: str | None, latest: str | None, ok: bool) -> None:
    assert hu.reached(installed, latest, "1.0.0", "1.1.0") is ok


def test_backup_fresh() -> None:
    assert hu.backup_fresh(MON - hu.BACKUP_MAX_AGE, MON)
    assert not hu.backup_fresh(MON - hu.BACKUP_MAX_AGE - timedelta(minutes=1), MON)
    assert hu.backup_fresh(MON, MON)
    assert not hu.backup_fresh(None, MON)
    assert not hu.backup_fresh(MON + timedelta(hours=1), MON)  # from the future: not a backup to trust
    assert timedelta(hours=26) == hu.BACKUP_MAX_AGE


def test_codes_are_fixed() -> None:
    assert hu.RESULTS == (
        "ok",
        "failed",
        "not_reached",
        "timeout",
        "backup_failed",
        "skipped_paused",
        "skipped_not_ready",
    )
    assert hu.KEPT == ("failed", "not_reached", "timeout")
    assert hu.PHASES == ("backing_up", "installing", "verifying")
    assert hu.SYSTEM_KINDS == ("supervisor", "core", "os")
    assert hu.LAST_FAILED in hu.CODES
