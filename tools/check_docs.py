#!/usr/bin/env python3
"""Every document exists in English and German, and the two versions keep the same shape (ADR-0143).

    python3 tools/check_docs.py          check the repository (build.sh); exit 1 on a finding

The documents are found, not listed: every Markdown file of the repository except the ADRs (`docs/adr/`, English
only, ADR-0072), the instructions for contributors and coding agents (`AGENTS.md`, `CLAUDE.md`, English only) and
hidden or generated folders. The English version is `name.md`, the German one `name.de.md` beside it.

For each pair it checks:
- both exist, and each carries the language line under its title (`English · [Deutsch](name.de.md)` and
  `[English](name.md) · Deutsch`);
- the same outline: the same sequence of heading levels, the same number of fenced code blocks and of table rows;
- every relative link resolves to a file, and a link with an anchor to a heading of that file;
- the English version links English documents and the German version German ones, wherever a German version exists;
- the German version uses Swiss spelling: no "ß".

It cannot check that both say the same; the pull request does that by changing both (AGENTS.md).
"""
import os
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
ENGLISH_ONLY = {"AGENTS.md", "CLAUDE.md"}
SKIP_DIRS = {"dist", "node_modules", "__pycache__"}
LINK = re.compile(r"\]\(([^)\s]+)\)")


def documents(root):
    """All Markdown files that need both languages, as paths relative to root (POSIX style)."""
    found = []
    for folder, dirs, files in os.walk(root):
        dirs[:] = sorted(d for d in dirs if not d.startswith(".") and d not in SKIP_DIRS)
        rel_folder = Path(folder).relative_to(root).as_posix()
        if rel_folder == "docs/adr" or rel_folder.startswith("docs/adr/"):
            continue
        for name in sorted(files):
            rel = name if rel_folder == "." else f"{rel_folder}/{name}"
            if name.endswith(".md") and rel not in ENGLISH_ONLY:
                found.append(rel)
    return found


def german_of(rel):
    return rel[: -len(".md")] + ".de.md"


def english_of(rel):
    return rel[: -len(".de.md")] + ".md"


def outline(text):
    """Heading levels, fenced code blocks and table rows outside code, and the heading texts."""
    levels, headings, blocks, rows, in_code = [], [], 0, 0, False
    for line in text.split("\n"):
        if line.startswith("```"):
            if not in_code:
                blocks += 1
            in_code = not in_code
            continue
        if in_code:
            continue
        m = re.match(r"^(#{1,6}) (.+?)\s*$", line)
        if m:
            levels.append(len(m.group(1)))
            headings.append(m.group(2))
        elif line.startswith("|"):
            rows += 1
    return levels, headings, blocks, rows


def slug(heading):
    """The anchor GitHub gives a heading: lower case, punctuation removed, spaces to hyphens."""
    text = heading.strip().lower().replace("`", "")
    return re.sub(r"[^\w\- ]", "", text).replace(" ", "-")


def anchors(text):
    seen, out = {}, set()
    for h in outline(text)[1]:
        base = slug(h)
        n = seen.get(base, 0)
        out.add(base if n == 0 else f"{base}-{n}")
        seen[base] = n + 1
    return out


def check(root):
    root = Path(root)
    findings = []
    docs = documents(root)
    have = set(docs)
    texts = {rel: (root / rel).read_text(encoding="utf-8") for rel in docs}

    for rel in docs:
        if rel.endswith(".de.md"):
            if english_of(rel) not in have:
                findings.append(f"{rel}: no English version {english_of(rel)}")
            continue
        de = german_of(rel)
        if de not in have:
            findings.append(f"{rel}: no German version {de}")
            continue
        en_line = f"English · [Deutsch]({Path(de).name})"
        de_line = f"[English]({Path(rel).name}) · Deutsch"
        if en_line not in texts[rel].split("\n"):
            findings.append(f"{rel}: language line missing: {en_line}")
        if de_line not in texts[de].split("\n"):
            findings.append(f"{de}: language line missing: {de_line}")
        en, ge = outline(texts[rel]), outline(texts[de])
        if en[0] != ge[0]:
            findings.append(f"{rel} / {de}: outlines differ ({len(en[0])} and {len(ge[0])} headings; levels "
                            f"{''.join(map(str, en[0]))} vs {''.join(map(str, ge[0]))})")
        if en[2] != ge[2]:
            findings.append(f"{rel} / {de}: {en[2]} and {ge[2]} code blocks")
        if en[3] != ge[3]:
            findings.append(f"{rel} / {de}: {en[3]} and {ge[3]} table rows")
        for n, line in enumerate(texts[de].split("\n"), 1):
            if "ß" in line:
                findings.append(f"{de}:{n}: \"ß\" - German texts use Swiss spelling (\"ss\")")

    for rel in docs:
        german = rel.endswith(".de.md")
        own_line_target = Path(english_of(rel) if german else german_of(rel)).name
        for target in LINK.findall(texts[rel]):
            if re.match(r"^[a-z][a-z0-9+.-]*:", target) or target.startswith("#"):
                continue
            path, _, anchor = target.partition("#")
            resolved = os.path.normpath(os.path.join(os.path.dirname(rel), path)).replace(os.sep, "/")
            if not (root / resolved).exists():
                findings.append(f"{rel}: link to a missing file: {target}")
                continue
            if anchor and resolved.endswith(".md"):
                text = texts.get(resolved) or (root / resolved).read_text(encoding="utf-8")
                if anchor not in anchors(text):
                    findings.append(f"{rel}: link to a missing heading: {target}")
            if path == own_line_target or resolved not in have:
                continue
            if german and not resolved.endswith(".de.md") and german_of(resolved) in have:
                findings.append(f"{rel}: German text links the English {target}; use {Path(german_of(path)).name}")
            if not german and resolved.endswith(".de.md"):
                findings.append(f"{rel}: English text links the German {target}")
    return findings


def main():
    findings = check(ROOT)
    for f in findings:
        print(f"check_docs: {f}", file=sys.stderr)
    if findings:
        print(f"check_docs: {len(findings)} finding(s); every document needs an English and a German version of the "
              "same shape (ADR-0143)", file=sys.stderr)
        return 1
    print(f"check_docs: {len(documents(ROOT)) // 2} documents in English and German")
    return 0


if __name__ == "__main__":
    sys.exit(main())
