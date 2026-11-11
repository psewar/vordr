"""Binary sensors: host connected, update problem, reboot needed; the hub connection."""

from __future__ import annotations

from typing import TYPE_CHECKING, Any

from homeassistant.components.binary_sensor import BinarySensorDeviceClass, BinarySensorEntity

from .coordinator import VordrCoordinator
from .entity import HostEntities, VordrHostEntity, VordrHubEntity, track_host_entities
from .texts import can_reboot, problem_key

if TYPE_CHECKING:
    from homeassistant.core import HomeAssistant
    from homeassistant.helpers.entity_platform import AddConfigEntryEntitiesCallback

    from . import VordrConfigEntry

PLATFORM = "binary_sensor"


async def async_setup_entry(
    hass: HomeAssistant, entry: VordrConfigEntry, async_add_entities: AddConfigEntryEntitiesCallback
) -> None:
    coordinator = entry.runtime_data
    async_add_entities([HubOnline(coordinator)])

    def wanted(node: str, host: dict[str, Any]) -> HostEntities:
        return {
            "online": lambda: HostOnline(coordinator, node),
            "problem": lambda: HostProblem(coordinator, node),
            "reboot_required": lambda: HostReboot(coordinator, node),
        }, set()

    track_host_entities(coordinator, entry, PLATFORM, wanted, async_add_entities)


class HostOnline(VordrHostEntity, BinarySensorEntity):
    """Off while the host is away; `power` says when it announced it (sleeping, shutdown) - then it is no problem."""

    _attr_device_class = BinarySensorDeviceClass.CONNECTIVITY

    def __init__(self, coordinator: VordrCoordinator, node: str) -> None:
        super().__init__(coordinator, node, "online", PLATFORM)

    @property
    def is_on(self) -> bool:
        return bool(self.host.get("online"))

    @property
    def extra_state_attributes(self) -> dict[str, Any]:
        power = self.host.get("power")
        return {"power": power} if power else {}


class HostProblem(VordrHostEntity, BinarySensorEntity):
    """The attributes hold codes; HA shows reason, next_step and check_failed_code translated (state_attributes)."""

    _attr_device_class = BinarySensorDeviceClass.PROBLEM
    _unrecorded_attributes = frozenset({"failed_install"})  # carries the run's detail (ADR-0101)

    def __init__(self, coordinator: VordrCoordinator, node: str) -> None:
        super().__init__(coordinator, node, "problem", PLATFORM)

    @property
    def is_on(self) -> bool:
        return self.host.get("problem") is not None

    @property
    def extra_state_attributes(self) -> dict[str, Any]:
        problem = self.host.get("problem")
        if not problem:
            return {}
        attrs = {
            "reason": problem.get("reason"),
            "reasons": problem.get("reasons") or [],
            "next_step": problem_key(problem, can_reboot(self.host)),
        }
        if problem.get("check_failed_code"):
            attrs["check_failed_code"] = problem["check_failed_code"]
        # ADR-0124: the kept failed install of the system packages, with its detail; "Last run" may show a later job.
        failed = self.host.get("failed_install")
        if failed:
            keys = ("job_id", "scope", "state", "by", "started", "finished", "log_excerpt")
            attrs["failed_install"] = {k: failed[k] for k in keys if failed.get(k)}
            if log_file := (failed.get("summary") or {}).get("log_file"):
                attrs["failed_install"]["log_file"] = log_file
        return attrs


class HostReboot(VordrHostEntity, BinarySensorEntity):
    """Unknown (None) when no signal answered on the host, e.g. in a container - never "no"."""

    def __init__(self, coordinator: VordrCoordinator, node: str) -> None:
        super().__init__(coordinator, node, "reboot_required", PLATFORM)

    @property
    def is_on(self) -> bool | None:
        required = (self.host.get("reboot") or {}).get("required")
        return None if required is None else bool(required)

    @property
    def extra_state_attributes(self) -> dict[str, Any]:
        reboot = self.host.get("reboot") or {}
        if reboot.get("required") is None:
            return {}
        attrs = {
            "reason": reboot.get("reason", ""),
            "packages": reboot.get("packages") or [],
            "checked": reboot.get("checked") or [],
        }
        for k in ("since", "kernel_running", "kernel_newest"):
            if reboot.get(k):
                attrs[k] = reboot[k]
        return attrs


class HubOnline(VordrHubEntity, BinarySensorEntity):
    """The connection to the hub itself: approved, connected, with a complete view."""

    _attr_device_class = BinarySensorDeviceClass.CONNECTIVITY

    def __init__(self, coordinator: VordrCoordinator) -> None:
        super().__init__(coordinator, "online", PLATFORM)

    @property
    def available(self) -> bool:
        return True

    @property
    def is_on(self) -> bool:
        return self.coordinator.connected

    @property
    def extra_state_attributes(self) -> dict[str, Any]:
        data = self.coordinator.data
        return {
            "connection": data.connection.value,
            "reason": data.reason,
            "fingerprint": self.coordinator.client.fingerprint,
        }
