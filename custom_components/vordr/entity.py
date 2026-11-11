"""Base entities and the bookkeeping that adds and removes host entities as the hub's views change."""

from __future__ import annotations

from collections.abc import Callable
from typing import TYPE_CHECKING, Any

from homeassistant.core import callback
from homeassistant.helpers import entity_registry as er
from homeassistant.helpers.device_registry import DeviceInfo
from homeassistant.helpers.entity import Entity
from homeassistant.helpers.update_coordinator import CoordinatorEntity

from .coordinator import HUB_DEVICE, VordrCoordinator, host_device
from .protocol import entity_part

if TYPE_CHECKING:
    from homeassistant.config_entries import ConfigEntry
    from homeassistant.helpers.entity_platform import AddConfigEntryEntitiesCallback

# For one host: the entities wanted now (object key -> factory), and the keys whose presence is not decided yet
# (no report so far): those are neither added nor removed.
HostEntities = tuple[dict[str, Callable[[], Entity]], set[str]]


class VordrHostEntity(CoordinatorEntity[VordrCoordinator]):
    """An entity of one host; ids as the MQTT side had them: <platform>.vordr_<node>_<key>."""

    _attr_has_entity_name = True

    def __init__(self, coordinator: VordrCoordinator, node: str, key: str, platform: str) -> None:
        super().__init__(coordinator)
        self.node = node
        self.key = key
        self._attr_unique_id = f"{node}:{key}"
        self._attr_translation_key = key
        self.entity_id = f"{platform}.vordr_{entity_part(node)}_{entity_part(key)}"
        device = (coordinator.data.hosts.get(node) or {}).get("device") or {}
        self._attr_device_info = DeviceInfo(
            identifiers={host_device(node)},
            name=device.get("name") or f"Vördr {node}",
            manufacturer="Vördr",
            model=device.get("model"),
            sw_version=device.get("agent_version"),
            via_device_id=coordinator.hub_device_id,
        )

    @property
    def host(self) -> dict[str, Any]:
        """The host's current view; empty while unknown."""
        return self.coordinator.data.hosts.get(self.node) or {}

    @property
    def available(self) -> bool:
        return self.coordinator.connected and self.node in self.coordinator.data.hosts


class VordrHubEntity(CoordinatorEntity[VordrCoordinator]):
    """An entity of the hub device."""

    _attr_has_entity_name = True

    def __init__(self, coordinator: VordrCoordinator, key: str, platform: str) -> None:
        super().__init__(coordinator)
        self.key = key
        self._attr_unique_id = f"hub:{key}"
        self._attr_translation_key = f"hub_{key}"
        self.entity_id = f"{platform}.vordr_hub_{key}"
        self._attr_device_info = DeviceInfo(
            identifiers={HUB_DEVICE},
            translation_key="hub",  # name in HA's system language, as in async_setup_entry
            manufacturer="Vördr",
            model="vordr-hub",
            sw_version=(coordinator.data.hub or {}).get("version"),
        )

    @property
    def hub(self) -> dict[str, Any]:
        return self.coordinator.data.hub or {}

    @property
    def available(self) -> bool:
        return self.coordinator.connected


def track_host_entities(
    coordinator: VordrCoordinator,
    entry: ConfigEntry,
    platform: str,
    wanted: Callable[[str, dict[str, Any]], HostEntities],
    async_add_entities: AddConfigEntryEntitiesCallback,
) -> None:
    """Add a platform's host entities when they become wanted and remove them once they are certainly not.

    Decided only on a complete view (after a snapshot). Removal goes by the entity registry, so entities left
    from before a restart are cleaned up as well; whole hosts go with their device (see the coordinator).
    """
    added: set[str] = set()  # unique ids added in this run

    @callback
    def sync() -> None:
        if not coordinator.data.synced:
            return
        hosts = coordinator.data.hosts
        new: list[Entity] = []
        keep: dict[str, set[str]] = {}
        for node, host in hosts.items():
            factories, undecided = wanted(node, host)
            keep[node] = set(factories) | undecided
            for key, make in factories.items():
                uid = f"{node}:{key}"
                if uid not in added:
                    added.add(uid)
                    new.append(make())
        reg = er.async_get(coordinator.hass)
        for ent in er.async_entries_for_config_entry(reg, entry.entry_id):
            if ent.domain != platform or ent.unique_id.startswith("hub:"):
                continue
            node, _, key = ent.unique_id.partition(":")
            if node in keep and key not in keep[node]:
                added.discard(ent.unique_id)
                reg.async_remove(ent.entity_id)
        if new:
            async_add_entities(new)

    sync()
    entry.async_on_unload(coordinator.async_add_listener(sync))
