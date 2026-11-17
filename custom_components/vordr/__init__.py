"""Vördr: updates of all hosts in Home Assistant, as a client of the hub (vordr-hub) over /v1/ha.

The hub admits hosts, signs jobs and decides what each host may do; this integration only shows its views and asks
it for commands. Besides, it plans Home Assistant's own updates under Vördr's rules, without the hub (ADR-0158), and
installs them once its options switch that on (ADR-0162). See design/ha-integration.md.
"""

from __future__ import annotations

from homeassistant.config_entries import ConfigEntry
from homeassistant.const import Platform
from homeassistant.core import HomeAssistant
from homeassistant.helpers import device_registry as dr
from homeassistant.helpers.storage import Store

from .agent_install import release_store_key
from .coordinator import HA_DEVICE, HUB_DEVICE, VordrCoordinator
from .ha_updates_installer import async_remove_store as async_remove_runs
from .ha_updates_tracker import async_remove_store

PLATFORMS = [Platform.BINARY_SENSOR, Platform.SENSOR, Platform.UPDATE, Platform.BUTTON, Platform.SWITCH]

type VordrConfigEntry = ConfigEntry[VordrCoordinator]


async def async_setup_entry(hass: HomeAssistant, entry: VordrConfigEntry) -> bool:
    """Set up the entities, then keep the connection to the hub in the background."""
    coordinator = VordrCoordinator(hass, entry)
    entry.runtime_data = coordinator
    await coordinator.texts.async_load()
    # Home Assistant's own updates: from the states and registries of this Home Assistant, not from the hub.
    await coordinator.ha_updates.async_start()
    # A run kept from before a restart is checked first; nothing else installs until its outcome is decided.
    await coordinator.ha_updates_installer.async_start()
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
    """Unload; the connection task ends with the entry. First sights not written yet are written now."""
    unloaded = await hass.config_entries.async_unload_platforms(entry, PLATFORMS)
    await entry.runtime_data.ha_updates.async_flush()
    return unloaded


async def async_remove_entry(hass: HomeAssistant, entry: VordrConfigEntry) -> None:
    """The entry is deleted: so are its first sights, runs and kept failures of Home Assistant's updates, and the
    newest agent release its installs took (ADR-0161)."""
    await async_remove_store(hass, entry.entry_id)
    await async_remove_runs(hass, entry.entry_id)
    await Store[dict[str, str]](hass, 1, release_store_key(entry.entry_id)).async_remove()


async def async_remove_config_entry_device(
    hass: HomeAssistant, entry: VordrConfigEntry, device: dr.DeviceEntry
) -> bool:
    """A host device may be removed by hand only when the hub no longer has the host; the hub device and the device of
    Home Assistant's updates never."""
    hosts = entry.runtime_data.data.hosts
    return not any(d == "vordr" and i.startswith("host:") and i[5:] in hosts for d, i in device.identifiers) and (
        HUB_DEVICE not in device.identifiers and HA_DEVICE not in device.identifiers
    )
