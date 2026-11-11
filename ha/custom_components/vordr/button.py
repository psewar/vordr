"""Buttons: check now, install security updates, reboot, install an app - as the host offers. The agent itself is an
update entity (update.py)."""

from __future__ import annotations

from typing import TYPE_CHECKING, Any

from homeassistant.components.button import ButtonDeviceClass, ButtonEntity
from homeassistant.exceptions import HomeAssistantError
from homeassistant.helpers import entity_platform
import voluptuous as vol

from .const import DOMAIN
from .coordinator import VordrCoordinator, valid_reason
from .entity import HostEntities, VordrHostEntity, track_host_entities

if TYPE_CHECKING:
    from homeassistant.core import HomeAssistant
    from homeassistant.helpers.entity_platform import AddConfigEntryEntitiesCallback

    from . import VordrConfigEntry

PLATFORM = "button"
SERVICE_PRESS_OUTSIDE_WINDOW = "press_outside_window"

# key -> (capability, command)
SIMPLE = {
    "check": ("check", "check"),
    "install_security": ("install_security", "install_security"),
    "reboot": ("reboot", "reboot"),
}


async def async_setup_entry(
    hass: HomeAssistant, entry: VordrConfigEntry, async_add_entities: AddConfigEntryEntitiesCallback
) -> None:
    coordinator = entry.runtime_data

    def wanted(node: str, host: dict[str, Any]) -> HostEntities:
        caps = host.get("capabilities") or {}
        factories = {}
        for key, (cap, command) in SIMPLE.items():
            if caps.get(cap):
                factories[key] = lambda k=key, c=command: HostButton(coordinator, node, k, c)
        for app in caps.get("apps") or []:
            app_id, name = str(app.get("id", "")), str(app.get("name", ""))
            if app_id:
                factories[f"install_app_{app_id}"] = lambda a=app_id, n=name: HostButton(
                    coordinator, node, f"install_app_{a}", f"install_app:{a}", n
                )
        return factories, set()

    track_host_entities(coordinator, entry, PLATFORM, wanted, async_add_entities)
    entity_platform.async_get_current_platform().async_register_entity_service(
        SERVICE_PRESS_OUTSIDE_WINDOW, {vol.Required("reason"): valid_reason}, "async_press_outside_window"
    )


class HostButton(VordrHostEntity, ButtonEntity):
    """Asks the hub for a command; the hub checks it against the host's verbs and policy, the agent again."""

    def __init__(self, coordinator: VordrCoordinator, node: str, key: str, command: str, app: str = "") -> None:
        super().__init__(coordinator, node, key, PLATFORM)
        self._command = command
        if app:
            self._attr_translation_key = "install_app"
            self._attr_translation_placeholders = {"app": app}
        if key == "reboot":
            self._attr_device_class = ButtonDeviceClass.RESTART

    async def async_press(self) -> None:
        code = await self.coordinator.command(self.node, self._command)
        if code != "ok":
            raise self.coordinator.refused(self.node, code)

    async def async_press_outside_window(self, reason: str) -> None:
        """Service vordr.press_outside_window: the button, also outside the host's window (ADR-0087). A check has no
        window."""
        if self._command == "check":
            raise HomeAssistantError(
                translation_domain=DOMAIN,
                translation_key="outside_window_no_window",
                translation_placeholders={"node": self.node},
            )
        await self.coordinator.command_outside_window(self.node, self._command, reason)
