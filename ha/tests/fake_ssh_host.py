"""A Linux host behind SSH for the tests of the install over SSH (ADR-0161).

An asyncssh server on 127.0.0.1 with password and key login. Each command runs with /bin/sh in a sandbox that plays
the host: PATH holds only the sandbox's bin/, with stand-ins for uname, id, sudo, systemd-run and apt-get and the real
tools the integration's scripts use (sh, cat, mktemp, sha256sum, base64, timeout, curl, wget, ...). TMPDIR, systemd's
folder and the hub CA's place are in the sandbox, so that a test sees what is left behind. The integration's scripts
run for real; only what needs root or another system is played. Its own loop in its own thread, like FakeHub.
"""

from __future__ import annotations

import asyncio
from pathlib import Path
import shlex
import shutil
import sys
import threading

import asyncssh

# The real tools the scripts call; each one a test leaves out is missing on the "host".
TOOLS = (
    "sh",
    "true",
    "cat",
    "rm",
    "mkdir",
    "mktemp",
    "chmod",
    "wc",
    "tr",
    "head",
    "tail",
    "sha256sum",
    "base64",
    "cp",
    "dirname",
    "rmdir",
    "sleep",
    "timeout",
    "curl",
    "wget",
)

STANDINS = {
    # The system and the processor as the test set them.
    "uname": """#!/bin/sh
case $1 in -s) cat "$SIM/os" ;; -m) cat "$SIM/machine" ;; *) cat "$SIM/os" ;; esac
""",
    # The user: root once sudo let it through.
    "id": """#!/bin/sh
[ "$1" = -u ] || exit 1
if [ -n "${SIM_UID:-}" ]; then echo "$SIM_UID"; else cat "$SIM/uid"; fi
""",
    # sudo with exactly the options the integration uses. sudo-denied: the user may not use it. sudo-password: the
    # password it reads from the first line of stdin (without the file it asks for none and reads nothing).
    "sudo": """#!/bin/sh
nonint= fromstdin=
while [ $# -gt 0 ]; do
  case $1 in
    -k) shift ;;
    -n) nonint=1; shift ;;
    -S) fromstdin=1; shift ;;
    -p) shift 2 ;;
    --) shift; break ;;
    -*) echo "sudo stand-in: unknown option $1" >&2; exit 1 ;;
    *) break ;;
  esac
done
[ ! -e "$SIM/sudo-denied" ] || exit 1
if [ -e "$SIM/sudo-password" ]; then
  [ -z "$nonint" ] && [ -n "$fromstdin" ] || exit 1
  IFS= read -r pw || exit 1
  [ "$pw" = "$(cat "$SIM/sudo-password")" ] || exit 1
fi
SIM_UID=0
export SIM_UID
exec "$@"
""",
    # systemd-run with exactly the options the install uses: records them, expands the command's arguments as systemd
    # does (${...} and $$, systemd_run_standin.py) and runs it. A real systemd-run starts it as a transient service
    # outside the login session, which the sandbox cannot show.
    "systemd-run": f"""#!/bin/sh
exec {shlex.quote(sys.executable)} -I {shlex.quote(str(Path(__file__).with_name("systemd_run_standin.py")))} "$@"
""",
    "apt-get": "#!/bin/sh\nexit 0\n",
}

# The "agent binary" the fake hub hands out: records how it was called and as whom, waits while agent-hold exists,
# then answers as the test set it up (agent-out, agent-exit).
AGENT = b"""#!/bin/sh
# vordr-agent stand-in for the tests of the install over SSH
printf '%s\\n' "$@" > "$SIM/agent-args"
printf '%s\\n' "${SIM_UID:-}" > "$SIM/agent-uid"
while [ -e "$SIM/agent-hold" ]; do sleep 0.05; done
cat "$SIM/agent-out"
exit "$(cat "$SIM/agent-exit")"
"""

FINGERPRINT = "ab" * 32


def installed(node: str = "web-01", fingerprint: str = FINGERPRINT) -> str:
    """What vordr-agent install prints when it installed: the approval command, then its last line."""
    return (
        f"at the hub, after comparing: vordr-hub approve {node} --fingerprint {fingerprint}\n"
        f"result=installed node={node} fingerprint={fingerprint}\n"
    )


class _Server(asyncssh.SSHServer):
    def __init__(self, host: FakeHost) -> None:
        self._host = host

    def begin_auth(self, username: str) -> bool:
        self._host.logins.append((username, "begin"))
        return True

    def password_auth_supported(self) -> bool:
        return True

    def validate_password(self, username: str, password: str) -> bool:
        self._host.logins.append((username, "password"))
        return self._host.users.get(username) == password

    def public_key_auth_supported(self) -> bool:
        return True

    def validate_public_key(self, username: str, key: asyncssh.SSHKey) -> bool:
        self._host.logins.append((username, "key"))
        return any(k.public_data == key.public_data for k in self._host.keys.get(username, []))


class FakeHost:
    """The host. Tests set users, keys and the files that steer the stand-ins, and read what ran and what is left."""

    def __init__(self, root: Path) -> None:
        self.root = root
        self.bin, self.tmp, self.home = root / "bin", root / "tmp", root / "home"
        self.systemd = root / "run" / "systemd" / "system"
        self.ca_path = root / "etc" / "vordr" / "hub-ca.pem"
        for d in (self.bin, self.tmp, self.home, self.systemd, root / "etc"):
            d.mkdir(parents=True)
        for tool in TOOLS:
            if (real := shutil.which(tool)) is not None:
                (self.bin / tool).symlink_to(real)
        for name, text in STANDINS.items():
            (self.bin / name).write_text(text, encoding="utf-8")
            (self.bin / name).chmod(0o755)
        self.users: dict[str, str] = {}
        self.keys: dict[str, list[asyncssh.SSHKey]] = {}
        self.host_key = asyncssh.generate_private_key("ssh-ed25519")
        self.other_host_keys: list[asyncssh.SSHKey] = []
        self.logins: list[tuple[str, str]] = []
        self.commands: list[str] = []
        self.stdins: list[str] = []
        self.port = 0
        self.set(os="Linux", machine="x86_64", uid="1000", agent_exit="0", agent_out=installed())
        self._acceptor: asyncssh.SSHAcceptor | None = None
        self._running: set[asyncio.subprocess.Process] = set()  # commands that have not ended yet
        self._handlers = 0  # _process calls that have not returned yet
        self._loop = asyncio.new_event_loop()
        self._thread = threading.Thread(target=self._loop.run_forever, name="fake-ssh-host", daemon=True)

    # --- steering

    def set(self, **files: str) -> None:
        """Files of the sandbox that steer the stand-ins (agent_out -> agent-out)."""
        for name, text in files.items():
            (self.root / name.replace("_", "-")).write_text(text, encoding="utf-8")

    def unset(self, *names: str) -> None:
        for name in names:
            (self.root / name.replace("_", "-")).unlink(missing_ok=True)

    def without(self, *tools: str) -> None:
        """Tools or stand-ins this host does not have."""
        for tool in tools:
            (self.bin / tool).unlink(missing_ok=True)

    def has(self, tool: str) -> bool:
        return (self.bin / tool).exists()

    def record_argv(self) -> None:
        """Every tool and stand-in writes its arguments to a file of its own in argv/ before it runs: what any user of
        the host reads in ps or /proc/<pid>/cmdline. Shell builtins (printf, read) start no process and show nowhere.
        One small file per process, as the download runs under a file size limit."""
        real = self.root / "real-bin"
        real.mkdir()
        (self.root / "argv").mkdir()
        for tool in sorted(self.bin.iterdir()):
            target = real / tool.name
            tool.rename(target)
            tool.write_text(
                f'#!/bin/sh\nprintf \'%s\\n\' "{tool.name} $*" > "$SIM/argv/$$"\nexec "{target}" "$@"\n',
                encoding="utf-8",
            )
            tool.chmod(0o755)

    def argv(self) -> str:
        """The arguments every process had since record_argv, one process after the other."""
        return "\n".join(p.read_text(encoding="utf-8") for p in sorted((self.root / "argv").iterdir()))

    def replace(self, tool: str, script: str) -> None:
        """A tool or stand-in of this host that does what the test says instead."""
        (self.bin / tool).unlink(missing_ok=True)
        (self.bin / tool).write_text(script, encoding="utf-8")
        (self.bin / tool).chmod(0o755)

    def file(self, name: str) -> str | None:
        path = self.root / name
        return path.read_text(encoding="utf-8") if path.exists() else None

    def leftovers(self) -> list[str]:
        """What is left in the temporary folder: nothing, after every install."""
        return sorted(p.name for p in self.tmp.iterdir())

    def new_host_key(self) -> None:
        """The server presents another host key from now on (as when someone sits between)."""
        self.host_key = asyncssh.generate_private_key("ssh-ed25519")
        asyncio.run_coroutine_threadsafe(self._restart(), self._loop).result(10)

    def present(self, *keys: asyncssh.SSHKey) -> None:
        """The server holds these host keys from now on, in this order; the client's list decides which one it shows.
        host_key is the last one."""
        self.host_key, self.other_host_keys = keys[-1], list(keys[:-1])
        asyncio.run_coroutine_threadsafe(self._restart(), self._loop).result(10)

    # --- the server

    def start(self) -> None:
        self._thread.start()
        asyncio.run_coroutine_threadsafe(self._listen(0), self._loop).result(10)

    def stop(self) -> None:
        if self._thread.is_alive():
            asyncio.run_coroutine_threadsafe(self._end_commands(), self._loop).result(10)
            asyncio.run_coroutine_threadsafe(self._close(), self._loop).result(10)
            self._loop.call_soon_threadsafe(self._loop.stop)
            self._thread.join(10)
        self._loop.close()

    async def _end_commands(self) -> None:
        """A command still running when the test ends (e.g. one the client gave up on after its timeout) is killed,
        and its handler returns before the loop stops."""
        for proc in list(self._running):
            proc.kill()
        async with asyncio.timeout(5):
            while self._handlers:
                await asyncio.sleep(0.01)

    async def _listen(self, port: int) -> None:
        self._acceptor = await asyncssh.listen(
            "127.0.0.1",
            port,
            server_factory=lambda: _Server(self),
            server_host_keys=[*self.other_host_keys, self.host_key],
            process_factory=self._process,
            encoding=None,
        )
        self.port = self._acceptor.get_port()

    async def _restart(self) -> None:
        port = self.port
        await self._close()
        await self._listen(port)

    async def _close(self) -> None:
        if self._acceptor is not None:
            self._acceptor.close()
            await self._acceptor.wait_closed()
            self._acceptor = None

    async def _process(self, process: asyncssh.SSHServerProcess) -> None:
        """One command: run it with /bin/sh in the sandbox, with what the client wrote as its stdin."""
        self._handlers += 1
        try:
            await self._run(process)
        finally:
            self._handlers -= 1

    async def _run(self, process: asyncssh.SSHServerProcess) -> None:
        command = process.command or ""
        stdin = await process.stdin.read()
        self.commands.append(command)
        self.stdins.append(stdin.decode(errors="replace"))
        env = {"PATH": str(self.bin), "TMPDIR": str(self.tmp), "SIM": str(self.root), "HOME": str(self.home)}
        proc = await asyncio.create_subprocess_exec(
            "/bin/sh",
            "-c",
            command,
            stdin=asyncio.subprocess.PIPE,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
            env={**env, "LC_ALL": "C"},
            cwd=self.home,
        )
        self._running.add(proc)
        try:
            out, err = await proc.communicate(stdin)
        finally:
            self._running.discard(proc)
        try:
            process.stdout.write(out)
            process.stderr.write(err)
            process.exit(max(proc.returncode or 0, 0))
        except (asyncssh.Error, OSError):  # the client went away meanwhile (its dialog closed): nobody to answer
            return
