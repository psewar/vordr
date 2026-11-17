"""Installs Home Assistant's own updates that the plan calls ready (ha_updates.py; ADR-0162 – ADR-0165, ADR-0181 –
ADR-0183).

Only while the options switch installing on and the pause switch is off, and only from one config entry; one update
at a time, only what the plan calls ready at the moment of acting (checked again right before update.install); Core,
OS and Supervisor last, one per opening of the window; Vördr's own entities never. Before the install a backup: the
entity's own where it has the BACKUP feature, else Home Assistant's automatic backup no older than 26 hours, made if
there is none - except for device firmware and the radio adapter, which no backup of Home Assistant can roll back.
After the install it waits for the entity to report the target version (30 minutes, device firmware and radio 2
hours). A run that installs is kept in a Store before update.install is called, so a restart of Home Assistant in the
middle (Core, OS, Supervisor) is survived: the next start checks its outcome before anything else starts. Every run
ends with the event vordr_ha_update_finished and a fixed result code; a failed install is kept, holds its entity and
is a repair until an install of it goes through. The repair's fix installs it at once, outside the window, under every
other rule. Independent of the hub.
"""

from __future__ import annotations

import asyncio
from collections.abc import Callable
from dataclasses import dataclass, replace
from datetime import datetime, timedelta
import logging
from typing import TYPE_CHECKING, Any, Final

from homeassistant.components.update import (
    ATTR_IN_PROGRESS,
    ATTR_INSTALLED_VERSION,
    ATTR_LATEST_VERSION,
    DOMAIN as UPDATE_DOMAIN,
)
from homeassistant.components.update.const import ATTR_BACKUP, ATTR_VERSION, SERVICE_INSTALL
from homeassistant.const import ATTR_ENTITY_ID, STATE_OFF, STATE_ON
from homeassistant.core import CALLBACK_TYPE, CoreState, HomeAssistant, State, callback
from homeassistant.helpers import entity_registry as er, issue_registry as ir
from homeassistant.helpers.event import async_call_later, async_track_state_change_event, async_track_time_interval
from homeassistant.helpers.start import async_at_started
from homeassistant.helpers.storage import Store
from homeassistant.util import dt as dt_util

from .const import DOMAIN, EVENT_HA_UPDATE_FINISHED
from .ha_updates import (
    AUTO_INSTALL_OFF,
    BACKING_UP,
    BACKUP_CREATED,
    BACKUP_ENTITY,
    BACKUP_EXEMPT,
    BACKUP_EXISTING,
    BACKUP_FAILED,
    BACKUP_UNAVAILABLE,
    BUSY,
    FAILED,
    GONE,
    IN_PROGRESS,
    INSTALLING,
    KEPT,
    NOT_REACHED,
    OK,
    OTHER_ENTRY,
    OUTSIDE_WINDOW,
    OWN_PLATFORM,
    READY,
    RESULTS,
    SKIPPED_NOT_READY,
    SKIPPED_PAUSED,
    SYSTEM_KINDS,
    TARGET_CHANGED,
    TIMEOUT,
    VERIFYING,
    Facts,
    Failure,
    Planned,
    backup_fresh,
    clear_failures,
    held,
    install_order,
    needs_ha_backup,
    next_install,
    policy_from_options,
    reached,
    verify_timeout,
    window_start,
)
from .ha_updates_tracker import HaUpdatesTracker

if TYPE_CHECKING:
    from homeassistant.config_entries import ConfigEntry

_LOGGER = logging.getLogger(__name__)

STORE_VERSION = 1
TICK = timedelta(minutes=1)
BACKUP_TIMEOUT = timedelta(hours=2)  # backup.create_automatic: a large Home Assistant takes its time
BACKUP_SEEN = timedelta(minutes=5)  # after it: until the backup integration's sensor shows the new backup
BACKUP_RETRY = timedelta(hours=24)  # after a failed backup: no other attempt before, unless a backup shows up
INSTALL_TIMEOUT = timedelta(hours=2)  # update.install: firmware over Zigbee can take long
# After it, or after a restart, until the entity reports the version: verify_timeout(kind) (ADR-0183).

# Home Assistant's backup integration, told by the registry (platform and unique id), not by entity ids.
BACKUP_DOMAIN: Final = "backup"
BACKUP_SERVICE: Final = "create_automatic"
BACKUP_MANAGER_UID: Final = "backup_manager_state"
BACKUP_LAST_UID: Final = "last_successful_automatic_backup"
BACKUP_IDLE: Final = "idle"

# What the installer does now, as a fact: the attribute `installer` of the last-run sensor (texts: translations).
INSTALLER_OFF: Final = "off"  # the options do not switch installing on (the default)
INSTALLER_PAUSED: Final = "paused"  # the pause switch is on
INSTALLER_OTHER_ENTRY: Final = "other_entry"  # another Vördr entry installs too: neither does
INSTALLER_IDLE: Final = "idle"  # on, waiting for something ready
INSTALLER_RUNNING: Final = "running"  # a run is going on (also while the outcome of one before a restart is checked)
INSTALLER_STATES: Final = (INSTALLER_OFF, INSTALLER_PAUSED, INSTALLER_OTHER_ENTRY, INSTALLER_IDLE, INSTALLER_RUNNING)

ISSUE_FAILED = "ha_update_failed_"  # + the entity's object id; translation key ha_update_<result>
ISSUE_BACKUP = "ha_updates_backup_failed"
FIX_KIND = "ha_update_retry"  # the repair's data kind: repairs.py picks the fix flow by it

# What the repair's fix did (ADR-0182): STARTED, or why it did not start an install now. Nothing is queued: the fix
# flow aborts with the code, texts in translations (issues.ha_update_<result>.fix_flow.abort).
STARTED: Final = "started"
NO_LONGER_NEEDED: Final = "no_longer_needed"  # the failure is gone
FIX_BUSY: Final = "busy"  # another install runs (Vördr's or anyone's), or Home Assistant's backup manager is working
FIX_PAUSED: Final = "paused"  # the pause switch is on
FIX_OFF: Final = "auto_install_off"  # the options do not switch installing on
FIX_OTHER_ENTRY: Final = "other_entry"  # another Vördr entry installs too
FIX_BACKUP_HELD: Final = "backup_held"  # it needs Home Assistant's backup, which failed lately (ADR-0163)
FIX_NOT_READY: Final = "not_ready"  # the rules do not let it through now, apart from the window
FIX_REFUSALS: Final = (NO_LONGER_NEEDED, FIX_BUSY, FIX_PAUSED, FIX_OFF, FIX_OTHER_ENTRY, FIX_BACKUP_HELD, FIX_NOT_READY)


def store_key(entry_id: str) -> str:
    """The Store of one config entry's runs (.storage/vordr.ha_updates_runs.<entry_id>)."""
    return f"{DOMAIN}.ha_updates_runs.{entry_id}"


async def async_remove_store(hass: HomeAssistant, entry_id: str) -> None:
    """The entry is deleted: its runs and kept failures go with it."""
    await Store[dict[str, Any]](hass, STORE_VERSION, store_key(entry_id)).async_remove()


def issue_id(entity_id: str) -> str:
    """The repair of a kept failed install of one update entity."""
    return ISSUE_FAILED + entity_id.partition(".")[2]


@dataclass(frozen=True)
class Run:
    """One install: what it is for, how it was backed up, its phase. restarted: Home Assistant started again while
    it ran; its outcome is checked from the entity's state. by_fix: started by a repair's fix, at once and outside the
    window (ADR-0182)."""

    entity_id: str
    kind: str
    from_version: str | None
    to_version: str
    started: datetime
    phase: str
    backup: str | None = None
    restarted: bool = False
    by_fix: bool = False


def _text(value: Any) -> str | None:
    return value if isinstance(value, str) and value else None


def _time(value: Any) -> datetime | None:
    at = dt_util.parse_datetime(value) if isinstance(value, str) else None
    return at if at is not None and at.tzinfo is not None else None


def _iso(at: datetime | None) -> str | None:
    return at.isoformat() if at is not None else None


def _run_from(raw: Any) -> Run | None:
    """The run kept in the Store, or None when there is none or it is not one."""
    if not isinstance(raw, dict):
        return None
    entity_id, kind, to = _text(raw.get("entity_id")), _text(raw.get("kind")), _text(raw.get("to_version"))
    started = _time(raw.get("started"))
    if entity_id is None or kind is None or to is None or started is None:
        return None
    return Run(entity_id, kind, _text(raw.get("from_version")), to, started, INSTALLING, _text(raw.get("backup")))


def _failure_from(entity_id: str, raw: Any) -> Failure | None:
    if not isinstance(raw, dict):
        return None
    to, result, at = _text(raw.get("to_version")), raw.get("result"), _time(raw.get("at"))
    if to is None or result not in KEPT or at is None:
        return None
    return Failure(entity_id, _text(raw.get("from_version")), to, result, at, raw.get("released") is True)


def _in_progress(state: State) -> bool:
    return state.attributes.get(ATTR_IN_PROGRESS) not in (None, False)


class HaUpdatesInstaller:
    """Acts on the tracker's plan: one run at a time, its outcome kept across restarts, failures kept until an install
    goes through."""

    def __init__(self, hass: HomeAssistant, entry: ConfigEntry, tracker: HaUpdatesTracker) -> None:
        self.hass = hass
        self.entry = entry
        self.tracker = tracker
        self._store: Store[dict[str, Any]] = Store(hass, STORE_VERSION, store_key(entry.entry_id))
        self.paused = False
        self.run: Run | None = None  # the run going on
        self.last: dict[str, Any] | None = None  # the last run that ended, as the event carried it
        self.failures: dict[str, Failure] = {}
        self.backup_failed_at: datetime | None = None
        self.system_window: datetime | None = None  # start of the window opening a system update ran in
        self._task: asyncio.Task[None] | None = None
        self._detached = False  # the entry unloaded: a run left going decides nothing; the next start does
        self._listeners: list[CALLBACK_TYPE] = []
        self._other_warned = False

    # --- life cycle

    async def async_start(self) -> None:
        """Load what is kept. A run kept from before a restart is checked first; nothing else starts until its outcome
        is decided."""
        self._load(await self._store.async_load())
        # A released failure stands only while the run of its fix goes (ADR-0182); without that run (it ended before
        # it installed, or Home Assistant stopped while it prepared) the version is held again and the repair returns.
        for entity_id, f in list(self.failures.items()):
            if f.released and (self.run is None or self.run.entity_id != entity_id):
                self.failures[entity_id] = replace(f, released=False)
        self._hold()  # the plan with the held versions, before anything acts on it
        self._check()
        self.entry.async_on_unload(self.tracker.async_add_listener(self._on_plan))
        self.entry.async_on_unload(async_track_time_interval(self.hass, self._on_tick, TICK))
        self.entry.async_on_unload(self.entry.add_update_listener(self._on_options))
        self.entry.async_on_unload(self._detach)
        self._issues()
        if self.run is not None:
            run = self.run = replace(self.run, phase=VERIFYING, restarted=True)
            self._task = self.hass.async_create_background_task(
                self._resume(run), f"vordr check {run.entity_id}", eager_start=False
            )
            return
        self.poke()

    @callback
    def _detach(self) -> None:
        """The entry unloads. A run going on is not cancelled (it may be an install half done); what it finds decides
        nothing. The run is kept in the Store, so the next start checks its outcome."""
        self._detached = True

    @callback
    def async_add_listener(self, listener: CALLBACK_TYPE) -> Callable[[], None]:
        """Call listener whenever the installer's state changes; returns the function that removes it."""
        self._listeners.append(listener)
        return lambda: self._listeners.remove(listener)

    @callback
    def _changed(self) -> None:
        for listener in list(self._listeners):
            listener()

    # --- what it does now

    @property
    def status(self) -> str:
        if self._task is not None:
            return INSTALLER_RUNNING
        if not self.tracker.policy.auto_install:
            return INSTALLER_OFF
        if self.paused:
            return INSTALLER_PAUSED
        if self._other_entry():
            return INSTALLER_OTHER_ENTRY
        return INSTALLER_IDLE

    def queue(self) -> list[str]:
        """The ready updates in the order they would be installed."""
        return [p.entity_id for p in install_order(self.tracker.planned)]

    def _other_entry(self) -> bool:
        """Another enabled entry of Vördr has installing switched on too: then none installs (ADR-0162)."""
        others = [
            e
            for e in self.hass.config_entries.async_entries(DOMAIN, include_ignore=False, include_disabled=False)
            if e.entry_id != self.entry.entry_id
            and (p := policy_from_options(e.options)) is not None
            and p.auto_install
        ]
        if others and not self._other_warned:
            _LOGGER.warning("another Vördr entry installs Home Assistant's updates too; neither installs")
        self._other_warned = bool(others)
        return bool(others)

    def _system_done(self) -> bool:
        start = window_start(self.tracker.clock(), self.tracker.policy)
        return start is not None and self.system_window == start

    def _backup_held(self) -> bool:
        at = self.backup_failed_at
        return at is not None and self.tracker.clock() - at < BACKUP_RETRY

    def _backup_entity_id(self, unique_id: str) -> str | None:
        return er.async_get(self.hass).async_get_entity_id("sensor", BACKUP_DOMAIN, unique_id)

    def _backup_entity(self, unique_id: str) -> State | None:
        entity_id = self._backup_entity_id(unique_id)
        return self.hass.states.get(entity_id) if entity_id is not None else None

    def _last_backup(self) -> datetime | None:
        state = self._backup_entity(BACKUP_LAST_UID)
        return _time(state.state) if state is not None else None

    def _backup_busy(self) -> bool:
        """Home Assistant's backup manager is making or restoring a backup (its sensor says so)."""
        state = self._backup_entity(BACKUP_MANAGER_UID)
        return state is not None and state.state not in (BACKUP_IDLE, "unknown", "unavailable")

    def _busy(self) -> bool:
        return any(p.code == IN_PROGRESS for p in self.tracker.planned) or self._backup_busy()

    # --- triggers

    @callback
    def _on_plan(self) -> None:
        self._check()
        self.poke()

    @callback
    def _on_tick(self, _now: datetime) -> None:
        self._look()

    async def _on_options(self, _hass: HomeAssistant, _entry: ConfigEntry) -> None:
        """Installing switched on or off: the plan does not change with it, so look now, with the rules as saved."""
        self._look()

    @callback
    def _look(self) -> None:
        """The plan as it is now (time passes without any state changing), then what stands and what starts."""
        self.tracker.refresh()
        self._check()
        self.poke()

    @callback
    def poke(self) -> None:
        """Start the next run, if there is one to start now."""
        if self._task is not None or self._detached:
            return
        candidate = None
        if self.status == INSTALLER_IDLE and not self._busy():
            candidate = next_install(self.tracker.planned, self.tracker.facts, self._system_done(), self._backup_held())
        if candidate is not None:
            # Not started eagerly: the run must not begin before it is the one run, or a plan change it causes would
            # start a second one.
            self._task = self.hass.async_create_background_task(
                self._run(candidate), f"vordr install {candidate.entity_id}", eager_start=False
            )
        self._changed()

    async def async_set_paused(self, paused: bool) -> None:
        """The pause switch: while on, no run starts, and one preparing does not install (skipped_paused)."""
        if self.paused != paused:
            self.paused = paused
            await self._save()
        self._changed()
        self._look()

    async def async_install_now(self, entity_id: str) -> str:
        """The repair's fix (ADR-0182): install what the entity offers at once, outside the window, under every other
        rule, with the backup before it and the check right before acting; one install at a time. STARTED, or the code
        why not - nothing is queued. The failure stays until an install goes through; its repair goes while the run
        goes and comes back if the run fails or does not get to install."""
        if (refused := self._fix_refusal(entity_id)) is not None:
            return refused
        failure = self.failures[entity_id]
        p = self._fix_target(entity_id)
        assert p is not None  # _fix_refusal checked it
        self.failures[entity_id] = replace(failure, released=True)
        # The run is the one run before anything else looks: releasing the hold changes the plan, and its listener
        # must not start another run first.
        self._task = self.hass.async_create_background_task(
            self._run(p, by_fix=True), f"vordr install {entity_id} (repair)", eager_start=False
        )
        await self._save()
        self._issues()
        self._hold()
        self._changed()
        return STARTED

    def _fix_target(self, entity_id: str) -> Planned | None:
        """What the fix would install: the entity's plan as it is now with its own hold left out."""
        others = {e: v for e, v in held(self.failures).items() if e != entity_id}
        return next((p for p in self.tracker.planned_with(others) if p.entity_id == entity_id), None)

    def _fix_refusal(self, entity_id: str) -> str | None:
        """Checked before the fix acts: None if it may install now, else the code why not."""
        if entity_id not in self.failures:
            return NO_LONGER_NEEDED
        self.tracker.refresh()
        if self._task is not None or self._busy():
            return FIX_BUSY
        if self.paused:
            return FIX_PAUSED
        if not self.tracker.policy.auto_install:
            return FIX_OFF
        if self._other_entry():
            return FIX_OTHER_ENTRY
        p = self._fix_target(entity_id)
        f = self.tracker.facts.get(entity_id)
        if p is None or f is None or p.latest is None or p.code not in (READY, OUTSIDE_WINDOW):
            return FIX_NOT_READY
        if self._backup_held() and needs_ha_backup(f):
            return FIX_BACKUP_HELD
        return None

    # --- a run

    async def _run(self, p: Planned, by_fix: bool = False) -> None:
        try:
            await self._steps(p, by_fix)
        finally:
            self._task = None
        self._look()

    async def _resume(self, run: Run) -> None:
        try:
            await self._started()
            await self._finish(run, await self._verify(run))
        finally:
            self._task = None
        self._look()

    async def _steps(self, p: Planned, by_fix: bool) -> None:
        assert p.latest is not None  # ready means pending: installed and latest are known
        run = Run(p.entity_id, p.kind, p.installed, p.latest, self.tracker.clock(), BACKING_UP, by_fix=by_fix)
        chosen = self.tracker.facts.get(p.entity_id)
        if chosen is None:  # gone since it was chosen
            await self._finish(run, SKIPPED_NOT_READY, GONE)
            return
        if chosen.backup:
            run = replace(run, backup=BACKUP_ENTITY)
        elif not needs_ha_backup(chosen):  # device firmware or the radio adapter (ADR-0181)
            run = replace(run, backup=BACKUP_EXEMPT)
        else:
            self.run = run
            self._changed()
            backup, reason = await self._ha_backup()
            if backup is None:
                await self._finish(run, BACKUP_FAILED, reason)
                return
            run = replace(run, backup=backup)
        if self._detached:
            return
        # Check first, then act: the plan as it is now, not as it was when the run was chosen (ADR-0164).
        if (refused := self._recheck(run)) is not None:
            await self._finish(run, *refused)
            return
        facts = self.tracker.facts[run.entity_id]
        run = self.run = replace(run, phase=INSTALLING)
        if run.kind in SYSTEM_KINDS:
            self.system_window = window_start(self.tracker.clock(), self.tracker.policy)
        await self._save()  # before acting: a restart in the middle finds the run and checks its outcome
        self._changed()
        result = await self._install(run, facts)
        if result == OK:
            run = self.run = replace(run, phase=VERIFYING)
            self._changed()
            result = await self._verify(run)
        await self._finish(run, result)

    def _recheck(self, run: Run) -> tuple[str, str | None] | None:
        """Right before update.install: None if it may go on, else the result and the reason it does not."""
        self.tracker.refresh()
        if self.paused:
            return SKIPPED_PAUSED, None
        if not self.tracker.policy.auto_install:
            return SKIPPED_NOT_READY, AUTO_INSTALL_OFF
        if self._other_entry():
            return SKIPPED_NOT_READY, OTHER_ENTRY
        p = next((x for x in self.tracker.planned if x.entity_id == run.entity_id), None)
        f = self.tracker.facts.get(run.entity_id)
        if p is None or f is None or f.platform == OWN_PLATFORM:
            return SKIPPED_NOT_READY, GONE
        if p.latest != run.to_version:
            return SKIPPED_NOT_READY, TARGET_CHANGED
        # A repair's fix installs now: the window is the one rule it does not wait for (ADR-0182).
        if p.code != READY and not (run.by_fix and p.code == OUTSIDE_WINDOW):
            return SKIPPED_NOT_READY, p.code
        if self._busy():
            return SKIPPED_NOT_READY, BUSY
        return None

    async def _ha_backup(self) -> tuple[str | None, str | None]:
        """Home Assistant's automatic backup for an entity that cannot back itself up (ADR-0163): an existing one no
        older than BACKUP_MAX_AGE, else one made now. (kind, None), or (None, reason) when none came about."""
        now = self.tracker.clock()
        if backup_fresh(self._last_backup(), now):
            return BACKUP_EXISTING, None
        sensor = self._backup_entity_id(BACKUP_LAST_UID)
        if sensor is None or not self.hass.services.has_service(BACKUP_DOMAIN, BACKUP_SERVICE):
            return None, BACKUP_UNAVAILABLE
        try:
            async with asyncio.timeout(BACKUP_TIMEOUT.total_seconds()):
                await self.hass.services.async_call(BACKUP_DOMAIN, BACKUP_SERVICE, {}, blocking=True)
        except TimeoutError:
            return None, TIMEOUT
        except Exception as err:  # the boundary to the backup integration: whatever it raises, no backup came about
            _LOGGER.warning("Home Assistant's backup before an update failed: %s", type(err).__name__)
            return None, FAILED
        # The backup integration shows it through its sensor, maybe a moment later.
        seen = await self._wait(lambda: (last := self._last_backup()) is not None and last >= now, BACKUP_SEEN, sensor)
        return (BACKUP_CREATED, None) if seen else (None, BACKUP_UNAVAILABLE)

    async def _wait(self, cond: Callable[[], bool], timeout: timedelta, entity_id: str) -> bool:
        """Wait until cond() holds, checked now and on every state change of entity_id, at most timeout."""
        done: asyncio.Future[bool] = self.hass.loop.create_future()

        @callback
        def check(*_: Any) -> None:
            if not done.done() and cond():
                done.set_result(True)

        @callback
        def give_up(_now: datetime) -> None:
            if not done.done():
                done.set_result(cond())

        unsub_state = async_track_state_change_event(self.hass, entity_id, check)
        unsub_time = async_call_later(self.hass, timeout, give_up)
        check()
        try:
            return await done
        finally:
            unsub_state()
            unsub_time()

    async def _install(self, run: Run, f: Facts) -> str:
        """update.install for the checked version, with the entity's own backup where it has one. Its errors are
        mapped to codes here, at the boundary, and nowhere else."""
        data: dict[str, Any] = {ATTR_ENTITY_ID: run.entity_id}
        if f.backup:
            data[ATTR_BACKUP] = True
        if f.specific_version:
            data[ATTR_VERSION] = run.to_version  # exactly the version the rules let through
        try:
            async with asyncio.timeout(INSTALL_TIMEOUT.total_seconds()):
                await self.hass.services.async_call(UPDATE_DOMAIN, SERVICE_INSTALL, data, blocking=True)
        except TimeoutError:
            return TIMEOUT
        except Exception as err:  # the boundary to another integration's install: anything it raises is a failure
            _LOGGER.warning("installing %s %s failed: %s", run.entity_id, run.to_version, type(err).__name__)
            return FAILED
        return OK

    def _reached(self, run: Run) -> bool:
        state = self.hass.states.get(run.entity_id)
        if state is None or state.state not in (STATE_ON, STATE_OFF) or _in_progress(state):
            return False
        a = state.attributes
        return reached(
            _text(a.get(ATTR_INSTALLED_VERSION)), _text(a.get(ATTR_LATEST_VERSION)), run.from_version, run.to_version
        )

    async def _verify(self, run: Run) -> str:
        """Wait until the entity is no longer in progress and reports the target version: ok. Otherwise, after
        verify_timeout(kind) (ADR-0183): timeout while it is still in progress, not_reached when it is not."""
        if await self._wait(lambda: self._reached(run), verify_timeout(run.kind), run.entity_id):
            return OK
        state = self.hass.states.get(run.entity_id)
        return TIMEOUT if state is not None and _in_progress(state) else NOT_REACHED

    async def _started(self) -> None:
        """Wait until Home Assistant has started: after a restart the entity's integration may load late."""
        if self.hass.state is CoreState.running:
            return
        started: asyncio.Future[None] = self.hass.loop.create_future()

        @callback
        def on_started(_hass: HomeAssistant) -> None:
            if not started.done():
                started.set_result(None)

        unsub = async_at_started(self.hass, on_started)
        try:
            await started
        finally:
            unsub()

    async def _finish(self, run: Run, result: str, reason: str | None = None) -> None:
        """The run ended: keep its result, the failure or its clearing, then the event, repairs and plan."""
        if self._detached:
            return
        state = self.hass.states.get(run.entity_id)
        installed = _text(state.attributes.get(ATTR_INSTALLED_VERSION)) if state is not None else None
        now = self.tracker.clock()
        self.last = {
            "entity_id": run.entity_id,
            "kind": run.kind,
            "from_version": run.from_version,
            "to_version": run.to_version,
            "installed_version": installed,
            "result": result,
            "reason": reason,
            "backup": run.backup,
            "started": run.started.isoformat(),
            "finished": now.isoformat(),
            "restarted": run.restarted,
        }
        self.run = None
        if result == OK:
            self.failures.pop(run.entity_id, None)  # an install of it went through (ADR-0165)
        elif result in KEPT:
            self.failures[run.entity_id] = Failure(run.entity_id, run.from_version, run.to_version, result, now)
        elif (f := self.failures.get(run.entity_id)) is not None and f.released:
            # A fix's run that did not get to install: held again, its repair comes back (ADR-0182).
            self.failures[run.entity_id] = replace(f, released=False)
        if result == BACKUP_FAILED:
            self.backup_failed_at = now
        if run.backup == BACKUP_CREATED:
            self.backup_failed_at = None
        await self._save()
        self.hass.bus.async_fire(EVENT_HA_UPDATE_FINISHED, dict(self.last))
        self._issues()
        self._hold()
        self._changed()

    # --- kept failures, repairs

    @callback
    def _check(self) -> None:
        """Clear what no longer stands: failures whose entity reached the version or has nothing pending, and the
        failed backup once Home Assistant shows a newer one."""
        ent_reg = er.async_get(self.hass)
        registered = {e for e in self.failures if ent_reg.async_is_registered(e)}
        failures = clear_failures(self.failures, self.tracker.facts.values(), registered)
        changed = failures != self.failures
        self.failures = failures
        last = self._last_backup()
        if self.backup_failed_at is not None and last is not None and last > self.backup_failed_at:
            self.backup_failed_at = None  # Home Assistant has made a backup since
            changed = True
        if changed:
            self._store.async_delay_save(self._data, 1.0)
            self._issues()
            self._hold()
            self._changed()

    @callback
    def _hold(self) -> None:
        """Tell the plan which versions are held after a failed install."""
        self.tracker.held = held(self.failures)
        self.tracker.refresh()

    @callback
    def _issues(self) -> None:
        """One repair per kept failure that is not released, and one while Home Assistant's backup failed; the
        others of this entry go."""
        wanted: set[str] = set()
        for entity_id, f in self.failures.items():
            if f.released:
                continue
            wanted.add(issue_id(entity_id))
            ir.async_create_issue(
                self.hass,
                DOMAIN,
                issue_id(entity_id),
                is_fixable=True,
                severity=ir.IssueSeverity.WARNING,
                translation_key=f"ha_update_{f.result}",
                translation_placeholders=self.failure_names(entity_id),
                data={"entry_id": self.entry.entry_id, "entity_id": entity_id, "kind": FIX_KIND},
            )
        if self.backup_failed_at is not None:
            wanted.add(ISSUE_BACKUP)
            ir.async_create_issue(
                self.hass,
                DOMAIN,
                ISSUE_BACKUP,
                is_fixable=False,
                severity=ir.IssueSeverity.WARNING,
                translation_key=ISSUE_BACKUP,
                translation_placeholders={"at": _local(self.backup_failed_at)},
                data={"entry_id": self.entry.entry_id},
            )
        for (domain, iid), entry in list(ir.async_get(self.hass).issues.items()):
            mine = (entry.data or {}).get("entry_id") == self.entry.entry_id
            if domain == DOMAIN and mine and iid.startswith((ISSUE_FAILED, ISSUE_BACKUP)) and iid not in wanted:
                ir.async_delete_issue(self.hass, DOMAIN, iid)

    def failure_names(self, entity_id: str) -> dict[str, str]:
        """How a repair names a kept failure."""
        return failure_names(entity_id, self.failures.get(entity_id))

    # --- the Store

    def _load(self, data: Any) -> None:
        """What is kept; a part that is not what it should be is left out."""
        d = data if isinstance(data, dict) else {}
        self.paused = d.get("paused") is True
        self.run = _run_from(d.get("run"))
        last = d.get("last")
        self.last = last if isinstance(last, dict) and last.get("result") in RESULTS else None
        raw = d.get("failures")
        self.failures = {}
        for entity_id, f in (raw if isinstance(raw, dict) else {}).items():
            if (failure := _failure_from(entity_id, f)) is not None:
                self.failures[entity_id] = failure
        self.backup_failed_at = _time(d.get("backup_failed_at"))
        self.system_window = _time(d.get("system_window"))
        self.tracker.held = held(self.failures)

    def _data(self) -> dict[str, Any]:
        run = self.run if self.run is not None and self.run.phase != BACKING_UP else None
        return {
            "paused": self.paused,
            "run": None
            if run is None
            else {
                "entity_id": run.entity_id,
                "kind": run.kind,
                "from_version": run.from_version,
                "to_version": run.to_version,
                "backup": run.backup,
                "started": run.started.isoformat(),
            },
            "last": self.last,
            "failures": {
                e: {
                    "from_version": f.from_version,
                    "to_version": f.to_version,
                    "result": f.result,
                    "at": f.at.isoformat(),
                    "released": f.released,
                }
                for e, f in sorted(self.failures.items())
            },
            "backup_failed_at": _iso(self.backup_failed_at),
            "system_window": _iso(self.system_window),
        }

    async def _save(self) -> None:
        await self._store.async_save(self._data())


def _local(at: datetime | None) -> str:
    return dt_util.as_local(at).isoformat(timespec="minutes") if at is not None else "-"


def failure_names(entity_id: str, f: Failure | None) -> dict[str, str]:
    """The placeholders of a failed install's repair: always all of them, "-" for what is not known."""
    return {
        "entity_id": entity_id,
        "from_version": (f.from_version or "-") if f is not None else "-",
        "to_version": f.to_version if f is not None else "-",
        "at": _local(f.at) if f is not None else "-",
    }
