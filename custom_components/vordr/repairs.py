"""Repairs with a fix: the reboot a host needs (ADR-0132), installing again after a kept failed install (ADR-0148),
installing updates pending too long that a button takes (ADR-0203), holding a release a host offers only in another
branch (ADR-0208), installing one of Home Assistant's own updates
again, at once, after its install failed (ADR-0165, ADR-0182), and approving a waiting host where the hub allows it
(ADR-0192)."""

from __future__ import annotations

from typing import TYPE_CHECKING, Any, Final

from homeassistant.components.repairs import RepairsFlow
from homeassistant.config_entries import ConfigEntryState
import voluptuous as vol

from .client import CODE_HUB_NOT_CONNECTED, CODE_HUB_WITHOUT_APPROVAL, CODE_HUB_WITHOUT_HOLD
from .coordinator import (
    APPROVE_HOST_KIND,
    BACKUP_REFUSALS,
    FINGERPRINT_PATTERN,
    HOLD_RELEASE_KIND,
    STALE_INSTALL_KIND,
    VordrCoordinator,
    failed_install,
    failed_install_names,
    install_again_offered,
    problem_reasons,
    reboot_held,
    reboot_required,
    refusal,
    stale_hold_release,
    stale_install_command,
    stale_install_names,
    waiting_hosts,
)
from .ha_updates_installer import FIX_KIND, STARTED, HaUpdatesInstaller, failure_names
from .texts import can_reboot, reboot_names

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

    def _held(self, host: dict[str, Any]) -> tuple[str, dict[str, str]] | None:
        """Why the command cannot run right now although the fix still applies, as an abort reason and its
        placeholders; None while it can. Asked when the dialog opens and again at confirmation."""
        return None

    def _shown(self, host: dict[str, Any] | None) -> dict[str, str]:
        """What the confirm step names besides the host, from the host's state as the hub last sent it (None: not
        known right now)."""
        return {}

    def _command(self, host: dict[str, Any]) -> str:
        """The command for host, asked after _still said the fix applies."""
        return self.command

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
            host = self._host()
            if host is not None and (held := self._held(host)) is not None:
                return self.async_abort(reason=held[0], description_placeholders=held[1])
            shown = {**names, **self._shown(host)}
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
        if (held := self._held(host)) is not None:
            return self.async_abort(reason=held[0], description_placeholders=held[1])
        code = await coordinator.command(self._node, self._command(host))
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

    def _held(self, host: dict[str, Any]) -> tuple[str, dict[str, str]] | None:
        # The fix sends the button's reboot, without a reason: a closed reboot window holds it (ADR-0187).
        return reboot_held(self._node, host)

    def _shown(self, host: dict[str, Any] | None) -> dict[str, str]:
        # The dialog of a repair with a reason names what that reason names, as the host last reported it (ADR-0201).
        return reboot_names(host)

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


class StaleInstallFlow(_HostFlow):
    """Install updates pending too long that a button takes (ADR-0203): the command of the button whose updates the hub
    counts as installable, the install button or the button for security updates. A run that installs them ends the
    problem, and with it the repair; updates no button takes keep it."""

    # As the install button: sent without a reason, the hub gives it one outside the window where the agent takes it
    # (ADR-0109), else outside_window.
    refusals = frozenset({"role_forbids", "outside_window", "rate_limited", *BACKUP_REFUSALS})

    def _shown(self, host: dict[str, Any] | None) -> dict[str, str]:
        return stale_install_names(host)

    def _still(self, host: dict[str, Any]) -> str:
        problem = host.get("problem")
        if not problem or "stale" not in problem_reasons(problem):
            return "no_longer_needed"
        if not stale_install_command(problem, host):
            return "install_not_offered"
        return ""

    def _command(self, host: dict[str, Any]) -> str:
        return stale_install_command(host.get("problem"), host)


# ADR-0208: the hub's codes of hold_release besides ok, and the client's, that have their own text in the fix dialog
# (hold_<code>). Any other shows with its code (hold_refused).
HOLD_REFUSALS: Final = frozenset(
    {"not_offered", "role_forbids", "client_not_approved", "already_in_state", "no_answer", CODE_HUB_WITHOUT_HOLD}
)


class HoldReleaseFlow(RepairsFlow):
    """Hold the release a host offers only in another branch (ADR-0208): the device stays on its branch, the release no
    longer counts as pending too long, and with that the problem and this repair end. The hub holds exactly the release
    the dialog names, while the host offers it; a newer release in the other branch brings the repair back. Checked
    again at confirmation, since the repair may be older than the host's state."""

    def __init__(self, entry_id: str, node: str, release: str) -> None:
        self._entry_id = entry_id
        self._node = node
        self._release = release

    async def async_step_init(self, user_input: dict[str, Any] | None = None) -> FlowResult:
        return await self.async_step_confirm()

    async def async_step_confirm(self, user_input: dict[str, Any] | None = None) -> FlowResult:
        names = {"node": self._node, "release": self._release}
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
        if not self._release or stale_hold_release(host.get("problem"), host) != self._release:
            return self.async_abort(reason="no_longer_needed", description_placeholders=names)
        code = await coordinator.hold_release(self._node, self._release)
        if code == "ok":
            return self.async_create_entry(data={})
        if code == CODE_HUB_NOT_CONNECTED:
            return self.async_abort(reason="hub_not_connected", description_placeholders=names)
        if code in HOLD_REFUSALS:
            return self.async_abort(reason=f"hold_{code}", description_placeholders=names)
        return self.async_abort(reason="hold_refused", description_placeholders={**names, "code": code})


# ADR-0192: the field of the fix, and its two refusals in the form, where nothing is sent.
CONF_FINGERPRINT: Final = "fingerprint"
FORM_INVALID: Final = "invalid_fingerprint"
FORM_DIFFERS: Final = "fingerprint_differs"
# The hub's codes of approve_host besides ok, and no_answer of the client: each has its own text in the fix dialog
# (approve_<code>). Any other shows with its code (approve_refused).
APPROVE_REFUSALS: Final = frozenset(
    {
        "ha_approval_off",
        "client_not_approved",
        "role_forbids",
        "unknown_node",
        "not_waiting",
        "fingerprint_mismatch",
        "bad_request",
        "store_failed",
        "no_answer",
    }
)


def normalized_fingerprint(value: Any) -> str | None:
    """The fingerprint as the person entered it, without spaces and in lower case; None unless that is 64 hex digits."""
    text = "".join(str(value or "").split()).lower()
    return text if FINGERPRINT_PATTERN.fullmatch(text) else None


def approve_refusal(node: str, code: str) -> tuple[str, dict[str, str]]:
    """Why the hub did not approve node, as the fix dialog's abort reason and its placeholders."""
    if code == CODE_HUB_NOT_CONNECTED:
        return "hub_not_connected", {"node": node}
    if code == CODE_HUB_WITHOUT_APPROVAL:  # the hub's last status no longer names approve_host
        return "approval_not_offered", {"node": node}
    if code in APPROVE_REFUSALS:
        return f"approve_{code}", {"node": node}
    return "approve_refused", {"node": node, "code": code}


class ApproveHostFlow(RepairsFlow):
    """Approve a waiting host from its repair (ADR-0192). The person enters the fingerprint `vordr-agent identity`
    printed on the host; spaces and upper case do not matter. Anything but 64 hex digits, and a fingerprint other than
    the one the hub saw, are refused in the form, and nothing is sent. A matching one goes to the hub, whose code
    decides what the dialog says. Checked when the dialog opens and again before sending: the hub may have decided
    meanwhile, the connection or the hub's permission may be gone; each has its own reason."""

    def __init__(self, entry_id: str, node: str) -> None:
        self._entry_id = entry_id
        self._node = node

    def _coordinator(self) -> VordrCoordinator | None:
        entry = self.hass.config_entries.async_get_entry(self._entry_id)
        if entry is None or entry.state is not ConfigEntryState.LOADED:
            return None
        return entry.runtime_data

    def _unable(self, coordinator: VordrCoordinator | None) -> str:
        """Why nothing can be sent now, as an abort reason; empty while the fingerprint can go to the hub."""
        if coordinator is None or not coordinator.connected:
            return "hub_not_connected"
        if self._node not in waiting_hosts(coordinator.data.hub):
            return "no_longer_waiting"
        if not coordinator.client.offers_host_approval:
            return "approval_not_offered"
        return ""

    async def async_step_init(self, user_input: dict[str, Any] | None = None) -> FlowResult:
        return await self.async_step_confirm()

    async def async_step_confirm(self, user_input: dict[str, Any] | None = None) -> FlowResult:
        coordinator = self._coordinator()
        if reason := self._unable(coordinator):
            return self.async_abort(reason=reason, description_placeholders={"node": self._node})
        assert coordinator is not None  # _unable said so
        names = waiting_hosts(coordinator.data.hub)[self._node]
        errors: dict[str, str] = {}
        if user_input is not None:
            given = normalized_fingerprint(user_input.get(CONF_FINGERPRINT))
            if given is None:
                errors[CONF_FINGERPRINT] = FORM_INVALID
            elif given != names["fingerprint"]:
                errors[CONF_FINGERPRINT] = FORM_DIFFERS  # another key asks under this name: nothing goes to the hub
            else:
                code = await coordinator.client.approve_host(self._node, given)
                if code != "ok":
                    key, shown = approve_refusal(self._node, code)
                    return self.async_abort(reason=key, description_placeholders=shown)
                return self.async_create_entry(data={})
        return self.async_show_form(
            step_id="confirm",
            data_schema=vol.Schema({vol.Required(CONF_FINGERPRINT): str}),
            errors=errors,
            description_placeholders=names,
        )


class HaUpdateRetryFlow(RepairsFlow):
    """A kept failed install of one of Home Assistant's own updates (ADR-0165): confirm, and Vördr installs it at
    once, outside the window, under every other rule (ADR-0182). Checked at confirmation: if it cannot start now -
    another install runs, paused, installing off, not ready - the flow says why with a fixed code and nothing is
    queued. The failure stays until an install goes through; its repair comes back if the run fails again."""

    def __init__(self, entry_id: str, entity_id: str) -> None:
        self._entry_id = entry_id
        self._entity_id = entity_id

    def _installer(self) -> HaUpdatesInstaller | None:
        entry = self.hass.config_entries.async_get_entry(self._entry_id)
        if entry is None or entry.state is not ConfigEntryState.LOADED:
            return None
        coordinator: VordrCoordinator = entry.runtime_data
        return coordinator.ha_updates_installer

    async def async_step_init(self, user_input: dict[str, Any] | None = None) -> FlowResult:
        return await self.async_step_confirm()

    async def async_step_confirm(self, user_input: dict[str, Any] | None = None) -> FlowResult:
        installer = self._installer()
        names = (
            installer.failure_names(self._entity_id) if installer is not None else failure_names(self._entity_id, None)
        )
        if user_input is None:
            return self.async_show_form(step_id="confirm", data_schema=vol.Schema({}), description_placeholders=names)
        if installer is None:
            return self.async_abort(reason="entry_not_loaded", description_placeholders=names)
        if (code := await installer.async_install_now(self._entity_id)) != STARTED:
            return self.async_abort(reason=code, description_placeholders=names)
        return self.async_create_entry(data={})


async def async_create_fix_flow(
    hass: HomeAssistant, issue_id: str, data: dict[str, str | int | float | None] | None
) -> RepairsFlow:
    """Home Assistant asks for the flow of a fixable repair: install_again_<node>, reboot_<node>, waiting_<node>,
    problem_<node> while a button takes its stale updates, or ha_update_failed_<object id>."""
    data = data or {}
    if data.get("kind") == FIX_KIND:
        return HaUpdateRetryFlow(str(data.get("entry_id") or ""), str(data.get("entity_id") or ""))
    if data.get("kind") == APPROVE_HOST_KIND:
        return ApproveHostFlow(str(data.get("entry_id") or ""), str(data.get("node") or ""))
    if data.get("kind") == STALE_INSTALL_KIND:
        return StaleInstallFlow(str(data.get("entry_id") or ""), str(data.get("node") or ""))
    if data.get("kind") == HOLD_RELEASE_KIND:
        return HoldReleaseFlow(
            str(data.get("entry_id") or ""), str(data.get("node") or ""), str(data.get("release") or "")
        )
    flow = InstallAgainFlow if data.get("kind") == "install_again" else RebootFlow
    return flow(str(data.get("entry_id") or ""), str(data.get("node") or ""))
