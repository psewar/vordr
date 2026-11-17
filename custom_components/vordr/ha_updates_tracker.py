"""Watches Home Assistant's own update entities for the plan (ha_updates.py, ADR-0158).

The facts of every update entity come from its state and from the entity and device registries; the first sight of
each offered version and of each version line is kept in a Store across restarts (ADR-0159, ADR-0179); the plan is
computed again on every change of an update entity, when the options change, and once a minute, since windows open and
versions grow old without any change. Independent of the hub: it runs with or without a connection. It installs
nothing itself: the installer (ha_updates_installer.py) acts on its plan and tells it which versions are held after a
failed install (ADR-0165).
"""

from __future__ import annotations

from collections.abc import Callable, Mapping
from datetime import datetime, timedelta
import logging
from typing import TYPE_CHECKING, Any

from homeassistant.components.update import (
    ATTR_AUTO_UPDATE,
    ATTR_IN_PROGRESS,
    ATTR_INSTALLED_VERSION,
    ATTR_LATEST_VERSION,
    ATTR_SKIPPED_VERSION,
    DOMAIN as UPDATE_DOMAIN,
    UpdateEntityFeature,
)
from homeassistant.const import ATTR_SUPPORTED_FEATURES, EVENT_STATE_CHANGED
from homeassistant.core import CALLBACK_TYPE, Event, EventStateChangedData, HomeAssistant, State, callback
from homeassistant.helpers import device_registry as dr, entity_registry as er
from homeassistant.helpers.event import async_track_time_interval
from homeassistant.helpers.storage import Store
from homeassistant.util import dt as dt_util

from .const import DOMAIN
from .ha_updates import (
    CLOSED,
    OPTIONS_KEY,
    Facts,
    Planned,
    Policy,
    Seen,
    kind_of,
    plan,
    policy_from_options,
    remember,
    remember_lines,
)

if TYPE_CHECKING:
    from homeassistant.config_entries import ConfigEntry

_LOGGER = logging.getLogger(__name__)

STORE_VERSION = 1
SAVE_DELAY = 10.0  # seconds; a first sight lost to a crash in between only starts the age again (ADR-0159)
TICK = timedelta(minutes=1)


def store_key(entry_id: str) -> str:
    """The Store of one config entry's first sights (.storage/vordr.ha_updates.<entry_id>)."""
    return f"{DOMAIN}.ha_updates.{entry_id}"


async def async_remove_store(hass: HomeAssistant, entry_id: str) -> None:
    """The entry is deleted: its first sights go with it."""
    await Store[dict[str, Any]](hass, STORE_VERSION, store_key(entry_id)).async_remove()


def _version(value: Any) -> str | None:
    return value if isinstance(value, str) and value else None


def _load(data: Any) -> dict[str, Seen]:
    """The first sights as stored; an entry that is not one is left out (its age starts again)."""
    out: dict[str, Seen] = {}
    raw = data.get("first_seen") if isinstance(data, dict) else None
    for entity_id, s in (raw if isinstance(raw, dict) else {}).items():
        if not isinstance(s, dict):
            continue
        at = _aware(s.get("at"))
        version = _version(s.get("version"))
        if at is not None and version is not None:
            out[entity_id] = Seen(version, at)
    return out


def _load_lines(data: Any) -> dict[str, dict[str, datetime]]:
    """The first sights of the lines as stored (entity -> line -> time); what is not one is left out (its age starts
    again)."""
    out: dict[str, dict[str, datetime]] = {}
    raw = data.get("lines") if isinstance(data, dict) else None
    for entity_id, lines in (raw if isinstance(raw, dict) else {}).items():
        kept = {
            line: at
            for line, value in (lines if isinstance(lines, dict) else {}).items()
            if isinstance(line, str) and (at := _aware(value)) is not None
        }
        if kept:
            out[entity_id] = kept
    return out


def _aware(value: Any) -> datetime | None:
    """A stored time with its zone; None for anything else."""
    at = dt_util.parse_datetime(value) if isinstance(value, str) else None
    return at if at is not None and at.tzinfo is not None else None


@callback
def _is_update(data: EventStateChangedData) -> bool:
    return data["entity_id"].startswith(f"{UPDATE_DOMAIN}.")


class HaUpdatesTracker:
    """The plan for Home Assistant's own update entities, kept current for the sensor."""

    def __init__(self, hass: HomeAssistant, entry: ConfigEntry) -> None:
        self.hass = hass
        self.entry = entry
        self._store: Store[dict[str, Any]] = Store(hass, STORE_VERSION, store_key(entry.entry_id))
        self._seen: dict[str, Seen] = {}
        self._lines: dict[str, dict[str, datetime]] = {}  # entity -> line -> first sight (ADR-0179)
        self._dirty = False  # first sights changed since the last write
        self._listeners: list[CALLBACK_TYPE] = []
        self._invalid_options: Any = None  # the stored options last found invalid, to say so once
        self.clock: Callable[[], datetime] = dt_util.now  # the time in Home Assistant's zone; tests set their own
        self.policy: Policy = CLOSED
        self.planned: list[Planned] = []
        self.facts: dict[str, Facts] = {}  # of the last refresh, by entity id
        self.held: dict[str, str] = {}  # entity -> version held after a failed install; set by the installer

    async def async_start(self) -> None:
        """Load the first sights, compute the plan, and keep it current while the entry is loaded."""
        stored = await self._store.async_load()
        self._seen, self._lines = _load(stored), _load_lines(stored)
        self.entry.async_on_unload(
            self.hass.bus.async_listen(EVENT_STATE_CHANGED, self._on_state_changed, event_filter=_is_update)
        )
        self.entry.async_on_unload(async_track_time_interval(self.hass, self._on_tick, TICK))
        self.entry.async_on_unload(self.entry.add_update_listener(self._on_options))
        self.refresh()

    async def async_flush(self) -> None:
        """Write pending first sights now (on unload: a reload loads them again at once)."""
        if self._dirty:
            self._dirty = False
            await self._store.async_save(self._data())

    @callback
    def async_add_listener(self, listener: CALLBACK_TYPE) -> Callable[[], None]:
        """Call listener whenever the plan changes; returns the function that removes it."""
        self._listeners.append(listener)
        return lambda: self._listeners.remove(listener)

    @callback
    def _on_state_changed(self, _event: Event[EventStateChangedData]) -> None:
        self.refresh()

    @callback
    def _on_tick(self, _now: datetime) -> None:
        self.refresh()

    async def _on_options(self, _hass: HomeAssistant, _entry: ConfigEntry) -> None:
        self.refresh()

    @callback
    def refresh(self) -> None:
        """Facts, first sights and plan anew."""
        now = self.clock()
        facts = self._facts()
        self.facts = {f.entity_id: f for f in facts}
        ent_reg = er.async_get(self.hass)
        registered = {entity_id for entity_id in {*self._seen, *self._lines} if ent_reg.async_is_registered(entity_id)}
        seen = remember(self._seen, facts, registered, now)
        lines = remember_lines(self._lines, facts, registered, now)
        if seen != self._seen or lines != self._lines:
            self._seen, self._lines = seen, lines
            self._dirty = True
            self._store.async_delay_save(self._data, SAVE_DELAY)
        self.policy = self._policy()
        planned = plan(facts, seen, self.policy, now, self.held, lines)
        if planned != self.planned:
            self.planned = planned
            for listener in list(self._listeners):
                listener()

    def planned_with(self, held: Mapping[str, str]) -> list[Planned]:
        """The plan from the last refresh's facts with other held versions: a repair's fix asks what the rules say
        apart from the hold it releases (ADR-0182)."""
        return plan(list(self.facts.values()), self._seen, self.policy, self.clock(), held, self._lines)

    def _policy(self) -> Policy:
        """The rules from the options; stored options that do not pass the check count as closed, said once."""
        policy = policy_from_options(self.entry.options)
        if policy is not None:
            self._invalid_options = None
            return policy
        raw = self.entry.options.get(OPTIONS_KEY)
        if raw != self._invalid_options:
            self._invalid_options = raw
            _LOGGER.warning("the stored options for Home Assistant's updates are not valid; nothing is allowed")
        return CLOSED

    def _facts(self) -> list[Facts]:
        ent_reg = er.async_get(self.hass)
        dev_reg = dr.async_get(self.hass)
        return [self._fact(state, ent_reg, dev_reg) for state in self.hass.states.async_all(UPDATE_DOMAIN)]

    @staticmethod
    def _fact(state: State, ent_reg: er.EntityRegistry, dev_reg: dr.DeviceRegistry) -> Facts:
        entry = ent_reg.async_get(state.entity_id)
        device = dev_reg.async_get(entry.device_id) if entry is not None and entry.device_id else None
        platform = entry.platform if entry is not None else None
        device_class = (entry.device_class or entry.original_device_class) if entry is not None else None
        a = state.attributes
        features = a.get(ATTR_SUPPORTED_FEATURES)
        features = features if isinstance(features, int) else 0
        return Facts(
            entity_id=state.entity_id,
            platform=platform,
            kind=kind_of(platform, device_class, device.identifiers if device is not None else set()),
            state=state.state,
            installed=_version(a.get(ATTR_INSTALLED_VERSION)),
            latest=_version(a.get(ATTR_LATEST_VERSION)),
            skipped=_version(a.get(ATTR_SKIPPED_VERSION)),
            auto_update=a.get(ATTR_AUTO_UPDATE) is True,
            in_progress=a.get(ATTR_IN_PROGRESS) is True,
            installable=bool(features & UpdateEntityFeature.INSTALL),
            backup=bool(features & UpdateEntityFeature.BACKUP),
            specific_version=bool(features & UpdateEntityFeature.SPECIFIC_VERSION),
        )

    def _data(self) -> dict[str, Any]:
        return {
            "first_seen": {
                entity_id: {"version": s.version, "at": s.at.isoformat()} for entity_id, s in sorted(self._seen.items())
            },
            "lines": {
                entity_id: {line: at.isoformat() for line, at in sorted(lines.items())}
                for entity_id, lines in sorted(self._lines.items())
            },
        }
