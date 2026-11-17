"""Switch: pause the automatic install of Home Assistant's own updates (ADR-0162)."""

from __future__ import annotations

from typing import TYPE_CHECKING, Any

from homeassistant.components.switch import SwitchEntity

from .entity import HA_UPDATES_UID
from .ha_updates_installer import HaUpdatesInstaller
from .sensor import ha_device

if TYPE_CHECKING:
    from homeassistant.core import HomeAssistant
    from homeassistant.helpers.entity_platform import AddConfigEntryEntitiesCallback

    from . import VordrConfigEntry


async def async_setup_entry(
    hass: HomeAssistant, entry: VordrConfigEntry, async_add_entities: AddConfigEntryEntitiesCallback
) -> None:
    async_add_entities([HaUpdatesPaused(entry.runtime_data.ha_updates_installer)])


class HaUpdatesPaused(SwitchEntity):
    """While on, Vördr starts no install of Home Assistant's own updates, and a run that is preparing (making Home
    Assistant's backup) ends as skipped_paused instead of installing. An install already called runs to its end. Kept
    across restarts. Off by default: what installs is decided by the options, where installing is off by default."""

    _attr_has_entity_name = True
    _attr_should_poll = False
    _attr_translation_key = "ha_updates_paused"

    def __init__(self, installer: HaUpdatesInstaller) -> None:
        self._installer = installer
        self._attr_unique_id = f"{HA_UPDATES_UID}paused"
        self.entity_id = "switch.vordr_ha_updates_paused"
        self._attr_device_info = ha_device()

    async def async_added_to_hass(self) -> None:
        self.async_on_remove(self._installer.async_add_listener(self.async_write_ha_state))

    @property
    def is_on(self) -> bool:
        return self._installer.paused

    async def async_turn_on(self, **kwargs: Any) -> None:
        await self._installer.async_set_paused(True)

    async def async_turn_off(self, **kwargs: Any) -> None:
        await self._installer.async_set_paused(False)
