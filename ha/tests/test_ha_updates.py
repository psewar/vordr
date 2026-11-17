"""Home Assistant's own updates under Vördr's rules in Home Assistant (ADR-0158, ADR-0159, ADR-0160): the facts from
states and registries, the sensor, the first sights across a restart, and the options flow."""

from __future__ import annotations

from datetime import UTC, datetime, timedelta
from typing import Any

from homeassistant.components.recorder.db_schema import StateAttributes
from homeassistant.components.update import UpdateEntityFeature
from homeassistant.const import EVENT_STATE_CHANGED, STATE_UNAVAILABLE
from homeassistant.core import Event, HomeAssistant
from homeassistant.data_entry_flow import FlowResultType, InvalidData
from homeassistant.helpers import device_registry as dr, entity_registry as er
from homeassistant.util import dt as dt_util
from homeassistant.util.json import json_loads
import pytest
from pytest_homeassistant_custom_component.common import MockConfigEntry, async_fire_time_changed

from custom_components.vordr import async_remove_config_entry_device
from custom_components.vordr.const import DOMAIN
from custom_components.vordr.coordinator import HA_DEVICE
from custom_components.vordr.ha_updates import CODES, DAYS
from custom_components.vordr.ha_updates_tracker import HaUpdatesTracker, store_key

from .conftest import FakeHub, host_view, wait_until

SENSOR = "sensor.vordr_ha_updates_ready"
INSTALL = UpdateEntityFeature.INSTALL
OPTIONS = {
    "ha_updates": {
        "kinds": ["app", "core", "hacs"],
        "entities": [],
        "days": ["mon"],
        "start": "02:00",
        "end": "05:00",
        "min_age_days": 7,
    }
}


class Clock:
    """The tracker's time, set by the test."""

    def __init__(self, now: datetime) -> None:
        self.now = now

    def __call__(self) -> datetime:
        return self.now


def monday(hour: int, weeks: int = 0) -> datetime:
    """A Monday in Home Assistant's time zone (the test harness sets one with daylight saving)."""
    t = datetime(2026, 11, 16, hour, tzinfo=dt_util.get_default_time_zone()) + timedelta(weeks=weeks)
    assert t.weekday() == 0
    return t


def iso(t: datetime) -> str:
    return t.isoformat(timespec="minutes")


def set_update(
    hass: HomeAssistant,
    entity_id: str,
    state: str = "on",
    installed: str = "1.0.0",
    latest: str = "1.1.0",
    features: int = INSTALL,
    **attrs: Any,
) -> None:
    hass.states.async_set(
        entity_id,
        state,
        {
            "installed_version": installed,
            "latest_version": latest,
            "supported_features": features,
            "auto_update": False,
            "in_progress": False,
            "skipped_version": None,
            **attrs,
        },
    )


def add_update(
    hass: HomeAssistant,
    object_id: str,
    platform: str,
    device: tuple[str, str] | None = None,
    device_class: str | None = None,
    **state: Any,
) -> str:
    """An update entity of another integration: its registry entry (platform, device, device class) and its state."""
    owner = MockConfigEntry(domain=platform)
    owner.add_to_hass(hass)
    device_id = None
    if device is not None:
        device_id = dr.async_get(hass).async_get_or_create(config_entry_id=owner.entry_id, identifiers={device}).id
    entity_id = (
        er.async_get(hass)
        .async_get_or_create(
            "update",
            platform,
            object_id,
            suggested_object_id=object_id,
            config_entry=owner,
            device_id=device_id,
            original_device_class=device_class,
        )
        .entity_id
    )
    set_update(hass, entity_id, **state)
    return entity_id


async def setup(hass: HomeAssistant, entry: MockConfigEntry, options: dict[str, Any] | None = None) -> HaUpdatesTracker:
    if entry.entry_id not in {e.entry_id for e in hass.config_entries.async_entries(DOMAIN)}:
        entry.add_to_hass(hass)
    if options is not None:
        hass.config_entries.async_update_entry(entry, options=options)
    assert await hass.config_entries.async_setup(entry.entry_id)
    await wait_until(lambda: entry.runtime_data.connected)
    await hass.async_block_till_done()
    return entry.runtime_data.ha_updates


def plan_of(hass: HomeAssistant) -> dict[str, dict[str, Any]]:
    return {u["entity_id"]: u for u in hass.states.get(SENSOR).attributes["updates"]}


def golf_with_install() -> dict[str, Any]:
    """A host whose update entity Vördr's own rules govern: pending updates, installable."""
    view = host_view("golf")
    view["capabilities"] = {**view["capabilities"], "install": True}
    return view


async def test_nothing_is_allowed_by_default(hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry) -> None:
    fake_hub.hosts["golf"] = golf_with_install()
    tracker = await setup(hass, entry)
    tracker.clock = Clock(monday(3))
    core = add_update(hass, "home_assistant_core_update", "hassio", ("hassio", "core"), installed="2026.11.1")
    # HACS's own entity for an integration that is named like Vördr: chosen by its platform, not by its name.
    hacs = add_update(hass, "vordr_update", "hacs", installed="v0.38.0", latest="v0.39.0")
    await hass.async_block_till_done()
    assert hass.states.get("update.vordr_golf_system").state == "on"
    assert hass.states.get("update.vordr_golf_system").attributes["supported_features"] & INSTALL
    state = hass.states.get(SENSOR)
    assert state.state == "0"
    assert {eid: u["code"] for eid, u in plan_of(hass).items()} == {core: "not_allowed", hacs: "not_allowed"}
    assert state.attributes["not_allowed"] == 2 and state.attributes["ready"] == 0
    assert entry.options == {}


async def test_the_sensor_shows_the_plan(hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry) -> None:
    fake_hub.hosts["golf"] = golf_with_install()
    tracker = await setup(hass, entry, OPTIONS)
    clock = Clock(monday(3))
    tracker.clock = clock
    features = INSTALL | UpdateEntityFeature.SPECIFIC_VERSION | UpdateEntityFeature.BACKUP
    core = add_update(
        hass,
        "home_assistant_core_update",
        "hassio",
        ("hassio", "core"),
        installed="2026.11.1",
        latest="2026.11.2",
        features=features,
    )
    auto = add_update(hass, "example_app_update", "hassio", ("hassio", "a0d7b954_example"), auto_update=True)
    major = add_update(
        hass, "example_tool_update", "hassio", ("hassio", "a0d7b954_tool"), installed="1.9.0", latest="2.0"
    )
    hacs = add_update(hass, "vordr_update", "hacs", installed="v0.38.0", latest="v0.39.0")
    node = add_update(hass, "example_node_firmware", "esphome", ("esphome", "example-node"), "firmware", features=0)
    bulb = add_update(
        hass,
        "example_bulb_firmware",
        "zha",
        ("zha", "example-bulb"),
        "firmware",
        state="off",
        installed="0x01",
        latest="0x02",
        skipped_version="0x02",
    )
    radio = add_update(hass, "example_radio_firmware", "homeassistant_sky_connect", None, "firmware")
    await hass.async_block_till_done()

    state = hass.states.get(SENSOR)
    assert state.state == "0"
    items = plan_of(hass)
    assert "update.vordr_golf_system" not in items  # Vördr's own: left out, by its platform
    assert {eid: u["code"] for eid, u in items.items()} == {
        core: "too_new",
        auto: "self_updating",
        major: "major",
        hacs: "too_new",
        node: "no_install",
        bulb: "skipped",
        radio: "not_allowed",
    }
    week = monday(3, weeks=1)
    assert items[core] == {
        "entity_id": core,
        "kind": "core",
        "code": "too_new",
        "installed_version": "2026.11.1",
        "latest_version": "2026.11.2",
        "first_seen": iso(monday(3)),
        "earliest": iso(week),  # in Home Assistant's time zone, with its offset
    }
    assert items[node] == {
        "entity_id": node,
        "kind": "esphome",
        "code": "no_install",
        "installed_version": "1.0.0",
        "latest_version": "1.1.0",
        "first_seen": iso(monday(3)),
    }
    counts = {code: state.attributes[code] for code in CODES}
    assert counts == {
        "ready": 0,
        "not_allowed": 1,
        "outside_window": 0,
        "too_new": 2,
        "major": 1,
        "last_failed": 0,
        "skipped": 1,
        "self_updating": 1,
        "in_progress": 0,
        "no_install": 1,
    }
    assert state.attributes["min_age_days"] == 7
    assert state.attributes["min_age_days_firmware"] == 14

    # A week later, in the window: Core and the HACS entity are ready. The minute's tick finds it; no entity changed.
    clock.now = week
    async_fire_time_changed(hass, dt_util.utcnow() + timedelta(minutes=1))
    await hass.async_block_till_done()
    assert hass.states.get(SENSOR).state == "2"
    assert plan_of(hass)[core]["code"] == "ready" and "earliest" not in plan_of(hass)[core]

    # After the window's end: the next Monday's start.
    clock.now = week + timedelta(hours=3)
    async_fire_time_changed(hass, dt_util.utcnow() + timedelta(minutes=2))
    await hass.async_block_till_done()
    assert hass.states.get(SENSOR).state == "0"
    assert plan_of(hass)[core]["code"] == "outside_window"
    assert plan_of(hass)[core]["earliest"] == iso(monday(2, weeks=2))

    # A state change counts at once: an install starts on the Core entity.
    set_update(hass, core, installed="2026.11.1", latest="2026.11.2", features=features, in_progress=True)
    await hass.async_block_till_done()
    assert plan_of(hass)[core]["code"] == "in_progress"


async def test_the_sensor_is_its_own_device_and_needs_no_hub(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry
) -> None:
    tracker = await setup(hass, entry, OPTIONS)
    tracker.clock = Clock(monday(3))
    add_update(hass, "home_assistant_core_update", "hassio", ("hassio", "core"), installed="2026.11.1")
    await hass.async_block_till_done()
    ent = er.async_get(hass).async_get(SENSOR)
    assert ent is not None and ent.unique_id == "ha_updates:ready" and ent.platform == DOMAIN
    device = dr.async_get(hass).async_get(ent.device_id)
    assert device is not None and HA_DEVICE in device.identifiers
    assert device.name == "Vördr Home Assistant" and device.via_device_id is None
    assert not await async_remove_config_entry_device(hass, entry, device)
    # The list stays out of the recorder; the counts do not (ADR-0101).
    state = hass.states.get(SENSOR)
    assert "updates" in state.state_info["unrecorded_attributes"]
    event = Event(EVENT_STATE_CHANGED, {"entity_id": SENSOR, "old_state": None, "new_state": state})
    recorded = json_loads(StateAttributes.shared_attrs_bytes_from_event(event, None))
    assert "updates" not in recorded and recorded["too_new"] == 1
    # Without the hub: the hub's entities go unavailable, this one stays.
    await fake_hub.shut_down()
    await wait_until(lambda: hass.states.get("sensor.vordr_hub_hosts_online").state == STATE_UNAVAILABLE)
    tracker.refresh()
    assert hass.states.get(SENSOR).state == "0" and plan_of(hass)


async def test_first_sight_survives_a_restart(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry, hass_storage: dict[str, Any]
) -> None:
    tracker = await setup(hass, entry, OPTIONS)
    tracker.clock = Clock(monday(3))
    core = add_update(
        hass, "home_assistant_core_update", "hassio", ("hassio", "core"), installed="2026.11.1", latest="2026.11.2"
    )
    await hass.async_block_till_done()
    assert plan_of(hass)[core]["code"] == "too_new"
    # Unloading writes the first sights at once.
    assert await hass.config_entries.async_unload(entry.entry_id)
    key = store_key(entry.entry_id)
    assert hass_storage[key]["data"]["first_seen"] == {core: {"version": "2026.11.2", "at": monday(3).isoformat()}}

    # Set up again a week later: the version keeps its first sight and is ready.
    tracker = await setup(hass, entry)
    tracker.clock = Clock(monday(3, weeks=1))
    tracker.refresh()
    assert plan_of(hass)[core]["code"] == "ready"
    assert plan_of(hass)[core]["first_seen"] == iso(monday(3))
    assert hass.states.get(SENSOR).state == "1"

    # A newer version starts its own age; the old first sight goes.
    set_update(hass, core, installed="2026.11.1", latest="2026.11.3")
    await hass.async_block_till_done()
    assert plan_of(hass)[core]["code"] == "too_new"
    await tracker.async_flush()
    later = {core: {"version": "2026.11.3", "at": monday(3, weeks=1).isoformat()}}
    assert hass_storage[key]["data"]["first_seen"] == later

    # An unavailable entity keeps it; one removed from the registry loses it.
    hass.states.async_set(core, STATE_UNAVAILABLE, {"supported_features": INSTALL})
    await hass.async_block_till_done()
    await tracker.async_flush()
    assert core in hass_storage[key]["data"]["first_seen"]
    er.async_get(hass).async_remove(core)
    hass.states.async_remove(core)
    await hass.async_block_till_done()
    await tracker.async_flush()
    assert hass_storage[key]["data"]["first_seen"] == {}
    assert hass_storage[key]["data"]["lines"] == {}

    # A deleted entry takes its first sights with it.
    assert await hass.config_entries.async_remove(entry.entry_id)
    await hass.async_block_till_done()
    assert key not in hass_storage


async def test_a_patch_release_inherits_its_line_across_a_restart(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry, hass_storage: dict[str, Any]
) -> None:
    """ADR-0159: Core is up to date when first seen; a week later a patch release of its line waits only its own two
    days, also after a restart; a new line waits the full minimum age, and device firmware 14 days."""
    every_day = {"ha_updates": {**OPTIONS["ha_updates"], "kinds": ["core", "firmware"], "days": list(DAYS)}}
    tracker = await setup(hass, entry, every_day)
    clock = Clock(monday(3))
    tracker.clock = clock
    core = add_update(
        hass,
        "home_assistant_core_update",
        "hassio",
        ("hassio", "core"),
        state="off",
        installed="2026.11.1",
        latest="2026.11.1",
    )
    bulb = add_update(
        hass, "example_bulb_firmware", "zha", ("zha", "example-bulb"), "firmware", installed="0x01", latest="0x02"
    )
    await hass.async_block_till_done()
    assert core not in plan_of(hass)  # up to date
    assert plan_of(hass)[bulb]["earliest"] == iso(monday(3, weeks=2))  # firmware: 14 days, not 7

    week = monday(3, weeks=1)
    clock.now = week
    set_update(hass, core, installed="2026.11.1", latest="2026.11.2")
    await hass.async_block_till_done()
    item = plan_of(hass)[core]
    assert item["code"] == "too_new"
    assert item["first_seen"] == iso(week) and item["line_first_seen"] == iso(monday(3))
    assert item["earliest"] == iso(week + timedelta(days=2))  # its own two days, in that day's window
    assert plan_of(hass)[bulb]["code"] == "too_new"

    # Unloading writes the lines with the first sights; set up again two days later, the release is ready.
    assert await hass.config_entries.async_unload(entry.entry_id)
    key = store_key(entry.entry_id)
    assert hass_storage[key]["data"]["lines"][core] == {"2026.11": monday(3).isoformat()}
    tracker = await setup(hass, entry)
    clock = Clock(week + timedelta(days=2))
    tracker.clock = clock
    tracker.refresh()
    assert plan_of(hass)[core]["code"] == "ready"

    # A new line (a minor bump) starts fresh: the full minimum age, nothing inherited.
    set_update(hass, core, installed="2026.11.1", latest="2026.12.0")
    await hass.async_block_till_done()
    item = plan_of(hass)[core]
    assert item["code"] == "too_new" and "line_first_seen" not in item
    assert item["earliest"] == iso(week + timedelta(days=9))

    # Firmware after its 14 days.
    clock.now = monday(3, weeks=2)
    tracker.refresh()
    assert plan_of(hass)[bulb]["code"] == "ready"


async def test_stored_first_sights_that_are_none_are_left_out(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry, hass_storage: dict[str, Any]
) -> None:
    hass_storage[store_key(entry.entry_id)] = {
        "version": 1,
        "key": store_key(entry.entry_id),
        "data": {
            "first_seen": {
                "update.kept": {"version": "1.1.0", "at": "2026-11-01T03:00:00+00:00"},
                "update.no_time": {"version": "1.1.0"},
                "update.naive_time": {"version": "1.1.0", "at": "2026-11-01T03:00:00"},
                "update.no_version": {"at": "2026-11-01T03:00:00+00:00"},
                "update.no_entry": "1.1.0",
            },
            "lines": {
                "update.kept": {"1.1": "2026-11-01T03:00:00+00:00", "1.0": "2026-11-01T03:00:00", "2.0": 5},
                "update.no_time": {"1.1": None},
                "update.no_entry": "1.1",
            },
        },
    }
    for object_id in ("kept", "no_time", "naive_time", "no_version", "no_entry"):
        er.async_get(hass).async_get_or_create("update", "hacs", object_id, suggested_object_id=object_id)
    tracker = await setup(hass, entry)
    assert set(tracker._seen) == {"update.kept"}
    assert tracker._lines == {"update.kept": {"1.1": datetime(2026, 11, 1, 3, tzinfo=UTC)}}


async def test_invalid_stored_options_allow_nothing(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry, caplog: pytest.LogCaptureFixture
) -> None:
    """Options the form would refuse (edited by hand, or left by another version) open nothing, and the log says so
    once."""
    invalid = {"ha_updates": {**OPTIONS["ha_updates"], "kinds": ["core", "everything"]}}
    tracker = await setup(hass, entry, invalid)
    tracker.clock = Clock(monday(3))
    core = add_update(hass, "home_assistant_core_update", "hassio", ("hassio", "core"), installed="2026.11.1")
    await hass.async_block_till_done()
    tracker.refresh()
    assert plan_of(hass)[core]["code"] == "not_allowed"
    assert caplog.text.count("not valid; nothing is allowed") == 1


async def start_options(hass: HomeAssistant, entry: MockConfigEntry) -> dict[str, Any]:
    result = await hass.config_entries.options.async_init(entry.entry_id)
    assert result["type"] is FlowResultType.MENU
    assert result["menu_options"] == ["ha_updates", "operator_keys", "install_agent", "ai_explanations"]
    return await hass.config_entries.options.async_configure(result["flow_id"], {"next_step_id": "ha_updates"})


def defaults(result: dict[str, Any]) -> dict[str, Any]:
    return {str(k): k.default() for k in result["data_schema"].schema if callable(getattr(k, "default", None))}


async def test_options_menu_and_saving(hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry) -> None:
    fake_hub.hosts["golf"] = golf_with_install()
    tracker = await setup(hass, entry, {"other_item": {"kept": True}})
    tracker.clock = Clock(monday(3))
    core = add_update(hass, "home_assistant_core_update", "hassio", ("hassio", "core"), installed="2026.11.1")
    await hass.async_block_till_done()
    assert plan_of(hass)[core]["code"] == "not_allowed"
    result = await start_options(hass, entry)
    assert result["type"] is FlowResultType.FORM and result["step_id"] == "ha_updates"
    # Closed defaults: nothing chosen.
    assert defaults(result) == {
        "kinds": [],
        "entities": [],
        "days": [],
        "start": "02:00",
        "end": "05:00",
        "min_age_days": 7,
        "auto_install": False,  # installing is off until a person switches it on (ADR-0162)
    }
    # Vördr's own update entities are not offered.
    entities = next(k for k in result["data_schema"].schema if str(k) == "entities")
    assert result["data_schema"].schema[entities].config["exclude_entities"] == ["update.vordr_golf_system"]
    result = await hass.config_entries.options.async_configure(
        result["flow_id"],
        {"kinds": ["core"], "entities": [], "days": ["mon"], "start": "02:00:00", "end": "05:00:00", "min_age_days": 3},
    )
    assert result["type"] is FlowResultType.CREATE_ENTRY
    await hass.async_block_till_done()
    assert entry.options == {
        "other_item": {"kept": True},  # another item of the menu keeps its options
        "ha_updates": {
            "kinds": ["core"],
            "entities": [],
            "days": ["mon"],
            "start": "02:00",
            "end": "05:00",
            "min_age_days": 3,
            "auto_install": False,
        },
    }
    # The plan follows the new options at once, without a reload: Core is allowed now, and too new.
    assert entry.runtime_data.ha_updates is tracker
    # First seen Monday 03:00, old enough on Thursday: the next window is the following Monday's.
    assert plan_of(hass)[core]["code"] == "too_new"
    assert plan_of(hass)[core]["earliest"] == iso(monday(2, weeks=1))
    assert hass.states.get(SENSOR).attributes["min_age_days"] == 3
    assert hass.states.get(SENSOR).attributes["min_age_days_firmware"] == 14  # never below 14 days
    # Opened again, the form shows what is stored.
    result = await start_options(hass, entry)
    assert defaults(result)["kinds"] == ["core"] and defaults(result)["min_age_days"] == 3


@pytest.mark.parametrize(
    ("change", "errors"),
    [
        ({"days": []}, {"days": "window_needs_days"}),
        ({"start": "03:00:00", "end": "03:00:00"}, {"end": "window_empty"}),
        ({"min_age_days": 7.5}, {"min_age_days": "min_age_days"}),
    ],
)
async def test_options_refused_with_a_text(
    hass: HomeAssistant, entry: MockConfigEntry, change: dict[str, Any], errors: dict[str, str]
) -> None:
    entry.add_to_hass(hass)
    result = await start_options(hass, entry)
    good = {"kinds": ["core"], "entities": [], "days": ["mon"], "start": "02:00:00", "end": "05:00:00"}
    result = await hass.config_entries.options.async_configure(result["flow_id"], {**good, "min_age_days": 7, **change})
    assert result["type"] is FlowResultType.FORM and result["errors"] == errors
    assert entry.options == {}
    # The form keeps what was entered.
    assert defaults(result)["days"] == change.get("days", ["mon"])


@pytest.mark.parametrize(
    "change",
    [
        {"min_age_days": 0},
        {"min_age_days": 91},
        {"kinds": ["toaster"]},
        {"days": ["monday"]},
        {"start": "25:00"},
        {"entities": ["light.example"]},
        {"entities": ["update.vordr_golf_system"]},  # Vördr's own: excluded by the selector
    ],
)
async def test_options_refused_by_the_form(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry, change: dict[str, Any]
) -> None:
    fake_hub.hosts["golf"] = golf_with_install()
    await setup(hass, entry)
    result = await start_options(hass, entry)
    good = {"kinds": ["core"], "entities": [], "days": ["mon"], "start": "02:00:00", "end": "05:00:00"}
    with pytest.raises(InvalidData):
        await hass.config_entries.options.async_configure(result["flow_id"], {**good, "min_age_days": 7, **change})
    assert entry.options == {}
