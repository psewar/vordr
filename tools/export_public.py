#!/usr/bin/env python3
"""Builds the tree that is published (ADR-0100, ADR-0133, ADR-0140) from a commit of the private repository.

    python3 tools/export_public.py <commit> <out-dir> [--no-build "<reason>"]

1. Exports the commit with git archive into <out-dir>, which must not exist yet.
2. Replaces every ADR with `scope: site` in its front matter by a stub named NNNN-site-decision.md: same number,
   status and date, a neutral title, and the supersede links the index check needs. Links to it are rewritten.
3. Moves the GitHub workflows from tools/github-actions/ to .github/workflows/ (ADR-0137).
4. Regenerates the ADR index and runs the checks on the result: docs/adr/check_index.py, tools/check_neutral.py (with
   the site's term list) and, unless --no-build names a reason, ./build.sh.
Exactly what the checks saw is what may be published: copy <out-dir> into the public repository as one commit.

Exit 0 ready, 1 a check failed (the tree stays for inspection), 2 refused (nothing exported).
"""
import argparse
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
FRONT = re.compile(r"^---\n(.*?)\n---\n", re.S)


def front(text):
    m = FRONT.match(text)
    return dict(re.findall(r"^(\w[\w-]*):\s*(.+)$", m.group(1), re.M)) if m else {}


def refuse(msg):
    print(f"export_public: refused: {msg}", file=sys.stderr)
    sys.exit(2)


def export(commit, out):
    git = shutil.which("git")
    if not git:
        refuse("git is missing")
    if subprocess.run([git, "-C", str(ROOT), "rev-parse", "--verify", "--quiet", f"{commit}^{{commit}}"],
                      capture_output=True).returncode != 0:
        refuse(f"{commit} is no commit of {ROOT}")
    if out.exists():
        refuse(f"{out} exists; export into a new directory")
    with tempfile.TemporaryDirectory() as tmp:
        archive = Path(tmp) / "tree.tar"
        subprocess.run([git, "-C", str(ROOT), "archive", "--format=tar", "-o", str(archive), commit], check=True)
        out.mkdir(parents=True)
        with tarfile.open(archive) as t:
            t.extractall(out, filter="data")


def stub_site_adrs(out):
    """Replace each `scope: site` ADR by a stub; returns {old file name: new file name}."""
    adr_dir = out / "docs" / "adr"
    files = sorted(adr_dir.glob("[0-9][0-9][0-9][0-9]-*.md"))
    metas = {p: front(p.read_text(encoding="utf-8")) for p in files}
    renamed = {}
    for p, meta in metas.items():
        if meta.get("scope", "").strip() != "site":
            continue
        num = p.name[:4]
        superseded = [q.name[:4] for q, m in metas.items()
                      if m.get("status", "").strip() == f"superseded by ADR-{num}"]
        lines = ["---", f"status: {meta.get('status', '').strip()}", f"date: {meta.get('date', '').strip()}",
                 "decision-makers: the operator", "scope: site", "---", "",
                 "# A decision about the operation of one site", "",
                 "This decision is about how one installation is run, not about how Vördr is built. Its text stays with",
                 "that site ([ADR-0133](0133-site-decisions-stay-with-the-site.md)); the number is kept so that links",
                 "and the order of the log stay intact."]
        if superseded:
            lines += ["", "Supersedes " + ", ".join(f"ADR-{n}" for n in superseded) + "."]
        new = adr_dir / f"{num}-site-decision.md"
        p.unlink()
        new.write_text("\n".join(lines) + "\n", encoding="utf-8", newline="\n")
        renamed[p.name] = new.name
    if renamed:
        for f in out.rglob("*"):
            if not f.is_file() or f.suffix not in (".md", ".go", ".py", ".yaml", ".json", ".sh", ".txt"):
                continue
            text = f.read_text(encoding="utf-8", errors="strict")
            changed = text
            for old, new in renamed.items():
                changed = changed.replace(old, new)
            if changed != text:
                f.write_text(changed, encoding="utf-8", newline="\n")
    return renamed


def place_workflows(out):
    """The GitHub workflows live in tools/github-actions/ here, because the operator's own Git hosting would run
    anything in .github/workflows (ADR-0137); the published tree has them where GitHub looks."""
    src = out / "tools" / "github-actions"
    if not src.is_dir():
        return 0
    dst = out / ".github" / "workflows"
    dst.mkdir(parents=True, exist_ok=True)
    moved = 0
    for f in sorted(src.glob("*.yml")):
        f.rename(dst / f.name)
        moved += 1
    if not any(src.iterdir()):
        src.rmdir()
    return moved


def run(cmd, cwd):
    print(f"export_public: {' '.join(cmd)}")
    return subprocess.run(cmd, cwd=cwd).returncode == 0


def main():
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    ap.add_argument("commit")
    ap.add_argument("out_dir")
    ap.add_argument("--no-build", metavar="REASON", help="skip ./build.sh, with a stated reason")
    a = ap.parse_args()
    if a.no_build is not None and not a.no_build.strip():
        refuse("--no-build needs a reason")
    out = Path(a.out_dir).resolve()
    export(a.commit, out)
    renamed = stub_site_adrs(out)
    print(f"export_public: {len(renamed)} site decision(s) replaced by stubs")
    print(f"export_public: {place_workflows(out)} workflow(s) placed in .github/workflows")
    py = sys.executable or "python3"
    ok = run([py, "docs/adr/check_index.py", "--write"], out) and run([py, "docs/adr/check_index.py"], out)
    ok = ok and run([py, "tools/check_neutral.py"], out)
    if ok and a.no_build is None:
        ok = run(["./build.sh"], out)
    elif ok:
        print(f"export_public: build skipped: {a.no_build}")
    if not ok:
        print(f"export_public: a check failed; {out} stays for inspection and must not be published", file=sys.stderr)
        return 1
    print(f"export_public: {out} is ready. Check the commit message with: python3 tools/check_neutral.py --text <file>")
    return 0


if __name__ == "__main__":
    sys.exit(main())
