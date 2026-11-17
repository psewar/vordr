"""Home Assistant's own updates under Vördr's rules: the plan, as pure functions (ADR-0158 – ADR-0160, ADR-0179,
ADR-0180), and the rules of installing it (ADR-0162 – ADR-0165, ADR-0181, ADR-0183).

For each of Home Assistant's update entities (Core, OS, apps, HACS, ESPHome, device firmware, ...): would Vördr's rules
install it now, and if not, why - as a fixed code - and from when; which ready update goes next; when a failed install
is kept and when it is cleared. The facts come from Home Assistant's states and registries (ha_updates_tracker.py), the
rules from the integration's options; no Home Assistant call in here, so every rule is tested on its own. What acts on
the plan is ha_updates_installer.py, and only once the options switch it on.
"""

from __future__ import annotations

from collections.abc import Collection, Iterable, Mapping
from dataclasses import dataclass
from datetime import datetime, time, timedelta
import re
from typing import Any, Final

OWN_PLATFORM: Final = "vordr"  # Vördr's own update entities follow the hub's rules, never these (ADR-0158)

# The options of this item of the options menu, under this key of the config entry's options.
OPTIONS_KEY: Final = "ha_updates"
CONF_KINDS: Final = "kinds"
CONF_ENTITIES: Final = "entities"
CONF_DAYS: Final = "days"
CONF_START: Final = "start"
CONF_END: Final = "end"
CONF_MIN_AGE: Final = "min_age_days"
CONF_AUTO_INSTALL: Final = "auto_install"  # off unless a person switches it on (ADR-0162)

# Kinds of update entities, told by the registry: the entity's integration (platform), its device, its device class.
# Never by the entity id: anything may be called update.vordr_... or update.home_assistant_core_update.
KINDS: Final = ("core", "os", "supervisor", "app", "hacs", "esphome", "firmware", "radio", "other")
HASSIO: Final = "hassio"
HASSIO_DEVICES: Final = {"core": "core", "OS": "os", "supervisor": "supervisor"}  # any other device: an app
HACS_PLATFORMS: Final = frozenset({"hacs", "marketplace"})  # the platform names HACS registers its entities under
# The firmware of the radio adapter that carries the Zigbee or Thread network: a kind of its own, so that allowing
# device firmware does not flash the network's coordinator.
RADIO_PLATFORMS: Final = frozenset(
    {"homeassistant_connect_zbt2", "homeassistant_hardware", "homeassistant_sky_connect", "homeassistant_yellow"}
)
FIRMWARE: Final = "firmware"  # UpdateDeviceClass.FIRMWARE

# Reason codes, one per update entity with something pending. Texts: translations/*.json, the attribute names of
# sensor.vordr_ha_updates_ready.
READY: Final = "ready"  # allowed, old enough, in the window: Vördr would install it now
NOT_ALLOWED: Final = "not_allowed"  # neither its kind nor the entity is chosen in the options
OUTSIDE_WINDOW: Final = "outside_window"  # waits for the window (earliest: when it opens)
TOO_NEW: Final = "too_new"  # first seen less than the minimum age ago (earliest: when it is old enough, in a window)
MAJOR: Final = "major"  # a new major version stays manual (ADR-0160)
# Vördr's install of this very version failed: not tried again until another version is offered or a person acts
# (ADR-0165).
LAST_FAILED: Final = "last_failed"
SKIPPED: Final = "skipped"  # the person skipped this version in Home Assistant
SELF_UPDATING: Final = (
    "self_updating"  # the entity installs its updates itself (auto_update): reported, never installed
)
IN_PROGRESS: Final = "in_progress"  # an install is running
NO_INSTALL: Final = "no_install"  # the entity cannot install (no INSTALL feature)
CODES: Final = (
    READY,
    NOT_ALLOWED,
    OUTSIDE_WINDOW,
    TOO_NEW,
    MAJOR,
    LAST_FAILED,
    SKIPPED,
    SELF_UPDATING,
    IN_PROGRESS,
    NO_INSTALL,
)

DAYS: Final = ("mon", "tue", "wed", "thu", "fri", "sat", "sun")  # index = datetime.weekday()
MIN_AGE_DEFAULT: Final = 7  # days, like the minimum age of pip and npm versions (ADR-0068)
MIN_AGE_MIN: Final = 1
MIN_AGE_MAX: Final = 90
# Device firmware and the radio adapter's firmware wait at least this long, or the minimum age if that is longer: a
# broken firmware is the hardest update to undo, no backup of Home Assistant reaches into a device (ADR-0180).
FIRMWARE_KINDS: Final = frozenset({"firmware", "radio"})
FIRMWARE_MIN_AGE_DAYS: Final = 14
# A newer release of a line seen before (the same major.minor) counts its age from the line's first sight, but waits
# at least this long itself (never longer than the minimum age), like the agent's min_age_days_security (ADR-0179).
PATCH_MIN_AGE_DAYS: Final = 2

# Error codes of check_options; texts: translations/*.json, options.error.
ERR_KIND: Final = "invalid_kind"
ERR_ENTITY: Final = "invalid_entity"
ERR_DAY: Final = "invalid_day"
ERR_TIME: Final = "invalid_time"
ERR_WINDOW_EMPTY: Final = "window_empty"
ERR_NEEDS_DAYS: Final = "window_needs_days"
ERR_MIN_AGE: Final = "min_age_days"
ERR_AUTO_INSTALL: Final = "invalid_auto_install"
ERRORS: Final = (
    ERR_KIND,
    ERR_ENTITY,
    ERR_DAY,
    ERR_TIME,
    ERR_WINDOW_EMPTY,
    ERR_NEEDS_DAYS,
    ERR_MIN_AGE,
    ERR_AUTO_INSTALL,
)
# Refused by the options flow, which sees the other config entries: one entry installs, never two (ADR-0162).
ERR_AUTO_INSTALL_OTHER: Final = "auto_install_other_entry"

# --- installing (ADR-0162 – ADR-0165)

# Core, OS and Supervisor restart Home Assistant or the machine: after every other kind, in this order (a Core or OS
# release may need a current Supervisor; OS restarts the whole machine), one per opening of the window, alone.
SYSTEM_KINDS: Final = ("supervisor", "core", "os")

# A run: its phases while it runs, its result once it ended. The event vordr_ha_update_finished and the sensor
# sensor.vordr_ha_updates_last_run carry these; texts: translations/*.json.
BACKING_UP: Final = "backing_up"  # Home Assistant's automatic backup is made before an entity without its own
INSTALLING: Final = "installing"  # update.install is called
VERIFYING: Final = "verifying"  # waiting for the entity to report the target version
PHASES: Final = (BACKING_UP, INSTALLING, VERIFYING)
OK: Final = "ok"  # the entity reports the target version
FAILED: Final = "failed"  # update.install failed: the entity's integration refused or raised
NOT_REACHED: Final = "not_reached"  # the install ended, but the entity does not report the target version
TIMEOUT: Final = "timeout"  # the install or the entity's progress did not end in time
BACKUP_FAILED: Final = "backup_failed"  # no fresh backup of Home Assistant came about: nothing was installed
SKIPPED_PAUSED: Final = "skipped_paused"  # paused while the run prepared: nothing was installed
SKIPPED_NOT_READY: Final = "skipped_not_ready"  # not ready any more at the moment of acting: nothing was installed
RESULTS: Final = (OK, FAILED, NOT_REACHED, TIMEOUT, BACKUP_FAILED, SKIPPED_PAUSED, SKIPPED_NOT_READY)
KEPT: Final = (FAILED, NOT_REACHED, TIMEOUT)  # results that hold the entity and make a repair (ADR-0165)

# Why a run did not act at the moment of acting (the event's reason with skipped_not_ready), besides the plan's codes.
GONE: Final = "gone"  # the entity offers nothing any more, or is unavailable
TARGET_CHANGED: Final = "target_changed"  # it offers another version than the one the run was for
AUTO_INSTALL_OFF: Final = "auto_install_off"  # the options switched installing off
OTHER_ENTRY: Final = "other_entry"  # another config entry of Vördr installs too: none does
BUSY: Final = "busy"  # another update is being installed, or Home Assistant's backup manager is working
BACKUP_UNAVAILABLE: Final = "backup_unavailable"  # Home Assistant offers no automatic backup to check or make

# How a run was backed up (ADR-0163).
BACKUP_ENTITY: Final = "entity"  # by the entity itself: update.install with backup (the BACKUP feature)
BACKUP_EXISTING: Final = "existing"  # Home Assistant's automatic backup, made within BACKUP_MAX_AGE
BACKUP_CREATED: Final = "created"  # Home Assistant's automatic backup, made for this run
# None: device firmware and the radio adapter, which no backup of Home Assistant can roll back (ADR-0181).
BACKUP_EXEMPT: Final = "exempt"
BACKUPS: Final = (BACKUP_ENTITY, BACKUP_EXISTING, BACKUP_CREATED, BACKUP_EXEMPT)
BACKUP_MAX_AGE: Final = timedelta(hours=26)  # like the hosts' backup check (ADR-0030); not an option

# How long Vördr waits for the entity to report the target version after update.install returned (ADR-0183): a device
# that flashes its firmware over Zigbee or Thread and restarts takes far longer than an app or Core.
VERIFY_TIMEOUT: Final = timedelta(minutes=30)
VERIFY_TIMEOUT_FIRMWARE: Final = timedelta(hours=2)

_TIME = re.compile(r"(\d{1,2}):(\d{2})(?::(\d{2}))?")
_ENTITY = re.compile(r"update\.[a-z0-9_]+")
# A release as the update entities name it: numbers separated by dots, at least major and minor, after one optional
# "v" (HACS tags "v1.4.2", Core "2026.11.1", OS "16.3"). Nothing else: no pre-release, build or hex suffix.
_RELEASE = re.compile(r"[vV]?([0-9]{1,9})\.([0-9]{1,9})(?:\.[0-9]{1,9})*")


@dataclass(frozen=True)
class Facts:
    """One update entity as Home Assistant shows it: its state and attributes, and from the registries its
    integration (platform, None without a registry entry) and kind."""

    entity_id: str
    platform: str | None
    kind: str
    state: str  # "on": an update is pending, "off": up to date or skipped; anything else: not known now
    installed: str | None
    latest: str | None
    skipped: str | None = None
    auto_update: bool = False
    in_progress: bool = False
    installable: bool = False  # supported_features has INSTALL
    backup: bool = False  # ... BACKUP: update.install backs up first when asked to (ADR-0163)
    specific_version: bool = False  # ... SPECIFIC_VERSION: update.install takes the version to install


@dataclass(frozen=True)
class Seen:
    """When the integration first saw an update entity offer a version (ADR-0159)."""

    version: str
    at: datetime


@dataclass(frozen=True)
class Planned:
    """What the rules say about one update entity: a code and, where it waits for a time, the earliest time it would be
    ready if nothing else changes. line_seen: the first sight of the offered version's line, when the version inherits
    its age from it (ADR-0179)."""

    entity_id: str
    kind: str
    code: str
    installed: str | None
    latest: str | None
    first_seen: datetime | None = None
    earliest: datetime | None = None
    line_seen: datetime | None = None


@dataclass(frozen=True)
class Policy:
    """The rules from the options. The default allows nothing: closed until a person chooses (ADR-0158), and nothing
    installs until a person switches installing on (ADR-0162)."""

    kinds: frozenset[str] = frozenset()
    entities: frozenset[str] = frozenset()
    days: frozenset[str] = frozenset()
    start: time = time(2, 0)
    end: time = time(5, 0)
    min_age: timedelta = timedelta(days=MIN_AGE_DEFAULT)
    auto_install: bool = False

    def allows(self, f: Facts) -> bool:
        return f.kind in self.kinds or f.entity_id in self.entities

    def min_age_for(self, kind: str) -> timedelta:
        """The minimum age of a kind: the one of the options; for device firmware and the radio adapter at least
        FIRMWARE_MIN_AGE_DAYS (ADR-0180)."""
        if kind in FIRMWARE_KINDS:
            return max(self.min_age, timedelta(days=FIRMWARE_MIN_AGE_DAYS))
        return self.min_age


CLOSED: Final = Policy()


def kind_of(platform: str | None, device_class: str | None, device_identifiers: Collection[tuple[str, str]]) -> str:
    """The kind of an update entity from the registries: Core, OS and Supervisor by their device of the Supervisor
    integration, any other device of it is an app; HACS, ESPHome and the radio adapters by their integration; other
    firmware by the device class."""
    if platform == HASSIO:
        for domain, ident in device_identifiers:
            if domain == HASSIO:
                return HASSIO_DEVICES.get(ident, "app")
        return "other"
    if platform in HACS_PLATFORMS:
        return "hacs"
    if platform == "esphome":
        return "esphome"
    if platform in RADIO_PLATFORMS:
        return "radio"
    if device_class == FIRMWARE:
        return "firmware"
    return "other"


def _leading_number(version: str | None) -> int | None:
    """The number a version begins with, after one leading "v" (tags such as "v2.1.0"); None without one."""
    if not version:
        return None
    if version.startswith(("v", "V")):
        version = version[1:]
    end = 0
    while end < len(version) and end < 9 and version[end].isascii() and version[end].isdigit():
        end += 1
    return int(version[:end]) if end else None


def major_jump(installed: str | None, latest: str | None) -> bool:
    """Installed and offered version begin with different numbers ("1.4.2" -> "2.0.1", "v2.9" -> "v3.0"), as the
    agent's rule for winget (internal/source MajorJump, ADR-0052). Not a jump: a version without a leading number,
    and calendar versions (a first number from 1000 on: Core "2026.10.4" -> "2026.11.0" is a month, not a break)."""
    a, b = _leading_number(installed), _leading_number(latest)
    return a is not None and b is not None and a != b and a < 1000 and b < 1000


def line_of(version: str | None) -> str | None:
    """The line of a release: its major and minor number ("v1.4.2" -> "1.4", Core "2026.11.1" -> "2026.11", OS "16.3"
    -> "16.3"). None for anything else - firmware such as "0x01020304", a commit, a pre-release such as "2026.12.0b1",
    a single number: such a version inherits no age, it waits its own (ADR-0179)."""
    m = _RELEASE.fullmatch(version) if version else None
    return f"{int(m.group(1))}.{int(m.group(2))}" if m is not None else None


def ready_from(first_seen: datetime | None, line_seen: datetime | None, min_age: timedelta, now: datetime) -> datetime:
    """When a version is old enough (ADR-0159, ADR-0179). Its age counts from its first sight; unknown counts as now.
    A version of a line seen before it (line_seen) counts from the line's first sight instead, but not before it is
    itself PATCH_MIN_AGE_DAYS old, or the minimum age if that is shorter. min_age: the kind's (Policy.min_age_for,
    ADR-0180)."""
    own = first_seen or now
    if line_seen is None or line_seen >= own:
        return own + min_age
    return max(line_seen + min_age, own + min(min_age, timedelta(days=PATCH_MIN_AGE_DAYS)))


def _minutes(t: time) -> int:
    return t.hour * 60 + t.minute


def in_window(t: datetime, p: Policy) -> bool:
    """Whether t (in Home Assistant's time zone) lies in the window, like the agent's install.window: from start to
    end on the chosen days; if start is after end, the window runs past midnight and belongs to the day it starts on.
    No day, or start equal to end: never."""
    s, e, m = _minutes(p.start), _minutes(p.end), t.hour * 60 + t.minute
    if not p.days or s == e:
        return False
    today = DAYS[t.weekday()] in p.days
    if s < e:
        return today and s <= m < e
    yesterday = DAYS[(t.weekday() + 6) % 7] in p.days
    return (today and m >= s) or (yesterday and m < e)


def window_from(t: datetime, p: Policy) -> datetime | None:
    """The earliest time from t on that lies in the window: t itself while the window is open, else its next start.
    None when the window never opens."""
    if in_window(t, p):
        return t
    if not p.days or _minutes(p.start) == _minutes(p.end):
        return None
    for d in range(8):
        day = (t + timedelta(days=d)).date()
        start = datetime.combine(day, p.start, tzinfo=t.tzinfo)
        if DAYS[day.weekday()] in p.days and start > t:
            return start
    return None


def window_start(t: datetime, p: Policy) -> datetime | None:
    """The start of the opening of the window that t lies in; None outside the window. A window past midnight belongs
    to the day it starts on."""
    if not in_window(t, p):
        return None
    start = datetime.combine(t.date(), p.start, tzinfo=t.tzinfo)
    if _minutes(p.start) < _minutes(p.end) or t.hour * 60 + t.minute >= _minutes(p.start):
        return start
    return datetime.combine(t.date() - timedelta(days=1), p.start, tzinfo=t.tzinfo)


def _decide(
    f: Facts,
    first_seen: datetime | None,
    line_seen: datetime | None,
    p: Policy,
    now: datetime,
    failed: str | None = None,
) -> tuple[str | None, datetime | None]:
    """The code for one update entity and the earliest time it would be ready; (None, None) when nothing is pending.
    line_seen: the first sight of the offered version's line, if it was seen before the version (ADR-0179). failed:
    the version whose install failed and is held (ADR-0165), if any."""
    if f.state == "off":
        if f.skipped is not None and f.skipped == f.latest and f.latest != f.installed:
            return SKIPPED, None
        return None, None  # up to date
    if f.state != "on" or f.installed is None or f.latest is None:
        return None, None  # unavailable or unknown: nothing to decide on
    # In this order: what Home Assistant or the entity itself decides comes before Vördr's rules.
    if f.in_progress:
        return IN_PROGRESS, None
    if f.auto_update:
        return SELF_UPDATING, None
    if not f.installable:
        return NO_INSTALL, None
    if not p.allows(f):
        return NOT_ALLOWED, None
    if major_jump(f.installed, f.latest):
        return MAJOR, None
    if failed is not None and failed == f.latest:
        return LAST_FAILED, None
    # Unknown first sight counts as now: the age starts, it is never assumed (ADR-0159).
    old_enough = ready_from(first_seen, line_seen, p.min_age_for(f.kind), now)
    if old_enough > now:
        return TOO_NEW, window_from(old_enough.astimezone(now.tzinfo), p)
    if not in_window(now, p):
        return OUTSIDE_WINDOW, window_from(now, p)
    return READY, None


def plan(
    facts: Iterable[Facts],
    seen: Mapping[str, Seen],
    p: Policy,
    now: datetime,
    held: Mapping[str, str] | None = None,
    lines: Mapping[str, Mapping[str, datetime]] | None = None,
) -> list[Planned]:
    """Every update entity with a pending or skipped version, by entity id, with its code; Vördr's own entities are
    not part of it. now: an aware time in Home Assistant's time zone, the zone of the window. held: per entity the
    version whose install failed and is not to be tried again (see held()). lines: per entity the first sight of each
    line (see remember_lines); without them, every version waits its own age."""
    out: list[Planned] = []
    for f in sorted(facts, key=lambda f: f.entity_id):
        if f.platform == OWN_PLATFORM:
            continue
        s = seen.get(f.entity_id)
        first_seen = s.at if s is not None and s.version == f.latest else None
        line = line_of(f.latest)
        line_at = (lines or {}).get(f.entity_id, {}).get(line) if line is not None else None
        # The version inherits only from a line seen before it: the first release of a line waits the full age.
        line_seen = line_at if line_at is not None and line_at < (first_seen or now) else None
        code, earliest = _decide(f, first_seen, line_seen, p, now, (held or {}).get(f.entity_id))
        if code is not None:
            out.append(Planned(f.entity_id, f.kind, code, f.installed, f.latest, first_seen, earliest, line_seen))
    return out


def remember(
    seen: Mapping[str, Seen], facts: Iterable[Facts], registered: Collection[str], now: datetime
) -> dict[str, Seen]:
    """The first sight of each offered version (ADR-0159), one per update entity: a version offered for the first time
    is seen now; an installed one, or one replaced by another, is dropped, and so is an entity that is gone from the
    registry. An entity that is unavailable or unknown keeps its first sight: its version did not move on.
    registered: of the entities in seen, those still in the entity registry."""
    out = dict(seen)
    present: set[str] = set()
    for f in facts:
        present.add(f.entity_id)
        if f.platform == OWN_PLATFORM or f.state not in ("on", "off") or f.installed is None or f.latest is None:
            continue
        if f.latest == f.installed:
            out.pop(f.entity_id, None)
        elif (s := out.get(f.entity_id)) is None or s.version != f.latest:
            out[f.entity_id] = Seen(f.latest, now)
    for entity_id in list(out):
        if entity_id not in present and entity_id not in registered:
            del out[entity_id]
    return out


def remember_lines(
    lines: Mapping[str, Mapping[str, datetime]],
    facts: Iterable[Facts],
    registered: Collection[str],
    now: datetime,
) -> dict[str, dict[str, datetime]]:
    """The first sight of each line (line_of) an update entity reports, installed or offered (ADR-0179): a line seen
    for the first time is seen now and kept while the entity reports a version of it - also once it is installed, so
    that a later release of the line inherits its age. A line the entity no longer reports is dropped, and so is an
    entity gone from the registry; an unavailable or unknown entity keeps its lines. registered: of the entities in
    lines, those still in the entity registry."""
    out = {entity_id: dict(seen) for entity_id, seen in lines.items()}
    present: set[str] = set()
    for f in facts:
        present.add(f.entity_id)
        if f.platform == OWN_PLATFORM or f.state not in ("on", "off") or f.installed is None or f.latest is None:
            continue
        known = out.get(f.entity_id, {})
        reported = sorted({line for line in (line_of(f.installed), line_of(f.latest)) if line is not None})
        if reported:
            out[f.entity_id] = {line: known.get(line, now) for line in reported}
        else:
            out.pop(f.entity_id, None)
    for entity_id in list(out):
        if entity_id not in present and entity_id not in registered:
            del out[entity_id]
    return out


# --- installing (ADR-0162 – ADR-0165)


@dataclass(frozen=True)
class Failure:
    """A kept failed install of one update entity (ADR-0165): what it was to install, how it ended, when. released:
    a person asked to try again (the repair's fix); the failure stays until an install goes through, but no longer
    holds the entity."""

    entity_id: str
    from_version: str | None
    to_version: str
    result: str
    at: datetime
    released: bool = False


def held(failures: Mapping[str, Failure]) -> dict[str, str]:
    """Per entity the version not to be tried again: the one whose install failed, unless a person released it. A
    newer version offered later is not held (plan() compares)."""
    return {e: f.to_version for e, f in failures.items() if not f.released}


def clear_failures(
    failures: Mapping[str, Failure], facts: Iterable[Facts], registered: Collection[str]
) -> dict[str, Failure]:
    """The kept failures that still stand. One goes once its entity reports the failed version or has nothing pending
    any more (installed by hand, or a later version went through), or once the entity is gone from the registry. An
    entity that is unavailable or unknown keeps it. registered: of the entities in failures, those still in the
    entity registry."""
    out = dict(failures)
    present: set[str] = set()
    for f in facts:
        present.add(f.entity_id)
        if f.entity_id not in out or f.state not in ("on", "off") or f.installed is None:
            continue
        if f.installed == out[f.entity_id].to_version or f.installed == f.latest:
            del out[f.entity_id]
    for entity_id in list(out):
        if entity_id not in present and entity_id not in registered:
            del out[entity_id]
    return out


def install_order(planned: Iterable[Planned]) -> list[Planned]:
    """The ready updates in the order Vördr installs them, one at a time: every other kind first, by entity id; then
    Supervisor, Core, OS (ADR-0164)."""
    ready = [p for p in planned if p.code == READY]
    rest = sorted((p for p in ready if p.kind not in SYSTEM_KINDS), key=lambda p: p.entity_id)
    system = sorted(
        (p for p in ready if p.kind in SYSTEM_KINDS), key=lambda p: (SYSTEM_KINDS.index(p.kind), p.entity_id)
    )
    return rest + system


def next_install(
    planned: Iterable[Planned], facts: Mapping[str, Facts], system_done: bool, backup_held: bool
) -> Planned | None:
    """The one update to install now, or None (ADR-0164). Nothing while any update entity is being installed, by
    Vördr or anyone. Nothing more in an opening of the window once a system update ran in it: it restarted Home
    Assistant or the machine, and that stays the opening's last step. A system update only when nothing else is
    ready. backup_held: Home Assistant's backup failed lately, so an entity that cannot back itself up waits
    (ADR-0163)."""
    planned = list(planned)
    if system_done or any(p.code == IN_PROGRESS for p in planned):
        return None
    for p in install_order(planned):
        f = facts.get(p.entity_id)
        if f is None or f.platform == OWN_PLATFORM:  # the plan leaves Vördr's own out; never, whatever it says
            continue
        if backup_held and needs_ha_backup(f):
            continue
        return p
    return None


def needs_ha_backup(f: Facts) -> bool:
    """Whether an install of f needs Home Assistant's automatic backup first (ADR-0163): not when the entity backs
    itself up, and not for device firmware and the radio adapter, whose firmware no backup of Home Assistant can roll
    back (ADR-0181)."""
    return not f.backup and f.kind not in FIRMWARE_KINDS


def verify_timeout(kind: str) -> timedelta:
    """How long a run of this kind waits for the entity to report the target version (ADR-0183)."""
    return VERIFY_TIMEOUT_FIRMWARE if kind in FIRMWARE_KINDS else VERIFY_TIMEOUT


def reached(installed: str | None, latest: str | None, from_version: str | None, target: str) -> bool:
    """The entity reports the version an install was for, or another one it calls up to date (a newer version came
    in between and is the one installed)."""
    if installed is None:
        return False
    return installed == target or (installed == latest and installed != from_version)


def backup_fresh(last: datetime | None, now: datetime) -> bool:
    """Home Assistant's last automatic backup is recent enough to go back to (ADR-0163)."""
    return last is not None and timedelta(0) <= now - last <= BACKUP_MAX_AGE


def parse_time(value: Any) -> time | None:
    """ "HH:MM" or "HH:MM:SS" (the time selector) as a time of day to the minute; None if it is none."""
    m = _TIME.fullmatch(value.strip()) if isinstance(value, str) else None
    if m is None:
        return None
    hour, minute, second = int(m.group(1)), int(m.group(2)), int(m.group(3) or 0)
    if hour > 23 or minute > 59 or second > 59:
        return None
    return time(hour, minute)


def _whole_days(value: Any) -> int | None:
    """A minimum age as the number selector sends it (a float), if it is a whole number of days in range."""
    if isinstance(value, bool) or not isinstance(value, int | float):
        return None
    if isinstance(value, float) and not value.is_integer():
        return None
    days = int(value)
    return days if MIN_AGE_MIN <= days <= MIN_AGE_MAX else None


def _names(value: Any) -> list[str] | None:
    """A list of names, sorted and without repeats; [] for none; None for anything that is no list of texts."""
    if value is None:
        return []
    if not isinstance(value, list) or not all(isinstance(v, str) for v in value):
        return None
    return sorted(set(value))


def _read(raw: Mapping[str, Any]) -> tuple[Policy | None, dict[str, Any], dict[str, str]]:
    """The rules, the options as they are to be stored, and what is wrong: field -> error code (then no rules)."""
    errors: dict[str, str] = {}
    kinds = _names(raw.get(CONF_KINDS))
    if kinds is None or not set(kinds) <= set(KINDS):
        errors[CONF_KINDS] = ERR_KIND
    entities = _names(raw.get(CONF_ENTITIES))
    if entities is None or not all(_ENTITY.fullmatch(e) for e in entities):
        errors[CONF_ENTITIES] = ERR_ENTITY
    days = _names(raw.get(CONF_DAYS))
    if days is None or not set(days) <= set(DAYS):
        errors[CONF_DAYS] = ERR_DAY
    elif not days and (kinds or entities):
        errors[CONF_DAYS] = ERR_NEEDS_DAYS  # something allowed, but a window that never opens
    start, end = parse_time(raw.get(CONF_START)), parse_time(raw.get(CONF_END))
    if start is None:
        errors[CONF_START] = ERR_TIME
    if end is None:
        errors[CONF_END] = ERR_TIME
    elif start == end:
        errors[CONF_END] = ERR_WINDOW_EMPTY  # like the agent's window: no window of no length, nor of a whole day
    min_age = _whole_days(raw.get(CONF_MIN_AGE))
    if min_age is None:
        errors[CONF_MIN_AGE] = ERR_MIN_AGE
    # Missing in options saved before installing existed: off, so that they keep meaning what they meant (ADR-0162).
    auto_install = raw.get(CONF_AUTO_INSTALL, False)
    if not isinstance(auto_install, bool):
        errors[CONF_AUTO_INSTALL] = ERR_AUTO_INSTALL
    if errors or kinds is None or entities is None or days is None or start is None or end is None or min_age is None:
        return None, {}, errors
    policy = Policy(
        frozenset(kinds), frozenset(entities), frozenset(days), start, end, timedelta(days=min_age), auto_install
    )
    clean = {
        CONF_KINDS: kinds,
        CONF_ENTITIES: entities,
        CONF_DAYS: [d for d in DAYS if d in days],
        CONF_START: f"{start:%H:%M}",
        CONF_END: f"{end:%H:%M}",
        CONF_MIN_AGE: min_age,
        CONF_AUTO_INSTALL: auto_install,
    }
    return policy, clean, {}


def check_options(raw: Mapping[str, Any]) -> tuple[dict[str, Any], dict[str, str]]:
    """The options of Home Assistant's updates as they are to be stored, and what is wrong with them: field -> error
    code. The options flow refuses with these; stored options that fail count as closed (policy_from_options)."""
    _, clean, errors = _read(raw)
    return clean, errors


def policy_from_options(options: Mapping[str, Any]) -> Policy | None:
    """The rules from the config entry's options: closed without any; None when the stored ones do not pass
    check_options (the caller treats that as closed, too, and says so)."""
    raw = options.get(OPTIONS_KEY)
    if raw is None:
        return CLOSED
    if not isinstance(raw, Mapping):
        return None
    policy, _, _ = _read(raw)
    return policy
