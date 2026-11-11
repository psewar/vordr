#!/usr/bin/env python3
r"""Keeps the repository site-neutral (ADR-0099): nothing in it may point at the infrastructure of whoever runs it.

    python3 tools/check_neutral.py                 every tracked or new (not ignored) file of the repository
    python3 tools/check_neutral.py --text FILE...  any text, e.g. a commit message or a PR description

Two kinds of rules:
- Generic rules, here in the code: private and carrier-grade NAT addresses (examples use 192.0.2.0/24, 198.51.100.0/24,
  203.0.113.0/24 or 2001:db8::/32), host names in URLs and server_name that are neither placeholders (*.example,
  *.invalid, *.test, example.org) nor a public site in PUBLIC, and WSL paths (/mnt/<drive>/, \\wsl.localhost\, \\wsl$\).
  Since ADR-0134 also the wording of an operations log: observations of runs ("live probe", "seen on the host", "on
  the canary", "in the pilot"), dated observations ("since 2026-10-30", "am 30.10."), private sources ("morning
  report", "night order", "(private)"), the operator's home ("home network", "in the house"), and operations facts in
  the "decision-makers:" line of an ADR. ADR front matter dates, "Backfilled on … decided on …" lines and data in
  quotes (fixture timestamps) stay allowed.
- Site terms: host names, people, domains, paths outside the repository. They are the site's own and stay outside the
  repository, because a list in here would give them away, and hashes of short names are found again by trying a list
  of candidates. Path: $VORDR_SITE_TERMS, else ~/.config/vordr/site-terms.txt. One term per line, matched as a whole
  word without regard to case; "re:<regex>" for a regular expression; "person:<name>" for a person, whose name may
  stand in the "decision-makers:" line of an ADR (authorship of a decision) and nowhere else. A line starting with "#"
  is a comment; " # " after a term starts its hint, which a hit prints, so the list can say what to do when a term is
  found (for example: remove the term once a driver for this product is built). Without a list the check refuses
  (exit 2) and says how to go on: create the list (it may be empty), or set VORDR_SITE_TERMS="none: <reason>" to run
  the generic rules only; the reason is printed with every run.

Not matched: the module path from go.mod (the publishing location), its account as "@account" (code owner) and in the
copyright line of LICENSE. Site terms only, no generic rules, for this file
and its tests (their rules and cases name what they look for) and for internal/osv/testdata (public OSV records kept
verbatim, with their reference links). Exit 0 clean, 1 hits (file:line and what matched), 2 no site list.
"""
import ipaddress
import os
import re
import shutil
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SELF = os.path.abspath(__file__)
SELF_TEST = os.path.join(os.path.dirname(SELF), "check_neutral_test.py")  # its cases are what the rules look for

# Public sites the repository may link to; a host matches itself and its subdomains.
PUBLIC = (
    "adr.github.io", "anthropic.com", "claude.com", "debian.org", "github.com", "github.io", "go.dev", "golang.org", "googleapis.com",
    "home-assistant.io", "it-connect.tech", "microsoft.com", "netgate.com", "npmjs.org", "osv.dev", "proxmox.com",
    "pypi.org", "python.org", "semver.org", "synology.com", "ubuntu.com", "w3.org",
)
PLACEHOLDER_SUFFIXES = (".example", ".invalid", ".test", ".localhost", "example.org", "example.com", "example.net")
SITE_NETS_V4 = [ipaddress.ip_network(n) for n in ("10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10")]
# IPv6 by prefix: unique local fc00::/7, link-local fe80::/10, global 2000::/3 except the documentation 2001:db8::/32.
SITE_V6 = re.compile(r"^(?:f[cd][0-9a-f]{0,2}|fe[89ab][0-9a-f]?|(?!2001:0?db8:)[23][0-9a-f]{3}):", re.IGNORECASE)

IPV4 = re.compile(r"(?<![\w.])(?:\d{1,3}\.){3}\d{1,3}(?![\w]|\.\d)")
IPV6 = re.compile(r"(?<![\w:.])[0-9a-fA-F]{1,4}:[0-9a-fA-F]{0,4}:[0-9a-fA-F:]*(?:/\d{1,3})?(?![\w:])")
URL_HOST = re.compile(r"\b(?:https?|wss?|ssh|mqtts?|tcp|ssl)://(?:[^@/\s\"'`<>]+@)?([A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)+)")
SERVER_NAME = re.compile(r"\bserver_name:\s*[\"']?([A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)+)")
WSL_MOUNT = re.compile(r"/mnt/[a-z]/")
WSL_UNC = re.compile(r"[\\/]{2}wsl(?:\.localhost|\$)[\\/]", re.IGNORECASE)

# ADR-0134: wording that turns a design text into the log of one installation.
OPERATIONS = re.compile(
    r"\blive[- ]probe\b|\bseen (?:on|at) (?:the|a|one) (?:host|canary|pc|pi|server|nas|firewall)\b"
    r"|\breal hosts?\b|\bfirst real\b|\bin the pilot\b|\bpilot (?:host|report|run)\b|\bmorning report\b"
    r"|\bnight order\b|\boperations reports?\b|\(private\)|\bhome network\b|\bin the house\b|\bat home\b(?!\s+assistant)"
    r"|\bim Pilot\b|\bMorgenbericht\b|\bim Haus\b|\bzu Hause\b|\bdaheim\b",
    re.IGNORECASE)
DATED = re.compile(r"\b(?:on|since|until|as of)\s+20\d\d-[01]\d-[0-3]\d\b|\b(?:am|seit|bis|vom)\s+[0-3]?\d\.[01]?\d\.",
                   re.IGNORECASE)
DATE_ALLOWED = re.compile(r"^(?:date|decision-makers):|Backfilled on|decided on", re.IGNORECASE)
QUOTED = re.compile(r"\"[^\"]*\"|`[^`]*`")
# In an ADR's "decision-makers:" line: who decided and when, not how it was rolled out.
AUTHORSHIP_FACTS = re.compile(r"\b(?:hosts?|since|rolled|rollout|live|night|morning|report(?:ed)?)\b(?!-)",
                              re.IGNORECASE)  # (?!-): design terms such as "report-only" stay


def operations(line, in_adr):
    """What the operations-log rules (ADR-0134) find in one line."""
    found = []
    if in_adr and line.startswith("decision-makers:"):
        m = AUTHORSHIP_FACTS.search(QUOTED.sub("", line))  # a quoted principle is no operations fact
        if m:
            found.append(f"operations fact {m.group()!r} in decision-makers (keep names, roles and the decision date)")
        return found
    plain = QUOTED.sub("", line)
    m = OPERATIONS.search(plain)
    if m:
        found.append(f"operations log {m.group()!r} (state the design or the check, not what happened at one site)")
    if not DATE_ALLOWED.search(line):
        m = DATED.search(plain)
        if m:
            found.append(f"dated observation {m.group()!r} (drop the date of a run; a release version may stay)")
    return found


def generic(line):
    """What the generic rules find in one line."""
    found = []
    for m in IPV4.finditer(line):
        parts = m.group().split(".")
        if any(len(p) > 1 and p[0] == "0" or int(p) > 255 for p in parts):
            continue  # leading zeros or parts above 255: a version string, no address
        if any(ipaddress.ip_address(m.group()) in n for n in SITE_NETS_V4):
            found.append(f"site address {m.group()}")
    for m in IPV6.finditer(line):
        if m.group().count(":") >= 2 and SITE_V6.match(m.group()):
            found.append(f"address {m.group()}")
    for rx in (URL_HOST, SERVER_NAME):
        for m in rx.finditer(line):
            host = m.group(1).lower().rstrip(".")
            if re.fullmatch(r"[\d.]+", host):
                continue  # an IPv4 literal: the address rule decides
            if host.endswith(PLACEHOLDER_SUFFIXES) or any(host == p or host.endswith("." + p) for p in PUBLIC):
                continue
            found.append(f"host name {host} (use a placeholder like host-a.example.org, or add a public site to PUBLIC)")
    if WSL_MOUNT.search(line):
        found.append("path outside the repository (/mnt/<drive>/)")
    if WSL_UNC.search(line):
        found.append("path outside the repository (\\\\wsl.localhost\\ or \\\\wsl$\\)")
    return found


def load_terms():
    """Site terms as (pattern, is_person, hint) and None, or None and the reason why the generic rules alone run.
    Exits 2 without a list."""
    setting = os.environ.get("VORDR_SITE_TERMS", "")
    if setting.lower().startswith("none:") and setting[5:].strip():
        return None, setting[5:].strip()
    path = os.path.expanduser(setting or "~/.config/vordr/site-terms.txt")
    if not os.path.isfile(path):
        sys.stderr.write(
            f"check_neutral: no site term list at {path}.\n"
            "  Next step: write the site's own host names, domains, people and paths there, one per line (an empty\n"
            "  file is fine), or set VORDR_SITE_TERMS to its path, or to \"none: <reason>\" for the generic rules only.\n")
        sys.exit(2)
    terms = []
    with open(path, encoding="utf-8") as f:
        for raw in f:
            line = raw.strip()
            if not line or line.startswith("#"):
                continue
            t, _, hint = line.partition(" # ")
            t, hint = t.strip(), hint.strip()
            person = t.startswith("person:")
            if person:
                t = t[len("person:"):].strip()
            if t.startswith("re:"):
                terms.append((re.compile(t[3:], re.IGNORECASE), person, hint))
            elif t:
                word = re.compile(r"(?<![A-Za-z0-9])" + re.escape(t) + r"(?![A-Za-z0-9])", re.IGNORECASE)
                terms.append((word, person, hint))
    return terms, None


def module_path():
    with open(os.path.join(ROOT, "go.mod"), encoding="utf-8") as f:
        for line in f:
            if line.startswith("module "):
                return line.split()[1]
    return None


def repo_files():
    """Tracked and new, not ignored files when git knows the tree (dist/ and other output stay out); else every file
    outside .git and dist."""
    git = shutil.which("git")
    if git and subprocess.run([git, "-C", ROOT, "rev-parse", "--is-inside-work-tree"], capture_output=True).returncode == 0:
        out = subprocess.run([git, "-C", ROOT, "ls-files", "-z", "--cached", "--others", "--exclude-standard"],
                             capture_output=True, check=True).stdout
        names = [p for p in out.decode("utf-8").split("\0") if p]
    else:
        names = []
        for d, subdirs, files in os.walk(ROOT):
            subdirs[:] = [s for s in subdirs if s not in (".git", "dist")]
            names += [os.path.relpath(os.path.join(d, f), ROOT) for f in files]
    return sorted(p for p in names if os.path.isfile(os.path.join(ROOT, p)))


def check(path, rel, terms, modpath):
    with open(path, "rb") as f:
        raw = f.read()
    if b"\0" in raw[:8192]:
        return []  # binary
    text = raw.decode("utf-8", errors="replace")
    in_adr = rel.replace("\\", "/").startswith("docs/adr/")
    own = os.path.abspath(path) in (SELF, SELF_TEST) or rel.replace("\\", "/").startswith("internal/osv/testdata/")
    # An ADR with "scope: site" is a decision about one installation; it is published only as a stub (ADR-0133), so
    # the operations-log rules do not apply to it. Site terms and the generic rules still do.
    site_adr = in_adr and re.match(r"^---\n(?:.*\n)*?scope:\s*site\s*\n(?:.*\n)*?---\n", text) is not None
    hits = []
    for no, line in enumerate(text.splitlines(), 1):
        found = [] if own else generic(line) + ([] if site_adr else operations(line, in_adr))
        authorship = in_adr and line.startswith("decision-makers:")
        plain = line.replace(modpath, "") if modpath else line
        if modpath and modpath.count("/") >= 2:
            account = modpath.split("/")[1]  # the account of the publishing location
            plain = plain.replace("@" + account, "")  # as code owner or mention (ADR-0136)
            if rel == "LICENSE" and line.startswith("Copyright"):
                plain = plain.replace(account, "")  # as copyright holder (ADR-0135)
        for pattern, person, hint in terms or []:
            m = pattern.search(plain)
            if m and not (person and authorship):
                found.append(f"site term {m.group()!r}" + (f": {hint}" if hint else ""))
        hits += [f"{rel}:{no}: {what}" for what in found]
    return hits


def main(argv):
    terms, why_generic_only = load_terms()
    modpath = module_path()
    if argv[:1] == ["--text"]:
        files = [(p, p) for p in argv[1:]]
    else:
        files = [(os.path.join(ROOT, r), r) for r in repo_files()]
    hits = [h for p, r in files for h in check(p, r, terms, modpath)]
    if why_generic_only:
        print(f"check_neutral: generic rules only, no site terms: {why_generic_only}")
    if hits:
        print("check_neutral: the repository must stay site-neutral (ADR-0099):", file=sys.stderr)
        print("\n".join("  " + h for h in hits), file=sys.stderr)
        return 1
    print(f"check_neutral: {len(files)} files clean")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
