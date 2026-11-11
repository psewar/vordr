"""Tests for the language check of the documents (ADR-0143): a complete pair passes, each rule stops what it is for.
Run by build.sh: python3 -m unittest discover -s tools -p "check_*_test.py"."""
import os
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import check_docs as cd  # noqa: E402

EN = """# Guide

English · [Deutsch](guide.de.md)

## 1. Setup

See [the design](design.md#2-reasons) and [ADR-0001](docs/adr/0001-x.md).

| a | b |
|---|---|

```bash
make
```
"""

DE = """# Anleitung

[English](guide.md) · Deutsch

## 1. Einrichten

Siehe [den Entwurf](design.de.md#2-gründe) und [ADR-0001](docs/adr/0001-x.md).

| a | b |
|---|---|

```bash
make
```
"""

DESIGN_EN = "# Design\n\nEnglish · [Deutsch](design.de.md)\n\n## 2. Reasons\n"
DESIGN_DE = "# Entwurf\n\n[English](design.md) · Deutsch\n\n## 2. Gründe\n"


class Docs(unittest.TestCase):
    def tree(self, files):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        for rel, text in files.items():
            p = Path(tmp.name) / rel
            p.parent.mkdir(parents=True, exist_ok=True)
            p.write_text(text, encoding="utf-8")
        return tmp.name

    def base(self, **changes):
        files = {"guide.md": EN, "guide.de.md": DE, "design.md": DESIGN_EN, "design.de.md": DESIGN_DE,
                 "docs/adr/0001-x.md": "# ADR\n", "docs/adr/README.md": "# ADRs\n", "AGENTS.md": "# Guidance\n",
                 "CLAUDE.md": "@AGENTS.md\n", ".cache/README.md": "# cache\n"}
        files.update(changes)
        return {k: v for k, v in files.items() if v is not None}

    def test_complete_pairs_pass(self):
        self.assertEqual(cd.check(self.tree(self.base())), [])

    def test_adrs_agents_and_hidden_folders_need_no_german_version(self):
        self.assertEqual(cd.documents(self.tree(self.base())),
                         ["design.de.md", "design.md", "guide.de.md", "guide.md"])

    def test_missing_version_in_either_language(self):
        self.assertIn("guide.md: no German version guide.de.md", cd.check(self.tree(self.base(**{"guide.de.md": None}))))
        found = cd.check(self.tree(self.base(**{"design.md": None})))
        self.assertIn("design.de.md: no English version design.md", found)

    def test_language_line(self):
        found = cd.check(self.tree(self.base(**{"guide.de.md": DE.replace("[English](guide.md) · Deutsch\n", "")})))
        self.assertTrue(any("language line missing" in f for f in found), found)

    def test_outline_code_blocks_and_tables(self):
        found = cd.check(self.tree(self.base(**{"guide.de.md": DE.replace("## 1. Einrichten", "### 1. Einrichten")})))
        self.assertTrue(any("outlines differ" in f for f in found), found)
        found = cd.check(self.tree(self.base(**{"guide.de.md": DE.replace("```bash\nmake\n```\n", "")})))
        self.assertTrue(any("code blocks" in f for f in found), found)
        found = cd.check(self.tree(self.base(**{"guide.de.md": DE.replace("| a | b |\n", "")})))
        self.assertTrue(any("table rows" in f for f in found), found)

    def test_a_hash_inside_code_is_no_heading(self):
        en = EN.replace("make\n", "# comment\nmake\n")
        self.assertEqual(cd.check(self.tree(self.base(**{"guide.md": en}))), [])

    def test_broken_link_and_anchor(self):
        found = cd.check(self.tree(self.base(**{"guide.md": EN.replace("design.md#2-reasons", "nowhere.md")})))
        self.assertIn("guide.md: link to a missing file: nowhere.md", found)
        found = cd.check(self.tree(self.base(**{"guide.md": EN.replace("#2-reasons", "#2-gründe")})))
        self.assertIn("guide.md: link to a missing heading: design.md#2-gründe", found)

    def test_each_language_links_its_own_versions(self):
        found = cd.check(self.tree(self.base(**{"guide.de.md": DE.replace("design.de.md#2-gründe", "design.md#2-reasons")})))
        self.assertTrue(any("German text links the English design.md" in f for f in found), found)
        found = cd.check(self.tree(self.base(**{"guide.md": EN.replace("design.md#2-reasons", "design.de.md#2-gründe")})))
        self.assertTrue(any("English text links the German design.de.md" in f for f in found), found)

    def test_swiss_spelling(self):
        found = cd.check(self.tree(self.base(**{"guide.de.md": DE.replace("Siehe", "Siehe gemäß")})))
        self.assertTrue(any("Swiss spelling" in f for f in found), found)

    def test_slug_like_github(self):
        self.assertEqual(cd.slug("4.4 Konfigurationssicherung (ADR-0090)"), "44-konfigurationssicherung-adr-0090")
        self.assertEqual(cd.slug("6. Die Integration `custom_components/vordr`"), "6-die-integration-custom_componentsvordr")


if __name__ == "__main__":
    unittest.main()
