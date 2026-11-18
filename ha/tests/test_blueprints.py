"""The automation blueprints under blueprints/automation/vordr/ (ADR-0173): each one loads with Home Assistant's own
blueprint schema, names only events, fields and entities the integration has, and does its job against a fired event
with a mocked notify service. The reboot offer presses the host's reboot button only after a person's tap, and only
where the host offers the reboot. A blueprint with a wrong event name fails the check."""

from __future__ import annotations

import asyncio
from collections.abc import AsyncIterator, Callable, Iterator
from datetime import timedelta
from pathlib import Path
import re
import shutil
import time
from typing import Any
import uuid

from homeassistant.components.blueprint.models import Blueprint
from homeassistant.components.blueprint.schemas import BLUEPRINT_SCHEMA
from homeassistant.core import HomeAssistant
from homeassistant.helpers import device_registry as dr, entity_registry as er
from homeassistant.setup import async_setup_component
from homeassistant.util import dt as dt_util
from homeassistant.util.yaml import load_yaml
import pytest
from pytest_homeassistant_custom_component.common import (
    MockConfigEntry,
    async_capture_events,
    async_fire_time_changed,
    async_mock_service,
)

from custom_components.vordr.const import CODES, DOMAIN, EVENT_PROBLEM, EVENT_RUN_FINISHED, REASONS
from custom_components.vordr.coordinator import host_device, local_time

from .conftest import FakeHub, host_view, wait_until

BLUEPRINTS = Path(__file__).resolve().parents[2] / "blueprints" / "automation" / "vordr"
FILES = sorted(BLUEPRINTS.glob("*.yaml"))
NAMES = ["problem.yaml", "run_failed.yaml", "reboot_offer.yaml", "hub_offline.yaml"]

# The Companion app's event for a tap on a notification action: not ours, the reboot offer waits for it.
APP_TAP = "mobile_app_notification_action"
EVENTS = {EVENT_PROBLEM, EVENT_RUN_FINISHED, APP_TAP}
# What the two events carry (design/ha-integration.md 6a): a blueprint may read these and nothing else.
FIELDS = {
    "node",
    "active",
    "reason",
    "reasons",
    "check_failed_code",
    "message",
    "job_id",
    "verb",
    "scope",
    "by",
    "state",
    "summary",
}
VERBS = {"check", "install", "reboot", "agent_update", "backup"}  # internal/protocol/codes.go
INPUTS = {
    "problem.yaml": {"notify_service", "language", "hosts", "ignored_reasons", "delay_minutes", "notify_when_ended"},
    "run_failed.yaml": {"notify_service", "language", "hosts", "verbs", "ignored_codes"},
    "reboot_offer.yaml": {"notify_service", "language", "hosts", "wait_hours", "notify_without_button"},
    "hub_offline.yaml": {"notify_service", "hub_online", "minutes", "language", "notify_when_back"},
}

CAN = {"check": True, "install": False, "install_security": False, "reboot": True, "agent_update": False}
CANNOT = {**CAN, "reboot": False}
NEEDED = {"required": True, "reason": "kernel", "packages": [], "checked": ["kernel"]}
PENDING = {"reason": "reboot_pending", "reasons": ["reboot_pending"]}
STALE = {"reason": "stale", "reasons": ["stale"]}
OFFLINE = {"reason": "offline", "reasons": ["offline"]}


def walk(node: Any) -> Iterator[tuple[str, Any]]:
    """Every (key, value) pair of a loaded blueprint, at any depth."""
    if isinstance(node, dict):
        for key, value in node.items():
            yield str(key), value
            yield from walk(value)
    elif isinstance(node, list):
        for item in node:
            yield from walk(item)


def load(path: Path) -> Blueprint:
    """The blueprint as Home Assistant loads it: its schema, its inputs, every !input defined."""
    return Blueprint(load_yaml(path), path=path.name, expected_domain="automation", schema=BLUEPRINT_SCHEMA)


def problems_in(path: Path) -> list[str]:
    """What is wrong with a blueprint against the integration; empty when nothing is."""
    errors: list[str] = []
    try:
        blueprint = load(path)
    except Exception as err:  # the schema's own refusal, whatever its type: it is the finding
        return [f"{path.name}: Home Assistant refuses it: {err}"]
    data = load_yaml(path)
    for key, value in walk(data):
        if key == "event_type" and value not in EVENTS:
            errors.append(f"{path.name}: event {value!r} is not fired by the integration")
    text = path.read_text(encoding="utf-8")
    for field in sorted(set(re.findall(r"trigger\.event\.data\.(\w+)", text)) - FIELDS):
        errors.append(f"{path.name}: the events carry no field {field!r}")
    options = {
        name: {o if isinstance(o, str) else o["value"] for o in spec["selector"]["select"]["options"]}
        for name, spec in blueprint.inputs.items()
        if spec and "select" in spec.get("selector", {})
    }
    for name, allowed in (("ignored_reasons", set(REASONS)), ("ignored_codes", set(CODES)), ("verbs", VERBS)):
        if name in options and not options[name] <= allowed:
            errors.append(f"{path.name}: input {name} offers {sorted(options[name] - allowed)}, unknown to Vördr")
    return errors


def test_the_blueprints_are_the_expected_ones() -> None:
    assert [f.name for f in FILES] == sorted(NAMES)


@pytest.mark.parametrize("path", FILES, ids=lambda p: p.name)
def test_loads_with_the_blueprint_schema(path: Path) -> None:
    blueprint = load(path)
    assert blueprint.domain == "automation"
    assert set(blueprint.inputs) == INPUTS[path.name]
    assert blueprint.metadata["homeassistant"]["min_version"]
    # No entity of a site is named: the one entity input is a selector, never a default.
    text = path.read_text(encoding="utf-8")
    assert not re.search(
        r"\b(?:sensor|binary_sensor|button|switch|light)\.[a-z0-9_]+_[a-z0-9_]+\b",
        text.replace("binary_sensor.vordr_hub_online", ""),
    )


@pytest.mark.parametrize("path", FILES, ids=lambda p: p.name)
def test_names_only_what_the_integration_has(path: Path) -> None:
    assert problems_in(path) == []


def test_a_blueprint_with_a_wrong_event_name_fails(tmp_path: Path) -> None:
    source = (BLUEPRINTS / "problem.yaml").read_text(encoding="utf-8")
    broken = tmp_path / "problem.yaml"
    broken.write_text(source.replace("event_type: vordr_problem\n", "event_type: vordr_problme\n", 1), encoding="utf-8")
    assert any("vordr_problme" in e for e in problems_in(broken))
    field = tmp_path / "field.yaml"
    field.write_text(source.replace("trigger.event.data.node", "trigger.event.data.nodename", 1), encoding="utf-8")
    assert any("nodename" in e for e in problems_in(field))
    code = tmp_path / "code.yaml"
    code.write_text(source.replace("            - no_report\n", "            - no_reportt\n", 1), encoding="utf-8")
    assert any("no_reportt" in e for e in problems_in(code))
    not_a_blueprint = tmp_path / "plain.yaml"
    not_a_blueprint.write_text("alias: x\ntriggers: []\n", encoding="utf-8")
    assert problems_in(not_a_blueprint)


async def setup(hass: HomeAssistant, entry: MockConfigEntry) -> None:
    entry.add_to_hass(hass)
    assert await hass.config_entries.async_setup(entry.entry_id)
    await wait_until(lambda: entry.runtime_data.connected)
    await hass.async_block_till_done()


@pytest.fixture
async def installed(hass: HomeAssistant) -> AsyncIterator[Any]:
    """Puts blueprints into the config folder as a user's import would, and removes them afterwards. A run that still
    waits for a tap or a timeout ends with the test. The config folder is the test package's own, shared by every test
    process on the machine: each test has a folder of its own in it, so that two runs at once do not remove each
    other's blueprints."""
    own = f"vordr-{uuid.uuid4().hex[:12]}"
    folder = Path(hass.config.path("blueprints", "automation", own))

    async def use(*uses: tuple[str, dict[str, Any]]) -> None:
        folder.mkdir(parents=True, exist_ok=True)
        for name, _ in uses:
            shutil.copy(BLUEPRINTS / name, folder / name)
        config = [{"use_blueprint": {"path": f"{own}/{name}", "input": inputs}} for name, inputs in uses]
        assert await async_setup_component(hass, "automation", {"automation": config})
        await hass.async_block_till_done()

    yield use
    await hass.services.async_call("automation", "turn_off", {"entity_id": "all", "stop_actions": True}, blocking=True)
    shutil.rmtree(folder, ignore_errors=True)


def device_id(hass: HomeAssistant, node: str) -> str:
    (entry,) = hass.config_entries.async_entries(DOMAIN)
    device = dr.async_get(hass).async_get_device_by_identifier(host_device(node), entry.entry_id)
    assert device is not None
    return device.id


async def settle(hass: HomeAssistant) -> None:
    """Events that were fired have been handled by the automations. Not async_block_till_done: a run that waits for
    a tap or a timeout would keep that from returning."""
    for _ in range(5):
        await asyncio.sleep(0.05)


async def later(
    hass: HomeAssistant, minutes: float, until: Callable[[], bool] | None = None, describe: Any = None
) -> None:
    """The clock moves on by `minutes`; with `until`, again and again until that holds (at most 5 s). A run starts in
    a task of its own after the event the test saw, and sets the timer of its wait from the clock when it gets there:
    a run that was not yet waiting when the clock moved would never time out. The next move catches it."""
    async_fire_time_changed(hass, dt_util.utcnow() + timedelta(minutes=minutes))
    await settle(hass)
    if until is None:
        return
    deadline = time.monotonic() + 5.0
    while not until():
        if time.monotonic() > deadline:
            raise TimeoutError(describe() if describe else f"not within 5.0 s after {minutes} minutes")
        async_fire_time_changed(hass, dt_util.utcnow() + timedelta(minutes=minutes))
        await asyncio.sleep(0.05)


def running(hass: HomeAssistant) -> int:
    """Runs of the test's automations that have not ended (the automations' attribute current)."""
    return sum(state.attributes.get("current", 0) for state in hass.states.async_all("automation"))


async def test_events_carry_the_fields_the_blueprints_read(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry
) -> None:
    """FIELDS is what the integration really fires, not a guess."""
    fake_hub.hosts["h"] = host_view("h", capabilities=CAN)
    problems = async_capture_events(hass, EVENT_PROBLEM)
    runs = async_capture_events(hass, EVENT_RUN_FINISHED)
    await setup(hass, entry)
    await fake_hub.push_host(host_view("h", capabilities=CAN, problem=STALE))
    await wait_until(lambda: len(problems) == 1)
    run = {"job_id": "j1", "verb": "install", "scope": "all", "by": "admin", "state": "failed_health"}
    await fake_hub.push_host(host_view("h", capabilities=CAN, last_run=run))
    await wait_until(lambda: len(runs) == 1)
    assert set(problems[0].data) | set(runs[0].data) == FIELDS
    # The reboot lookup of the offer: the entity ids end the way the blueprint expects.
    ids = er.async_entries_for_device(er.async_get(hass), device_id(hass, "h"))
    assert any(e.entity_id.startswith("button.") and e.entity_id.endswith("_reboot") for e in ids)
    assert any(e.entity_id.startswith("binary_sensor.") and e.entity_id.endswith("_reboot_required") for e in ids)


async def test_problem_notifies_in_the_chosen_language(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry, installed: Any
) -> None:
    fake_hub.hosts["h"] = host_view("h")
    await setup(hass, entry)
    de = async_mock_service(hass, "notify", "de")
    en = async_mock_service(hass, "notify", "en")
    xx = async_mock_service(hass, "notify", "xx")
    await installed(
        ("problem.yaml", {"notify_service": "notify.de", "language": "de"}),
        ("problem.yaml", {"notify_service": "notify.en", "language": "en"}),
        ("problem.yaml", {"notify_service": "notify.xx", "language": "xx"}),
    )
    await fake_hub.push_host(host_view("h", problem=STALE))
    await wait_until(lambda: len(de) == 1 and len(en) == 1 and len(xx) == 1)
    assert "Updates liegen seit über 7 Tagen" in de[0].data["message"]
    assert "Updates have been pending for more than 7 days" in en[0].data["message"]
    assert xx[0].data["message"] == en[0].data["message"]  # a language Vördr does not ship: English
    assert de[0].data["title"] == "Vördr h" and de[0].data["data"]["tag"] == "vordr_problem_h"


async def test_problem_ignores_a_listed_reason_and_other_hosts(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry, installed: Any
) -> None:
    fake_hub.hosts["h"] = host_view("h")
    fake_hub.hosts["k"] = host_view("k")
    await setup(hass, entry)
    calls = async_mock_service(hass, "notify", "test")
    await installed(
        (
            "problem.yaml",
            {
                "notify_service": "notify.test",
                "hosts": [device_id(hass, "h")],
                "ignored_reasons": ["reboot_pending"],
            },
        )
    )
    problems = async_capture_events(hass, EVENT_PROBLEM)
    await fake_hub.push_host(host_view("k", problem=STALE))  # not a chosen host
    await fake_hub.push_host(host_view("h", problem=PENDING))  # an ignored reason
    await wait_until(lambda: len(problems) == 2)
    await settle(hass)
    assert calls == []
    await fake_hub.push_host(host_view("h", problem={"reason": "stale", "reasons": ["reboot_pending", "stale"]}))
    await wait_until(lambda: len(calls) == 1)  # one reason that is not ignored is enough
    assert calls[0].data["title"] == "Vördr h"


async def test_problem_waits_and_drops_a_problem_that_ends_or_changes(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry, installed: Any
) -> None:
    fake_hub.hosts["h"] = host_view("h")
    fake_hub.hosts["k"] = host_view("k")
    await setup(hass, entry)
    calls = async_mock_service(hass, "notify", "test")
    problems = async_capture_events(hass, EVENT_PROBLEM)
    await installed(("problem.yaml", {"notify_service": "notify.test", "delay_minutes": 10}))

    await fake_hub.push_host(host_view("h", problem=STALE))
    await fake_hub.push_host(host_view("k", problem=OFFLINE))
    await wait_until(lambda: len(problems) == 2)
    await settle(hass)
    assert calls == []  # waiting
    await fake_hub.push_host(host_view("h"))  # h ends by itself; k does not
    await wait_until(lambda: len(problems) == 3)
    await later(hass, 11, until=lambda: len(calls) == 1, describe=lambda: str([c.data for c in calls]))
    await settle(hass)
    assert len(calls) == 1 and calls[0].data["title"] == "Vördr k"  # h was dropped; h's end did not cancel k's wait

    # A change inside the wait: the first run drops, the changed problem has its own wait.
    await fake_hub.push_host(host_view("h", problem=STALE))
    await wait_until(lambda: len(problems) == 4)
    await fake_hub.push_host(host_view("h", problem=OFFLINE))
    await wait_until(lambda: len(problems) == 5)
    await later(hass, 22, until=lambda: len(calls) == 2, describe=lambda: str([c.data for c in calls]))
    await settle(hass)
    assert len(calls) == 2 and "Not connected to the hub" in calls[1].data["message"]


async def test_problem_end_notice_only_when_asked_and_without_a_wait(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry, installed: Any
) -> None:
    fake_hub.hosts["h"] = host_view("h")
    await setup(hass, entry)
    calls = async_mock_service(hass, "notify", "test")
    problems = async_capture_events(hass, EVENT_PROBLEM)
    await installed(("problem.yaml", {"notify_service": "notify.test", "language": "de", "notify_when_ended": True}))
    await fake_hub.push_host(host_view("h", problem=STALE))
    await wait_until(lambda: len(calls) == 1)
    await fake_hub.push_host(host_view("h"))
    await wait_until(lambda: len(calls) == 2)
    assert calls[1].data["title"] == "Vördr h: Problem behoben" and problems[-1].data["active"] is False


async def test_problem_ended_is_silent_by_default(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry, installed: Any
) -> None:
    fake_hub.hosts["h"] = host_view("h")
    await setup(hass, entry)
    calls = async_mock_service(hass, "notify", "test")
    problems = async_capture_events(hass, EVENT_PROBLEM)
    await installed(("problem.yaml", {"notify_service": "notify.test"}))
    await fake_hub.push_host(host_view("h", problem=STALE))
    await wait_until(lambda: len(calls) == 1)
    await fake_hub.push_host(host_view("h"))
    await wait_until(lambda: len(problems) == 2)
    await settle(hass)
    assert len(calls) == 1


async def test_a_wrong_event_name_in_the_blueprint_notifies_nothing(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry, installed: Any
) -> None:
    """The refused case for the live path: what the static check finds is also what makes a blueprint dead."""
    fake_hub.hosts["h"] = host_view("h")
    await setup(hass, entry)
    calls = async_mock_service(hass, "notify", "test")
    own = f"vordr-{uuid.uuid4().hex[:12]}"  # a folder of its own, as the fixture installed gives each test
    folder = Path(hass.config.path("blueprints", "automation", own))
    folder.mkdir(parents=True, exist_ok=True)
    source = (BLUEPRINTS / "problem.yaml").read_text(encoding="utf-8")
    (folder / "problem.yaml").write_text(
        source.replace("event_type: vordr_problem\n", "event_type: vordr_problme\n", 1), encoding="utf-8"
    )
    try:
        assert await async_setup_component(
            hass,
            "automation",
            {
                "automation": [
                    {"use_blueprint": {"path": f"{own}/problem.yaml", "input": {"notify_service": "notify.test"}}}
                ]
            },
        )
        assert hass.states.async_all("automation"), "the blueprint did not load: the test would prove nothing"
        problems = async_capture_events(hass, EVENT_PROBLEM)
        await fake_hub.push_host(host_view("h", problem=STALE))
        await wait_until(lambda: len(problems) == 1)
        await settle(hass)
        assert calls == []
    finally:
        shutil.rmtree(folder, ignore_errors=True)


async def test_failed_run_notifies_only_a_failure_of_a_chosen_kind(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry, installed: Any
) -> None:
    fake_hub.hosts["h"] = host_view("h", capabilities=CAN)
    await setup(hass, entry)
    calls = async_mock_service(hass, "notify", "test")
    runs = async_capture_events(hass, EVENT_RUN_FINISHED)
    await installed(("run_failed.yaml", {"notify_service": "notify.test", "language": "de"}))

    async def finish(job: str, verb: str, state: str) -> None:
        run = {"job_id": job, "verb": verb, "scope": "all", "by": "admin", "state": state}
        await fake_hub.push_host(host_view("h", capabilities=CAN, last_run=run))
        await wait_until(lambda: len(runs) == int(job))
        await settle(hass)

    await finish("1", "install", "ok")  # success
    await finish("2", "install", "ok_reboot_required")  # not a failure; the reboot offer handles it
    await finish("3", "check", "failed_package_manager")  # a kind not chosen
    assert calls == []
    await finish("4", "install", "failed_package_manager")
    assert len(calls) == 1
    assert calls[0].data["title"] == "Vördr h: install - failed_package_manager"
    assert calls[0].data["message"] == runs[3].data["message"]["de"] and calls[0].data["message"]


async def test_reboot_offer_presses_the_button_only_after_a_tap(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry, installed: Any
) -> None:
    fake_hub.hosts["h"] = host_view("h", capabilities=CAN)
    await setup(hass, entry)
    calls = async_mock_service(hass, "notify", "test")
    await installed(("reboot_offer.yaml", {"notify_service": "notify.test", "language": "de"}))
    runs = async_capture_events(hass, EVENT_RUN_FINISHED)
    done = {"job_id": "j1", "verb": "install", "scope": "all", "by": "admin", "state": "ok_reboot_required"}
    await fake_hub.push_host(host_view("h", capabilities=CAN, last_run=done, reboot=NEEDED))
    await wait_until(lambda: len(calls) == 1)
    note = calls[0].data
    assert note["message"] == runs[0].data["message"]["de"] and "Knopf" in note["message"]
    (action,) = note["data"]["actions"]
    assert action["title"] == "Jetzt neu starten" and action["action"].startswith("VORDR_REBOOT_h_")
    await settle(hass)
    assert fake_hub.commands == []  # the offer alone reboots nothing

    # A tap on some other notification does nothing either.
    hass.bus.async_fire("mobile_app_notification_action", {"action": "SOMETHING_ELSE"})
    await settle(hass)
    assert fake_hub.commands == []

    hass.bus.async_fire("mobile_app_notification_action", {"action": action["action"]})
    await wait_until(lambda: len(fake_hub.commands) == 1)
    assert fake_hub.commands[0]["command"] == "reboot" and fake_hub.commands[0]["node"] == "h"


async def test_reboot_offer_expires_and_checks_again_at_the_tap(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry, installed: Any
) -> None:
    fake_hub.hosts["h"] = host_view("h", capabilities=CAN)
    await setup(hass, entry)
    calls = async_mock_service(hass, "notify", "test")
    await installed(("reboot_offer.yaml", {"notify_service": "notify.test", "wait_hours": 1}))
    done = {"job_id": "j1", "verb": "install", "scope": "all", "by": "admin", "state": "ok_reboot_required"}
    await fake_hub.push_host(host_view("h", capabilities=CAN, last_run=done, reboot=NEEDED))
    await wait_until(lambda: len(calls) == 1)
    first = calls[0].data["data"]["actions"][0]["action"]
    assert running(hass) == 1  # the offer's run waits for a tap
    await later(hass, 61, until=lambda: running(hass) == 0)  # the offer has expired: its run ended
    hass.bus.async_fire("mobile_app_notification_action", {"action": first})
    await settle(hass)
    assert fake_hub.commands == []

    # A second offer; before the tap the host no longer needs the reboot (somebody rebooted it by hand).
    done2 = {**done, "job_id": "j2"}
    await fake_hub.push_host(host_view("h", capabilities=CAN, last_run=done2, reboot=NEEDED))
    await wait_until(lambda: len(calls) == 2)
    await fake_hub.push_host(
        host_view("h", capabilities=CAN, last_run=done2, reboot={"required": False, "checked": ["kernel"]})
    )
    await wait_until(lambda: hass.states.get("binary_sensor.vordr_h_reboot_required").state == "off")
    hass.bus.async_fire("mobile_app_notification_action", {"action": calls[1].data["data"]["actions"][0]["action"]})
    await wait_until(lambda: len(calls) == 3)  # it says that nothing was done
    assert fake_hub.commands == [] and "nothing was done" in calls[2].data["message"]


async def test_reboot_offer_says_when_the_reboot_window_opens(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry, installed: Any
) -> None:
    """ADR-0187: a tap while the host's reboot window is closed presses nothing and says from when the reboot runs;
    once the window is open, or the time the hub named has passed, a tap presses the button."""
    fake_hub.hosts["h"] = host_view("h", capabilities=CAN)
    await setup(hass, entry)
    calls = async_mock_service(hass, "notify", "test")
    await installed(("reboot_offer.yaml", {"notify_service": "notify.test"}))
    nxt = dt_util.utcnow().replace(microsecond=0) + timedelta(hours=3)
    window = {"days": ["mon"], "start": "06:00", "end": "16:00", "tz": "Europe/Paris", "window_only": True}
    closed = {"capabilities": CAN, "reboot": NEEDED, "reboot_window": window, "next_reboot": nxt.isoformat()}
    done = {"job_id": "j1", "verb": "install", "scope": "all", "by": "admin", "state": "ok_reboot_required"}
    await fake_hub.push_host(host_view("h", **closed, last_run=done))
    await wait_until(lambda: len(calls) == 1)
    hass.bus.async_fire("mobile_app_notification_action", {"action": calls[0].data["data"]["actions"][0]["action"]})
    await wait_until(lambda: len(calls) == 2)
    assert fake_hub.commands == []
    said = f"h can be rebooted only from {local_time(nxt)}: its reboot window is closed. Nothing was done."
    assert calls[1].data["message"] == said

    # The next offer; by the tap the window is open (the hub sends no time any more).
    await fake_hub.push_host(host_view("h", **closed, last_run={**done, "job_id": "j2"}))
    await wait_until(lambda: len(calls) == 3)
    await fake_hub.push_host(host_view("h", capabilities=CAN, reboot=NEEDED, reboot_window=window))
    await wait_until(lambda: "next_reboot" not in hass.states.get("button.vordr_h_reboot").attributes)
    hass.bus.async_fire("mobile_app_notification_action", {"action": calls[2].data["data"]["actions"][0]["action"]})
    await wait_until(lambda: len(fake_hub.commands) == 1)
    assert fake_hub.commands[0]["command"] == "reboot" and len(calls) == 3

    # A third offer; by the tap the time the hub named has passed, before its next view says so: the tap presses.
    third = {**done, "job_id": "j3"}
    await fake_hub.push_host(host_view("h", **closed, last_run=third))
    await wait_until(lambda: len(calls) == 4)
    past = dt_util.utcnow().replace(microsecond=0) - timedelta(seconds=5)
    await fake_hub.push_host(host_view("h", **{**closed, "next_reboot": past.isoformat()}, last_run=third))
    reboot = "button.vordr_h_reboot"
    await wait_until(lambda: hass.states.get(reboot).attributes.get("next_reboot") == past.isoformat())
    hass.bus.async_fire("mobile_app_notification_action", {"action": calls[3].data["data"]["actions"][0]["action"]})
    await wait_until(lambda: len(fake_hub.commands) == 2)
    assert fake_hub.commands[1]["command"] == "reboot" and len(calls) == 4


async def test_reboot_offer_has_no_action_where_the_host_does_not_offer_the_reboot(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry, installed: Any
) -> None:
    fake_hub.hosts["h"] = host_view("h", capabilities=CANNOT)
    fake_hub.hosts["k"] = host_view("k", capabilities=CANNOT)
    await setup(hass, entry)
    assert hass.states.get("button.vordr_h_reboot") is None
    plain = async_mock_service(hass, "notify", "plain")
    silent = async_mock_service(hass, "notify", "silent")
    await installed(
        ("reboot_offer.yaml", {"notify_service": "notify.plain"}),
        ("reboot_offer.yaml", {"notify_service": "notify.silent", "notify_without_button": False}),
    )
    done = {"job_id": "j1", "verb": "install", "scope": "all", "by": "admin", "state": "ok_reboot_required"}
    await fake_hub.push_host(host_view("h", capabilities=CANNOT, last_run=done, reboot=NEEDED))
    await wait_until(lambda: len(plain) == 1)
    await settle(hass)
    assert "actions" not in plain[0].data["data"]
    assert "by hand" in plain[0].data["message"]  # the integration's text for a host without the button
    assert silent == [] and fake_hub.commands == []


async def test_reboot_offer_again_for_a_reboot_that_waited_too_long(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry, installed: Any
) -> None:
    fake_hub.hosts["h"] = host_view("h", capabilities=CAN, reboot=NEEDED)
    await setup(hass, entry)
    calls = async_mock_service(hass, "notify", "test")
    await installed(("reboot_offer.yaml", {"notify_service": "notify.test"}))
    await fake_hub.push_host(host_view("h", capabilities=CAN, reboot=NEEDED, problem=STALE))
    await settle(hass)
    assert calls == []  # another problem is no reboot offer
    await fake_hub.push_host(host_view("h", capabilities=CAN, reboot=NEEDED, problem=PENDING))
    await wait_until(lambda: len(calls) == 1)
    assert calls[0].data["data"]["actions"][0]["action"].startswith("VORDR_REBOOT_h_")


async def test_hub_offline_notifies_after_the_wait_and_when_back(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry, installed: Any
) -> None:
    await setup(hass, entry)
    # The entity the input's selector offers: the integration's, a connectivity sensor. The test drives a stand-in with
    # that id's shape, since moving the clock would make the real entity refresh itself and answer for the test.
    real = hass.states.get("binary_sensor.vordr_hub_online")
    assert real.state == "on" and real.attributes["device_class"] == "connectivity"
    hub = "binary_sensor.stand_in_hub_online"
    calls = async_mock_service(hass, "notify", "test")
    await installed(("hub_offline.yaml", {"notify_service": "notify.test", "hub_online": hub, "minutes": 5}))
    hass.states.async_set(hub, "off")
    await hass.async_block_till_done()
    await later(hass, 3)
    assert calls == []
    hass.states.async_set(hub, "on")  # back before the wait ended: nothing was announced, nothing to retract
    await hass.async_block_till_done()
    assert calls == []
    # The state is set as if it had been off for ten minutes: "now" in the template is the real clock.
    hass.states.async_set(hub, "off", timestamp=time.time() - 600)
    await hass.async_block_till_done()
    await later(hass, 6, until=lambda: len(calls) == 1, describe=lambda: str([c.data for c in calls]))
    assert calls[0].data["title"] == "Vördr hub unreachable" and "5 minutes" in calls[0].data["message"]
    hass.states.async_set(hub, "on")
    await wait_until(lambda: len(calls) == 2)
    assert calls[1].data["title"] == "Vördr hub reachable again"


async def test_the_clock_catches_a_run_that_starts_its_wait_late(hass: HomeAssistant) -> None:
    """The helper itself: a run that reaches its wait only after the clock moved. Here the first move ends the run's
    delay, so its wait starts after that move and sets its timer from the clock as it is then; one move does not end
    it, later(until=...) does. The delay is a minute, so that only the move ends it, never the real time a slow machine
    takes."""
    calls = async_mock_service(hass, "notify", "test")
    late = {
        "triggers": [{"trigger": "event", "event_type": "vordr_test_late"}],
        "actions": [
            {"delay": {"minutes": 1}},
            {"wait_for_trigger": [{"trigger": "event", "event_type": "vordr_test_never"}], "timeout": {"minutes": 10}},
            {"action": "notify.test", "data": {"message": "late"}},
        ],
    }
    assert await async_setup_component(hass, "automation", {"automation": [late]})
    hass.bus.async_fire("vordr_test_late")
    await settle(hass)
    await later(hass, 11)
    assert calls == [] and running(hass) == 1  # one move: the run waits on
    await later(hass, 11, until=lambda: len(calls) == 1)
    assert running(hass) == 0
