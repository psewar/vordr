#!/usr/bin/env python3
"""Keep docs/adr/README.md in step with the ADR files (ADR-0072).

Every `docs/adr/NNNN-*.md` needs front matter with `status`, `date` and `decision-makers` (and may have `scope: site`,
ADR-0133) and a `# Title`; the numbers
are consecutive from 0001 without gaps; `superseded by ADR-NNNN` must point at an existing ADR that links back. The
index table in `docs/adr/README.md` is generated from the files: `--write` rewrites it, the default only checks that
it matches (build.sh).

Copied from the ADR check of an earlier project and adapted (ADR-0012): file location, `decision-makers`, back-links.

Run: python3 docs/adr/check_index.py           (check; exit 1 on a finding)
     python3 docs/adr/check_index.py --write   (regenerate the index table)
"""
import re
import sys
from pathlib import Path

ADR_DIR = Path(__file__).resolve().parent
ROOT = ADR_DIR.parent.parent
INDEX = ADR_DIR / "README.md"
STATUSES = ("proposed", "accepted", "rejected", "deprecated")
BEGIN, END = "<!-- adr-index:begin -->", "<!-- adr-index:end -->"


def read_adr(path):
    """(number, title, status, date), the text, and a list of findings for one ADR."""
    text = path.read_text(encoding="utf-8")
    findings = []
    fm = re.match(r"^---\n(.*?)\n---\n", text, re.S)
    meta = dict(re.findall(r"^(\w[\w-]*):\s*(.+)$", fm.group(1), re.M)) if fm else {}
    if not fm:
        findings.append(f"{path.name}: no front matter")
    status, date = meta.get("status", "").strip(), meta.get("date", "").strip()
    if not (status in STATUSES or re.fullmatch(r"superseded by ADR-\d{4}", status)):
        findings.append(f"{path.name}: status {status!r} - one of {', '.join(STATUSES)} or 'superseded by ADR-NNNN'")
    if not re.fullmatch(r"\d{4}-\d{2}-\d{2}", date):
        findings.append(f"{path.name}: date {date!r} - YYYY-MM-DD expected")
    if not meta.get("decision-makers", "").strip():
        findings.append(f"{path.name}: no decision-makers")
    if "scope" in meta and meta["scope"].strip() != "site":
        findings.append(f"{path.name}: scope {meta['scope'].strip()!r} - only 'site' (ADR-0133) or no scope line")
    title = re.search(r"^# (.+)$", text, re.M)
    if not title:
        findings.append(f"{path.name}: no '# Title' line")
    return (int(path.name[:4]), title.group(1).strip() if title else "", status, date), text, findings


def table(rows):
    lines = ["| ADR | Title | Status | Date |", "|---|---|---|---|"]
    for (num, title, status, date), path, _ in rows:
        lines.append(f"| [{num:04d}]({path.name}) | {title} | {status} | {date} |")
    return "\n".join(lines)


def main():
    files = sorted(p for p in ADR_DIR.glob("[0-9][0-9][0-9][0-9]-*.md"))
    findings, rows = [], []
    for p in files:
        row, text, f = read_adr(p)
        findings += f
        rows.append((row, p, text))
    numbers = [r[0][0] for r in rows]
    if numbers != list(range(1, len(numbers) + 1)):
        findings.append(f"numbers not consecutive from 0001: {numbers}")
    texts = {r[0][0]: r[2] for r in rows}
    for (num, _, status, _), _, _ in rows:
        m = re.fullmatch(r"superseded by ADR-(\d{4})", status)
        if not m:
            continue
        new = int(m.group(1))
        if new not in texts:
            findings.append(f"ADR-{num:04d}: superseded by ADR-{new:04d}, which does not exist")
        elif f"ADR-{num:04d}" not in texts[new]:
            findings.append(f"ADR-{new:04d} supersedes ADR-{num:04d} but does not mention it")
    index = INDEX.read_text(encoding="utf-8") if INDEX.exists() else ""
    if BEGIN not in index or END not in index:
        findings.append(f"{INDEX.relative_to(ROOT)}: markers {BEGIN} / {END} missing")
    else:
        head, rest = index.split(BEGIN, 1)
        _, tail = rest.split(END, 1)
        wanted = f"{head}{BEGIN}\n{table(rows)}\n{END}{tail}"
        if "--write" in sys.argv:
            INDEX.write_text(wanted, encoding="utf-8", newline="\n")   # LF on every platform (.gitattributes eol=lf)
            print(f"{INDEX.relative_to(ROOT)}: {len(rows)} ADRs written")
        elif wanted != index:
            findings.append(f"{INDEX.relative_to(ROOT)} is out of date - run: python3 docs/adr/check_index.py --write")
    for f in findings:
        print("FINDING", f)
    if not findings and "--write" not in sys.argv:
        print(f"ADR index ok: {len(rows)} ADRs")
    sys.exit(1 if findings else 0)


if __name__ == "__main__":
    main()
