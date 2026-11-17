"""The plan for Home Assistant's own updates as pure functions (ADR-0158, ADR-0159, ADR-0160): every reason code,
their order, the window (also past midnight), the minimum age and its boundary, the age a release inherits from its
line, the longer minimum age of device firmware and radio, major jumps, kinds, first sights, and the check of the
options."""

from __future__ import annotations

from dataclasses import replace
from datetime import datetime, time, timedelta, timezone
from typing import Any

import pytest

from custom_components.vordr import ha_updates as hu
from custom_components.vordr.ha_updates import CLOSED, Facts, Policy, Seen, in_window, plan, remember, window_from

TZ = timezone(timedelta(hours=1))  # a zone without daylight saving: every day has 24 hours


def at(day: int, hour: int, minute: int = 0) -> datetime:
    """Day 0 is a Monday."""
    return datetime(2026, 11, 16 + day, hour, minute, tzinfo=TZ)


MON = at(0, 3)  # Monday 03:00, inside the window of RULES
APP = "update.example_app"
RULES = Policy(
    kinds=frozenset({"app", "core"}),
    days=frozenset({"mon"}),
    start=time(2, 0),
    end=time(5, 0),
    min_age=timedelta(days=7),
)
OLD = {APP: Seen("1.1.0", MON - timedelta(days=8))}  # first seen long enough ago


def fact(**over: Any) -> Facts:
    """An app of the Supervisor with a pending update it can install."""
    base: dict[str, Any] = {
        "entity_id": APP,
        "platform": "hassio",
        "kind": "app",
        "state": "on",
        "installed": "1.0.0",
        "latest": "1.1.0",
        "installable": True,
    }
    return Facts(**{**base, **over})


def one(
    f: Facts,
    seen: dict[str, Seen] | None = None,
    p: Policy = RULES,
    now: datetime = MON,
    lines: dict[str, dict[str, datetime]] | None = None,
) -> Any:
    """(code, earliest) of the one entity, None when the plan leaves it out."""
    out = plan([f], OLD if seen is None else seen, p, now, lines=lines)
    assert len(out) <= 1
    return (out[0].code, out[0].earliest) if out else None


def test_the_test_days() -> None:
    assert MON.weekday() == 0 and at(4, 0).weekday() == 4


def test_ready_when_allowed_old_enough_and_in_the_window() -> None:
    [p] = plan([fact()], OLD, RULES, MON)
    assert p == hu.Planned(APP, "app", "ready", "1.0.0", "1.1.0", MON - timedelta(days=8), None)


def test_nothing_is_allowed_by_default() -> None:
    assert one(fact(), p=CLOSED) == ("not_allowed", None)
    assert Policy() == CLOSED and not CLOSED.kinds and not CLOSED.entities and not CLOSED.days


def test_not_allowed_unless_kind_or_entity_is_chosen() -> None:
    hacs = fact(entity_id="update.example_card", platform="hacs", kind="hacs")
    seen = {"update.example_card": Seen("1.1.0", MON - timedelta(days=8))}
    assert one(hacs, seen) == ("not_allowed", None)
    # A single entity besides the kinds: this one, not the others of its kind.
    single = replace(RULES, entities=frozenset({"update.example_card"}))
    assert one(hacs, seen, single) == ("ready", None)
    other = replace(hacs, entity_id="update.other_card")
    assert one(other, {"update.other_card": seen["update.example_card"]}, single) == ("not_allowed", None)


def test_outside_the_window_with_its_next_start() -> None:
    assert one(fact(), now=at(0, 1, 30)) == ("outside_window", at(0, 2))  # before the start today
    assert one(fact(), now=at(0, 5)) == ("outside_window", at(7, 2))  # the end: next Monday
    assert one(fact(), now=at(2, 3)) == ("outside_window", at(7, 2))  # Wednesday, in the hours but not the day
    two_days = replace(RULES, days=frozenset({"mon", "wed"}))
    assert one(fact(), p=two_days, now=at(0, 6)) == ("outside_window", at(2, 2))


def test_a_window_without_days_or_length_never_opens() -> None:
    no_days = replace(RULES, days=frozenset())
    assert one(fact(), p=no_days) == ("outside_window", None)
    no_length = replace(RULES, end=time(2, 0))
    assert one(fact(), p=no_length) == ("outside_window", None)
    assert not in_window(at(0, 2), no_length) and window_from(at(0, 1), no_length) is None


def test_window_past_midnight_belongs_to_the_day_it_starts_on() -> None:
    night = replace(RULES, days=frozenset({"fri"}), start=time(22, 0), end=time(4, 0))
    inside = [at(4, 22), at(4, 23, 59), at(5, 0), at(5, 3, 59)]  # Friday 22:00 to Saturday 03:59
    outside = [at(4, 21, 59), at(5, 4), at(5, 22, 30), at(3, 23), at(4, 1)]  # Friday 01:00: Thursday's night
    assert [in_window(t, night) for t in inside] == [True] * len(inside)
    assert [in_window(t, night) for t in outside] == [False] * len(outside)
    assert window_from(at(5, 3), night) == at(5, 3)  # open now
    assert window_from(at(5, 4), night) == at(11, 22)  # next Friday
    assert one(fact(), p=night, now=at(5, 3)) == ("ready", None)
    assert one(fact(), p=night, now=at(5, 4)) == ("outside_window", at(11, 22))


def test_too_new_until_the_minimum_age_and_then_the_window() -> None:
    # First seen 6 days ago: old enough on Tuesday 03:00, outside the window; ready from next Monday 02:00.
    six_days = {APP: Seen("1.1.0", MON - timedelta(days=6))}
    assert one(fact(), six_days) == ("too_new", at(7, 2))
    # Old enough in 30 minutes, inside today's window: that is the earliest time.
    almost = {APP: Seen("1.1.0", MON - timedelta(days=7) + timedelta(minutes=30))}
    assert one(fact(), almost) == ("too_new", at(0, 3, 30))
    # Without a window: too new, and no time it would be ready.
    assert one(fact(), six_days, replace(RULES, days=frozenset())) == ("too_new", None)


def test_the_age_boundary() -> None:
    exactly = {APP: Seen("1.1.0", MON - timedelta(days=7))}
    assert one(fact(), exactly) == ("ready", None)
    a_minute_short = {APP: Seen("1.1.0", MON - timedelta(days=7) + timedelta(minutes=1))}
    assert one(fact(), a_minute_short) == ("too_new", MON + timedelta(minutes=1))
    longer = replace(RULES, min_age=timedelta(days=14))  # old enough on Tuesday 03:00: next Monday's window
    assert one(fact(), {APP: Seen("1.1.0", MON - timedelta(days=13))}, longer) == ("too_new", at(7, 2))


@pytest.mark.parametrize(
    ("version", "line"),
    [
        ("1.4.2", "1.4"),
        ("v1.4.2", "1.4"),  # a tag as HACS shows it
        ("V2.0", "2.0"),
        ("2026.11.1", "2026.11"),  # Core, Supervisor, ESPHome: year and month
        ("16.3", "16.3"),  # OS
        ("1.02.3", "1.2"),
        ("1.2.3.4", "1.2"),
        ("0x01020304", None),  # firmware as one number
        ("2026.12.0b1", None),  # a pre-release
        ("1.2.3-beta.1", None),
        ("1.2.3+build5", None),
        ("abc1234", None),  # a commit
        ("17", None),  # a single number
        ("1.2.", None),
        ("vv1.2", None),
        (" 1.2.3", None),
        ("１.２", None),  # digits, but not ASCII
        ("v", None),
        ("", None),
        (None, None),
    ],
)
def test_the_line_of_a_version(version: str | None, line: str | None) -> None:
    assert hu.line_of(version) == line


def patch(**over: Any) -> Facts:
    """A patch release 1.1.2 of the line 1.1, with 1.1.0 installed."""
    return fact(installed="1.1.0", latest="1.1.2", **over)


def test_a_patch_release_inherits_the_age_of_its_line() -> None:
    """The line 1.1 first seen 8 days ago, its release 1.1.2 two days ago: old enough by its line."""
    lines = {APP: {"1.1": MON - timedelta(days=8)}}
    seen = {APP: Seen("1.1.2", MON - timedelta(days=2))}
    [p] = plan([patch()], seen, RULES, MON, lines=lines)
    assert (p.code, p.earliest) == ("ready", None)
    assert (p.first_seen, p.line_seen) == (MON - timedelta(days=2), MON - timedelta(days=8))
    # Without its line, the same release waits its own age: old enough on Saturday, the next Monday's window.
    assert one(patch(), seen) == ("too_new", at(7, 2))


def test_a_patch_release_waits_two_days_itself() -> None:
    lines = {APP: {"1.1": MON - timedelta(days=30)}}
    young = {APP: Seen("1.1.2", MON - timedelta(days=2) + timedelta(minutes=1))}
    assert one(patch(), young, lines=lines) == ("too_new", MON + timedelta(minutes=1))
    exactly = {APP: Seen("1.1.2", MON - timedelta(days=2))}
    assert one(patch(), exactly, lines=lines) == ("ready", None)
    # Not seen before: its own age starts now, two days, then the next window.
    assert one(patch(), {}, lines=lines) == ("too_new", at(7, 2))
    # Never longer than the minimum age.
    one_day = replace(RULES, min_age=timedelta(days=1))
    assert one(patch(), {APP: Seen("1.1.2", MON - timedelta(days=1))}, one_day, lines=lines) == ("ready", None)


def test_the_line_still_needs_the_full_minimum_age() -> None:
    """The line first seen not quite 7 days ago: its age decides, not the patch bound."""
    lines = {APP: {"1.1": MON - timedelta(days=7) + timedelta(minutes=30)}}
    seen = {APP: Seen("1.1.2", MON - timedelta(days=2))}
    assert one(patch(), seen, lines=lines) == ("too_new", at(0, 3, 30))


def test_a_new_line_starts_fresh() -> None:
    """A minor bump is a line of its own: 1.2.0 waits its full age, however old the line 1.1 is."""
    minor = fact(installed="1.1.5", latest="1.2.0")
    seen = {APP: Seen("1.2.0", MON - timedelta(days=2))}
    assert one(minor, seen, lines={APP: {"1.1": MON - timedelta(days=60)}}) == ("too_new", at(7, 2))
    # Its line, first seen together with it, gives nothing to inherit.
    both = {APP: {"1.1": MON - timedelta(days=60), "1.2": MON - timedelta(days=2)}}
    [p] = plan([minor], seen, RULES, MON, lines=both)
    assert (p.code, p.earliest, p.line_seen) == ("too_new", at(7, 2), None)


@pytest.mark.parametrize(
    ("installed", "latest"),
    [("0x01020304", "0x01020305"), ("2026.12.0", "2026.12.1b1"), ("abc1234", "def5678")],
)
def test_a_version_without_a_line_waits_its_own_age(installed: str, latest: str) -> None:
    lines = {APP: {"2026.12": MON - timedelta(days=60), "1.1": MON - timedelta(days=60)}}
    seen = {APP: Seen(latest, MON - timedelta(days=2))}
    [p] = plan([fact(installed=installed, latest=latest)], seen, RULES, MON, lines=lines)
    assert (p.code, p.earliest, p.line_seen) == ("too_new", at(7, 2), None)


@pytest.mark.parametrize(("kind", "platform"), [("firmware", "zha"), ("radio", "homeassistant_sky_connect")])
def test_device_firmware_and_radio_wait_at_least_14_days(kind: str, platform: str) -> None:
    rules = replace(RULES, kinds=frozenset({kind, "app"}))
    f = fact(kind=kind, platform=platform, installed="0x01", latest="0x02")

    def seen(days: int) -> dict[str, Seen]:
        return {APP: Seen("0x02", MON - timedelta(days=days))}

    assert rules.min_age_for(kind) == timedelta(days=14) and rules.min_age_for("app") == timedelta(days=7)
    assert one(f, seen(13), rules) == ("too_new", at(7, 2))  # old enough on Tuesday: the next Monday's window
    assert one(f, seen(14), rules) == ("ready", None)
    assert one(fact(), {APP: Seen("1.1.0", MON - timedelta(days=7))}, rules) == ("ready", None)  # an app: 7 days
    # A longer minimum age of the options holds for them too.
    longer = replace(rules, min_age=timedelta(days=30))
    assert longer.min_age_for(kind) == timedelta(days=30)
    assert one(f, seen(29), longer) == ("too_new", at(7, 2))
    assert one(f, seen(30), longer) == ("ready", None)


def test_a_firmware_line_inherits_after_14_days() -> None:
    rules = replace(RULES, kinds=frozenset({"firmware"}))
    f = fact(kind="firmware", platform="zha", installed="1.4.0", latest="1.4.1")
    seen = {APP: Seen("1.4.1", MON - timedelta(days=2))}
    assert one(f, seen, rules, lines={APP: {"1.4": MON - timedelta(days=14)}}) == ("ready", None)
    assert one(f, seen, rules, lines={APP: {"1.4": MON - timedelta(days=10)}}) == ("too_new", at(7, 2))


def test_first_sight_of_another_version_does_not_count() -> None:
    """The age belongs to the version: a first sight of an older offer, or none, starts it now."""
    assert one(fact(), {APP: Seen("1.0.5", MON - timedelta(days=30))}) == ("too_new", at(7, 3))
    assert one(fact(), {}) == ("too_new", at(7, 3))
    [p] = plan([fact()], {}, RULES, MON)
    assert p.first_seen is None


@pytest.mark.parametrize(
    ("installed", "latest", "code"),
    [
        ("1.9.3", "2.0.0", "major"),
        ("v1.9", "v2.0", "major"),  # tags as HACS shows them
        ("16.3", "17.0", "major"),
        ("2026.10.4", "2026.11.0", "ready"),  # a calendar version: a month, not a break
        ("1.0.0", "1.1.0", "ready"),
    ],
)
def test_a_major_version_stays_manual(installed: str, latest: str, code: str) -> None:
    seen = {APP: Seen(latest, MON - timedelta(days=8))}
    assert one(fact(installed=installed, latest=latest), seen) == (code, None)


def test_skipped_in_home_assistant() -> None:
    assert one(fact(state="off", skipped="1.1.0")) == ("skipped", None)
    # Off without the skip of this version: up to date, or an older skip Home Assistant drops - nothing to plan.
    assert one(fact(state="off", installed="1.1.0")) is None
    assert one(fact(state="off", skipped="1.0.5")) is None


def test_reported_but_never_installed() -> None:
    assert one(fact(auto_update=True)) == ("self_updating", None)
    assert one(fact(in_progress=True)) == ("in_progress", None)
    assert one(fact(installable=False)) == ("no_install", None)


def test_vordrs_own_entities_are_not_part_of_it() -> None:
    """By the registry's platform: allowed by kind or by name, still left out."""
    own = fact(entity_id="update.vordr_example_system", platform="vordr", kind="other")
    allowing = replace(RULES, kinds=frozenset(hu.KINDS), entities=frozenset({own.entity_id}))
    assert one(own, {}, allowing) is None
    named_alike = replace(own, platform="hacs", kind="hacs")  # e.g. HACS's entity for the integration itself
    assert one(named_alike, {}, allowing) == ("too_new", at(7, 3))


def test_unknown_or_unavailable_is_left_out() -> None:
    assert one(fact(state="unavailable", installed=None, latest=None)) is None
    assert one(fact(state="unknown")) is None
    assert one(fact(installed=None)) is None


def test_the_order_of_the_reasons() -> None:
    """What Home Assistant or the entity decides comes first, then the options, the major version, the age, the
    window."""
    f = fact(
        in_progress=True,
        auto_update=True,
        installable=False,
        kind="hacs",
        platform="hacs",
        installed="1.0.0",
        latest="2.0.0",
    )
    steps = [
        ({"in_progress": False}, "self_updating"),
        ({"auto_update": False}, "no_install"),
        ({"installable": True}, "not_allowed"),
        ({"kind": "app"}, "major"),
        ({"latest": "1.1.0"}, "too_new"),
    ]
    now = at(0, 6)
    assert one(f, {}, now=now)[0] == "in_progress"
    for change, code in steps:
        f = replace(f, **change)
        assert one(f, {}, now=now)[0] == code, change
    assert one(f, now=now)[0] == "outside_window"
    assert one(f) == ("ready", None)


def test_the_plan_is_sorted_and_complete() -> None:
    facts = [fact(entity_id=f"update.app_{c}") for c in "cab"] + [fact(entity_id="update.done", state="off")]
    out = plan(facts, {}, RULES, MON)
    assert [p.entity_id for p in out] == ["update.app_a", "update.app_b", "update.app_c"]


@pytest.mark.parametrize(
    ("installed", "latest", "jump"),
    [
        ("1.4.2", "2.0.1", True),
        ("v2.9.0", "v3.0.0", True),
        ("V1", "v2", True),
        ("1.9", "v2.0", True),
        ("16.3", "17.0", True),
        ("2.0.0", "2.1.0", False),
        ("2026.10.4", "2026.11.0", False),
        ("999.1", "1000.0", False),
        ("0x01020304", "0x01020305", False),
        ("abc", "2.0", False),
        (None, "2.0", False),
        ("1.0", None, False),
        ("", "1", False),
        ("v", "1", False),
    ],
)
def test_major_jump(installed: str | None, latest: str | None, jump: bool) -> None:
    assert hu.major_jump(installed, latest) is jump


@pytest.mark.parametrize(
    ("platform", "device_class", "identifiers", "kind"),
    [
        ("hassio", None, {("hassio", "core")}, "core"),
        ("hassio", None, {("hassio", "OS")}, "os"),
        ("hassio", None, {("hassio", "supervisor")}, "supervisor"),
        ("hassio", None, {("hassio", "core_mosquitto")}, "app"),
        ("hassio", None, set(), "other"),
        ("hacs", None, set(), "hacs"),
        ("marketplace", None, set(), "hacs"),
        ("esphome", "firmware", {("esphome", "example-node")}, "esphome"),
        ("zha", "firmware", {("zha", "example-bulb")}, "firmware"),
        ("homeassistant_sky_connect", "firmware", set(), "radio"),
        ("homeassistant_yellow", "firmware", set(), "radio"),
        ("frigate", None, set(), "other"),
        (None, None, set(), "other"),
    ],
)
def test_kind_from_the_registries(
    platform: str | None, device_class: str | None, identifiers: set[tuple[str, str]], kind: str
) -> None:
    assert hu.kind_of(platform, device_class, identifiers) == kind


def test_first_sight_per_version() -> None:
    t1 = MON + timedelta(hours=1)
    first = remember({}, [fact()], set(), MON)
    assert first == {APP: Seen("1.1.0", MON)}
    assert remember(first, [fact()], set(), t1) == first  # seen before: kept
    assert remember(first, [fact(latest="1.2.0")], set(), t1) == {APP: Seen("1.2.0", t1)}  # moved on
    assert remember(first, [fact(state="off", installed="1.1.0")], set(), t1) == {}  # installed
    assert remember({}, [fact(state="off", skipped="1.1.0")], set(), MON) == first  # skipped: still offered
    # Unavailable or unknown, or not there but still registered: the version did not move on.
    gone = fact(state="unavailable", installed=None, latest=None)
    assert remember(first, [gone], set(), t1) == first
    assert remember(first, [], {APP}, t1) == first
    assert remember(first, [], set(), t1) == {}  # removed from the registry
    assert remember({}, [fact(platform="vordr")], set(), MON) == {}  # Vördr's own: not counted


def test_first_sight_per_line() -> None:
    t1, t2 = MON + timedelta(hours=1), MON + timedelta(days=3)
    first = hu.remember_lines({}, [fact()], set(), MON)  # 1.0.0 installed, 1.1.0 offered: both lines seen
    assert first == {APP: {"1.0": MON, "1.1": MON}}
    # Installed: its line stays, the line no longer reported goes.
    done = hu.remember_lines(first, [fact(state="off", installed="1.1.0", latest="1.1.0")], set(), t1)
    assert done == {APP: {"1.1": MON}}
    # A patch release of it: the line keeps its first sight. A new line: seen now.
    assert hu.remember_lines(done, [patch()], set(), t2) == {APP: {"1.1": MON}}
    minor = fact(installed="1.1.0", latest="1.2.0")
    assert hu.remember_lines(done, [minor], set(), t2) == {APP: {"1.1": MON, "1.2": t2}}
    # Up to date when first seen: the installed line counts as seen.
    up_to_date = fact(state="off", installed="1.1.0", latest="1.1.0")
    assert hu.remember_lines({}, [up_to_date], set(), MON) == {APP: {"1.1": MON}}
    # Versions without a line: nothing kept.
    assert hu.remember_lines(first, [fact(installed="0x01", latest="0x02")], set(), t1) == {}
    # Unavailable, or not there but still registered: kept. Gone from the registry: dropped. Vördr's own: not counted.
    gone = fact(state="unavailable", installed=None, latest=None)
    assert hu.remember_lines(first, [gone], set(), t1) == first
    assert hu.remember_lines(first, [], {APP}, t1) == first
    assert hu.remember_lines(first, [], set(), t1) == {}
    assert hu.remember_lines({}, [fact(platform="vordr")], set(), MON) == {}


FLOW_INPUT = {  # as the selectors send it
    "kinds": ["hacs", "core", "core"],
    "entities": ["update.example_tool"],
    "days": ["wed", "mon"],
    "start": "22:30:00",
    "end": "04:00:00",
    "min_age_days": 7.0,
}


def test_options_as_stored_and_their_rules() -> None:
    clean, errors = hu.check_options(FLOW_INPUT)
    assert errors == {}
    assert clean == {
        "kinds": ["core", "hacs"],
        "entities": ["update.example_tool"],
        "days": ["mon", "wed"],
        "start": "22:30",
        "end": "04:00",
        "min_age_days": 7,
        "auto_install": False,  # missing, as in options saved before installing existed: off
    }
    assert hu.policy_from_options({"ha_updates": clean}) == Policy(
        frozenset({"core", "hacs"}),
        frozenset({"update.example_tool"}),
        frozenset({"mon", "wed"}),
        time(22, 30),
        time(4, 0),
        timedelta(days=7),
    )


def test_closed_options_are_valid() -> None:
    closed = {
        "kinds": [],
        "entities": [],
        "days": [],
        "start": "02:00",
        "end": "05:00",
        "min_age_days": 7,
        "auto_install": False,
    }
    assert hu.check_options(closed) == (closed, {})
    assert hu.policy_from_options({"ha_updates": closed}) == CLOSED
    assert hu.policy_from_options({}) == CLOSED  # never opened
    assert hu.policy_from_options({"other_item": {"x": 1}}) == CLOSED


REFUSED = [
    ({"days": []}, "days", "window_needs_days"),
    ({"kinds": [], "days": []}, "days", "window_needs_days"),  # one entity allowed is enough to need a day
    ({"start": "03:00", "end": "03:00:00"}, "end", "window_empty"),
    ({"start": "24:00"}, "start", "invalid_time"),
    ({"end": "7"}, "end", "invalid_time"),
    ({"start": None}, "start", "invalid_time"),
    ({"min_age_days": 0}, "min_age_days", "min_age_days"),
    ({"min_age_days": 91}, "min_age_days", "min_age_days"),
    ({"min_age_days": 7.5}, "min_age_days", "min_age_days"),
    ({"min_age_days": "7"}, "min_age_days", "min_age_days"),
    ({"min_age_days": True}, "min_age_days", "min_age_days"),
    ({"kinds": ["core", "toaster"]}, "kinds", "invalid_kind"),
    ({"entities": ["light.example"]}, "entities", "invalid_entity"),
    ({"entities": "update.example_tool"}, "entities", "invalid_entity"),
    ({"days": ["monday"]}, "days", "invalid_day"),
    ({"auto_install": "yes"}, "auto_install", "invalid_auto_install"),
    ({"auto_install": 1}, "auto_install", "invalid_auto_install"),
    ({"auto_install": None}, "auto_install", "invalid_auto_install"),
]


@pytest.mark.parametrize(("change", "field", "code"), REFUSED)
def test_options_refused(change: dict[str, Any], field: str, code: str) -> None:
    clean, errors = hu.check_options({**FLOW_INPUT, **change})
    assert (clean, errors) == ({}, {field: code})
    # Stored like this (by hand, or by another version), they allow nothing.
    assert hu.policy_from_options({"ha_updates": {**FLOW_INPUT, **change}}) is None


def test_every_refusal_is_tested() -> None:
    assert {code for _, _, code in REFUSED} == set(hu.ERRORS)
    assert hu.policy_from_options({"ha_updates": "all"}) is None
