"""Repairs with a fix: the reboot a host needs (ADR-0132), and installing again after a kept failed install
(ADR-0148)."""

from __future__ import annotations

from typing import TYPE_CHECKING, Any

from homeassistant.components.repairs import RepairsFlow
from homeassistant.config_entries import ConfigEntryState
import voluptuous as vol

from .coordinator import (
    BACKUP_REFUSALS,
    VordrCoordinator,
    failed_install,
    failed_install_names,
    install_again_offered,
    reboot_required,
    refusal,
)
from .texts import can_reboot

if TYPE_CHECKING:
    from homeassistant.core import HomeAssistant
    from homeassistant.data_entry_flow import FlowResult


class _HostFlow(RepairsFlow):
    """Confirm, then ask the hub for the host's command - the same as its button. Checked again at confirmation, since
    the repair may be older than the host's state; each case it refuses has its own reason. A refusal by the hub says
    why, as the buttons do, for the codes the flow names in refusals; any other shows its code."""

    command = ""
    # The hub's codes for which this flow's dialog has the text the buttons have (coordinator.refusal), under the same
    # key; only those the hub can send for its command.
    refusals: frozenset[str] = frozenset()

    def __init__(self, entry_id: str, node: str) -> None:
        self._entry_id = entry_id
        self._node = node

    def _still(self, host: dict[str, Any]) -> str:
        """Empty while the fix still applies to host, else the abort reason."""
        raise NotImplementedError

    def _asked(self, coordinator: VordrCoordinator) -> None:
        """The hub took the command."""

    def _shown(self, host: dict[str, Any] | None) -> dict[str, str]:
        """What the confirm step names besides the host, from the host's state as the hub last sent it (None: not
        known right now)."""
        return {}

    def _host(self) -> dict[str, Any] | None:
        entry = self.hass.config_entries.async_get_entry(self._entry_id)
        if entry is None or entry.state is not ConfigEntryState.LOADED:
            return None
        coordinator: VordrCoordinator = entry.runtime_data
        return coordinator.data.hosts.get(self._node)

    async def async_step_init(self, user_input: dict[str, Any] | None = None) -> FlowResult:
        return await self.async_step_confirm()

    async def async_step_confirm(self, user_input: dict[str, Any] | None = None) -> FlowResult:
        names = {"node": self._node}
        if user_input is None:
            shown = {**names, **self._shown(self._host())}
            return self.async_show_form(step_id="confirm", data_schema=vol.Schema({}), description_placeholders=shown)
        entry = self.hass.config_entries.async_get_entry(self._entry_id)
        if entry is None or entry.state is not ConfigEntryState.LOADED:
            return self.async_abort(reason="hub_not_connected", description_placeholders=names)
        coordinator: VordrCoordinator = entry.runtime_data
        if not coordinator.connected:
            return self.async_abort(reason="hub_not_connected", description_placeholders=names)
        host = coordinator.data.hosts.get(self._node)
        if host is None:
            return self.async_abort(reason="host_gone", description_placeholders=names)
        if reason := self._still(host):
            return self.async_abort(reason=reason, description_placeholders=names)
        code = await coordinator.command(self._node, self.command)
        if code != "ok":
            if code not in self.refusals:
                return self.async_abort(reason="command_refused", description_placeholders={**names, "code": code})
            # Filled from the host's state as the hub last sent it, read after its answer, as the buttons do.
            key, shown = refusal(self._node, coordinator.data.hosts.get(self._node), code)
            return self.async_abort(reason=key, description_placeholders=shown)
        self._asked(coordinator)
        return self.async_create_entry(data={})


class RebootFlow(_HostFlow):
    """The reboot a host needs (ADR-0132)."""

    command = "reboot"
    # The window, the minimum interval and the backup check hold only installs; a reboot needs the admin role.
    refusals = frozenset({"role_forbids"})

    def _still(self, host: dict[str, Any]) -> str:
        if not reboot_required(host):
            return "no_longer_needed"
        if not can_reboot(host):
            return "reboot_not_offered"
        return ""

    def _asked(self, coordinator: VordrCoordinator) -> None:
        coordinator.reboot_asked(self._node)


class InstallAgainFlow(_HostFlow):
    """Install again after a kept failed install (ADR-0148): a run that goes through clears it at the hub, and with it
    the repair; a run that fails again keeps it."""

    command = "install"
    # Sent without a reason: the hub gives it one outside the window where the agent takes it (ADR-0109), else
    # outside_window. outside_window_unsupported comes only with a reason.
    refusals = frozenset({"role_forbids", "outside_window", "rate_limited", *BACKUP_REFUSALS})

    def _shown(self, host: dict[str, Any] | None) -> dict[str, str]:
        return failed_install_names(host)

    def _still(self, host: dict[str, Any]) -> str:
        if failed_install(host) is None:
            return "no_longer_needed"
        if not install_again_offered(host):
            return "install_not_offered"
        return ""


async def async_create_fix_flow(
    hass: HomeAssistant, issue_id: str, data: dict[str, str | int | float | None] | None
) -> RepairsFlow:
    """Home Assistant asks for the flow of a fixable repair: install_again_<node> or reboot_<node>."""
    data = data or {}
    flow = InstallAgainFlow if data.get("kind") == "install_again" else RebootFlow
    return flow(str(data.get("entry_id") or ""), str(data.get("node") or ""))
