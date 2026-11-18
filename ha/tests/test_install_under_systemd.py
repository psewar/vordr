"""The install script of "Add host" (ADR-0161) under the real systemd-run, not the fake host's stand-in.

systemd expands a service's command line before the shell sees it, and the fake host only imitates that
(systemd_run_standin.py). Here the command Home Assistant sends runs as it is, locally instead of over SSH, against the
fake hub's HTTPS download: the download, the size and hash check, the hub's CA and the agent's start, with a stand-in
for the agent binary that only checks its arguments. Nothing is installed.

Where it runs, checked before and chosen explicitly (ADR-0190):
* as root through `sudo -k -n`, in the system's systemd, when VORDR_TEST_SUDO=1 asks for it (the public checks set it;
  a person's machine runs nothing as root unasked). Asked for, a missing precondition fails the tests instead of
  skipping them: a green run must have run them;
* else in the user's own systemd manager (`systemd-run --user`), which expands a command line the same way;
* else skipped, with the reason.
"""

from __future__ import annotations

import base64
import hashlib
import os
from pathlib import Path
import secrets
import shlex
import shutil
import subprocess
import sys

from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
import pytest

from custom_components.vordr import agent_install as ai

from .conftest import FakeHub, make_release

FILE = "vordr-agent-linux-amd64"
NODE = "probe-1"
FINGERPRINT = "cd" * 32
HUB_KEY = base64.b64encode(b"h" * 32).decode()
OPERATOR_KEY = base64.b64encode(b"o" * 32).decode()
# A manager that starts services. degraded: some other unit failed; starting: the boot is not done yet. Both run a
# transient service.
UP = ("running", "degraded", "starting")
SUDO_ASKED = os.environ.get("VORDR_TEST_SUDO") == "1"


def _answer(*argv: str) -> str:
    """What a check command prints."""
    return subprocess.run(argv, capture_output=True, text=True, check=False, timeout=30).stdout.strip()


def _where() -> tuple[str, str]:
    """Where the install can run: ("system" | "user", "") or ("", why not)."""
    if not sys.platform.startswith("linux"):
        return "", "not Linux"
    for tool in ("systemd-run", "systemctl", "sh", "curl", "sha256sum"):
        if shutil.which(tool) is None:
            return "", f"no {tool}"
    if SUDO_ASKED:
        # the same check the install's probe makes: -k, so that cached credentials do not count
        check = shlex.split(ai.SUDO_NOPASSWD_CHECK)
        if shutil.which("sudo") is None or subprocess.run(check, check=False).returncode != 0:
            return "", "VORDR_TEST_SUDO=1, but sudo -k -n does not run"
        if _answer("systemctl", "is-system-running") not in UP:
            return "", "VORDR_TEST_SUDO=1, but the system's systemd does not run services"
        return "system", ""
    if _answer("systemctl", "--user", "is-system-running") in UP:
        return "user", ""
    return "", "no systemd manager for a transient service; VORDR_TEST_SUDO=1 uses the system's through sudo -n"


WHERE, WHY_NOT = _where()


@pytest.fixture(autouse=True)
def runs_where_asked() -> None:
    """Without a manager to run on: skipped, unless VORDR_TEST_SUDO=1 asked for the system's; then the test fails."""
    if not WHERE:
        if SUDO_ASKED:
            pytest.fail(WHY_NOT)
        pytest.skip(WHY_NOT)


def agent_stand_in(port: int, ca_file: Path) -> bytes:
    """The "agent binary": answers as vordr-agent install does, if it got exactly the arguments the install owes it."""
    want = " ".join(
        [
            "install",
            "--hub",
            f"wss://127.0.0.1:{port}/v1/tunnel",
            "--hub-key",
            HUB_KEY,
            "--operator-key",
            OPERATOR_KEY,
            "--node",
            NODE,
            "--ca-file",
            str(ca_file),
        ]
    )
    return (
        "#!/bin/sh\n"
        f'[ "$*" = "{want}" ] || {{ echo "result=failed code=wrong_arguments"; exit 1; }}\n'
        f'echo "result=installed node={NODE} fingerprint={FINGERPRINT}"\n'
    ).encode()


def install(
    fake_hub: FakeHub, tmp_path: Path, monkeypatch: pytest.MonkeyPatch, signed: bytes, served: bytes
) -> ai.Outcome:
    """The install as Home Assistant sends it, for a release that signs `signed` while the hub serves `served`. The
    CA's folder is the test's own, so that a CA written as root can be removed again."""
    ca_file = tmp_path / "etc-vordr" / "hub-ca.pem"
    ca_file.parent.mkdir()
    monkeypatch.setattr(ai, "CA_PATH", str(ca_file))
    fake_hub.release = make_release(Ed25519PrivateKey.generate(), {("linux", "amd64"): served})
    token = secrets.token_hex(24)
    fake_hub.tokens[token] = {"file": FILE, "used": False}
    release = ai.Release("1.4.0", FILE, hashlib.sha256(signed).hexdigest(), len(signed), "", HUB_KEY)
    hub = f"wss://127.0.0.1:{fake_hub.port}/v1/tunnel"
    sudo = ai.Sudo.NOPASSWD if WHERE == "system" else ai.Sudo.NONE
    command = ai.install_command(release, hub, NODE, ai.OperatorKeys(OPERATOR_KEY, ""), True, sudo)
    env = dict(os.environ)
    if WHERE == "user":
        shim = tmp_path / "bin"
        shim.mkdir()
        (shim / "systemd-run").write_text(f'#!/bin/sh\nexec {shutil.which("systemd-run")} --user "$@"\n')
        (shim / "systemd-run").chmod(0o755)
        env["PATH"] = f"{shim}{os.pathsep}{env['PATH']}"
    url = f"https://127.0.0.1:{fake_hub.port}/v1/agent-download/{FILE}?token={token}"
    run = subprocess.run(
        ["sh", "-c", command],
        input=ai.install_input(url, fake_hub.cert_pem),
        capture_output=True,
        text=True,
        env=env,
        check=False,
        timeout=180,
    )
    assert fake_hub.tokens[token]["used"], run.stdout + run.stderr
    return ai.outcome(run.stdout, "1.4.0")


def test_the_install_runs_under_systemd(fake_hub: FakeHub, tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    """The script reaches the shell as written: the hash it computes matches, the CA lands where the agent is told,
    and the agent gets exactly its arguments."""
    agent = agent_stand_in(fake_hub.port, tmp_path / "etc-vordr" / "hub-ca.pem")
    got = install(fake_hub, tmp_path, monkeypatch, agent, agent)
    assert (got.code, got.names.get("node"), got.names.get("fingerprint")) == ("agent_installed", NODE, FINGERPRINT)
    assert (tmp_path / "etc-vordr" / "hub-ca.pem").read_text(encoding="utf-8") == fake_hub.cert_pem


def test_a_binary_that_differs_is_not_run_under_systemd(
    fake_hub: FakeHub, tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """Served bytes of the signed size but another content: hash_mismatch, nothing run, no CA written."""
    agent = agent_stand_in(fake_hub.port, tmp_path / "etc-vordr" / "hub-ca.pem")
    other = agent.replace(b"installed", b"INSTALLED")
    got = install(fake_hub, tmp_path, monkeypatch, agent, other)
    assert got.code == "hash_mismatch"
    assert not (tmp_path / "etc-vordr" / "hub-ca.pem").exists()
