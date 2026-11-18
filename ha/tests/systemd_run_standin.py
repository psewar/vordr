"""systemd-run as the fake SSH host plays it (fake_ssh_host.py): the options the install uses, then the command, its
arguments expanded as systemd expands the command line of a service, and run.

systemd replaces ${NAME}, ${NAME:-default} and ${NAME:+alternative} anywhere in an argument with the service's variable,
with nothing when the service has no such variable (also for ${sum%% *}, which is no name at all), turns "$$" into "$",
and splits an argument that is exactly $NAME at whitespace. systemd-run hands its command over for exactly that: before
systemd 254 always, since then unless --expand-environment=no, which older versions refuse. A shell script passed as the
argument of sh -c therefore loses every ${...} before the shell sees it.
"""

from __future__ import annotations

import os
import re
import sys

OPTIONS = ("--pipe", "--wait", "--collect", "--quiet")
BARE = re.compile(r"\$([A-Za-z_][A-Za-z0-9_]*)")
WITH_DEFAULT = re.compile(r"([A-Za-z_][A-Za-z0-9_]*):([-+])(.*)", re.S)


def _value(inner: str, env: dict[str, str]) -> str:
    """What ${inner} becomes."""
    m = WITH_DEFAULT.fullmatch(inner)
    if m is None:
        return env.get(inner, "")
    value = env.get(m.group(1), "")
    if m.group(2) == "-":
        return value or m.group(3)
    return m.group(3) if value else ""


def expand_word(word: str, env: dict[str, str]) -> str:
    """One argument with its ${...} replaced and "$$" made "$"."""
    out: list[str] = []
    i = 0
    while i < len(word):
        end = word.find("}", i + 2) if word.startswith("${", i) else -1
        if word.startswith("$$", i):
            out.append("$")
            i += 2
        elif end >= 0:
            out.append(_value(word[i + 2 : end], env))
            i = end + 1
        else:
            out.append(word[i])
            i += 1
    return "".join(out)


def expand(args: list[str], env: dict[str, str]) -> list[str]:
    """The command line as the service runs it."""
    out: list[str] = []
    for arg in args:
        bare = BARE.fullmatch(arg)
        if bare is not None:
            out.extend(env.get(bare.group(1), "").split())
        else:
            out.append(expand_word(arg, env))
    return out


def main() -> int:
    args = sys.argv[1:]
    seen: list[str] = []
    while args:
        if args[0] == "--":
            args.pop(0)
            break
        if args[0] not in OPTIONS and not args[0].startswith("--description="):
            print(f"systemd-run stand-in: unknown option {args[0]}", file=sys.stderr)
            return 1
        seen.append(args.pop(0))
    with open(os.path.join(os.environ["SIM"], "systemd-run-args"), "w", encoding="utf-8") as f:
        f.write("".join(f"{option}\n" for option in seen))
    if not args:
        print("systemd-run stand-in: no command", file=sys.stderr)
        return 1
    # The service's own environment, not the caller's: systemd's PATH. The command then runs in the sandbox's.
    command = expand(args, {"PATH": os.environ.get("PATH", "")})
    os.execvp(command[0], command)
    return 1  # execvp does not return


if __name__ == "__main__":
    sys.exit(main())
