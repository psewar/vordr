"""Repairs with a fix: the reboot a host needs (ADR-0132)."""

from __future__ import annotations

from typing import TYPE_CHECKING, Any

from homeassistant.components.repairs import RepairsFlow
from homeassistant.config_entries import ConfigEntryState
import voluptuous as vol

from .coordinator import VordrCoordinator, reboot_required
from .texts import can_reboot

if TYPE_CHECKING:
    from homeassistant.core import HomeAssistant
    from homeassistant.data_entry_flow import FlowResult


class RebootFlow(RepairsFlow):
    """Confirm, then ask the hub for the reboot - the same command as the host's reboot button. Checked again at
    confirmation, since the repair may be older than the host's state; each case it refuses has its own reason."""

    def __init__(self, entry_id: str, node: str) -> None:
        self._entry_id = entry_id
        self._node = node

    async def async_step_init(self, user_input: dict[str, Any] | None = None) -> FlowResult:
        return await self.async_step_confirm()

    async def async_step_confirm(self, user_input: dict[str, Any] | None = None) -> FlowResult:
        names = {"node": self._node}
        if user_input is None:
            return self.async_show_form(step_id="confirm", data_schema=vol.Schema({}), description_placeholders=names)
        entry = self.hass.config_entries.async_get_entry(self._entry_id)
        if entry is None or entry.state is not ConfigEntryState.LOADED:
            return self.async_abort(reason="hub_not_connected", description_placeholders=names)
        coordinator: VordrCoordinator = entry.runtime_data
        if not coordinator.connected:
            return self.async_abort(reason="hub_not_connected", description_placeholders=names)
        host = coordinator.data.hosts.get(self._node)
        if host is None:
            return self.async_abort(reason="host_gone", description_placeholders=names)
        if not reboot_required(host):
            return self.async_abort(reason="no_longer_needed", description_placeholders=names)
        if not can_reboot(host):
            return self.async_abort(reason="reboot_not_offered", description_placeholders=names)
        code = await coordinator.command(self._node, "reboot")
        if code != "ok":
            return self.async_abort(reason="command_refused", description_placeholders={**names, "code": code})
        coordinator.reboot_asked(self._node)
        return self.async_create_entry(data={})


async def async_create_fix_flow(
    hass: HomeAssistant, issue_id: str, data: dict[str, str | int | float | None] | None
) -> RepairsFlow:
    """Home Assistant asks for the flow of a fixable repair; only reboot_<node> is fixable."""
    data = data or {}
    return RebootFlow(str(data.get("entry_id") or ""), str(data.get("node") or ""))
