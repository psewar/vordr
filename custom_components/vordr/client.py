"""The connection to vordr-hub over /v1/ha: one long-lived WebSocket that reconnects with backoff.

The client reports its state as a fact (`Connection`) and every command's outcome as a code; callers never have
to read anything out of an exception.
"""

from __future__ import annotations

import asyncio
import base64
import binascii
from collections.abc import Callable
from dataclasses import dataclass
from enum import StrEnum
import json
import logging
import secrets
import ssl
from typing import Any

import aiohttp
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
from homeassistant.core import HomeAssistant
from homeassistant.helpers.aiohttp_client import async_get_clientsession
from yarl import URL

from . import protocol as p

_LOGGER = logging.getLogger(__name__)

COMMAND_TIMEOUT = 15.0
REJECTED_RETRY = 600.0  # like the agent: a refused client asks again after 10 minutes
BACKOFF_MAX = 30.0

# Codes of the client itself, besides those the hub sends in command_result.
CODE_HUB_NOT_CONNECTED = "hub_not_connected"
CODE_NO_ANSWER = "no_answer"


class Connection(StrEnum):
    """What the client knows about its connection, at any moment."""

    CONNECTING = "connecting"
    UNREACHABLE = "unreachable"
    PENDING_APPROVAL = "pending_approval"
    APPROVED = "approved"
    REJECTED = "rejected"


class ProbeStage(StrEnum):
    """The step of reaching the hub that failed (same stages as `vordr-agent hub-check`)."""

    DNS = "dns"
    CONNECT = "connect"
    TLS = "tls"
    WEBSOCKET = "websocket"
    CHALLENGE = "challenge"


@dataclass(frozen=True)
class Status:
    """A status message of the hub."""

    state: str
    reason: str
    fingerprint: str


async def ssl_context(hass: HomeAssistant, ca_pem: str) -> ssl.SSLContext:
    """TLS context: only the given CA when one is set (like the agent's hub.ca_file), else the system roots.

    Raises ssl.SSLError for a PEM that is no certificate: there is no way to tell short of parsing it.
    """

    def make() -> ssl.SSLContext:
        if ca_pem:
            return ssl.create_default_context(cadata=ca_pem)
        return ssl.create_default_context()

    return await hass.async_add_executor_job(make)


async def _read(ws: aiohttp.ClientWebSocketResponse, timeout: float) -> dict[str, Any] | None:
    """The next message as a dict; None when the connection ended or the message is no JSON object with a type."""
    msg = await ws.receive(timeout=timeout)
    if msg.type != aiohttp.WSMsgType.TEXT:
        return None
    try:
        data = json.loads(msg.data)
    except ValueError:
        return None
    if not isinstance(data, dict) or not isinstance(data.get("t"), str):
        return None
    return data


def _nonce(data: dict[str, Any]) -> bytes | None:
    raw = data.get("nonce")
    if not isinstance(raw, str):
        return None
    try:
        nonce = base64.b64decode(raw, validate=True)
    except binascii.Error:  # malformed base64 cannot be told apart without decoding it
        return None
    return nonce if len(nonce) == p.NONCE_SIZE else None


async def probe(hass: HomeAssistant, url: str, ca_pem: str) -> ProbeStage | None:
    """Walk the stages of reaching the hub without logging in; the first that fails is the answer."""
    u = URL(url)
    host, port = u.host or "", u.port or 443
    loop = asyncio.get_running_loop()
    try:
        await loop.getaddrinfo(host, port)
    except OSError:
        return ProbeStage.DNS
    try:
        _, writer = await asyncio.wait_for(asyncio.open_connection(host, port), 10)
    except (OSError, TimeoutError):
        return ProbeStage.CONNECT
    writer.close()
    ctx = await ssl_context(hass, ca_pem)
    try:
        _, writer = await asyncio.wait_for(asyncio.open_connection(host, port, ssl=ctx, server_hostname=host), 10)
    except ssl.SSLError:
        return ProbeStage.TLS
    except (OSError, TimeoutError):
        return ProbeStage.CONNECT
    writer.close()
    session = async_get_clientsession(hass)
    try:
        async with session.ws_connect(url, ssl=ctx, max_msg_size=p.MAX_MESSAGE) as ws:
            data = await _read(ws, p.HELLO_TIMEOUT)
    except aiohttp.WSServerHandshakeError:
        return ProbeStage.WEBSOCKET
    except (aiohttp.ClientError, OSError, TimeoutError):
        return ProbeStage.CONNECT
    if data is None or data.get("t") != p.T_CHALLENGE or _nonce(data) is None:
        return ProbeStage.CHALLENGE
    return None


class VordrClient:
    """Keeps the connection to the hub and hands every message on."""

    def __init__(
        self,
        hass: HomeAssistant,
        url: str,
        ca_pem: str,
        name: str,
        key: Ed25519PrivateKey,
        version: str,
        on_message: Callable[[dict[str, Any]], None],
        on_connection: Callable[[Connection, str], None],
    ) -> None:
        self.hass = hass
        self._url = url
        self._ca_pem = ca_pem
        self._name = name
        self._key = key
        self._version = version
        self._on_message = on_message
        self._on_connection = on_connection
        self._ws: aiohttp.ClientWebSocketResponse | None = None
        self._approved = False
        self._pending: dict[str, asyncio.Future[dict[str, Any]]] = {}
        self.fingerprint = p.fingerprint(p.public_key(key))

    async def run(self) -> None:
        """Keep connecting until cancelled."""
        backoff = 1.0
        while True:
            outcome = await self._session()
            if outcome == Connection.REJECTED:
                delay = REJECTED_RETRY
            elif outcome == Connection.APPROVED:
                backoff, delay = 1.0, 1.0  # it worked: reconnect at once, start backoff afresh
            else:
                delay, backoff = backoff, min(backoff * 2, BACKOFF_MAX)
            await asyncio.sleep(delay)

    async def _connect(self) -> aiohttp.ClientWebSocketResponse | None:
        ctx = await ssl_context(self.hass, self._ca_pem)
        try:
            return await async_get_clientsession(self.hass).ws_connect(self._url, ssl=ctx, max_msg_size=p.MAX_MESSAGE)
        except (aiohttp.ClientError, OSError, TimeoutError) as err:
            _LOGGER.debug("hub %s not reachable: %s", self._url, err)
            return None

    async def _login(self, ws: aiohttp.ClientWebSocketResponse) -> Status | None:
        """Answer the challenge; None when the other side is no hub speaking this protocol."""
        data = await _read(ws, p.HELLO_TIMEOUT)
        nonce = _nonce(data) if data and data.get("t") == p.T_CHALLENGE else None
        if nonce is None:
            return None
        await ws.send_json(p.hello(nonce, self._name, self._key, self._version))
        data = await _read(ws, p.HELLO_TIMEOUT)
        if data is None or data.get("t") != p.T_STATUS:
            return None
        return Status(str(data.get("state", "")), str(data.get("reason", "")), str(data.get("fingerprint", "")))

    async def _session(self) -> Connection:
        """One connection from login to its end; returns how far it got."""
        self._on_connection(Connection.CONNECTING, "")
        ws = await self._connect()
        if ws is None:
            self._on_connection(Connection.UNREACHABLE, "")
            return Connection.UNREACHABLE
        got = Connection.UNREACHABLE
        heartbeat: asyncio.Task[None] | None = None
        try:
            status = await self._login(ws)
            if status is None:
                self._on_connection(Connection.UNREACHABLE, "no_hub")
                return Connection.UNREACHABLE
            self._ws = ws
            heartbeat = asyncio.create_task(self._heartbeat(ws))
            got = self._status(status)
            while got != Connection.REJECTED:
                data = await _read(ws, p.IDLE_TIMEOUT)
                if data is None:
                    break
                got = max(got, self._dispatch(data), key=_rank)
        except (aiohttp.ClientError, OSError, TimeoutError) as err:
            _LOGGER.debug("connection to %s ended: %s", self._url, err)
        finally:
            if heartbeat:
                heartbeat.cancel()
            self._ws, self._approved = None, False
            for fut in self._pending.values():
                if not fut.done():
                    fut.set_result({"code": CODE_HUB_NOT_CONNECTED})
            await ws.close()
        if got != Connection.REJECTED:
            self._on_connection(Connection.CONNECTING, "")
        return got

    def _status(self, status: Status) -> Connection:
        if status.state == p.STATE_APPROVED:
            self._approved = True
            self._on_connection(Connection.APPROVED, "")
            return Connection.APPROVED
        if status.state == p.STATE_PENDING:
            self._on_connection(Connection.PENDING_APPROVAL, status.fingerprint)
            return Connection.PENDING_APPROVAL
        self._approved = False
        self._on_connection(Connection.REJECTED, status.reason)
        return Connection.REJECTED

    def _dispatch(self, data: dict[str, Any]) -> Connection:
        t = data["t"]
        if t == p.T_STATUS:
            return self._status(
                Status(str(data.get("state", "")), str(data.get("reason", "")), str(data.get("fingerprint", "")))
            )
        if t == p.T_COMMAND_RESULT:
            fut = self._pending.get(str(data.get("request_id", "")))
            if fut and not fut.done():
                fut.set_result(data)
        elif t != p.T_HEARTBEAT:
            self._on_message(data)
        return Connection.APPROVED if self._approved else Connection.PENDING_APPROVAL

    async def _heartbeat(self, ws: aiohttp.ClientWebSocketResponse) -> None:
        while not ws.closed:
            await asyncio.sleep(p.HEARTBEAT_INTERVAL)
            try:
                await ws.send_json({"t": p.T_HEARTBEAT})
            except (aiohttp.ClientError, ConnectionError):
                return

    async def command(self, node: str, command: str, reason: str = "") -> dict[str, Any]:
        """Ask the hub for a command; the answer carries a code (ok, a refusal of the hub, or one of ours). A reason
        asks for it outside the host's window (ADR-0087)."""
        ws = self._ws
        if ws is None or not self._approved:
            return {"code": CODE_HUB_NOT_CONNECTED}
        rid = secrets.token_hex(8)
        fut: asyncio.Future[dict[str, Any]] = asyncio.get_running_loop().create_future()
        self._pending[rid] = fut
        msg = {"t": p.T_COMMAND, "request_id": rid, "node": node, "command": command}
        if reason:
            msg["reason"] = reason
        try:
            await ws.send_json(msg)
            return await asyncio.wait_for(fut, COMMAND_TIMEOUT)
        except TimeoutError:
            return {"code": CODE_NO_ANSWER}
        except (aiohttp.ClientError, ConnectionError):
            return {"code": CODE_HUB_NOT_CONNECTED}
        finally:
            self._pending.pop(rid, None)

    async def wait_for_approval(self, timeout: float) -> Status | None:
        """For the config flow: log in and wait until the operator decides. None: no hub reached."""
        ws = await self._connect()
        if ws is None:
            return None
        heartbeat: asyncio.Task[None] | None = None
        try:
            status = await self._login(ws)
            if status is None or status.state != p.STATE_PENDING:
                return status
            heartbeat = asyncio.create_task(self._heartbeat(ws))
            async with asyncio.timeout(timeout):
                while True:
                    data = await _read(ws, p.IDLE_TIMEOUT)
                    if data is None:
                        return None
                    if data["t"] == p.T_STATUS and data.get("state") != p.STATE_PENDING:
                        return Status(
                            str(data.get("state")), str(data.get("reason", "")), str(data.get("fingerprint", ""))
                        )
        except TimeoutError:  # before OSError, of which it is a subclass: the time ran out, still undecided
            return Status(p.STATE_PENDING, "", self.fingerprint)
        except (aiohttp.ClientError, OSError):
            return None
        finally:
            if heartbeat:
                heartbeat.cancel()
            await ws.close()


def _rank(c: Connection) -> int:
    """How far a session got: approved beats pending beats unreachable; rejected ends it."""
    return {
        Connection.UNREACHABLE: 0,
        Connection.CONNECTING: 0,
        Connection.PENDING_APPROVAL: 1,
        Connection.APPROVED: 2,
        Connection.REJECTED: 3,
    }[c]
