"""Sensors: last report, last run, pending and security updates per host; the hub's counts over all hosts and its
connection."""

from __future__ import annotations

from datetime import datetime
from typing import TYPE_CHECKING, Any

from homeassistant.components.sensor import SensorDeviceClass, SensorEntity, SensorStateClass
from homeassistant.const import EntityCategory
from homeassistant.helpers.device_registry import DeviceInfo
from homeassistant.util import dt as dt_util

from .ai_explain import ATTRIBUTES as AI_ATTRIBUTES
from .client import Connection
from .const import CODES, IMAGE_UPDATE_KEYS, PHASES
from .coordinator import HA_DEVICE, VordrCoordinator
from .entity import HA_UPDATES_UID, HostEntities, VordrHostEntity, VordrHubEntity, track_host_entities
from .ha_updates import CODES as HA_UPDATE_CODES, PHASES as HA_UPDATE_PHASES, READY, RESULTS, Planned
from .ha_updates_installer import HaUpdatesInstaller
from .ha_updates_tracker import HaUpdatesTracker

if TYPE_CHECKING:
    from homeassistant.core import HomeAssistant
    from homeassistant.helpers.entity_platform import AddConfigEntryEntitiesCallback

    from . import VordrConfigEntry

PLATFORM = "sensor"


async def async_setup_entry(
    hass: HomeAssistant, entry: VordrConfigEntry, async_add_entities: AddConfigEntryEntitiesCallback
) -> None:
    coordinator = entry.runtime_data
    async_add_entities(
        [
            HubHostsOnline(coordinator),
            HubHostsWithProblem(coordinator),
            HubHostsWithUpdates(coordinator),
            HubHostsWithSecurityUpdates(coordinator),
            HubHostsNeedingReboot(coordinator),
            HubPendingApprovals(coordinator),
            HubConnection(coordinator),
            HubVulnerabilityData(coordinator),
            HaUpdatesReady(coordinator.ha_updates),
            HaUpdatesLastRun(coordinator.ha_updates_installer),
        ]
    )

    def wanted(node: str, host: dict[str, Any]) -> HostEntities:
        factories = {
            "last_report": lambda: HostLastReport(coordinator, node),
            "last_run": lambda: HostLastRun(coordinator, node),
        }
        backup = host.get("backup")
        if isinstance(backup, dict) and backup.get("state") != "exempt":
            factories["last_backup"] = lambda: HostLastBackup(coordinator, node)
        # pip/npm packages: only for agents that send an inventory; until then (older agent) leave them as they are.
        undecided = {"vulnerable_packages", "malicious_packages", "outdated_packages"}
        if isinstance(host.get("packages"), dict):
            undecided = set()
            factories["vulnerable_packages"] = lambda: HostPackageFindings(coordinator, node, malicious=False)
            factories["malicious_packages"] = lambda: HostPackageFindings(coordinator, node, malicious=True)
            factories["outdated_packages"] = lambda: HostOutdatedPackages(coordinator, node)
        # container images (ADR-0113), for hosts that report them, like the update entity "Container images"
        if isinstance(host.get("containers"), dict):
            factories["outdated_images"] = lambda: HostOutdatedImages(coordinator, node)
        package = {"pending_updates", "security_updates"}
        if host.get("last_report") is None:
            return factories, package | undecided  # no report yet: whether there is a package source is open
        if host.get("system") is not None:
            factories["pending_updates"] = lambda: HostCount(coordinator, node, "pending_updates", "pending")
            factories["security_updates"] = lambda: HostCount(coordinator, node, "security_updates", "security")
        return factories, undecided

    track_host_entities(coordinator, entry, PLATFORM, wanted, async_add_entities)


def _time(value: Any) -> datetime | None:
    return dt_util.parse_datetime(value) if isinstance(value, str) else None


class HostLastReport(VordrHostEntity, SensorEntity):
    _attr_device_class = SensorDeviceClass.TIMESTAMP
    _attr_entity_category = EntityCategory.DIAGNOSTIC  # how Vördr sees the host (ADR-0202)

    def __init__(self, coordinator: VordrCoordinator, node: str) -> None:
        super().__init__(coordinator, node, "last_report", PLATFORM)

    @property
    def native_value(self) -> datetime | None:
        return _time(self.host.get("last_report"))


class HostLastBackup(VordrHostEntity, SensorEntity):
    """The host's last backup on Proxmox Backup Server as the hub's backup check sees it; an install only starts while
    its state is ok. The attribute state is a code (ok, stale, missing, unreachable, unmapped), translated by HA."""

    _attr_device_class = SensorDeviceClass.TIMESTAMP
    _attr_entity_category = EntityCategory.DIAGNOSTIC  # ADR-0202

    def __init__(self, coordinator: VordrCoordinator, node: str) -> None:
        super().__init__(coordinator, node, "last_backup", PLATFORM)

    @property
    def _backup(self) -> dict[str, Any]:
        return self.host.get("backup") or {}

    @property
    def native_value(self) -> datetime | None:
        return _time(self._backup.get("last"))

    @property
    def extra_state_attributes(self) -> dict[str, Any]:
        b = self._backup
        attrs = {"state": b.get("state"), "group": b.get("group"), "max_age_hours": b.get("max_age_hours")}
        if b.get("before_install"):  # backed up right before every install, by this node's agent (ADR-0096)
            attrs["before_install"] = b["before_install"]
        return attrs


class HostLastRun(VordrHostEntity, SensorEntity):
    """Phase while a job runs, its result code once done. With AI explanations switched on (ADR-0166), the result for
    the run it shows: ai_code, and with ok the select fields, the explanation and the checked quote; out of the
    recorder, advice only."""

    _attr_device_class = SensorDeviceClass.ENUM
    _attr_options = [*PHASES, *CODES]
    _unrecorded_attributes = frozenset(AI_ATTRIBUTES)

    def __init__(self, coordinator: VordrCoordinator, node: str) -> None:
        super().__init__(coordinator, node, "last_run", PLATFORM)

    @property
    def native_value(self) -> str | None:
        state = (self.host.get("last_run") or {}).get("state")
        return state if state in self._attr_options else None

    @property
    def extra_state_attributes(self) -> dict[str, Any]:
        run = self.host.get("last_run")
        if not run:
            return {}
        attrs = {k: run.get(k) for k in ("job_id", "verb", "scope", "started")}
        for k in ("by", "outside_window", "finished", "reason", "log_excerpt", "progress", "item"):
            if run.get(k):
                attrs[k] = run[k]
        # What an install run did: packages, duration, the run log on the host, services still on old libraries.
        for k, v in (run.get("summary") or {}).items():
            if v not in (None, "", []):
                attrs[k] = v
        # Commands waiting at the hub until this run has ended (e.g. "install:all" after an agent update).
        if self.host.get("queued"):
            attrs["queued"] = self.host["queued"]
        return attrs | self.coordinator.ai.attributes(self.node, str(run.get("job_id") or ""))


class HostCount(VordrHostEntity, SensorEntity):
    """Pending or security updates; unknown when the last check failed."""

    _attr_state_class = SensorStateClass.MEASUREMENT

    def __init__(self, coordinator: VordrCoordinator, node: str, key: str, field: str) -> None:
        super().__init__(coordinator, node, key, PLATFORM)
        self._field = field

    @property
    def native_value(self) -> int | None:
        value = self.host.get(self._field)
        return value if isinstance(value, int) else None


class HostPackageFindings(VordrHostEntity, SensorEntity):
    """Installed pip/npm packages with an OSV advisory: the malicious ones, or the vulnerable ones (malware not counted
    again). Unknown when the hub has no OSV data for an ecosystem the host has packages of - never "none"."""

    _attr_state_class = SensorStateClass.MEASUREMENT
    # The lists stay in the state but out of the recorder (ADR-0101): up to MaxFindings entries exceed its 16 KiB
    # limit, and then it stores none of the attributes. The history keeps the counts.
    _unrecorded_attributes = frozenset({"findings", "locations", "undecided"})

    def __init__(self, coordinator: VordrCoordinator, node: str, malicious: bool) -> None:
        super().__init__(coordinator, node, "malicious_packages" if malicious else "vulnerable_packages", PLATFORM)
        self._malicious = malicious

    def _packages(self) -> dict[str, Any]:
        packages = self.host.get("packages")
        return packages if isinstance(packages, dict) else {}

    @property
    def native_value(self) -> int | None:
        value = self._packages().get("malicious" if self._malicious else "vulnerable")
        return value if isinstance(value, int) else None

    @property
    def extra_state_attributes(self) -> dict[str, Any]:
        packages = self._packages()
        # reach and held_by (ADR-0129): where the fix is, and what keeps the package below it; known_exploited and the
        # kev_ fields (ADR-0168): a CVE of the finding is in the KEV catalog
        keys = (
            "location",
            "name",
            "version",
            "via",
            "advisories",
            "aliases",
            "severity",
            "fixed_in",
            "reach",
            "held_by",
            "known_exploited",
            "kev_cves",
            "kev_added",
            "kev_ransomware",
        )
        findings = [
            {k: f[k] for k in keys if f.get(k)}
            for f in packages.get("findings") or []
            if bool(f.get("malicious")) == self._malicious
        ]
        attrs: dict[str, Any] = {"findings": findings, "checked_at": packages.get("checked_at")}
        if not self._malicious:  # the overview once per host: where the agent looked and how much it found
            if isinstance(packages.get("waiting"), int):
                attrs["waiting"] = packages["waiting"]  # whose fix nothing on the host can take now: no problem
            if isinstance(packages.get("known_exploited"), int):  # absent while the hub has no KEV catalog
                attrs["known_exploited"] = packages["known_exploited"]
            attrs["packages_total"] = packages.get("total")
            attrs["locations"] = [
                {k: loc[k] for k in ("id", "ecosystem", "path", "packages", "code") if loc.get(k) is not None}
                for loc in packages.get("locations") or []
            ]
            if packages.get("undecided"):
                attrs["undecided"] = packages["undecided"]
        return attrs


class HostOutdatedPackages(VordrHostEntity, SensorEntity):
    """pip/npm packages installed for their own sake with a newer release (design 11.3). The attribute updates says,
    per package, what an update would install (candidate: public for at least min_age_days) and what waits; unknown
    when the agent does not ask the registries."""

    _attr_state_class = SensorStateClass.MEASUREMENT
    _unrecorded_attributes = frozenset({"updates"})  # like the findings above (ADR-0101)

    def __init__(self, coordinator: VordrCoordinator, node: str) -> None:
        super().__init__(coordinator, node, "outdated_packages", PLATFORM)

    def _packages(self) -> dict[str, Any]:
        packages = self.host.get("packages")
        return packages if isinstance(packages, dict) else {}

    @property
    def native_value(self) -> int | None:
        value = self._packages().get("outdated")
        return value if isinstance(value, int) else None

    @property
    def extra_state_attributes(self) -> dict[str, Any]:
        packages = self._packages()
        keys = ("location", "name", "installed", "latest", "candidate", "latest_at", "major", "candidate_flagged")
        return {
            "updates": [{k: u[k] for k in keys if u.get(k)} for u in packages.get("updates") or []],
            "outdated_at": packages.get("outdated_at"),
            "min_age_days": packages.get("min_age_days"),
        }


class HostOutdatedImages(VordrHostEntity, SensorEntity):
    """Images of the host's running containers rebuilt under their tag or with a newer tag (ADR-0113), also those that
    stay manual: a newer tag, a service the host does not let Home Assistant update, or a host whose images are only
    reported. The update entity "Container images" is on only for what its install button would take (ADR-0150); this
    sensor keeps the rest in view. ready: of those, the images with a candidate at least min_age_days old; installable:
    the ones the button would take. Unknown while the images were never compared with their registries."""

    _attr_state_class = SensorStateClass.MEASUREMENT
    _unrecorded_attributes = frozenset({"updates"})  # can be long (ADR-0101)

    def __init__(self, coordinator: VordrCoordinator, node: str) -> None:
        super().__init__(coordinator, node, "outdated_images", PLATFORM)

    def _view(self) -> dict[str, Any]:
        view = self.host.get("containers")
        return view if isinstance(view, dict) else {}

    @property
    def native_value(self) -> int | None:
        value = self._view().get("outdated")
        return value if isinstance(value, int) else None

    @property
    def extra_state_attributes(self) -> dict[str, Any]:
        view = self._view()
        updates = view.get("updates") or []
        attrs: dict[str, Any] = {"updates": [{k: u[k] for k in IMAGE_UPDATE_KEYS if u.get(k)} for u in updates]}
        for key in ("ready", "installable", "min_age_days"):  # a hub before installable does not send it
            if isinstance(view.get(key), int):
                attrs[key] = view[key]
        if view.get("compared_at"):
            attrs["compared_at"] = view["compared_at"]
        return attrs


class HubHostsOnline(VordrHubEntity, SensorEntity):
    _attr_state_class = SensorStateClass.MEASUREMENT

    def __init__(self, coordinator: VordrCoordinator) -> None:
        super().__init__(coordinator, "hosts_online", PLATFORM)

    @property
    def native_value(self) -> int | None:
        value = self.hub.get("hosts_online")
        return value if isinstance(value, int) else None


class HubHostsWithProblem(VordrHubEntity, SensorEntity):
    _attr_state_class = SensorStateClass.MEASUREMENT

    def __init__(self, coordinator: VordrCoordinator) -> None:
        super().__init__(coordinator, "hosts_with_problem", PLATFORM)

    def _problems(self) -> list[dict[str, Any]]:
        return [
            {"node": node, "reason": h["problem"].get("reason")}
            for node, h in sorted(self.coordinator.data.hosts.items())
            if h.get("problem")
        ]

    @property
    def native_value(self) -> int:
        return len(self._problems())

    @property
    def extra_state_attributes(self) -> dict[str, Any]:
        return {"problems": self._problems()}


def _count(value: Any) -> int | None:
    return value if isinstance(value, int) and not isinstance(value, bool) else None


def host_updates(host: dict[str, Any]) -> tuple[dict[str, int], bool]:
    """What a host has to update, by kind, only the kinds above zero: system (pending updates of its package source),
    images (container images with a newer build or tag), packages (pip/npm with a newer release); and whether it cannot
    be said: no report yet, or the system check failed (pending absent while the host has a package source). Images
    and packages count where the host compares them; their count is absent while it does not (switched off, no
    container engine), which is no failure. The same fields as the host's own sensors (ADR-0188)."""
    kinds: dict[str, int] = {}
    unsure = host.get("last_report") is None
    if host.get("system") is not None:
        pending = _count(host.get("pending"))
        if pending is None:
            unsure = True
        elif pending:
            kinds["system"] = pending
    for kind, field in (("images", "containers"), ("packages", "packages")):
        view = host.get(field)
        outdated = _count(view.get("outdated")) if isinstance(view, dict) else None
        if outdated:
            kinds[kind] = outdated
    return kinds, unsure


def host_security(host: dict[str, Any]) -> tuple[int, bool]:
    """Security updates of the host's package source, and whether they are unknown (no report yet, or the check
    failed). A host without a package source has none."""
    if host.get("last_report") is None:
        return 0, True
    if host.get("system") is None:
        return 0, False
    security = _count(host.get("security"))
    return security or 0, security is None


class HubFleetCount(VordrHubEntity, SensorEntity):
    """A count over all hosts of the hub (ADR-0188): how many have something; the attribute hosts names them, the
    attribute unknown the hosts for which it cannot be said (no report yet, or the check failed). An unknown host is
    neither counted nor taken as done."""

    _attr_state_class = SensorStateClass.MEASUREMENT
    _unrecorded_attributes = frozenset({"hosts", "unknown"})  # grow with the fleet (ADR-0101)

    def _rows(self) -> tuple[list[dict[str, Any]], list[str]]:
        raise NotImplementedError

    @property
    def native_value(self) -> int:
        return len(self._rows()[0])

    @property
    def extra_state_attributes(self) -> dict[str, Any]:
        rows, unknown = self._rows()
        return {"hosts": rows, "unknown": unknown}


class HubHostsWithUpdates(HubFleetCount):
    def __init__(self, coordinator: VordrCoordinator) -> None:
        super().__init__(coordinator, "hosts_with_updates", PLATFORM)

    def _rows(self) -> tuple[list[dict[str, Any]], list[str]]:
        rows, unknown = [], []
        for node, host in sorted(self.coordinator.data.hosts.items()):
            kinds, unsure = host_updates(host)
            if kinds:
                rows.append({"node": node, **kinds})
            elif unsure:
                unknown.append(node)
        return rows, unknown


class HubHostsWithSecurityUpdates(HubFleetCount):
    def __init__(self, coordinator: VordrCoordinator) -> None:
        super().__init__(coordinator, "hosts_with_security_updates", PLATFORM)

    def _rows(self) -> tuple[list[dict[str, Any]], list[str]]:
        rows, unknown = [], []
        for node, host in sorted(self.coordinator.data.hosts.items()):
            security, unsure = host_security(host)
            if security:
                rows.append({"node": node, "security": security})
            elif unsure:
                unknown.append(node)
        return rows, unknown


class HubHostsNeedingReboot(HubFleetCount):
    """Hosts whose reboot is required. Unknown: hosts without a report yet. A reporting host where no signal answers
    (e.g. a container) is not unknown here: it has no reboot to wait for."""

    def __init__(self, coordinator: VordrCoordinator) -> None:
        super().__init__(coordinator, "hosts_needing_reboot", PLATFORM)

    def _rows(self) -> tuple[list[dict[str, Any]], list[str]]:
        rows, unknown = [], []
        for node, host in sorted(self.coordinator.data.hosts.items()):
            reboot = host.get("reboot")
            if isinstance(reboot, dict) and reboot.get("required") is True:
                rows.append({"node": node, **({"since": reboot["since"]} if reboot.get("since") else {})})
            elif host.get("last_report") is None:
                unknown.append(node)
        return rows, unknown


class HubPendingApprovals(VordrHubEntity, SensorEntity):
    _attr_state_class = SensorStateClass.MEASUREMENT

    def __init__(self, coordinator: VordrCoordinator) -> None:
        super().__init__(coordinator, "pending_approvals", PLATFORM)

    @property
    def native_value(self) -> int:
        return len(self.hub.get("waiting") or [])

    @property
    def extra_state_attributes(self) -> dict[str, Any]:
        return {"waiting": self.hub.get("waiting") or []}


class HubVulnerabilityData(VordrHubEntity, SensorEntity):
    """When the hub last fetched its OSV data (the oldest of its ecosystems); unknown while one has none, or when the
    hub has no osv section. The state of each ecosystem is in the attributes, and with a kev section the state of the
    KEV catalog (ADR-0168)."""

    _attr_device_class = SensorDeviceClass.TIMESTAMP
    _attr_entity_category = EntityCategory.DIAGNOSTIC

    def __init__(self, coordinator: VordrCoordinator) -> None:
        super().__init__(coordinator, "vulnerability_data", PLATFORM)

    def _osv(self) -> list[dict[str, Any]]:
        osv = self.hub.get("osv")
        return osv if isinstance(osv, list) else []

    @property
    def native_value(self) -> datetime | None:
        times = [_time(e.get("checked")) for e in self._osv()]
        if not times or None in times:
            return None
        return min(t for t in times if t is not None)

    @property
    def extra_state_attributes(self) -> dict[str, Any]:
        attrs: dict[str, Any] = {
            "ecosystems": {
                e.get("ecosystem"): {
                    k: e[k] for k in ("state", "code", "modified", "advisories", "malicious") if e.get(k)
                }
                for e in self._osv()
            }
        }
        kev = self.hub.get("kev")
        if isinstance(kev, dict):  # absent without a kev section at the hub
            keys = ("state", "code", "checked", "released", "catalog_version", "entries")
            attrs["kev"] = {k: kev[k] for k in keys if kev.get(k)}
        return attrs


def _local(at: datetime | None) -> str | None:
    """A time for the attributes: ISO 8601 in Home Assistant's time zone, to the minute."""
    return dt_util.as_local(at).isoformat(timespec="minutes") if at is not None else None


class HaUpdatesReady(SensorEntity):
    """Home Assistant's own update entities that Vördr's rules would install now (ADR-0158); Vördr installs them only
    once the options switch that on (ADR-0162), and the sensor "Last run" shows what it did. State: how many are ready.
    Attributes: per reason code how many (their names are the codes' texts), the minimum age and the one of device
    firmware and radio (ADR-0180), and the list `updates` - per update entity its kind, code, versions, first sight
    (ADR-0159), that of its line where it inherits from it (ADR-0179), and, where it waits for a time, the earliest
    time it would be ready. The list stays out of the recorder (ADR-0101). On the device "Vördr Home Assistant", not the
    hub's: it does not need the hub and stays available without a connection."""

    _attr_has_entity_name = True
    _attr_should_poll = False
    _attr_translation_key = "ha_updates_ready"
    _attr_state_class = SensorStateClass.MEASUREMENT
    _unrecorded_attributes = frozenset({"updates"})

    def __init__(self, tracker: HaUpdatesTracker) -> None:
        self._tracker = tracker
        self._attr_unique_id = f"{HA_UPDATES_UID}ready"
        self.entity_id = "sensor.vordr_ha_updates_ready"
        self._attr_device_info = ha_device()

    async def async_added_to_hass(self) -> None:
        self.async_on_remove(self._tracker.async_add_listener(self.async_write_ha_state))

    @property
    def native_value(self) -> int:
        return sum(1 for p in self._tracker.planned if p.code == READY)

    @staticmethod
    def _item(p: Planned) -> dict[str, Any]:
        item = {
            "entity_id": p.entity_id,
            "kind": p.kind,
            "code": p.code,
            "installed_version": p.installed,
            "latest_version": p.latest,
            "first_seen": _local(p.first_seen),
            "line_first_seen": _local(p.line_seen),
            "earliest": _local(p.earliest),
        }
        return {k: v for k, v in item.items() if v is not None}

    @property
    def extra_state_attributes(self) -> dict[str, Any]:
        counts = dict.fromkeys(HA_UPDATE_CODES, 0)
        for p in self._tracker.planned:
            counts[p.code] += 1
        return {
            **counts,
            "min_age_days": self._tracker.policy.min_age.days,
            # the same for both kinds of FIRMWARE_KINDS: device firmware and the radio adapter
            "min_age_days_firmware": self._tracker.policy.min_age_for("firmware").days,
            "updates": [self._item(p) for p in self._tracker.planned],
        }


def ha_device() -> DeviceInfo:
    """The device "Vördr Home Assistant" of Home Assistant's own updates (ADR-0158)."""
    return DeviceInfo(
        identifiers={HA_DEVICE},
        translation_key="home_assistant",  # name in HA's system language, like the hub device
        manufacturer="Vördr",
    )


class HaUpdatesLastRun(SensorEntity):
    """The last install of one of Home Assistant's own updates by Vördr (ADR-0164): its phase while it runs, its result
    code once it ended; kept across restarts. Attributes: the entity, its kind, the versions, how it was backed up, when
    it started and ended, whether Home Assistant restarted during it, and what the installer does now (`installer`)
    with the ready updates in the order it would take them (`queue`, out of the recorder)."""

    _attr_has_entity_name = True
    _attr_should_poll = False
    _attr_translation_key = "ha_updates_last_run"
    _attr_device_class = SensorDeviceClass.ENUM
    _attr_options = [*HA_UPDATE_PHASES, *RESULTS]
    _unrecorded_attributes = frozenset({"queue"})

    def __init__(self, installer: HaUpdatesInstaller) -> None:
        self._installer = installer
        self._attr_unique_id = f"{HA_UPDATES_UID}last_run"
        self.entity_id = "sensor.vordr_ha_updates_last_run"
        self._attr_device_info = ha_device()

    async def async_added_to_hass(self) -> None:
        self.async_on_remove(self._installer.async_add_listener(self.async_write_ha_state))

    @property
    def native_value(self) -> str | None:
        run, last = self._installer.run, self._installer.last
        if run is not None:
            return run.phase
        return last.get("result") if last is not None else None

    @property
    def extra_state_attributes(self) -> dict[str, Any]:
        run, last = self._installer.run, self._installer.last
        shown: dict[str, Any] = {}
        if run is not None:
            shown = {
                "entity_id": run.entity_id,
                "kind": run.kind,
                "from_version": run.from_version,
                "to_version": run.to_version,
                "backup": run.backup,
                "started": _local(run.started),
                "restarted": run.restarted,
            }
        elif last is not None:
            shown = {k: last.get(k) for k in ("entity_id", "kind", "from_version", "to_version", "installed_version")}
            shown |= {k: last.get(k) for k in ("reason", "backup", "restarted")}
            shown |= {k: _local(_time(last.get(k))) for k in ("started", "finished")}
        attrs = {k: v for k, v in shown.items() if v not in (None, False)}
        return {**attrs, "installer": self._installer.status, "queue": self._installer.queue()}


class HubConnection(VordrHubEntity, SensorEntity):
    """The client's connection state as a fact: connecting, unreachable, pending approval, approved, rejected."""

    _attr_device_class = SensorDeviceClass.ENUM
    _attr_options = [c.value for c in Connection]
    _attr_entity_category = EntityCategory.DIAGNOSTIC

    def __init__(self, coordinator: VordrCoordinator) -> None:
        super().__init__(coordinator, "connection", PLATFORM)

    @property
    def available(self) -> bool:
        return True

    @property
    def native_value(self) -> str:
        return self.coordinator.data.connection.value

    @property
    def extra_state_attributes(self) -> dict[str, Any]:
        data = self.coordinator.data
        return {
            "reason": data.reason,
            "fingerprint": self.coordinator.client.fingerprint,
            "hub_version": (data.hub or {}).get("version"),
            "protocol": (data.hub or {}).get("protocol"),
        }
