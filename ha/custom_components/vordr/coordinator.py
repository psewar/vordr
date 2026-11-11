"""State of one hub as Home Assistant sees it: pushed by the hub, never polled."""

from __future__ import annotations

from dataclasses import dataclass, field, replace
from datetime import datetime, timedelta
import logging
from typing import TYPE_CHECKING, Any
import unicodedata

from homeassistant.core import HomeAssistant, callback
from homeassistant.exceptions import HomeAssistantError
from homeassistant.helpers import config_validation as cv, device_registry as dr, issue_registry as ir
from homeassistant.helpers.update_coordinator import DataUpdateCoordinator
from homeassistant.util import dt as dt_util
import voluptuous as vol

from . import protocol as p
from .client import Connection, VordrClient
from .const import (
    CODES,
    CONF_CA_PEM,
    CONF_NAME,
    CONF_PRIVATE_KEY,
    CONF_URL,
    DOMAIN,
    EVENT_PROBLEM,
    EVENT_RUN_FINISHED,
    REASON_REBOOT_PENDING,
    VERSION,
)
from .texts import TEXT_KEYS, Texts, can_reboot, placeholders, problem_key

if TYPE_CHECKING:
    from homeassistant.config_entries import ConfigEntry

_LOGGER = logging.getLogger(__name__)

HUB_DEVICE = (DOMAIN, "hub")

# ADR-0132: after a reboot asked for through the repair, the repair stays away this long while the host still reports
# "reboot required" - the reboot takes a moment. A finished reboot run ends the wait earlier.
REBOOT_ASKED_HOLD = timedelta(minutes=30)

# The hub's refusals by its backup check (internal/hub/registry.go, prebackup.go): an install waits for a fresh
# backup; backup_host_unavailable: the hypervisor's agent, which makes it right before the install, is not there.
BACKUP_REFUSALS = (
    "backup_stale",
    "backup_missing",
    "backup_unreachable",
    "backup_unmapped",
    "backup_host_unavailable",
)


# A reason to run outside the window, as the hub takes it (protocol.ValidReason): at most this many bytes of UTF-8.
MAX_REASON_BYTES = 200


def valid_reason(value: Any) -> str:
    """Service field "reason" (ADR-0087): text, not blank, at most MAX_REASON_BYTES bytes, no control characters."""
    text = cv.string(value).strip()
    if not text or len(text.encode()) > MAX_REASON_BYTES or any(unicodedata.category(c) == "Cc" for c in text):
        raise vol.Invalid(f"a reason of 1 to {MAX_REASON_BYTES} bytes, without line breaks or control characters")
    return text


def local_time(at: Any) -> str:
    """A time for people in Home Assistant's zone: the clock time today, with the date on another day; "?" unknown."""
    if at is None:
        return "?"
    local = dt_util.as_local(at)
    if local.date() == dt_util.now().date():
        return local.strftime("%H:%M")
    return local.strftime("%d.%m. %H:%M")


def reboot_required(host: dict[str, Any] | None) -> bool:
    """The host reports that it needs a reboot - True only for a definite yes, never for unknown."""
    return ((host or {}).get("reboot") or {}).get("required") is True


def problem_reasons(problem: dict[str, Any]) -> set[str]:
    """All reasons of a problem; older hubs send only "reason"."""
    return {str(r) for r in (problem.get("reasons") or [problem.get("reason")]) if r}


def only_reboot_pending(problem: dict[str, Any]) -> bool:
    return problem_reasons(problem) == {REASON_REBOOT_PENDING}


def host_device(node: str) -> tuple[str, str]:
    """Device identifier of a host."""
    return (DOMAIN, f"host:{node}")


@dataclass(frozen=True)
class VordrData:
    """Everything the entities show. hosts maps node -> HostView as the hub sent it."""

    connection: Connection = Connection.CONNECTING
    reason: str = ""
    hub: dict[str, Any] | None = None
    hosts: dict[str, dict[str, Any]] = field(default_factory=dict)
    # A snapshot arrived on the current connection: hosts is complete, entities may be added and removed.
    synced: bool = False


class VordrCoordinator(DataUpdateCoordinator[VordrData]):
    """Holds the hub's views and tells entities, events and repairs about changes."""

    config_entry: ConfigEntry

    def __init__(self, hass: HomeAssistant, entry: ConfigEntry) -> None:
        super().__init__(hass, _LOGGER, config_entry=entry, name=DOMAIN, update_interval=None)
        self.data = VordrData()
        self._problems: dict[str, dict[str, Any] | None] = {}
        self._runs: dict[str, tuple[str, str]] = {}  # node -> (job_id, state) of the last run seen
        self._packages: dict[str, str | None] = {}  # node -> the packages its problem names (placeholder)
        self._reboot_asked: dict[str, datetime] = {}  # node -> when a reboot was asked for through the repair
        self.hub_device_id = ""  # set at setup; host devices hang below it
        self.texts = Texts(hass)  # loaded at setup, before the first message
        self.client = VordrClient(
            hass,
            entry.data[CONF_URL],
            entry.data.get(CONF_CA_PEM, ""),
            entry.data[CONF_NAME],
            p.load_private_key(entry.data[CONF_PRIVATE_KEY]),
            VERSION,
            self._on_message,
            self._on_connection,
        )

    async def _async_update_data(self) -> VordrData:
        return self.data  # pushed only; a refresh has nothing to fetch

    @property
    def connected(self) -> bool:
        """Approved, connected and holding a complete view."""
        return self.data.connection == Connection.APPROVED and self.data.synced

    async def command(self, node: str, command: str) -> str:
        """Send a command; returns the hub's code (ok or the reason for refusing)."""
        result = await self.client.command(node, command)
        return str(result.get("code", ""))

    async def command_outside_window(self, node: str, command: str, reason: str) -> None:
        """Send an install or reboot command that may run outside the host's window, with the person's reason
        (ADR-0087). Only where the hub offers it: an older hub would drop the reason, an older agent refuse the job."""
        if not ((self.data.hosts.get(node) or {}).get("capabilities") or {}).get("outside_window"):
            raise HomeAssistantError(
                translation_domain=DOMAIN,
                translation_key="outside_window_unsupported",
                translation_placeholders={"node": node},
            )
        result = await self.client.command(node, command, reason)
        code = str(result.get("code", ""))
        if code != "ok":
            raise self.refused(node, code)

    def refused(self, node: str, code: str) -> HomeAssistantError:
        """The hub's refusal as an error for people. A refusal by the backup check says what is missing."""
        if code in BACKUP_REFUSALS:
            backup = (self.data.hosts.get(node) or {}).get("backup") or {}
            last = dt_util.parse_datetime(backup.get("last") or "")
            age = round((dt_util.utcnow() - last).total_seconds() / 3600) if last else "?"
            placeholders = {
                "node": node,
                "group": str(backup.get("group") or "-"),
                "age": str(age),
                "max": str(backup.get("max_age_hours", "?")),
                "hypervisor": str(backup.get("before_install") or "-"),
            }
            return HomeAssistantError(
                translation_domain=DOMAIN,
                translation_key=f"command_refused_{code}",
                translation_placeholders=placeholders,
            )
        if code == "role_forbids":  # this client's role at the hub does not allow the command
            return HomeAssistantError(
                translation_domain=DOMAIN,
                translation_key="command_refused_role_forbids",
                translation_placeholders={"node": node},
            )
        if code == "outside_window_unsupported":  # the host's agent is older than ADR-0087
            return HomeAssistantError(
                translation_domain=DOMAIN,
                translation_key="outside_window_unsupported",
                translation_placeholders={"node": node},
            )
        if code == "outside_window":  # closed window, and the agent takes no reason (ADR-0109)
            return HomeAssistantError(
                translation_domain=DOMAIN,
                translation_key="command_refused_outside_window",
                translation_placeholders={"node": node},
            )
        if code == "rate_limited":  # the agent's minimum interval has not passed (ADR-0110)
            nxt = dt_util.parse_datetime((self.data.hosts.get(node) or {}).get("next_install") or "")
            return HomeAssistantError(
                translation_domain=DOMAIN,
                translation_key="command_refused_rate_limited",
                translation_placeholders={"node": node, "next": local_time(nxt)},
            )
        return HomeAssistantError(
            translation_domain=DOMAIN,
            translation_key="command_refused",
            translation_placeholders={"node": node, "code": code},
        )

    # --- from the client

    @callback
    def _on_connection(self, state: Connection, reason: str) -> None:
        synced = self.data.synced and state == Connection.APPROVED
        self._connection_issues(state, reason)
        if state == self.data.connection and reason == self.data.reason and synced == self.data.synced:
            return
        self.async_set_updated_data(replace(self.data, connection=state, reason=reason, synced=synced))

    @callback
    def _on_message(self, msg: dict[str, Any]) -> None:
        t = msg["t"]
        hosts, hub, synced = dict(self.data.hosts), self.data.hub, self.data.synced
        changed: set[str] = set()
        removed: set[str] = set()
        if t == p.T_SNAPSHOT:
            hub, synced = msg.get("hub"), True
            new = {h["node"]: h for h in msg.get("hosts") or [] if isinstance(h, dict) and "node" in h}
            removed = set(hosts) - set(new)
            changed = set(new)
            hosts = new
        elif t == p.T_HOST and isinstance(msg.get("host"), dict) and "node" in msg["host"]:
            host = msg["host"]
            hosts[host["node"]] = host
            changed = {host["node"]}
        elif t == p.T_HOST_REMOVED:
            node = str(msg.get("node", ""))
            if hosts.pop(node, None) is not None:
                removed = {node}
        elif t == p.T_HUB:
            hub = msg.get("hub")
        else:
            _LOGGER.debug("ignoring message type %s", t)
            return
        limits = (hub or {}).get("limits")
        for node in changed:
            self._track(node, hosts[node], limits, t == p.T_SNAPSHOT)
        for node in removed:
            self._forget(node)
        self.async_set_updated_data(replace(self.data, hub=hub, hosts=hosts, synced=synced))
        if t == p.T_SNAPSHOT:
            self._remove_stale_devices(set(hosts))
        self._hub_version((hub or {}).get("version"))

    def _hub_version(self, version: str | None) -> None:
        """The hub device is created before the first connection; its version arrives with the hub's view."""
        reg = dr.async_get(self.hass)
        dev = reg.async_get(self.hub_device_id) if self.hub_device_id else None
        if dev and version and dev.sw_version != version:
            reg.async_update_device(dev.id, sw_version=version)

    # --- events, repairs, devices

    def _track(self, node: str, host: dict[str, Any], limits: dict[str, Any] | None, snapshot: bool) -> None:
        problem = host.get("problem")
        old = self._problems.get(node, _UNSEEN)
        # A package problem is also new when other packages are behind it (another malicious version turned up).
        packages = placeholders(node, problem, limits, host)["packages"] if problem else None
        changed = old is _UNSEEN or _problem_key(old) != _problem_key(problem) or packages != self._packages.get(node)
        reboot = can_reboot(host)
        if changed and (problem or old not in (_UNSEEN, None)):
            # A new or changed problem, or one that ended; after a restart only the problems still there.
            self.hass.bus.async_fire(EVENT_PROBLEM, self._problem_event(node, problem, limits, reboot, host))
        if changed or snapshot:  # after a reconnect too: the hub's limits, which the repair names, may have changed
            self._problem_issue(node, problem, limits, reboot, host)
        self._problems[node] = problem
        self._packages[node] = packages

        run = host.get("last_run") or {}
        seen = (str(run.get("job_id", "")), str(run.get("state", "")))
        before = self._runs.get(node)
        if run and seen[1] in CODES and before is not None and before != seen:
            state = seen[1]
            key = problem_key({"reason": state}, reboot)
            self.hass.bus.async_fire(
                EVENT_RUN_FINISHED,
                {
                    "node": node,
                    "job_id": seen[0],
                    "verb": run.get("verb"),
                    "scope": run.get("scope", ""),
                    "by": run.get("by", ""),  # who started it: ha:<client>, admin, rollout
                    "state": state,
                    # What it means and what to do, in every shipped language; none for plain ok.
                    "message": self.texts.messages(key, placeholders(node, {"reason": state}, limits))
                    if state in TEXT_KEYS
                    else {},
                    "summary": run.get("summary") or {},
                },
            )
            if run.get("verb") == "reboot":  # the asked-for reboot ran: from now on the host's report decides
                self._reboot_asked.pop(node, None)
        self._runs[node] = seen
        self._reboot_issue(node, host, problem)

        device = host.get("device") or {}
        reg = dr.async_get(self.hass)
        entry = reg.async_get_device_by_identifier(host_device(node), self.config_entry.entry_id)
        if entry and (entry.sw_version != device.get("agent_version") or entry.model != device.get("model")):
            reg.async_update_device(entry.id, sw_version=device.get("agent_version"), model=device.get("model"))

    def _problem_event(
        self,
        node: str,
        problem: dict[str, Any] | None,
        limits: dict[str, Any] | None,
        reboot: bool = True,
        host: dict[str, Any] | None = None,
    ) -> dict[str, Any]:
        """Codes for automations, plus the repair text in every shipped language (message.de, message.en, ...)."""
        if not problem:
            return {
                "node": node,
                "active": False,
                "reason": None,
                "reasons": [],
                "check_failed_code": None,
                "message": {},
            }
        return {
            "node": node,
            "active": True,
            "reason": problem.get("reason"),
            "reasons": problem.get("reasons") or [],
            "check_failed_code": problem.get("check_failed_code"),
            "message": self.texts.messages(problem_key(problem, reboot), placeholders(node, problem, limits, host)),
        }

    def _forget(self, node: str) -> None:
        if self._problems.get(node):
            self.hass.bus.async_fire(EVENT_PROBLEM, self._problem_event(node, None, None))
        self._problems.pop(node, None)
        self._packages.pop(node, None)
        self._runs.pop(node, None)
        self._reboot_asked.pop(node, None)
        ir.async_delete_issue(self.hass, DOMAIN, f"problem_{node}")
        ir.async_delete_issue(self.hass, DOMAIN, f"reboot_{node}")
        reg = dr.async_get(self.hass)
        if entry := reg.async_get_device_by_identifier(host_device(node), self.config_entry.entry_id):
            reg.async_remove_device(entry.id)

    def _remove_stale_devices(self, nodes: set[str]) -> None:
        """After a snapshot: devices of hosts the hub no longer has (forgotten while HA was down) go too."""
        reg = dr.async_get(self.hass)
        for entry in dr.async_entries_for_config_entry(reg, self.config_entry.entry_id):
            for domain, ident in entry.identifiers:
                if domain == DOMAIN and ident.startswith("host:") and ident[5:] not in nodes:
                    reg.async_remove_device(entry.id)

    def _problem_issue(
        self,
        node: str,
        problem: dict[str, Any] | None,
        limits: dict[str, Any] | None,
        reboot: bool,
        host: dict[str, Any] | None = None,
    ) -> None:
        """One text per problem key, so that the frontend shows all of it in the reader's language."""
        issue_id = f"problem_{node}"
        if not problem or (only_reboot_pending(problem) and reboot_required(host)):
            # A problem that is nothing but the long-pending reboot shows in the host's reboot repair (ADR-0132),
            # which can also do the reboot; the problem sensor and event stay as they are.
            ir.async_delete_issue(self.hass, DOMAIN, issue_id)
            return
        ir.async_create_issue(
            self.hass,
            DOMAIN,
            issue_id,
            is_fixable=False,
            severity=ir.IssueSeverity.WARNING,
            translation_key=f"problem_{problem_key(problem, reboot)}",
            translation_placeholders=placeholders(node, problem, limits, host),
        )

    def reboot_asked(self, node: str) -> None:
        """The repair's fix sent a reboot the hub accepted: keep the repair away until it ran (REBOOT_ASKED_HOLD)."""
        self._reboot_asked[node] = dt_util.utcnow()

    def _reboot_issue(self, node: str, host: dict[str, Any], problem: dict[str, Any] | None) -> None:
        """ADR-0132: a host that needs a reboot is a repair from its first report on, until the host reports it no
        longer needs one. Fixable where the hub offers the reboot; a warning that turns into an error once the hub
        calls it a problem (reboot_pending, ADR-0014)."""
        issue_id = f"reboot_{node}"
        if not reboot_required(host):  # no, or unknown (no signal answered): nothing to claim
            self._reboot_asked.pop(node, None)
            ir.async_delete_issue(self.hass, DOMAIN, issue_id)
            return
        asked = self._reboot_asked.get(node)
        if asked is not None and dt_util.utcnow() - asked < REBOOT_ASKED_HOLD:
            return
        self._reboot_asked.pop(node, None)
        fixable = can_reboot(host)
        overdue = bool(problem) and REASON_REBOOT_PENDING in problem_reasons(problem)
        since = (host.get("reboot") or {}).get("since")
        ir.async_create_issue(
            self.hass,
            DOMAIN,
            issue_id,
            is_fixable=fixable,
            severity=ir.IssueSeverity.ERROR if overdue else ir.IssueSeverity.WARNING,
            translation_key="reboot_required" if fixable else "reboot_required_manual",
            translation_placeholders={
                "node": node,
                "since": local_time(dt_util.parse_datetime(since) if since else None),
            },
            data={"entry_id": self.config_entry.entry_id, "node": node},
        )

    def _connection_issues(self, state: Connection, reason: str) -> None:
        if state == Connection.REJECTED and reason == p.REASON_PROTOCOL_MISMATCH:
            ir.async_create_issue(
                self.hass,
                DOMAIN,
                "protocol_mismatch",
                is_fixable=False,
                severity=ir.IssueSeverity.ERROR,
                translation_key="protocol_mismatch",
                translation_placeholders={"protocol": str(p.PROTOCOL)},
            )
        elif state == Connection.REJECTED:
            ir.async_create_issue(
                self.hass,
                DOMAIN,
                "client_rejected",
                is_fixable=False,
                severity=ir.IssueSeverity.ERROR,
                translation_key="client_rejected",
                translation_placeholders={
                    "reason": reason or "-",
                    "name": self.config_entry.data[CONF_NAME],
                    "fingerprint": self.client.fingerprint,
                },
            )
        elif state == Connection.APPROVED:
            ir.async_delete_issue(self.hass, DOMAIN, "protocol_mismatch")
            ir.async_delete_issue(self.hass, DOMAIN, "client_rejected")


_UNSEEN: Any = object()


def _problem_key(problem: dict[str, Any] | None) -> tuple[Any, ...] | None:
    if not problem:
        return None
    return (problem.get("reason"), tuple(problem.get("reasons") or ()), problem.get("check_failed_code"))
