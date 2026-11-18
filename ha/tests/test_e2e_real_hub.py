"""End to end with the real binaries: vordr-hub, a vordr-agent checking this machine, and the integration.

Proves that Go and Python agree on the protocol (hello signature, fingerprints, messages), that a button in
Home Assistant ends as a signed job at the agent, that the repair of a waiting host names the command the hub
takes, and that its fix approves the host where the hub allows it. Needs dist/vordr-hub-linux-amd64 and
dist/vordr-agent-linux-amd64
(./build.sh); skipped without them.
"""

from __future__ import annotations

import json
from pathlib import Path
import socket
import subprocess
import time
from typing import Any

from homeassistant.components.repairs import repairs_flow_manager
from homeassistant.core import HomeAssistant
from homeassistant.data_entry_flow import FlowResultType
from homeassistant.helpers import issue_registry as ir
from homeassistant.setup import async_setup_component
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
    def __init__(self, tmp: Path, hub_extra: str = "") -> None:
        self.tmp = tmp
        self.cert_pem, key_pem = self_signed()
        (tmp / "tls.crt").write_text(self.cert_pem)
        (tmp / "tls.key").write_text(key_pem)
        self.port = free_port()
        self.sock = tmp / "run" / "admin.sock"
        self.config = tmp / "hub.yaml"
        self.config.write_text(
            f"listen: 127.0.0.1:{self.port}\ntls_cert: {tmp / 'tls.crt'}\ntls_key: {tmp / 'tls.key'}\n"
            f"state_dir: {tmp / 'state'}\nadmin_socket: {self.sock}\nmax_pending: 5\n{hub_extra}"
        )
        self.procs: list[subprocess.Popen[bytes]] = []
        self.agent_config = tmp / "agent.yaml"

    def start(self, approve: bool = True) -> None:
        """Hub and agent; the agent is admitted at once unless approve is False, then it stays waiting."""
        self.procs.append(subprocess.Popen([HUB_BIN, "run", "--config", self.config], stderr=subprocess.DEVNULL))
        deadline = time.monotonic() + 10
        while not self.sock.exists():
            if time.monotonic() > deadline:
                raise TimeoutError("hub did not start")
            time.sleep(0.05)
        key = subprocess.run([HUB_BIN, "key", "--config", self.config], capture_output=True, text=True, check=True)
        pub = key.stdout.split()[1]  # "public_key <b64>\nfingerprint <hex>"
        agent = self.agent_config
        agent.write_text(
            f"node: e2e-host\nstate_dir: {self.tmp / 'agent'}\nhub:\n  url: wss://127.0.0.1:{self.port}/v1/tunnel\n"
            f"  public_key: {pub}\n  ca_file: {self.tmp / 'tls.crt'}\n"
        )
        self.procs.append(subprocess.Popen([AGENT_BIN, "run", "--config", agent], stderr=subprocess.DEVNULL))
        deadline = time.monotonic() + 20
        while True:
            # The document for programs (ADR-0171), as an operator's script reads it.
            status, doc = self.hub_json("list")
            assert status == 0 and doc["format"] == 1 and doc["code"] == "ok", doc
            if doc["nodes"]:
                break
            if time.monotonic() > deadline:
                raise TimeoutError("agent did not register")
            time.sleep(0.1)
        waiting = doc["nodes"][0]
        assert waiting["state"] == "pending_approval" and "view" not in waiting, waiting
        if approve:
            resp = admin(self.sock, {"cmd": "approve", "node": "e2e-host", "fingerprint": waiting["fingerprint"]})
            assert resp["code"] == "ok", resp

    def hub_json(self, *args: str) -> tuple[int, dict[str, Any]]:
        """vordr-hub <args> --json: the exit status and the one JSON document on stdout."""
        cli = subprocess.run(
            [HUB_BIN, *args, "--json", "--config", self.config], capture_output=True, text=True, check=False
        )
        return cli.returncode, json.loads(cli.stdout)

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


@pytest.fixture
def waiting_stack(tmp_path: Path, socket_enabled: None) -> Stack:
    """Like stack, but the agent stays waiting for approval."""
    pytest_socket.socket_allow_hosts(["127.0.0.1"], allow_unix_socket=True)
    s = Stack(tmp_path)
    s.start(approve=False)
    yield s
    s.stop()


@pytest.fixture
def approving_stack(tmp_path: Path, socket_enabled: None) -> Stack:
    """Like waiting_stack, on a hub that lets Home Assistant approve hosts (ha_approves_hosts, ADR-0192)."""
    pytest_socket.socket_allow_hosts(["127.0.0.1"], allow_unix_socket=True)
    s = Stack(tmp_path, "ha_approves_hosts: true\n")
    s.start(approve=False)
    yield s
    s.stop()


def entry_for(stack: Stack, key: str) -> MockConfigEntry:
    return MockConfigEntry(
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


async def test_waiting_host_repair_names_the_real_command(hass: HomeAssistant, waiting_stack: Stack) -> None:
    """ADR-0157 against the real hub and agent: the repair of the waiting host shows the agent's own fingerprint, as
    `vordr-agent identity` prints it on the host, and its command, run as it stands at the hub, admits the host. The
    repair then goes, and the host comes."""
    stack = waiting_stack
    key = p.new_private_key()
    entry = entry_for(stack, key)
    entry.add_to_hass(hass)
    assert await hass.config_entries.async_setup(entry.entry_id)
    await wait_until(lambda: hass.states.get("sensor.vordr_hub_connection").state == "pending_approval", 10)
    fp = p.fingerprint(p.public_key(p.load_private_key(key)))
    resp = await hass.async_add_executor_job(
        admin, stack.sock, {"cmd": "approve-client", "client": "e2e-ha", "fingerprint": fp}
    )
    assert resp["code"] == "ok", resp
    await wait_until(lambda: entry.runtime_data.connected, 10)

    def repair() -> ir.IssueEntry | None:
        return ir.async_get(hass).async_get_issue(DOMAIN, "waiting_e2e-host")

    await wait_until(lambda: repair() is not None, 10)
    names = repair().translation_placeholders
    assert names["node"] == "e2e-host" and names["os"] not in ("", "?")
    ident = await hass.async_add_executor_job(
        lambda: subprocess.run(
            [AGENT_BIN, "identity", "--config", stack.agent_config], capture_output=True, text=True, check=True
        )
    )
    assert f"fingerprint {names['fingerprint']}\n" in ident.stdout, ident.stdout
    argv = names["command"].split()
    assert argv[:2] == ["vordr-hub", "approve"], argv
    cli = await hass.async_add_executor_job(
        lambda: subprocess.run([HUB_BIN, *argv[1:], "--config", stack.config], capture_output=True, text=True)
    )
    assert cli.returncode == 0, cli.stdout + cli.stderr
    await wait_until(
        lambda: repair() is None and hass.states.get("binary_sensor.vordr_e2e_host_online") is not None, 30
    )
    await hass.config_entries.async_unload(entry.entry_id)


async def test_the_waiting_repair_approves_at_the_real_hub(hass: HomeAssistant, approving_stack: Stack) -> None:
    """ADR-0192 against the real hub and agent: with ha_approves_hosts, the hub names approve_host only once this
    client has role admin, and the waiting host's repair turns fixable without a change of the hub's view. Its fix
    refuses a fingerprint other than the one the hub saw, and approves the host with the one `vordr-agent identity`
    prints on the host. The repair then goes, the host comes, and the hub's event log names the client."""
    stack = approving_stack
    assert await async_setup_component(hass, "repairs", {})
    key = p.new_private_key()
    entry = entry_for(stack, key)
    entry.add_to_hass(hass)
    assert await hass.config_entries.async_setup(entry.entry_id)
    await wait_until(lambda: hass.states.get("sensor.vordr_hub_connection").state == "pending_approval", 10)
    fp = p.fingerprint(p.public_key(p.load_private_key(key)))
    resp = await hass.async_add_executor_job(
        admin, stack.sock, {"cmd": "approve-client", "client": "e2e-ha", "fingerprint": fp}
    )
    assert resp["code"] == "ok", resp
    await wait_until(lambda: entry.runtime_data.connected, 10)

    def repair() -> ir.IssueEntry | None:
        return ir.async_get(hass).async_get_issue(DOMAIN, "waiting_e2e-host")

    await wait_until(lambda: repair() is not None, 10)
    # Role operate, the default: the hub does not name the feature, the repair is not fixable.
    assert not repair().is_fixable and not entry.runtime_data.client.offers_host_approval
    resp = await hass.async_add_executor_job(
        admin, stack.sock, {"cmd": "client-role", "client": "e2e-ha", "role": "admin"}
    )
    assert resp["code"] == "ok", resp
    await wait_until(lambda: repair().is_fixable, 10)

    ident = await hass.async_add_executor_job(
        lambda: subprocess.run(
            [AGENT_BIN, "identity", "--config", stack.agent_config], capture_output=True, text=True, check=True
        )
    )
    printed = next(line.split()[1] for line in ident.stdout.splitlines() if line.startswith("fingerprint "))
    manager = repairs_flow_manager(hass)
    assert manager is not None
    flow = await manager.async_init(DOMAIN, data={"issue_id": "waiting_e2e-host"})
    assert flow["type"] is FlowResultType.FORM and flow["step_id"] == "confirm"
    result = await manager.async_configure(flow["flow_id"], {"fingerprint": "0" * 64})
    assert result["type"] is FlowResultType.FORM and result["errors"] == {"fingerprint": "fingerprint_differs"}
    result = await manager.async_configure(flow["flow_id"], {"fingerprint": printed.upper()})
    assert result["type"] is FlowResultType.CREATE_ENTRY, result
    await wait_until(
        lambda: repair() is None and hass.states.get("binary_sensor.vordr_e2e_host_online") is not None, 30
    )
    status, doc = await hass.async_add_executor_job(stack.hub_json, "show", "e2e-host")
    assert status == 0 and doc["nodes"][0]["state"] == "approved", doc
    events = await hass.async_add_executor_job(admin, stack.sock, {"cmd": "events", "events": {"event": "approve"}})
    assert [(e["node"], e["client"], e["code"]) for e in events["events"]] == [("e2e-host", "e2e-ha", "ok")], events
    await hass.config_entries.async_unload(entry.entry_id)


async def test_real_hub_and_agent(hass: HomeAssistant, stack: Stack) -> None:
    key = p.new_private_key()
    fp = p.fingerprint(p.public_key(p.load_private_key(key)))
    entry = entry_for(stack, key)
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

    # Button -> command -> signed job -> agent -> result in the host's view. What counts is that this job came back.
    # The agent may refuse it, and that is a result as well: within minutes of the first check (rate limit), or while
    # it still gathers the first check's package inventory, which takes a while where much software is installed
    # (busy: it checks the lock before the minimum interval).
    await hass.services.async_call("button", "press", {"entity_id": "button.vordr_e2e_host_check"}, blocking=True)
    await wait_until(lambda: hass.states.get("sensor.vordr_e2e_host_last_run").attributes.get("verb") == "check", 30)
    run = hass.states.get("sensor.vordr_e2e_host_last_run")

    def last_run() -> str:
        state = hass.states.get("sensor.vordr_e2e_host_last_run")
        return f"last run {state.state}: {dict(state.attributes)}"

    await wait_until(
        lambda: (
            hass.states.get("sensor.vordr_e2e_host_last_run").state in ("ok", "rejected_rate_limited", "rejected_busy")
        ),
        60,
        describe=last_run,
    )
    assert run.attributes["job_id"]

    # The hub's command line shows the host as Home Assistant got it; a host it does not know is a code, not text.
    status, doc = await hass.async_add_executor_job(stack.hub_json, "show", "e2e-host")
    host = doc["nodes"][0]
    assert status == 0 and host["state"] == "approved" and host["fingerprint"], doc
    assert host["view"]["last_run"]["job_id"] == run.attributes["job_id"], host["view"]
    status, doc = await hass.async_add_executor_job(stack.hub_json, "show", "no-such-host")
    assert (status, doc["code"], doc["nodes"]) == (1, "unknown_node", []), doc

    # Revoked at the hub: the integration shows it as a fact and raises a repair.
    await hass.async_add_executor_job(admin, stack.sock, {"cmd": "reject-client", "client": "e2e-ha"})
    await wait_until(lambda: hass.states.get("sensor.vordr_hub_connection").state == "rejected", 10)
    assert hass.states.get("update.vordr_e2e_host_system").state == "unavailable"
    await hass.config_entries.async_unload(entry.entry_id)
