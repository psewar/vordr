"""The one place that speaks SSH, for installing the agent on a new host (ADR-0161).

Every failure of the network or of the other side becomes a fixed code here, at the boundary; the callers only read
codes. Nothing is read from or written to files: no ~/.ssh/config, no known_hosts, no agent, no default keys. The host
key is fetched once without logging in, shown to the person, and then the only key the login accepts. Only "Add host"
imports this module (and with it asyncssh); the rest of the integration loads without it.
"""

from __future__ import annotations

import asyncio
from dataclasses import dataclass
import logging
from typing import Any

import asyncssh

_LOGGER = logging.getLogger(__name__)

CONNECT_TIMEOUT = 15.0
LOGIN_TIMEOUT = 30.0
KEEPALIVE = 30.0
# Characters of a command's stdout Home Assistant reads at most: our scripts print a few short lines. A host that sends
# more (a hostile or broken one: a confirmed host key does not make it trustworthy) cannot fill Home Assistant's memory.
MAX_OUTPUT = 64 << 10

# Codes of this boundary.
OK = "ok"  # the same "ok" as agent_install.OK
UNREACHABLE = "ssh_unreachable"  # name, route or port
TIMEOUT = "ssh_timeout"
FAILED = "ssh_failed"  # the other side speaks no SSH we can use
NO_HOST_KEY = "ssh_no_host_key"
AUTH_FAILED = "ssh_auth_failed"
HOST_KEY_CHANGED = "ssh_host_key_changed"
LOST = "ssh_lost"  # the connection ended while a command ran
KEY_INVALID = "ssh_key_invalid"
OUTPUT_TOO_LONG = "host_output_too_long"

HostKey = asyncssh.SSHKey
Connection = asyncssh.SSHClientConnection


@dataclass(frozen=True)
class Ran:
    """A command's end: OK with its exit status and output, or the code why there is none."""

    code: str
    exit_status: int | None = None
    stdout: str = ""


def describe(key: HostKey) -> dict[str, str]:
    """The host key as `ssh-keygen -l` shows it: algorithm and SHA256 fingerprint."""
    return {"algorithm": key.get_algorithm(), "fingerprint": key.get_fingerprint("sha256")}


def _failure(err: BaseException) -> str:
    """The code of a failure at the boundary. Only the type decides, never the text; the text is not logged either,
    it may name the user."""
    _LOGGER.debug("ssh: %s", type(err).__name__)
    if isinstance(err, asyncssh.HostKeyNotVerifiable):
        return HOST_KEY_CHANGED
    if isinstance(err, asyncssh.PermissionDenied):
        return AUTH_FAILED
    if isinstance(err, TimeoutError):  # before OSError, of which it is a subclass
        return TIMEOUT
    if isinstance(err, OSError):
        return UNREACHABLE
    return FAILED


def _common() -> dict[str, Any]:
    """Options of every connection: nothing from files, the user's agent or default keys."""
    return {
        "config": None,
        "agent_path": None,
        "x509_trusted_certs": None,
        "connect_timeout": CONNECT_TIMEOUT,
        "login_timeout": LOGIN_TIMEOUT,
        "keepalive_interval": KEEPALIVE,
        "errors": "replace",  # output that is no UTF-8 must not end the install
    }


async def host_key(host: str, port: int) -> tuple[str, HostKey | None]:
    """The host key the server presents, without logging in: nothing secret is sent."""
    try:
        options = await asyncssh.SSHClientConnectionOptions.construct(known_hosts=None, client_keys=None, **_common())
        key = await asyncssh.get_server_host_key(host, port, options=options, config=None)
    except (asyncssh.Error, OSError) as err:  # network and the other side: only known by trying
        return _failure(err), None
    if key is None:
        return NO_HOST_KEY, None
    return OK, key


def load_key(text: str, passphrase: str) -> tuple[str, asyncssh.SSHKey | None]:
    """A pasted private key (OpenSSH or PEM), decrypted with its passphrase. Blocking (key derivation): run it in the
    executor. Whether the text is a usable key only parsing tells."""
    try:
        return OK, asyncssh.import_private_key(text, passphrase or None)
    except (asyncssh.KeyImportError, asyncssh.KeyEncryptionError, ValueError):
        return KEY_INVALID, None


class _Login(asyncssh.SSHClient):
    """The login's password and key, handed to asyncssh once, when it asks for them, and dropped then and when the
    login ends. The connection keeps its options and this client for its lifetime: neither holds a secret afterwards
    (ADR-0161)."""

    def __init__(self, password: str, key: asyncssh.SSHKey | None) -> None:
        self._password = password
        self._key = key

    def once_password(self) -> str | None:
        """Given as the password option: asyncssh calls it once, for password or keyboard-interactive login."""
        password, self._password = self._password, ""
        return password or None

    def public_key_auth_requested(self) -> list[asyncssh.SSHKey] | None:
        """asyncssh asks the client for a key, as no key is in the options."""
        key, self._key = self._key, None
        return [key] if key is not None else None

    def drop(self) -> None:
        self._password, self._key = "", None


async def connect(
    host: str, port: int, username: str, pinned: HostKey, password: str, key: asyncssh.SSHKey | None
) -> tuple[str, Connection | None]:
    """Log in, accepting only the host key the person confirmed (nothing is written to a known_hosts file). The
    password and the key are not kept by the connection once it is open."""
    login = _Login(password, key)
    try:
        options = await asyncssh.SSHClientConnectionOptions.construct(
            username=username,
            password=login.once_password if password else None,
            client_keys=None,  # no default keys and no agent; the key comes from _Login
            client_factory=lambda: login,
            known_hosts=([pinned], [], []),
            **_common(),
        )
        conn = await asyncssh.connect(host, port, options=options, config=None)
    except (asyncssh.Error, OSError) as err:  # network, the host key, the login: only known by trying
        return _failure(err), None
    finally:
        login.drop()
    return OK, conn


async def run(conn: Connection, command: str, stdin: str = "", timeout: float | None = None) -> Ran:
    """Run one command; stdin is written and closed (without any, it is closed at once). stderr is discarded; stdout
    is read up to MAX_OUTPUT characters, and a command that prints more is closed and ends with OUTPUT_TOO_LONG."""
    feed: dict[str, Any] = {"input": stdin} if stdin else {"stdin": asyncssh.DEVNULL}
    process: asyncssh.SSHClientProcess[str] | None = None
    out = ""
    try:
        async with asyncio.timeout(timeout):
            process = await conn.create_process(command, stderr=asyncssh.DEVNULL, **feed)
            while len(out) <= MAX_OUTPUT:
                chunk = await process.stdout.read(MAX_OUTPUT + 1 - len(out))
                if not chunk:  # end of the output
                    break
                out += chunk
            if len(out) > MAX_OUTPUT:
                return Ran(OUTPUT_TOO_LONG)
            await process.wait_closed()
    except TimeoutError:  # the process did not end in time
        return Ran(TIMEOUT)
    except (asyncssh.Error, OSError) as err:  # the connection ended under it
        _LOGGER.debug("ssh: %s", type(err).__name__)
        return Ran(LOST)
    finally:
        if process is not None:
            process.close()  # also the end of a command that printed too much or ran too long
    return Ran(OK, process.exit_status, out)


async def close(conn: Connection | None) -> None:
    """Close the connection; a connection that is gone already is closed too."""
    if conn is None:
        return
    conn.close()
    try:
        await conn.wait_closed()
    except (asyncssh.Error, OSError):  # closing a broken connection may report its break once more
        return
