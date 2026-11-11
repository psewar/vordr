"""Sensors: last report, last run, pending and security updates per host; the hub's counts and connection."""

from __future__ import annotations

from datetime import datetime
from typing import TYPE_CHECKING, Any

from homeassistant.components.sensor import SensorDeviceClass, SensorEntity, SensorStateClass
from homeassistant.const import EntityCategory
from homeassistant.util import dt as dt_util

from .client import Connection
from .const import CODES, PHASES
from .coordinator import VordrCoordinator
from .entity import HostEntities, VordrHostEntity, VordrHubEntity, track_host_entities

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
            HubPendingApprovals(coordinator),
            HubConnection(coordinator),
            HubVulnerabilityData(coordinator),
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

    def __init__(self, coordinator: VordrCoordinator, node: str) -> None:
        super().__init__(coordinator, node, "last_report", PLATFORM)

    @property
    def native_value(self) -> datetime | None:
        return _time(self.host.get("last_report"))


class HostLastBackup(VordrHostEntity, SensorEntity):
    """The host's last backup on Proxmox Backup Server as the hub's backup check sees it; an install only starts while
    its state is ok. The attribute state is a code (ok, stale, missing, unreachable, unmapped), translated by HA."""

    _attr_device_class = SensorDeviceClass.TIMESTAMP

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
    """Phase while a job runs, its result code once done."""

    _attr_device_class = SensorDeviceClass.ENUM
    _attr_options = [*PHASES, *CODES]

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
        return attrs


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
        # reach and held_by (ADR-0129): where the fix is, and what keeps the package below it
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
    hub has no osv section. The state of each ecosystem is in the attributes."""

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
        return {
            "ecosystems": {
                e.get("ecosystem"): {
                    k: e[k] for k in ("state", "code", "modified", "advisories", "malicious") if e.get(k)
                }
                for e in self._osv()
            }
        }


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
