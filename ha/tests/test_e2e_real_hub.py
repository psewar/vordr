"""End to end with the real binaries: vordr-hub, a vordr-agent checking this machine, and the integration.

Proves that Go and Python agree on the protocol (hello signature, fingerprints, messages) and that a button in
Home Assistant ends as a signed job at the agent. Needs dist/vordr-hub-linux-amd64 and dist/vordr-agent-linux-amd64
(./build.sh); skipped without them.
"""

from __future__ import annotations

import json
from pathlib import Path
import socket
import subprocess
import time
from typing import Any

from homeassistant.core import HomeAssistant
import pytest
from pytest_homeassistant_custom_component.common import MockConfigEntry
import pytest_socket

from custom_components.vordr import protocol as p
from custom_components.vordr.const import CONF_CA_PEM, CONF_NAME, CONF_PRIVATE_KEY, CONF_URL, DOMAIN

from .conftest import self_signed, wait_until

DIST = Path(__file__).resolve().parents[2] / "dist"
HUB_BIN = DIST / "vordr-hub-linux-amd64"
AGENT_BIN = DIST / "vordr-agent-linux-amd64"

pytestmark = pytest.mark.skipif(
    not (HUB_BIN.exists() and AGENT_BIN.exists()), reason="binaries missing - run ./build.sh first"
)


def free_port() -> int:
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def admin(sock: Path, req: dict[str, Any]) -> dict[str, Any]:
    """One request to the hub's admin socket, as the CLI sends it."""
    with socket.socket(socket.AF_UNIX) as s:
        s.settimeout(10)
        s.connect(str(sock))
        s.sendall(json.dumps(req).encode() + b"\n")
        data = b""
        while not data.endswith(b"\n"):
            chunk = s.recv(65536)
            if not chunk:
                break
            data += chunk
    return json.loads(data)


class Stack:
    def __init__(self, tmp: Path) -> None:
        self.tmp = tmp
        self.cert_pem, key_pem = self_signed()
        (tmp / "tls.crt").write_text(self.cert_pem)
        (tmp / "tls.key").write_text(key_pem)
        self.port = free_port()
        self.sock = tmp / "run" / "admin.sock"
        self.config = tmp / "hub.yaml"
        self.config.write_text(
            f"listen: 127.0.0.1:{self.port}\ntls_cert: {tmp / 'tls.crt'}\ntls_key: {tmp / 'tls.key'}\n"
            f"state_dir: {tmp / 'state'}\nadmin_socket: {self.sock}\nmax_pending: 5\n"
        )
        self.procs: list[subprocess.Popen[bytes]] = []

    def start(self) -> None:
        self.procs.append(subprocess.Popen([HUB_BIN, "run", "--config", self.config], stderr=subprocess.DEVNULL))
        deadline = time.monotonic() + 10
        while not self.sock.exists():
            if time.monotonic() > deadline:
                raise TimeoutError("hub did not start")
            time.sleep(0.05)
        key = subprocess.run([HUB_BIN, "key", "--config", self.config], capture_output=True, text=True, check=True)
        pub = key.stdout.split()[1]  # "public_key <b64>\nfingerprint <hex>"
        agent = self.tmp / "agent.yaml"
        agent.write_text(
            f"node: e2e-host\nstate_dir: {self.tmp / 'agent'}\nhub:\n  url: wss://127.0.0.1:{self.port}/v1/tunnel\n"
            f"  public_key: {pub}\n  ca_file: {self.tmp / 'tls.crt'}\n"
        )
        self.procs.append(subprocess.Popen([AGENT_BIN, "run", "--config", agent], stderr=subprocess.DEVNULL))
        deadline = time.monotonic() + 20
        while True:
            nodes = admin(self.sock, {"cmd": "list"}).get("nodes") or []
            if nodes:
                break
            if time.monotonic() > deadline:
                raise TimeoutError("agent did not register")
            time.sleep(0.1)
        resp = admin(self.sock, {"cmd": "approve", "node": "e2e-host", "fingerprint": nodes[0]["fingerprint"]})
        assert resp["code"] == "ok", resp

    def stop(self) -> None:
        for proc in reversed(self.procs):
            proc.terminate()
            proc.wait(10)


@pytest.fixture
def stack(tmp_path: Path, socket_enabled: None) -> Stack:
    # The hub's admin socket is a Unix socket; the HA test harness allows only 127.0.0.1 by default.
    pytest_socket.socket_allow_hosts(["127.0.0.1"], allow_unix_socket=True)
    s = Stack(tmp_path)
    s.start()
    yield s
    s.stop()


async def test_real_hub_and_agent(hass: HomeAssistant, stack: Stack) -> None:
    key = p.new_private_key()
    fp = p.fingerprint(p.public_key(p.load_private_key(key)))
    entry = MockConfigEntry(
        domain=DOMAIN,
        title="Vördr (e2e)",
        unique_id=f"127.0.0.1:{stack.port}",
        data={
            CONF_URL: f"wss://127.0.0.1:{stack.port}/v1/ha",
            CONF_NAME: "e2e-ha",
            CONF_CA_PEM: stack.cert_pem,
            CONF_PRIVATE_KEY: key,
        },
    )
    entry.add_to_hass(hass)
    assert await hass.config_entries.async_setup(entry.entry_id)
    await wait_until(lambda: hass.states.get("sensor.vordr_hub_connection").state == "pending_approval", 10)

    # The hub lists the client with the fingerprint Python computed; the CLI admits it.
    clients = await hass.async_add_executor_job(admin, stack.sock, {"cmd": "clients"})
    assert [(c["name"], c["fingerprint"], c["state"]) for c in clients["clients"]] == [
        ("e2e-ha", fp, "pending_approval")
    ]
    cli = await hass.async_add_executor_job(
        lambda: subprocess.run(
            [HUB_BIN, "approve-client", "e2e-ha", "--fingerprint", fp, "--config", stack.config],
            capture_output=True,
            text=True,
        )
    )
    assert cli.returncode == 0, cli.stdout + cli.stderr

    await wait_until(lambda: entry.runtime_data.connected, 10)
    await wait_until(lambda: hass.states.get("update.vordr_e2e_host_system") is not None, 30)
    await hass.async_block_till_done()
    assert hass.states.get("binary_sensor.vordr_e2e_host_online").state == "on"
    upd = hass.states.get("update.vordr_e2e_host_system")
    assert upd.attributes["installed_version"]  # the OS of this machine, as the agent reported it
    # The hub sends counts and limits, the integration words them (HA's test language: en).
    latest = upd.attributes["latest_version"]
    assert latest == upd.attributes["installed_version"] or latest.endswith(" security)"), latest
    assert entry.runtime_data.data.hub["limits"] == {
        "offline_after_minutes": 10,
        "stale_after_days": 7,
        "reboot_pending_after_days": 7,
        "no_report_after_hours": 26,
    }
    assert hass.states.get("button.vordr_e2e_host_check") is not None
    assert hass.states.get("button.vordr_e2e_host_reboot") is None  # not announced, not allowed

    # Button -> command -> signed job -> agent -> result in the host's view. The agent may refuse a second check
    # within minutes (rate limit) - that is a result as well; what counts is that this job came back.
    await hass.services.async_call("button", "press", {"entity_id": "button.vordr_e2e_host_check"}, blocking=True)
    await wait_until(lambda: hass.states.get("sensor.vordr_e2e_host_last_run").attributes.get("verb") == "check", 30)
    run = hass.states.get("sensor.vordr_e2e_host_last_run")
    await wait_until(
        lambda: hass.states.get("sensor.vordr_e2e_host_last_run").state in ("ok", "rejected_rate_limited"), 60
    )
    assert run.attributes["job_id"]

    # Revoked at the hub: the integration shows it as a fact and raises a repair.
    await hass.async_add_executor_job(admin, stack.sock, {"cmd": "reject-client", "client": "e2e-ha"})
    await wait_until(lambda: hass.states.get("sensor.vordr_hub_connection").state == "rejected", 10)
    assert hass.states.get("update.vordr_e2e_host_system").state == "unavailable"
    await hass.config_entries.async_unload(entry.entry_id)
