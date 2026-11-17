"""Installing the agent on a new Linux host over SSH from the options flow (ADR-0161): the steps and what they hold.

The credentials, the open connection and the hub's single-use download token live only in an AgentInstall, in the
flow's memory. clear() drops them and ends the install; the flow calls it however it ends (done, aborted, the dialog
closed). From then on nothing new starts: a login still under way closes its connection and reports install_cancelled,
and nothing asks the hub or runs on the host. Nothing of them is logged, stored, put in a form or raised. Each step
checks its precondition first and answers with a code that has a text; a failure of the network or the host is mapped
to a code at the boundary (ssh.py).

The options flow imports this module (and with it asyncssh) only when the install item is opened.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, Any

from homeassistant.config_entries import ConfigEntry, ConfigEntryState
from homeassistant.core import HomeAssistant
from homeassistant.helpers.storage import Store

from . import agent_install as ai, ssh
from .const import CONF_CA_PEM, CONF_NAME, CONF_URL

if TYPE_CHECKING:
    from .coordinator import VordrCoordinator

PROBE_TIMEOUT = 30.0
SUDO_TIMEOUT = 30.0
# The download (curl and wget give up after 15 minutes) and vordr-agent install, which checks the hub before it
# installs.
INSTALL_TIMEOUT = 1200.0
RELEASE_STORE_VERSION = 1

# The hub's answers that have their own text (ai.ABORTS); any other shows as hub_refused with its code.
HUB_ANSWERS = {*ai.HUB_CODES, "hub_not_connected", "hub_without_download", "no_answer"}
CANCELLED = "install_cancelled"


class AgentInstall:
    """One install on one host. The target and the confirmed host key are no secret; the credentials, the connection,
    sudo's password and the download are, and go with clear()."""

    def __init__(self, hass: HomeAssistant, entry: ConfigEntry) -> None:
        self.hass = hass
        self.entry = entry
        self.target: ai.Target | None = None
        self.host_key: ssh.HostKey | None = None
        self.ended = False  # clear() was called: nothing new starts
        self._facts: ai.Facts | None = None
        self._conn: ssh.Connection | None = None
        self._sudo = ai.Sudo.NONE
        self._sudo_password = ""
        self._releases: Store[dict[str, Any]] = Store(hass, RELEASE_STORE_VERSION, ai.release_store_key(entry.entry_id))

    # --- before anything is entered

    def precondition(self) -> str | None:
        """Why the install cannot run now; None when it can. Checked again before the hub is asked."""
        if self.entry.state is not ConfigEntryState.LOADED:
            return "entry_not_loaded"
        if ai.operator_keys(dict(self.entry.options)) is None:
            return "install_needs_operator_key"
        coordinator: VordrCoordinator = self.entry.runtime_data
        if not coordinator.connected:
            return "hub_not_connected"
        if not coordinator.client.offers_agent_download:
            return "hub_without_download"
        return None

    # --- the host key, without logging in

    async def fetch_host_key(self, target: ai.Target) -> str:
        code, key = await ssh.host_key(target.host, target.port)
        if code == ssh.OK:
            self.target, self.host_key = target, key
        return code

    def host_key_names(self) -> dict[str, str]:
        assert self.target is not None and self.host_key is not None
        return {"host": self.target.host, "port": str(self.target.port), **ssh.describe(self.host_key)}

    # --- log in, probe, sudo

    async def login(self, creds: ai.Credentials) -> tuple[str, dict[str, str]]:
        """Log in with exactly the confirmed host key, probe the host and find how to become root. ok, a code the
        login form shows, one that ends the flow with the values its text names, or install_cancelled when the flow
        ended meanwhile (its connection is closed then). The connection does not keep the login password or the key
        (ssh.connect); sudo's password is kept until the install sent it."""
        assert self.target is not None and self.host_key is not None
        key = None
        if creds.ssh_key:
            code, key = await self.hass.async_add_executor_job(ssh.load_key, creds.ssh_key, creds.ssh_key_passphrase)
            if code != ssh.OK:
                return code, {}
        if self.ended:
            return CANCELLED, {}
        code, conn = await ssh.connect(
            self.target.host, self.target.port, self.target.username, self.host_key, creds.password, key
        )
        del key
        if conn is None:
            return code, {}
        if self.ended:  # closed while connecting
            await ssh.close(conn)
            return CANCELLED, {}
        self._conn = conn  # from now on clear() closes it at once, also in the middle of the probe
        code, names = await self._probe(conn, creds)
        if self.ended:
            await ssh.close(conn)
            return CANCELLED, {}
        if code != ssh.OK:
            self._conn = None
            await ssh.close(conn)
            return code, names
        return ssh.OK, {}

    async def _probe(self, conn: ssh.Connection, creds: ai.Credentials) -> tuple[str, dict[str, str]]:
        ran = await ssh.run(conn, ai.probe_command(), timeout=PROBE_TIMEOUT)
        if ran.code != ssh.OK:
            return ran.code, {}
        code, names, facts = ai.check_facts(ran.stdout)
        if facts is None:
            return code, names
        self._facts, self._sudo, self._sudo_password = facts, ai.Sudo.NONE, ""
        if facts.root:
            return ssh.OK, {}
        ran = await ssh.run(conn, ai.SUDO_NOPASSWD_CHECK, timeout=SUDO_TIMEOUT)
        if ran.code != ssh.OK:
            return ran.code, {}
        if ran.exit_status == 0:
            self._sudo = ai.Sudo.NOPASSWD
            return ssh.OK, {}
        password = creds.for_sudo()
        if not password or "\n" in password or "\r" in password:
            return "sudo_needs_password", {}
        ran = await ssh.run(conn, ai.SUDO_PASSWORD_CHECK, stdin=password + "\n", timeout=SUDO_TIMEOUT)
        if ran.code != ssh.OK:
            return ran.code, {}
        if ran.exit_status != 0:
            return "sudo_failed", {}
        self._sudo, self._sudo_password = ai.Sudo.PASSWORD, password
        return ssh.OK, {}

    # --- the install

    async def install(self) -> ai.Outcome:
        """Ask the hub for the release, check it, install on the host. The connection and every secret go at the end,
        however it ends (also when the flow is closed and this task cancelled)."""
        try:
            return await self._install()
        finally:
            await ssh.close(self._conn)
            self.clear()

    async def _install(self) -> ai.Outcome:
        if self.ended:
            return ai.Outcome(CANCELLED)
        keys = ai.operator_keys(dict(self.entry.options))
        if keys is None:
            return ai.Outcome("install_needs_operator_key")
        if self._conn is None or self._facts is None or self.target is None:
            return ai.Outcome("ssh_lost")
        coordinator: VordrCoordinator = self.entry.runtime_data
        answer = await coordinator.client.agent_download("linux", self._facts.arch)
        code = str(answer.get("code", ""))
        if code != ai.OK:
            if code in HUB_ANSWERS:
                return ai.Outcome(code)
            return ai.Outcome("hub_refused", {"code": ai.shown(code)})
        code, release = ai.check_release(answer, keys.raw(), self._facts.arch)
        if release is None:
            return ai.Outcome(code)
        newest = ai.stored_release(await self._releases.async_load())
        if (code := ai.check_release_age(release.version, newest)) != ai.OK:
            return ai.Outcome(code, {"version": ai.shown(release.version), "newest": newest})
        if release.version != newest and ai.RELEASE_PATTERN.fullmatch(release.version):  # newer, as checked above
            await self._releases.async_save({"version": release.version})
        ha_url, ca_pem = self.entry.data[CONF_URL], self.entry.data.get(CONF_CA_PEM, "")
        url = ai.download_url(ha_url, release.path)
        if not ai.DOWNLOAD_URL_PATTERN.fullmatch(url):
            return ai.Outcome("download_invalid")
        command = ai.install_command(
            release, ai.agent_hub_url(ha_url), self.target.node, keys, bool(ca_pem), self._sudo
        )
        stdin = ai.install_input(url, ca_pem, self._sudo_password)
        self._sudo_password = ""
        if self.ended:  # the last moment before anything runs on the host
            return ai.Outcome(CANCELLED)
        ran = await ssh.run(self._conn, command, stdin=stdin, timeout=INSTALL_TIMEOUT)
        if ran.code != ssh.OK:
            return ai.Outcome(ran.code)
        return ai.outcome(ran.stdout, release.version)

    # --- texts and the end

    def names(self, code: str, names: dict[str, str] | None = None) -> dict[str, str]:
        """Exactly the values the text of an end names (ai.ABORTS)."""
        known = {
            "host": self.target.host if self.target else "-",
            "ca_path": ai.CA_PATH,
            "hub": ai.agent_hub_url(self.entry.data[CONF_URL]),
            "client": self.entry.data[CONF_NAME],  # this Home Assistant's name at the hub
            **(names or {}),
        }
        return {k: known.get(k) or "-" for k in ai.ABORTS.get(code, ())}

    def clear(self) -> None:
        """End the install: nothing new starts, the connection is closed (a command running on it ends at once) and
        everything secret is dropped. Python keeps no copy we know of; the garbage collector frees the strings once
        nothing refers to them."""
        self.ended = True
        if self._conn is not None:
            self._conn.close()
        self._conn, self._sudo_password = None, ""
