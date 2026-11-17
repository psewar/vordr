"""Home Assistant's own updates installed by Vördr (ADR-0162 – ADR-0165), in Home Assistant's test harness. Other
integrations' update entities are played by the test: with or without the backup and specific-version features,
reporting progress, reaching the target version or not, refusing, or hanging like Core while Home Assistant restarts.
Home Assistant's backup integration is played by its two sensors and its action backup.create_automatic.

Nothing installs with installing off, while paused, outside the window, or for any other code than ready; one at a
time, Core, OS and Supervisor last; the entity's own backup where it has one, else a fresh automatic backup of Home
Assistant, and no install without one; checked again right before acting; verified by the version; a restart in the
middle of a Core update (the run kept in the Store, its outcome checked first); the event, the last-run sensor, the
pause switch, the repair of a failed install and its life cycle; one entry installs, never two."""

from __future__ import annotations

import asyncio
from collections.abc import Awaitable, Callable
from dataclasses import dataclass
from datetime import datetime, timedelta
from types import SimpleNamespace
from typing import Any

from homeassistant import config_entries
from homeassistant.components.repairs import repairs_flow_manager
from homeassistant.components.update import UpdateDeviceClass, UpdateEntity, UpdateEntityFeature
from homeassistant.const import Platform
from homeassistant.core import HomeAssistant, ServiceCall, callback
from homeassistant.data_entry_flow import FlowResultType
from homeassistant.exceptions import HomeAssistantError
from homeassistant.helpers import entity_registry as er, issue_registry as ir
from homeassistant.helpers.device_registry import DeviceInfo
from homeassistant.setup import async_setup_component
from homeassistant.util import dt as dt_util
import pytest
from pytest_homeassistant_custom_component.common import (
    MockConfigEntry,
    MockModule,
    MockPlatform,
    async_capture_events,
    async_fire_time_changed,
    mock_integration,
    mock_platform,
)

from custom_components.vordr import ha_updates_installer as hi, ha_updates_tracker
from custom_components.vordr.const import DOMAIN
from custom_components.vordr.ha_updates_installer import HaUpdatesInstaller

from .conftest import FakeHub, wait_until
from .test_ha_updates import golf_with_install

INSTALL = UpdateEntityFeature.INSTALL
FIRMWARE = UpdateDeviceClass.FIRMWARE
BACKUP = UpdateEntityFeature.BACKUP
SPECIFIC = UpdateEntityFeature.SPECIFIC_VERSION
PROGRESS = UpdateEntityFeature.PROGRESS

EVENT = "vordr_ha_update_finished"
READY = "sensor.vordr_ha_updates_ready"
LAST_RUN = "sensor.vordr_ha_updates_last_run"
PAUSE = "switch.vordr_ha_updates_paused"
RULES: dict[str, Any] = {
    "kinds": ["app", "core", "os", "supervisor", "hacs"],
    "entities": [],
    "days": ["mon"],
    "start": "02:00",
    "end": "05:00",
    "min_age_days": 7,
    "auto_install": True,
}


class Clock:
    """The time every tracker of the test takes as now."""

    def __init__(self, now: datetime) -> None:
        self.now = now

    def __call__(self) -> datetime:
        return self.now


def monday(hour: int, minute: int = 0, weeks: int = 0) -> datetime:
    """A Monday in Home Assistant's time zone; the window of RULES is Monday 02:00-05:00."""
    t = datetime(2026, 11, 16, hour, minute, tzinfo=dt_util.get_default_time_zone()) + timedelta(weeks=weeks)
    assert t.weekday() == 0
    return t


@pytest.fixture
def clock(monkeypatch: pytest.MonkeyPatch) -> Clock:
    """Now for every tracker the test sets up, also after a reload: a week before the window, so that what the test
    offers is seen then and is old enough at monday(3)."""
    c = Clock(monday(3) - timedelta(days=8))
    monkeypatch.setattr(ha_updates_tracker, "dt_util", SimpleNamespace(now=c, parse_datetime=dt_util.parse_datetime))
    return c


class PlayedFlow(config_entries.ConfigFlow):
    """The config flow of a played integration: never run, only there."""


@pytest.fixture(autouse=True)
def played_flows(monkeypatch: pytest.MonkeyPatch) -> None:
    """The integrations the test plays have a config flow, as the config entries of real ones do."""
    for domain in ("hassio", "hacs", "zha", "esphome", "homeassistant_sky_connect"):
        monkeypatch.setitem(config_entries.HANDLERS, domain, PlayedFlow)


@pytest.fixture
def log() -> list[tuple[Any, ...]]:
    """What the played integrations were asked to do, in order: ("install", entity_id, version, backup), ("backup",)."""
    return []


class FakeUpdate(UpdateEntity):
    """An update entity of another integration. outcome: what update.install does - reach (the version), miss (stays),
    fail (raises), hang (waits until released, then reaches)."""

    _attr_should_poll = False

    def __init__(
        self,
        log: list[tuple[Any, ...]],
        object_id: str,
        installed: str = "1.0.0",
        latest: str = "1.1.0",
        features: int = INSTALL,
        device: tuple[str, str] | None = None,
        outcome: str = "reach",
        auto_update: bool = False,
        device_class: UpdateDeviceClass | None = None,
    ) -> None:
        self._log = log
        self._attr_unique_id = object_id
        self._attr_name = object_id
        self.entity_id = f"update.{object_id}"
        self._attr_installed_version = installed
        self._attr_latest_version = latest
        self._attr_supported_features = UpdateEntityFeature(features)
        self._attr_auto_update = auto_update
        self._attr_device_class = device_class
        if device is not None:
            self._attr_device_info = DeviceInfo(identifiers={device})
        self.outcome = outcome
        self.release = asyncio.Event()

    async def async_install(self, version: str | None, backup: bool, **kwargs: Any) -> None:
        self._log.append(("install", self.entity_id, version, backup))
        if self.outcome == "fail":
            raise HomeAssistantError("the integration refused")
        if self.outcome == "hang":
            await self.release.wait()
        if self.outcome in ("reach", "hang"):
            self._attr_installed_version = version or self.latest_version

    def report(self, installed: str | None = None, latest: str | None = None, in_progress: bool = False) -> None:
        """The entity's integration reports a new state."""
        if installed is not None:
            self._attr_installed_version = installed
        if latest is not None:
            self._attr_latest_version = latest
        self._attr_in_progress = in_progress
        self.async_write_ha_state()


async def add_platform(hass: HomeAssistant, domain: str, entities: list[FakeUpdate]) -> None:
    """Another integration, domain, whose update platform holds entities: the registry's platform is domain."""

    async def setup_entry(hass: HomeAssistant, entry: MockConfigEntry) -> bool:
        await hass.config_entries.async_forward_entry_setups(entry, [Platform.UPDATE])
        return True

    async def setup_platform(hass: HomeAssistant, entry: MockConfigEntry, add: Callable[..., None]) -> None:
        add(entities)

    mock_integration(hass, MockModule(domain, async_setup_entry=setup_entry))
    mock_platform(hass, f"{domain}.update", MockPlatform(async_setup_entry=setup_platform))
    mock_platform(hass, f"{domain}.config_flow", None)
    owner = MockConfigEntry(domain=domain)
    owner.add_to_hass(hass)
    assert await hass.config_entries.async_setup(owner.entry_id)
    await hass.async_block_till_done()


class FakeBackup:
    """Home Assistant's backup integration as far as Vördr reads and uses it."""

    def __init__(self, hass: HomeAssistant, clock: Clock, log: list[tuple[Any, ...]], last: datetime | None) -> None:
        self.hass, self.clock, self.log = hass, clock, log
        reg = er.async_get(hass)
        self.manager = reg.async_get_or_create(
            "sensor", "backup", "backup_manager_state", suggested_object_id="backup_backup_manager_state"
        ).entity_id
        self.last = reg.async_get_or_create(
            "sensor",
            "backup",
            "last_successful_automatic_backup",
            suggested_object_id="backup_last_successful_automatic_backup",
        ).entity_id
        hass.states.async_set(self.manager, "idle")
        self.set_last(last)
        self.fail = False
        self.during: Callable[[], Awaitable[None]] | None = None  # what happens while the backup is made
        hass.services.async_register("backup", "create_automatic", self._create)

    def set_last(self, at: datetime | None) -> None:
        self.hass.states.async_set(self.last, at.isoformat() if at is not None else "unknown")

    async def _create(self, call: ServiceCall) -> None:
        self.log.append(("backup",))
        if self.during is not None:
            await self.during()
        if self.fail:
            raise HomeAssistantError("no space left")
        self.set_last(self.clock.now)


@dataclass
class Rig:
    hass: HomeAssistant
    entry: MockConfigEntry
    clock: Clock
    log: list[tuple[Any, ...]]
    events: list[Any]
    backup: FakeBackup | None

    @property
    def installer(self) -> HaUpdatesInstaller:
        return self.entry.runtime_data.ha_updates_installer

    async def settle(self) -> None:
        """Let what was triggered run. Not async_block_till_done: Home Assistant runs update.install as a task of its
        own, and that waits for an install the test keeps hanging."""
        for _ in range(10):
            await asyncio.sleep(0.01)

    async def tick(self) -> None:
        """A minute passes: the tracker and the installer look again."""
        async_fire_time_changed(self.hass, dt_util.utcnow() + timedelta(minutes=1))
        await self.settle()

    async def at(self, when: datetime) -> None:
        self.clock.now = when
        await self.tick()

    async def later(self, minutes: int) -> None:
        """Time passes for the timeouts (verification, backup)."""
        async_fire_time_changed(self.hass, dt_util.utcnow() + timedelta(minutes=minutes))
        await self.settle()

    async def ended(self, n: int) -> list[dict[str, Any]]:
        """The first n runs ended: their events' data."""
        await wait_until(lambda: len(self.events) >= n, describe=lambda: f"{self.events} {self.log}")
        await self.settle()
        return [e.data for e in self.events]

    def installs(self) -> list[str]:
        return [x[1] for x in self.log if x[0] == "install"]

    def code(self, entity_id: str) -> str:
        return {u["entity_id"]: u["code"] for u in self.hass.states.get(READY).attributes["updates"]}[entity_id]

    async def options(self, **over: Any) -> None:
        self.hass.config_entries.async_update_entry(self.entry, options={"ha_updates": {**RULES, **over}})
        await self.settle()

    async def pause(self, on: bool) -> None:
        service = "turn_on" if on else "turn_off"
        await self.hass.services.async_call("switch", service, {"entity_id": PAUSE}, blocking=True)
        await self.settle()


async def setup_entry(hass: HomeAssistant, entry: MockConfigEntry) -> None:
    if entry.entry_id not in {e.entry_id for e in hass.config_entries.async_entries(DOMAIN)}:
        entry.add_to_hass(hass)
    assert await hass.config_entries.async_setup(entry.entry_id)
    await wait_until(lambda: entry.runtime_data.connected)
    await hass.async_block_till_done()


FRESH = "fresh"  # a backup an hour before monday(3)


async def rig(
    hass: HomeAssistant,
    entry: MockConfigEntry,
    clock: Clock,
    log: list[tuple[Any, ...]],
    platforms: dict[str, list[FakeUpdate]],
    options: dict[str, Any] | None = None,
    backup: str | datetime | None = FRESH,
) -> Rig:
    """The played integrations, Home Assistant's backup, then Vördr with the options; all seen a week before monday(3).
    Then the clock stands at monday(3), in the window, and a minute passes."""
    assert await async_setup_component(hass, "repairs", {})
    for domain, entities in platforms.items():
        await add_platform(hass, domain, entities)
    fake_backup = None
    if backup is not None:
        last = monday(2) if backup == FRESH else backup
        assert isinstance(last, datetime)
        fake_backup = FakeBackup(hass, clock, log, last)
    events = async_capture_events(hass, EVENT)
    entry.add_to_hass(hass)
    hass.config_entries.async_update_entry(entry, options={"ha_updates": options or RULES})
    await setup_entry(hass, entry)
    r = Rig(hass, entry, clock, log, events, fake_backup)
    assert r.installs() == []  # seen a week early, on a Sunday: nothing
    await r.at(monday(3))
    return r


def app(log: list[tuple[Any, ...]], name: str = "example_app_update", **over: Any) -> FakeUpdate:
    """An app of the Supervisor: makes its own backup."""
    over.setdefault("features", INSTALL | BACKUP)
    return FakeUpdate(log, name, device=("hassio", f"a0d7b954_{name}"), **over)


def card(log: list[tuple[Any, ...]], name: str = "example_card_update", **over: Any) -> FakeUpdate:
    """A HACS repository: no backup of its own."""
    over.setdefault("installed", "v1.0.0")
    over.setdefault("latest", "v1.1.0")
    return FakeUpdate(log, name, **over)


def core(log: list[tuple[Any, ...]], **over: Any) -> FakeUpdate:
    over.setdefault("features", INSTALL | BACKUP | SPECIFIC)
    return FakeUpdate(log, "home_assistant_core_update", "2026.11.1", "2026.11.2", device=("hassio", "core"), **over)


# --- nothing installs unless everything allows it


async def test_nothing_installs_until_switched_on_unpaused_and_in_the_window(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry, clock: Clock, log: list[tuple[Any, ...]]
) -> None:
    a = app(log)
    r = await rig(hass, entry, clock, log, {"hassio": [a]}, {**RULES, "auto_install": False})
    assert r.code(a.entity_id) == "ready" and hass.states.get(READY).state == "1"  # the plan says so
    await r.tick()
    assert log == []
    assert hass.states.get(LAST_RUN).attributes["installer"] == "off"
    assert hass.states.get(LAST_RUN).state == "unknown"
    # Paused, then switched on: still nothing.
    await r.pause(True)
    assert hass.states.get(PAUSE).state == "on"
    await r.options()
    await r.tick()
    assert log == [] and hass.states.get(LAST_RUN).attributes["installer"] == "paused"
    # Not paused, but past the window's end: nothing.
    r.clock.now = monday(5)
    await r.pause(False)
    await r.tick()
    assert log == [] and hass.states.get(LAST_RUN).attributes["installer"] == "idle"
    # In the window: it installs.
    await r.at(monday(4, weeks=1))
    [event] = await r.ended(1)
    assert event["result"] == "ok" and r.installs() == [a.entity_id]


async def test_only_what_is_ready_installs(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry, clock: Clock, log: list[tuple[Any, ...]]
) -> None:
    """Every other code of the plan installs nothing: not allowed, major, skipped, self-updating, no install feature,
    too new - and Vördr's own entities, even when the stored options name them."""
    fake_hub.hosts["golf"] = golf_with_install()
    hassio = [
        app(log, "major_app_update", installed="1.9.0", latest="2.0.0"),
        app(log, "self_updating_app_update", auto_update=True),
        app(log, "no_install_app_update", features=0),
        app(log, "skipped_app_update"),
    ]
    firmware = UpdateDeviceClass.FIRMWARE
    bulb = FakeUpdate(log, "example_bulb_firmware", "0x01", "0x02", device=("zha", "x"), device_class=firmware)
    by_hand = {**RULES, "kinds": [*RULES["kinds"], "other"], "entities": ["update.vordr_golf_system"]}
    r = await rig(hass, entry, clock, log, {"hassio": hassio, "zha": [bulb]}, {**by_hand, "auto_install": False})
    await hass.services.async_call("update", "skip", {"entity_id": "update.skipped_app_update"}, blocking=True)
    young = card(log)
    await add_platform(hass, "hacs", [young])
    hass.config_entries.async_update_entry(entry, options={"ha_updates": by_hand})
    await hass.async_block_till_done()
    for _ in range(3):
        await r.tick()
    codes = {e.entity_id: r.code(e.entity_id) for e in [*hassio, bulb, young]}
    assert codes == {
        "update.example_bulb_firmware": "not_allowed",
        "update.major_app_update": "major",
        "update.self_updating_app_update": "self_updating",
        "update.no_install_app_update": "no_install",
        "update.skipped_app_update": "skipped",
        "update.example_card_update": "too_new",
    }
    assert hass.states.get("update.vordr_golf_system").state == "on"
    assert log == [] and fake_hub.commands == [] and r.events == []
    assert hass.states.get(LAST_RUN).attributes["queue"] == []


# --- one at a time, the system last


async def test_one_at_a_time_and_core_os_supervisor_last(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry, clock: Clock, log: list[tuple[Any, ...]]
) -> None:
    a = app(log, outcome="hang")
    c = card(log)
    k = core(log)
    os_ = FakeUpdate(log, "home_assistant_operating_system_update", "16.2", "16.3", INSTALL | BACKUP, ("hassio", "OS"))
    sup = FakeUpdate(
        log, "home_assistant_supervisor_update", "2026.11.0", "2026.11.1", INSTALL, ("hassio", "supervisor")
    )
    r = await rig(hass, entry, clock, log, {"hassio": [a, k, os_, sup], "hacs": [c]})
    await wait_until(lambda: r.installs() == [a.entity_id])
    assert hass.states.get(LAST_RUN).state == "installing"
    assert hass.states.get(LAST_RUN).attributes["installer"] == "running"
    for _ in range(3):  # one at a time: nothing else starts while it runs
        await r.tick()
    assert r.installs() == [a.entity_id]
    a.release.set()
    # The others first, then one system update (the Supervisor first of them), then nothing more in this opening.
    events = await r.ended(3)
    assert [(e["entity_id"], e["result"]) for e in events] == [
        (a.entity_id, "ok"),
        (c.entity_id, "ok"),
        (sup.entity_id, "ok"),
    ]
    for _ in range(3):
        await r.tick()
    assert r.installs() == [a.entity_id, c.entity_id, sup.entity_id]
    assert hass.states.get(LAST_RUN).attributes["queue"] == [k.entity_id, os_.entity_id]
    # The next opening: Core, with its own backup and exactly the checked version.
    await r.at(monday(3, weeks=1))
    await r.ended(4)
    assert log[-1] == ("install", k.entity_id, "2026.11.2", True)
    for _ in range(2):
        await r.tick()
    assert r.installs()[-1] == k.entity_id
    # And the one after: OS.
    await r.at(monday(3, weeks=2))
    await r.ended(5)
    assert r.installs() == [a.entity_id, c.entity_id, sup.entity_id, k.entity_id, os_.entity_id]


async def test_nothing_starts_while_another_update_installs(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry, clock: Clock, log: list[tuple[Any, ...]]
) -> None:
    """Also one not Vördr's: a firmware a person started, or one not allowed."""
    a = app(log)
    bulb = FakeUpdate(log, "example_bulb_firmware", "0x01", "0x02", INSTALL | PROGRESS, ("zha", "example-bulb"))
    r = await rig(hass, entry, clock, log, {"zha": [bulb], "hassio": [a]}, {**RULES, "auto_install": False})
    bulb.report(in_progress=True)
    await r.options()
    await r.tick()
    assert log == []
    bulb.report(in_progress=False)
    await r.tick()
    await r.ended(1)
    assert r.installs() == [a.entity_id]


# --- the backup


async def test_the_entitys_own_backup_or_home_assistants(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry, clock: Clock, log: list[tuple[Any, ...]]
) -> None:
    """With BACKUP: update.install with backup, no other. Without: Home Assistant's automatic backup, made when the
    last one is older than 26 hours, and then used for the next one."""
    a = app(log)
    c1, c2 = card(log, "example_card_update"), card(log, "other_card_update")
    stale = monday(3) - timedelta(hours=27)
    r = await rig(hass, entry, clock, log, {"hassio": [a], "hacs": [c1, c2]}, backup=stale)
    events = await r.ended(3)
    assert log == [
        ("install", a.entity_id, None, True),
        ("backup",),
        ("install", c1.entity_id, None, False),
        ("install", c2.entity_id, None, False),
    ]
    assert [(e["entity_id"], e["backup"]) for e in events] == [
        (a.entity_id, "entity"),
        (c1.entity_id, "created"),
        (c2.entity_id, "existing"),
    ]


async def test_no_install_without_a_backup(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry, clock: Clock, log: list[tuple[Any, ...]]
) -> None:
    c1, c2 = card(log, "example_card_update"), card(log, "other_card_update")
    a = app(log, "zz_app_update")
    stale = monday(3) - timedelta(hours=27)
    platforms = {"hacs": [c1, c2], "hassio": [a]}
    r = await rig(hass, entry, clock, log, platforms, {**RULES, "auto_install": False}, backup=stale)
    assert r.backup is not None
    r.backup.fail = True
    await r.options()
    first, _ = await r.ended(2)
    assert first["entity_id"] == c1.entity_id
    assert (first["result"], first["reason"], first["backup"]) == ("backup_failed", "failed", None)
    issue = ir.async_get(hass).async_get_issue(DOMAIN, "ha_updates_backup_failed")
    assert issue is not None and issue.translation_placeholders == {"at": monday(3).isoformat(timespec="minutes")}
    # No second attempt, no install that needs it; an entity with its own backup still goes.
    for _ in range(3):
        await r.tick()
    assert log == [("backup",), ("install", a.entity_id, None, True)]
    assert hass.states.get(LAST_RUN).state == "ok"
    # A newer automatic backup shows up (the nightly one, or one made by hand): the repair goes, the cards install.
    r.backup.set_last(monday(3, 30))
    await r.at(monday(3, 31))
    await r.ended(4)
    assert ir.async_get(hass).async_get_issue(DOMAIN, "ha_updates_backup_failed") is None
    assert r.installs() == [a.entity_id, c1.entity_id, c2.entity_id]
    assert log.count(("backup",)) == 1


async def test_nothing_else_starts_while_a_run_makes_its_backup(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry, clock: Clock, log: list[tuple[Any, ...]]
) -> None:
    """While a run makes Home Assistant's backup, no entity is in progress yet; still nothing else starts."""
    c1, c2 = card(log, "example_card_update"), card(log, "other_card_update")
    stale = monday(3) - timedelta(hours=27)
    r = await rig(hass, entry, clock, log, {"hacs": [c1, c2]}, {**RULES, "auto_install": False}, backup=stale)
    assert r.backup is not None
    seen_during: list[list[tuple[Any, ...]]] = []

    async def during() -> None:
        for _ in range(3):
            await r.tick()
        seen_during.append(list(log))

    r.backup.during = during
    await r.options()
    await r.ended(2)
    assert seen_during == [[("backup",)]]
    assert log == [("backup",), ("install", c1.entity_id, None, False), ("install", c2.entity_id, None, False)]


async def test_no_backup_integration_no_install(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry, clock: Clock, log: list[tuple[Any, ...]]
) -> None:
    c = card(log)
    r = await rig(hass, entry, clock, log, {"hacs": [c]}, backup=None)
    [event] = await r.ended(1)
    assert (event["result"], event["reason"]) == ("backup_failed", "backup_unavailable")
    assert log == []


def firmware(log: list[tuple[Any, ...]], domain: str, **over: Any) -> FakeUpdate:
    """Device firmware (zha) or the radio adapter's (homeassistant_sky_connect): no backup of its own."""
    device = ("zha", "example-bulb") if domain == "zha" else None
    return FakeUpdate(log, f"example_{domain}_firmware", "0x01", "0x02", device=device, device_class=FIRMWARE, **over)


@pytest.mark.parametrize("domain", ["zha", "homeassistant_sky_connect"])
async def test_device_firmware_and_radio_install_without_a_backup(
    hass: HomeAssistant,
    fake_hub: FakeHub,
    entry: MockConfigEntry,
    clock: Clock,
    log: list[tuple[Any, ...]],
    domain: str,
) -> None:
    """ADR-0181: no backup of Home Assistant before device firmware or the radio adapter, not even when there is no
    backup integration at all; a HACS repository and an ESPHome device still need one and wait (ADR-0163). The
    firmware waits its 14 days first (ADR-0180)."""
    fw = firmware(log, domain)
    node = FakeUpdate(
        log,
        "example_node_firmware",
        "2026.10.0",
        "2026.10.1",
        device=("esphome", "example-node"),
        device_class=FIRMWARE,
    )
    c = card(log)
    rules = {**RULES, "kinds": ["firmware", "radio", "esphome", "hacs"]}
    r = await rig(hass, entry, clock, log, {domain: [fw], "esphome": [node], "hacs": [c]}, rules, backup=None)
    [first] = await r.ended(1)
    assert (first["entity_id"], first["result"], first["reason"]) == (
        c.entity_id,
        "backup_failed",
        "backup_unavailable",
    )
    assert r.code(fw.entity_id) == "too_new"  # 8 days old: firmware waits 14
    for _ in range(2):
        await r.tick()
    assert log == [] and len(r.events) == 1  # the ESPHome device waits after the failed backup
    # A week later: the card tries its backup again, fails; the firmware goes without one; ESPHome still waits.
    await r.at(monday(3, weeks=1))
    events = await r.ended(3)
    assert [(e["entity_id"], e["result"], e["backup"]) for e in events[1:]] == [
        (c.entity_id, "backup_failed", None),
        (fw.entity_id, "ok", "exempt"),
    ]
    assert log == [("install", fw.entity_id, None, False)]
    assert r.code(node.entity_id) == "ready"


async def test_device_firmware_gets_two_hours_to_report_its_version(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry, clock: Clock, log: list[tuple[Any, ...]]
) -> None:
    """ADR-0183: an app has 30 minutes after update.install to report the version (see
    test_a_failed_install_is_kept_held_and_a_repair), device firmware 2 hours."""
    fw = firmware(log, "zha", outcome="miss")
    r = await rig(hass, entry, clock, log, {"zha": [fw]}, {**RULES, "kinds": ["firmware"]})
    await r.at(monday(3, weeks=1))  # 15 days old
    await wait_until(lambda: hass.states.get(LAST_RUN).state == "verifying")
    await r.later(31)
    assert r.events == [] and hass.states.get(LAST_RUN).state == "verifying"
    await r.later(119)
    assert r.events == [] and hass.states.get(LAST_RUN).state == "verifying"
    await r.later(121)
    [event] = await r.ended(1)
    assert (event["result"], event["backup"]) == ("not_reached", "exempt")


# --- checked again right before acting


async def _pause(r: Rig, entity: FakeUpdate) -> None:
    await r.hass.services.async_call("switch", "turn_on", {"entity_id": PAUSE}, blocking=True)


async def _newer(r: Rig, entity: FakeUpdate) -> None:
    entity.report(latest="v1.2.0")


async def _window_closes(r: Rig, entity: FakeUpdate) -> None:
    r.clock.now = monday(5)


async def _switched_off(r: Rig, entity: FakeUpdate) -> None:
    r.hass.config_entries.async_update_entry(r.entry, options={"ha_updates": {**RULES, "auto_install": False}})


async def _skipped(r: Rig, entity: FakeUpdate) -> None:
    await r.hass.services.async_call("update", "skip", {"entity_id": entity.entity_id}, blocking=True)


@pytest.mark.parametrize(
    ("change", "result", "reason"),
    [
        (_pause, "skipped_paused", None),
        (_newer, "skipped_not_ready", "target_changed"),
        (_window_closes, "skipped_not_ready", "outside_window"),
        (_switched_off, "skipped_not_ready", "auto_install_off"),
        (_skipped, "skipped_not_ready", "skipped"),
    ],
)
async def test_checked_again_right_before_acting(
    hass: HomeAssistant,
    fake_hub: FakeHub,
    entry: MockConfigEntry,
    clock: Clock,
    log: list[tuple[Any, ...]],
    change: Callable[[Rig, FakeUpdate], Awaitable[None]],
    result: str,
    reason: str | None,
) -> None:
    """While Home Assistant's backup is made (minutes, in real life), something changes: the run does not install."""
    c = card(log)
    stale = monday(3) - timedelta(hours=27)
    r = await rig(hass, entry, clock, log, {"hacs": [c]}, {**RULES, "auto_install": False}, backup=stale)
    assert r.backup is not None
    phases: list[str] = []

    async def during() -> None:
        phases.append(hass.states.get(LAST_RUN).state)
        await change(r, c)

    r.backup.during = during
    await r.options()
    [event] = await r.ended(1)
    assert phases == ["backing_up"]
    assert (event["result"], event["reason"]) == (result, reason)
    assert event["backup"] == "created"  # the backup came about; it stays for the next run
    assert log == [("backup",)]
    for _ in range(2):
        await r.tick()
    assert r.installs() == []
    assert ir.async_get(hass).async_get_issue(DOMAIN, hi.issue_id(c.entity_id)) is None


# --- verified by the version


async def test_ok_is_the_target_version_with_the_whole_event(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry, clock: Clock, log: list[tuple[Any, ...]]
) -> None:
    a = app(log, features=INSTALL | BACKUP | SPECIFIC)
    r = await rig(hass, entry, clock, log, {"hassio": [a]})
    [event] = await r.ended(1)
    assert event == {
        "entity_id": a.entity_id,
        "kind": "app",
        "from_version": "1.0.0",
        "to_version": "1.1.0",
        "installed_version": "1.1.0",
        "result": "ok",
        "reason": None,
        "backup": "entity",
        "started": monday(3).isoformat(),
        "finished": monday(3).isoformat(),
        "restarted": False,
    }
    assert log == [("install", a.entity_id, "1.1.0", True)]
    state = hass.states.get(LAST_RUN)
    assert state.state == "ok"
    assert state.attributes["entity_id"] == a.entity_id and state.attributes["installed_version"] == "1.1.0"
    assert state.attributes["started"] == monday(3).isoformat(timespec="minutes")
    assert "queue" in state.state_info["unrecorded_attributes"]
    # Kept across a reload: the sensor shows the last run again.
    assert await hass.config_entries.async_reload(entry.entry_id)
    await wait_until(lambda: entry.runtime_data.connected)
    await hass.async_block_till_done()
    assert hass.states.get(LAST_RUN).state == "ok"
    assert hass.states.get(LAST_RUN).attributes["to_version"] == "1.1.0"


async def test_waits_for_progress_and_the_version(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry, clock: Clock, log: list[tuple[Any, ...]]
) -> None:
    """update.install returned, but the entity reports the version later (a device that restarts), first still in
    progress: verifying until it is not in progress and reports the version."""
    a = app(log, features=INSTALL | BACKUP | PROGRESS, outcome="miss")
    r = await rig(hass, entry, clock, log, {"hassio": [a]})
    await wait_until(lambda: hass.states.get(LAST_RUN).state == "verifying")
    a.report(installed="1.1.0", in_progress=True)
    await r.later(10)
    assert r.events == [] and hass.states.get(LAST_RUN).state == "verifying"
    a.report(installed="1.1.0", in_progress=False)
    [event] = await r.ended(1)
    assert event["result"] == "ok"


@pytest.mark.parametrize(
    ("outcome", "in_progress", "result"),
    [("miss", False, "not_reached"), ("miss", True, "timeout"), ("fail", False, "failed")],
)
async def test_a_failed_install_is_kept_held_and_a_repair(
    hass: HomeAssistant,
    fake_hub: FakeHub,
    entry: MockConfigEntry,
    clock: Clock,
    log: list[tuple[Any, ...]],
    hass_storage: dict[str, Any],
    outcome: str,
    in_progress: bool,
    result: str,
) -> None:
    a = app(log, features=INSTALL | BACKUP | PROGRESS, outcome=outcome)
    r = await rig(hass, entry, clock, log, {"hassio": [a]})
    if outcome == "miss":
        await wait_until(lambda: hass.states.get(LAST_RUN).state == "verifying")
        a.report(in_progress=in_progress)
        await r.later(31)  # the verification gives up
    [event] = await r.ended(1)
    assert event["result"] == result and event["installed_version"] == "1.0.0"
    # The repair, with its own text per result; held: not tried again, also in the next window.
    issue = ir.async_get(hass).async_get_issue(DOMAIN, "ha_update_failed_example_app_update")
    assert issue is not None and issue.is_fixable and issue.translation_key == f"ha_update_{result}"
    assert issue.translation_placeholders == {
        "entity_id": a.entity_id,
        "from_version": "1.0.0",
        "to_version": "1.1.0",
        "at": issue.translation_placeholders["at"],
    }
    a.report(in_progress=False)
    assert r.code(a.entity_id) == "last_failed"
    await r.at(monday(3, weeks=1))
    assert r.installs() == [a.entity_id]
    stored = hass_storage[hi.store_key(entry.entry_id)]["data"]["failures"]
    assert stored[a.entity_id]["result"] == result and stored[a.entity_id]["released"] is False
    # Kept across a reload, from the first look on: no run even starts for it (not one the check before acting stops).
    assert await hass.config_entries.async_reload(entry.entry_id)
    await wait_until(lambda: entry.runtime_data.connected)
    await hass.async_block_till_done()
    await r.tick()
    assert r.code(a.entity_id) == "last_failed" and r.installs() == [a.entity_id]
    assert len(r.events) == 1
    assert ir.async_get(hass).async_get_issue(DOMAIN, "ha_update_failed_example_app_update") is not None


async def test_the_repairs_fix_installs_at_once_and_the_repair_goes_once_it_goes_through(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry, clock: Clock, log: list[tuple[Any, ...]]
) -> None:
    """ADR-0182: confirmed outside the window, the fix installs at once, under every other rule."""
    a = app(log, outcome="fail")
    r = await rig(hass, entry, clock, log, {"hassio": [a]})
    await r.ended(1)
    await r.at(monday(6))  # the window has closed
    issue_id = "ha_update_failed_example_app_update"
    manager = repairs_flow_manager(hass)
    assert manager is not None
    flow = await manager.async_init(DOMAIN, data={"issue_id": issue_id})
    assert flow["type"] is FlowResultType.FORM and flow["step_id"] == "confirm"
    assert flow["description_placeholders"]["to_version"] == "1.1.0"
    # The cause is fixed; confirmed: installed now, not in the next window.
    a.outcome = "reach"
    result = await manager.async_configure(flow["flow_id"], {})
    assert result["type"] is FlowResultType.CREATE_ENTRY
    [_, event] = await r.ended(2)
    assert (event["entity_id"], event["result"], event["backup"]) == (a.entity_id, "ok", "entity")
    assert event["started"] == monday(6).isoformat()
    assert r.installs() == [a.entity_id, a.entity_id]
    assert ir.async_get(hass).async_get_issue(DOMAIN, issue_id) is None
    assert r.installer.failures == {}
    # A second confirmation of a repair that is gone: nothing to install.
    assert await r.installer.async_install_now(a.entity_id) == "no_longer_needed"


async def test_a_fix_that_fails_again_brings_the_repair_back(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry, clock: Clock, log: list[tuple[Any, ...]]
) -> None:
    a = app(log, outcome="fail")
    r = await rig(hass, entry, clock, log, {"hassio": [a]})
    await r.ended(1)
    await r.at(monday(6))
    assert await r.installer.async_install_now(a.entity_id) == "started"
    [_, event] = await r.ended(2)
    assert event["result"] == "failed"
    assert ir.async_get(hass).async_get_issue(DOMAIN, hi.issue_id(a.entity_id)) is not None
    assert r.code(a.entity_id) == "last_failed" and not r.installer.failures[a.entity_id].released


async def test_the_fix_says_why_it_cannot_install_now_and_queues_nothing(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry, clock: Clock, log: list[tuple[Any, ...]]
) -> None:
    """ADR-0182: one install at a time - while another runs, the fix says so (busy) and starts nothing, also not
    afterwards. Paused, installing off, another entry installing, no longer allowed: each its own code. The repair stays
    every time."""
    a = app(log, outcome="fail")
    b = app(log, "other_app_update", outcome="hang")
    r = await rig(hass, entry, clock, log, {"hassio": [a, b]})
    await wait_until(lambda: r.installs() == [a.entity_id, b.entity_id])  # a failed, b is installing
    issue_id = hi.issue_id(a.entity_id)
    manager = repairs_flow_manager(hass)
    assert manager is not None
    flow = await manager.async_init(DOMAIN, data={"issue_id": issue_id})
    result = await manager.async_configure(flow["flow_id"], {})
    assert (result["type"], result["reason"]) == (FlowResultType.ABORT, "busy")
    a.outcome = "reach"
    b.release.set()
    await r.ended(2)
    for _ in range(3):
        await r.tick()
    assert r.installs() == [a.entity_id, b.entity_id]  # nothing was queued
    assert ir.async_get(hass).async_get_issue(DOMAIN, issue_id) is not None
    await r.pause(True)
    assert await r.installer.async_install_now(a.entity_id) == "paused"
    await r.pause(False)
    await r.options(auto_install=False)
    assert await r.installer.async_install_now(a.entity_id) == "auto_install_off"
    other = MockConfigEntry(domain=DOMAIN, title="Vördr (other hub)", data={}, options={"ha_updates": RULES})
    other.add_to_hass(hass)
    await r.options()
    assert await r.installer.async_install_now(a.entity_id) == "other_entry"
    hass.config_entries.async_update_entry(other, options={"ha_updates": {**RULES, "auto_install": False}})
    await r.options(kinds=["core"])  # apps are no longer allowed
    assert await r.installer.async_install_now(a.entity_id) == "not_ready"
    for _ in range(2):
        await r.tick()
    assert r.installs() == [a.entity_id, b.entity_id]
    assert ir.async_get(hass).async_get_issue(DOMAIN, issue_id) is not None
    assert len(r.events) == 2


async def test_the_fix_waits_for_a_backup_it_needs_after_a_failed_one(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry, clock: Clock, log: list[tuple[Any, ...]]
) -> None:
    c = card(log, outcome="fail")
    r = await rig(hass, entry, clock, log, {"hacs": [c]})
    await r.ended(1)
    # Home Assistant's backup failed since, as test_no_install_without_a_backup shows it: what needs one waits.
    r.installer.backup_failed_at = clock.now
    assert await r.installer.async_install_now(c.entity_id) == "backup_held"
    assert r.installs() == [c.entity_id]


async def test_a_fix_that_does_not_get_to_install_holds_the_version_again(
    hass: HomeAssistant,
    fake_hub: FakeHub,
    entry: MockConfigEntry,
    clock: Clock,
    log: list[tuple[Any, ...]],
    hass_storage: dict[str, Any],
) -> None:
    """ADR-0182: the fix's run fails at the backup: nothing installed, the version is held again and the repair is
    back - nothing installs it later by itself. The same at a start that finds a released failure without its run."""
    c = card(log, outcome="fail")
    r = await rig(hass, entry, clock, log, {"hacs": [c]})
    await r.ended(1)
    await r.at(monday(6))
    assert r.backup is not None
    r.backup.set_last(monday(6) - timedelta(hours=27))
    r.backup.fail = True
    c.outcome = "reach"
    assert await r.installer.async_install_now(c.entity_id) == "started"
    [_, event] = await r.ended(2)
    assert (event["result"], event["reason"]) == ("backup_failed", "failed")
    assert r.code(c.entity_id) == "last_failed"
    assert ir.async_get(hass).async_get_issue(DOMAIN, hi.issue_id(c.entity_id)) is not None
    # A newer backup shows; the next window does not try the held version by itself.
    r.backup.fail = False
    r.backup.set_last(monday(2, weeks=1))
    await r.at(monday(3, weeks=1))
    assert r.installs() == [c.entity_id] and len(r.events) == 2
    # A release kept without its run (Home Assistant stopped while the fix prepared): held again at the next start.
    hass_storage[hi.store_key(entry.entry_id)]["data"]["failures"][c.entity_id]["released"] = True
    assert await hass.config_entries.async_reload(entry.entry_id)
    await wait_until(lambda: entry.runtime_data.connected)
    await r.tick()
    assert r.code(c.entity_id) == "last_failed" and r.installs() == [c.entity_id]
    assert ir.async_get(hass).async_get_issue(DOMAIN, hi.issue_id(c.entity_id)) is not None


async def test_a_newer_version_is_tried_and_the_repair_stays_until_it_goes_through(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry, clock: Clock, log: list[tuple[Any, ...]]
) -> None:
    a = app(log, outcome="fail")
    r = await rig(hass, entry, clock, log, {"hassio": [a]})
    await r.ended(1)
    issue_id = "ha_update_failed_example_app_update"
    a.outcome = "reach"
    a.report(latest="1.2.0")  # first seen now: old enough in a week
    await r.tick()
    assert r.code(a.entity_id) == "too_new"
    assert ir.async_get(hass).async_get_issue(DOMAIN, issue_id) is not None  # not held, but not through yet
    await r.at(monday(3, weeks=1))
    [_, event] = await r.ended(2)
    assert (event["to_version"], event["result"]) == ("1.2.0", "ok")
    assert ir.async_get(hass).async_get_issue(DOMAIN, issue_id) is None


async def test_installed_by_hand_clears_the_failure(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry, clock: Clock, log: list[tuple[Any, ...]]
) -> None:
    a = app(log, outcome="fail")
    r = await rig(hass, entry, clock, log, {"hassio": [a]})
    await r.ended(1)
    a.report(installed="1.1.0")
    await hass.async_block_till_done()
    assert ir.async_get(hass).async_get_issue(DOMAIN, "ha_update_failed_example_app_update") is None
    assert r.installer.failures == {}


# --- a restart in the middle


@pytest.mark.parametrize(("reaches", "result"), [(True, "ok"), (False, "not_reached")])
async def test_a_restart_in_the_middle_of_a_core_update(
    hass: HomeAssistant,
    fake_hub: FakeHub,
    entry: MockConfigEntry,
    clock: Clock,
    log: list[tuple[Any, ...]],
    hass_storage: dict[str, Any],
    reaches: bool,
    result: str,
) -> None:
    """Core's install restarts Home Assistant: the run was kept before update.install was called, and the next start
    checks its outcome from Core's state before it starts anything else."""
    k = core(log, outcome="hang")
    a = app(log)
    r = await rig(hass, entry, clock, log, {"hassio": [k, a]}, {**RULES, "kinds": ["core"]})
    await wait_until(lambda: r.installs() == [k.entity_id])
    kept = hass_storage[hi.store_key(entry.entry_id)]["data"]["run"]
    assert kept == {
        "entity_id": k.entity_id,
        "kind": "core",
        "from_version": "2026.11.1",
        "to_version": "2026.11.2",
        "backup": "entity",
        "started": monday(3).isoformat(),
    }
    # The app becomes allowed while Core installs; it waits.
    await r.options()
    await r.tick()
    assert r.installs() == [k.entity_id]
    # Home Assistant goes down: the entry unloads; whatever the old run still sees decides nothing.
    assert await hass.config_entries.async_unload(entry.entry_id)
    k.outcome = "hang" if reaches else "miss"
    k.release.set()
    await hass.async_block_till_done()
    if not reaches:
        k.report(installed="2026.11.1")
    assert r.events == []
    # ... and starts again. The kept run is checked first.
    seen_at_event: list[list[str]] = []

    @callback
    def at_event(_event: Any) -> None:
        seen_at_event.append(r.installs())

    hass.bus.async_listen(EVENT, at_event)
    await setup_entry(hass, entry)
    if not reaches:
        await r.later(31)
    events = await r.ended(1)
    assert (events[0]["entity_id"], events[0]["result"], events[0]["restarted"]) == (k.entity_id, result, True)
    assert seen_at_event[0] == [k.entity_id]  # nothing else had started
    assert hass_storage[hi.store_key(entry.entry_id)]["data"]["run"] is None
    if reaches:
        # Core was this opening's system update: the app waits for the next opening.
        await r.tick()
        assert r.installs() == [k.entity_id]
        await r.at(monday(3, weeks=1))
        await r.ended(2)
        assert r.installs() == [k.entity_id, a.entity_id]
    else:
        assert ir.async_get(hass).async_get_issue(DOMAIN, "ha_update_failed_home_assistant_core_update") is not None


# --- the pause switch, one entry


async def test_the_pause_is_kept_and_holds_every_install(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry, clock: Clock, log: list[tuple[Any, ...]]
) -> None:
    a = app(log)
    r = await rig(hass, entry, clock, log, {"hassio": [a]}, {**RULES, "auto_install": False})
    await r.pause(True)
    ent = er.async_get(hass).async_get(PAUSE)
    assert ent is not None and ent.unique_id == "ha_updates:paused"
    assert await hass.config_entries.async_reload(entry.entry_id)
    await wait_until(lambda: entry.runtime_data.connected)
    await hass.async_block_till_done()
    assert hass.states.get(PAUSE).state == "on"
    await r.options()
    await r.tick()
    assert log == []
    await r.pause(False)
    await r.ended(1)
    assert r.installs() == [a.entity_id]


async def test_one_entry_installs_never_two(
    hass: HomeAssistant, fake_hub: FakeHub, entry: MockConfigEntry, clock: Clock, log: list[tuple[Any, ...]]
) -> None:
    a = app(log)
    other = MockConfigEntry(domain=DOMAIN, title="Vördr (other hub)", data={}, options={"ha_updates": RULES})
    other.add_to_hass(hass)
    r = await rig(hass, entry, clock, log, {"hassio": [a]}, {**RULES, "auto_install": False})
    # The options refuse to switch it on here while the other entry has it on.
    result = await hass.config_entries.options.async_init(entry.entry_id)
    result = await hass.config_entries.options.async_configure(result["flow_id"], {"next_step_id": "ha_updates"})
    form = {**RULES, "start": "02:00:00", "end": "05:00:00"}
    result = await hass.config_entries.options.async_configure(result["flow_id"], form)
    assert result["type"] is FlowResultType.FORM and result["errors"] == {"auto_install": "auto_install_other_entry"}
    # Stored by hand on both: neither installs, and says why.
    await r.options()
    await r.tick()
    assert log == [] and hass.states.get(LAST_RUN).attributes["installer"] == "other_entry"
    # Off on the other: this one installs.
    hass.config_entries.async_update_entry(other, options={"ha_updates": {**RULES, "auto_install": False}})
    await r.tick()
    await r.ended(1)
    assert r.installs() == [a.entity_id]
