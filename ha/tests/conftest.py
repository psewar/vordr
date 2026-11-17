"""Fixtures: a fake hub speaking /v1/ha over TLS on 127.0.0.1, and a config entry pointing at it."""

from __future__ import annotations

import asyncio
import base64
from dataclasses import dataclass
import datetime as dt
import hashlib
import ipaddress
import json
import os
from pathlib import Path
import secrets
import ssl
import threading
from typing import Any

from aiohttp import WSMsgType, web
from cryptography import x509
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import ec
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey, Ed25519PublicKey
from cryptography.x509.oid import NameOID
import pytest
from pytest_homeassistant_custom_component.common import MockConfigEntry

from custom_components.vordr import protocol as p
from custom_components.vordr.const import CONF_CA_PEM, CONF_NAME, CONF_PRIVATE_KEY, CONF_URL, DOMAIN

from .fake_ssh_host import FakeHost


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


@dataclass
class FakeRelease:
    """An agent release as vordr-release signs it: manifest.json, its signature, the binaries by file. served is what
    the hub hands out for each file (another content tests the host's check), hub_key the hub's job key."""

    version: str
    manifest: bytes
    sig: bytes
    served: dict[str, bytes]
    hub_key: bytes


MANIFEST_CONTEXT = b"vordr/agent-manifest/v1\x00"  # internal/protocol/manifest.go


def make_release(
    signer: Ed25519PrivateKey, binaries: dict[tuple[str, str], bytes], version: str = "1.4.0"
) -> FakeRelease:
    """A release signed by signer, one binary per platform, named as build.sh names them."""
    entries = [
        {
            "os": os_name,
            "arch": arch,
            "file": f"vordr-agent-{os_name}-{arch}",
            "sha256": hashlib.sha256(data).hexdigest(),
            "size": len(data),
        }
        for (os_name, arch), data in binaries.items()
    ]
    manifest = json.dumps({"version": version, "issued": "2030-01-01T00:00:00Z", "binaries": entries}).encode()
    return FakeRelease(
        version=version,
        manifest=manifest,
        sig=signer.sign(MANIFEST_CONTEXT + manifest),
        served={e["file"]: binaries[(e["os"], e["arch"])] for e in entries},
        hub_key=Ed25519PrivateKey.generate().public_key().public_bytes_raw(),
    )


class FakeHub:
    """Enough of vordr-hub's /v1/ha for the integration: login with a real signature check, status, snapshot,
    pushes, command results and the agent release for a new host, all driven by the test."""

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
        # What the hub accepts beyond the base protocol, in every status (agent_download, ADR-0161).
        self.features: list[str] = []
        # The agent release for a new host: the requests, the code to answer, the release, the single-use downloads.
        self.download_requests: list[dict[str, Any]] = []
        self.download_code = "ok"
        self.release: FakeRelease | None = None
        self.tokens: dict[str, dict[str, Any]] = {}  # token -> {"file", "used"}
        self.download_refusal: tuple[int, str] | None = None  # (status, code) instead of the binary
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

    async def push_hub(self, **over: Any) -> None:
        """Change the hub's own view (e.g. the waiting hosts) and push it, as the hub does on a change."""
        self.hub.update(over)
        await self._on_hub(self._send_approved({"t": "hub", "hub": self.hub}))

    async def drop_connections(self) -> None:
        """End every connection while the hub keeps serving, as a restart of the hub does: clients come back and get a
        new snapshot."""
        await self._on_hub(self._drop_connections())

    async def shut_down(self) -> None:
        """Stop serving (connections end), as when the hub goes down."""
        await self._on_hub(self._stop())

    async def _start(self, path: str = p.PATH) -> None:
        app = web.Application()
        app.router.add_get(path, self._handle)
        app.router.add_get("/v1/agent-download/{file}", self._download)
        # No access log: it would name the download tokens, which the tests look for in every log line (the real hub
        # logs a download_id instead).
        self._runner = web.AppRunner(app, access_log=None)
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
        if not self._runner:
            return
        servers = [site._server for site in self._runner.sites if site._server is not None]
        await self._runner.cleanup()
        self._runner = None
        # Connections the app never saw stay open after the cleanup: a TLS handshake the client gave up on, or a TLS
        # shutdown that waits for the client's close_notify. Left behind, they are collected after the loop stopped
        # and detach from a server that is already closed (asyncio raises in __del__). So end them, and wait until
        # every connection has detached (wait_closed does that since Python 3.12) before the loop goes.
        for server in servers:
            server.abort_clients()
            async with asyncio.timeout(5):
                await server.wait_closed()

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
        await ws.send_json({"t": "status", "state": client["state"], "fingerprint": fp, "features": self.features})
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
                elif data["t"] == "agent_download":
                    self.download_requests.append(data)
                    await ws.send_json(self._agent_download(data))
        finally:
            self.conns.remove((name, ws))
        return ws

    def _agent_download(self, data: dict[str, Any]) -> dict[str, Any]:
        """The hub's agent_download_result (design/ha-integration.md 5b): with ok, the release as the operator signed
        it and one single-use path per binary of the platform."""
        answer: dict[str, Any] = {"t": "agent_download_result", "request_id": data["request_id"]}
        if self.download_code != "ok" or self.release is None:
            return {**answer, "code": self.download_code if self.download_code != "ok" else "no_release"}
        r = self.release
        downloads = []
        for b in json.loads(r.manifest)["binaries"]:
            if (b["os"], b["arch"]) == (data.get("os"), data.get("arch")):
                token = secrets.token_hex(24)
                self.tokens[token] = {"file": b["file"], "used": False}
                downloads.append({"file": b["file"], "path": f"/v1/agent-download/{b['file']}?token={token}"})
        expires = dt.datetime.now(dt.UTC) + dt.timedelta(minutes=15)
        return {
            **answer,
            "code": "ok",
            "version": r.version,
            "manifest": base64.b64encode(r.manifest).decode(),
            "manifest_sig": base64.b64encode(r.sig).decode(),
            "hub_key": base64.b64encode(r.hub_key).decode(),
            "expires_at": expires.isoformat().replace("+00:00", "Z"),
            "downloads": downloads,
        }

    async def _download(self, request: web.Request) -> web.Response:
        """GET /v1/agent-download/<file>?token=: the binary once, or a fixed status with the code as the body."""
        if self.download_refusal is not None:
            status, code = self.download_refusal
            return web.Response(status=status, text=code)
        entry = self.tokens.get(request.query.get("token", ""))
        if entry is None:
            return web.Response(status=403, text="unknown_token")
        if entry["used"]:
            return web.Response(status=409, text="token_used")
        if entry["file"] != request.match_info["file"] or self.release is None:
            return web.Response(status=404, text="wrong_file")
        entry["used"] = True
        return web.Response(body=self.release.served[entry["file"]], content_type="application/octet-stream")

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
                status = {"t": "status", "state": "approved", "fingerprint": self.clients[name]["fp"]}
                await ws.send_json({**status, "features": self.features})
                await self._snapshot(ws)

    async def _push_host(self, view: dict[str, Any]) -> None:
        self.hosts[view["node"]] = view
        await self._send_approved({"t": "host", "host": view})

    async def _remove_host(self, node: str) -> None:
        self.hosts.pop(node, None)
        await self._send_approved({"t": "host_removed", "node": node})

    async def _drop_connections(self) -> None:
        for _, ws in list(self.conns):
            await ws.close()


@pytest.fixture
def fake_hub(socket_enabled: None) -> FakeHub:
    hub = FakeHub()
    hub.start()
    yield hub
    hub.stop()


@pytest.fixture
def fake_host(tmp_path: Path, socket_enabled: None) -> FakeHost:
    """A Linux host behind SSH, played in a sandbox (fake_ssh_host.py)."""
    host = FakeHost(tmp_path / "host")
    host.start()
    yield host
    host.stop()


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


async def wait_until(cond: Any, timeout: float = 5.0, describe: Any = None) -> None:
    """Let the event loop run until cond() holds. On timeout, describe() says what was there instead."""
    loop = asyncio.get_running_loop()
    deadline = loop.time() + timeout
    while not cond():
        if loop.time() > deadline:
            raise TimeoutError(describe() if describe else f"not within {timeout} s")
        await asyncio.sleep(0.02)
