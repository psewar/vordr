"""Vördr: updates of all hosts in Home Assistant, as a client of the hub (vordr-hub) over /v1/ha.

The hub admits hosts, signs jobs and decides what each host may do; this integration only shows its views and asks
it for commands. See design/ha-integration.md.
"""

from __future__ import annotations

from homeassistant.config_entries import ConfigEntry
from homeassistant.const import Platform
from homeassistant.core import HomeAssistant
from homeassistant.helpers import device_registry as dr

from .coordinator import HUB_DEVICE, VordrCoordinator

PLATFORMS = [Platform.BINARY_SENSOR, Platform.SENSOR, Platform.UPDATE, Platform.BUTTON]

type VordrConfigEntry = ConfigEntry[VordrCoordinator]


async def async_setup_entry(hass: HomeAssistant, entry: VordrConfigEntry) -> bool:
    """Set up the entities, then keep the connection to the hub in the background."""
    coordinator = VordrCoordinator(hass, entry)
    entry.runtime_data = coordinator
    await coordinator.texts.async_load()
    # The hub device first: host devices name it as their parent by its registry id. Its name follows HA's system
    # language, like entity names.
    hub = dr.async_get(hass).async_get_or_create(
        config_entry_id=entry.entry_id,
        identifiers={HUB_DEVICE},
        translation_key="hub",
        manufacturer="Vördr",
        model="vordr-hub",
    )
    coordinator.hub_device_id = hub.id
    await hass.config_entries.async_forward_entry_setups(entry, PLATFORMS)
    entry.async_create_background_task(hass, coordinator.client.run(), f"vordr connection {entry.title}")
    return True


async def async_unload_entry(hass: HomeAssistant, entry: VordrConfigEntry) -> bool:
    """Unload; the connection task ends with the entry."""
    return await hass.config_entries.async_unload_platforms(entry, PLATFORMS)


async def async_remove_config_entry_device(
    hass: HomeAssistant, entry: VordrConfigEntry, device: dr.DeviceEntry
) -> bool:
    """A host device may be removed by hand only when the hub no longer has the host."""
    hosts = entry.runtime_data.data.hosts
    return not any(d == "vordr" and i.startswith("host:") and i[5:] in hosts for d, i in device.identifiers) and (
        ("vordr", "hub") not in device.identifiers
    )
