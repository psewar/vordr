"""Fixtures: a fake hub speaking /v1/ha over TLS on 127.0.0.1, and a config entry pointing at it."""

from __future__ import annotations

import asyncio
import base64
import datetime as dt
import hashlib
import ipaddress
import json
import os
import ssl
import threading
from typing import Any

from aiohttp import WSMsgType, web
from cryptography import x509
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import ec
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PublicKey
from cryptography.x509.oid import NameOID
import pytest
from pytest_homeassistant_custom_component.common import MockConfigEntry

from custom_components.vordr import protocol as p
from custom_components.vordr.const import CONF_CA_PEM, CONF_NAME, CONF_PRIVATE_KEY, CONF_URL, DOMAIN


@pytest.fixture(autouse=True)
def auto_enable_custom_integrations(enable_custom_integrations: None) -> None:
    """Load custom_components/vordr in every test."""


def self_signed() -> tuple[str, str]:
    """Certificate and key (PEM) for 127.0.0.1."""
    key = ec.generate_private_key(ec.SECP256R1())
    name = x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, "vordr-test")])
    now = dt.datetime.now(dt.UTC)
    cert = (
        x509.CertificateBuilder()
        .subject_name(name)
        .issuer_name(name)
        .public_key(key.public_key())
        .serial_number(x509.random_serial_number())
        .not_valid_before(now - dt.timedelta(minutes=5))
        .not_valid_after(now + dt.timedelta(days=1))
        .add_extension(
            x509.SubjectAlternativeName([x509.IPAddress(ipaddress.ip_address("127.0.0.1")), x509.DNSName("localhost")]),
            critical=False,
        )
        .add_extension(x509.BasicConstraints(ca=True, path_length=None), critical=True)
        .sign(key, hashes.SHA256())
    )
    return (
        cert.public_bytes(serialization.Encoding.PEM).decode(),
        key.private_bytes(
            serialization.Encoding.PEM, serialization.PrivateFormat.PKCS8, serialization.NoEncryption()
        ).decode(),
    )


def host_view(node: str, **over: Any) -> dict[str, Any]:
    """A HostView as the hub sends it: reported, 2 pending, 1 security, only check offered."""
    view: dict[str, Any] = {
        "node": node,
        "device": {"name": f"Vördr {node}", "model": "TestOS 1 · lxc", "agent_version": "1.0"},
        "online": True,
        "problem": None,
        "pending": 2,
        "security": 1,
        "system": {
            "installed_version": "TestOS 1",
            "packages": [
                {"name": "a", "from": "1", "to": "2", "security": True},
                {"name": "b", "from": "1", "to": "2"},
            ],
        },
        "reboot": {"required": None, "checked": []},
        "last_report": "2026-10-29T00:00:00Z",
        "last_run": None,
        "capabilities": {
            "check": True,
            "install": False,
            "install_security": False,
            "reboot": False,
            "agent_update": False,
        },
    }
    view.update(over)
    return view


class FakeHub:
    """Enough of vordr-hub's /v1/ha for the integration: login with a real signature check, status, snapshot,
    pushes and command results, all driven by the test."""

    def __init__(self) -> None:
        self.cert_pem, self._key_pem = self_signed()
        self.clients: dict[str, dict[str, str]] = {}  # name -> {"fp", "state"}
        self.auto_approve = False
        self.protocol = p.PROTOCOL
        self.hub: dict[str, Any] = {
            "version": "test",
            "protocol": p.PROTOCOL,
            "hosts_online": 1,
            "waiting": [],
            "limits": {
                "offline_after_minutes": 10,
                "stale_after_days": 7,
                "reboot_pending_after_days": 7,
                "no_report_after_hours": 26,
            },
        }
        self.hosts: dict[str, dict[str, Any]] = {}
        self.commands: list[dict[str, Any]] = []
        self.command_code = "ok"
        self.conns: list[tuple[str, web.WebSocketResponse]] = []
        self.port = 0
        self._runner: web.AppRunner | None = None
        # Its own loop in its own thread, like a real hub: what goes wrong on the hub side (e.g. a TLS handshake the
        # client aborts) stays there instead of landing in Home Assistant's loop.
        self._loop = asyncio.new_event_loop()
        self._thread = threading.Thread(target=self._loop.run_forever, name="fake-vordr-hub", daemon=True)

    @property
    def url(self) -> str:
        return f"wss://127.0.0.1:{self.port}{p.PATH}"

    def start(self) -> None:
        self._thread.start()
        asyncio.run_coroutine_threadsafe(self._start(), self._loop).result(10)

    def stop(self) -> None:
        if self._thread.is_alive():
            asyncio.run_coroutine_threadsafe(self._stop(), self._loop).result(10)
            self._loop.call_soon_threadsafe(self._loop.stop)
            self._thread.join(10)
        self._loop.close()

    async def _on_hub(self, coro: Any) -> Any:
        """Run a coroutine on the hub's loop and wait for it from the test's loop."""
        return await asyncio.wrap_future(asyncio.run_coroutine_threadsafe(coro, self._loop))

    async def approve(self, name: str) -> None:
        await self._on_hub(self._approve(name))

    async def push_host(self, view: dict[str, Any]) -> None:
        await self._on_hub(self._push_host(view))

    async def remove_host(self, node: str) -> None:
        await self._on_hub(self._remove_host(node))

    async def shut_down(self) -> None:
        """Stop serving (connections end), as when the hub goes down."""
        await self._on_hub(self._stop())

    async def _start(self, path: str = p.PATH) -> None:
        app = web.Application()
        app.router.add_get(path, self._handle)
        self._runner = web.AppRunner(app)
        await self._runner.setup()
        ctx = ssl.create_default_context(ssl.Purpose.CLIENT_AUTH)
        cert_file = os.path.join(os.environ.get("TMPDIR", "/tmp"), f"vordr-fake-{id(self)}.pem")
        with open(cert_file, "w", encoding="utf-8") as f:
            f.write(self.cert_pem + self._key_pem)
        ctx.load_cert_chain(cert_file)
        os.unlink(cert_file)
        site = web.TCPSite(self._runner, "127.0.0.1", 0, ssl_context=ctx)
        await site.start()
        self.port = site._server.sockets[0].getsockname()[1]

    async def _stop(self) -> None:
        for _, ws in list(self.conns):
            await ws.close()
        if self._runner:
            await self._runner.cleanup()
            self._runner = None
        await asyncio.sleep(0.1)  # let the closed transports detach before the loop goes

    async def _handle(self, request: web.Request) -> web.WebSocketResponse:
        ws = web.WebSocketResponse()
        await ws.prepare(request)
        nonce = os.urandom(p.NONCE_SIZE)
        await ws.send_json({"t": "challenge", "nonce": base64.b64encode(nonce).decode()})
        hello = await ws.receive_json(timeout=5)
        pub = base64.b64decode(hello["public_key"])
        name = hello["name"]
        signed = p.HELLO_CONTEXT + nonce + b"\x00" + name.encode() + b"\x00" + pub
        try:
            Ed25519PublicKey.from_public_bytes(pub).verify(base64.b64decode(hello["signature"]), signed)
        except Exception:
            await ws.send_json({"t": "status", "state": "rejected", "reason": "bad_hello"})
            await ws.close()
            return ws
        if hello.get("role") != "ha" or hello.get("protocol") != self.protocol:
            reason = "bad_hello" if hello.get("role") != "ha" else "protocol_mismatch"
            await ws.send_json({"t": "status", "state": "rejected", "reason": reason})
            await ws.close()
            return ws
        fp = hashlib.sha256(pub).hexdigest()
        client = self.clients.setdefault(
            name, {"fp": fp, "state": "approved" if self.auto_approve else "pending_approval"}
        )
        if client["fp"] != fp:
            await ws.send_json({"t": "status", "state": "rejected", "reason": "key_mismatch"})
            await ws.close()
            return ws
        await ws.send_json({"t": "status", "state": client["state"], "fingerprint": fp})
        if client["state"] == "rejected":
            await ws.close()
            return ws
        self.conns.append((name, ws))
        if client["state"] == "approved":
            await self._snapshot(ws)
        try:
            async for msg in ws:
                if msg.type != WSMsgType.TEXT:
                    break
                data = json.loads(msg.data)
                if data["t"] == "heartbeat":
                    await ws.send_json({"t": "heartbeat"})
                elif data["t"] == "command":
                    self.commands.append(data)
                    code = self.command_code if self.clients[name]["state"] == "approved" else "client_not_approved"
                    await ws.send_json(
                        {
                            "t": "command_result",
                            "request_id": data["request_id"],
                            "code": code,
                            "job_id": "job1" if code == "ok" else "",
                        }
                    )
        finally:
            self.conns.remove((name, ws))
        return ws

    async def _snapshot(self, ws: web.WebSocketResponse) -> None:
        await ws.send_json({"t": "snapshot", "hub": self.hub, "hosts": list(self.hosts.values())})

    async def _send_approved(self, msg: dict[str, Any]) -> None:
        for name, ws in list(self.conns):
            if self.clients[name]["state"] == "approved":
                await ws.send_json(msg)

    async def _approve(self, name: str) -> None:
        self.clients[name]["state"] = "approved"
        for n, ws in list(self.conns):
            if n == name:
                await ws.send_json({"t": "status", "state": "approved", "fingerprint": self.clients[name]["fp"]})
                await self._snapshot(ws)

    async def _push_host(self, view: dict[str, Any]) -> None:
        self.hosts[view["node"]] = view
        await self._send_approved({"t": "host", "host": view})

    async def _remove_host(self, node: str) -> None:
        self.hosts.pop(node, None)
        await self._send_approved({"t": "host_removed", "node": node})


@pytest.fixture
def fake_hub(socket_enabled: None) -> FakeHub:
    hub = FakeHub()
    hub.start()
    yield hub
    hub.stop()


@pytest.fixture
def entry(fake_hub: FakeHub) -> MockConfigEntry:
    """A config entry of an already approved client."""
    fake_hub.auto_approve = True
    return MockConfigEntry(
        domain=DOMAIN,
        title="Vördr (127.0.0.1)",
        unique_id=f"127.0.0.1:{fake_hub.port}",
        data={
            CONF_URL: fake_hub.url,
            CONF_NAME: "homeassistant",
            CONF_CA_PEM: fake_hub.cert_pem,
            CONF_PRIVATE_KEY: p.new_private_key(),
        },
    )


async def wait_until(cond: Any, timeout: float = 5.0) -> None:
    """Let the event loop run until cond() holds."""
    async with asyncio.timeout(timeout):
        while not cond():
            await asyncio.sleep(0.02)
