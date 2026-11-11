"""Tests for the site-neutral check (ADR-0099, ADR-0134): each rule stops what it is for and lets ordinary text pass.
Run by build.sh: python3 -m unittest discover -s tools -p "check_neutral_test.py"."""
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import check_neutral as cn  # noqa: E402


class GenericRules(unittest.TestCase):
    def test_wsl_paths(self):
        for line in ("font = r'\\\\wsl.localhost\\Distro\\usr\\share'", "see //wsl$/Distro/home", "cd /mnt/d/x"):
            self.assertTrue(cn.generic(line), line)
        self.assertEqual(cn.generic("the agent runs under WSL as well"), [])

    def test_addresses_and_hosts(self):
        self.assertTrue(cn.generic("hub at 192.168.1.2"))
        self.assertEqual(cn.generic("hub at 192.0.2.10, https://hub.example/ and version 1.2.3.4"), [])


class OperationsRules(unittest.TestCase):
    def test_operations_log_stops(self):
        for line in (
            "Checked in a live probe on the hub.",
            "This was seen on the canary after an update.",
            "Seen on the host with the most packages.",
            "In the pilot every reboot took longer.",
            "Source: morning report, section 3.",
            "Sources: expansion report (private).",
            "No package list should leave the home network.",
            "Wird im Pilot erprobt.",
        ):
            self.assertTrue(cn.operations(line, in_adr=False), line)

    def test_dated_observation_stops(self):
        for line in ("Every host has it since 2026-10-30.", "Am 30.10. lief der erste Agent.", "as of 2026-11-05"):
            self.assertTrue(cn.operations(line, in_adr=False), line)

    def test_ordinary_text_passes(self):
        for line in (
            "date: 2026-10-30",
            "Backfilled on 2026-10-30, decided on 2026-10-29. Source: design/architecture.md 10.4.",
            "The canary ring gets a release first (ADR-0036).",
            "A failed run on the canary stops the later rings.",
            "Since hub 0.22.0 the event names the reason.",
            'want := "checked on 2026-10-30T10:00:00Z"',
            "A live check shows the new boot ID.",
            "Pilot lights stay off.",
            "The operator looks at Home Assistant.",
        ):
            self.assertEqual(cn.operations(line, in_adr=True), [], line)

    def test_decision_makers_keep_names_and_dates(self):
        self.assertEqual(cn.operations("decision-makers: alex (2026-11-01, chose the option); proposed by sam",
                                       in_adr=True), [])
        self.assertTrue(cn.operations("decision-makers: alex; on all Linux hosts since 2026-10-30", in_adr=True))
        self.assertTrue(cn.operations("decision-makers: sam; reported in the morning report", in_adr=True))
        self.assertEqual(cn.operations("decision-makers: alex; the second half of a report-only source", in_adr=True), [])
        # Outside an ADR the line is ordinary text.
        self.assertEqual(cn.operations("decision-makers: alex", in_adr=False), [])


class LicenseLine(unittest.TestCase):
    def test_copyright_may_name_the_publishing_account(self):
        import re
        import tempfile
        terms = [(re.compile(r"(?<![A-Za-z0-9])acct(?![A-Za-z0-9])", re.IGNORECASE), False, "")]
        with tempfile.TemporaryDirectory() as d:
            path = os.path.join(d, "LICENSE")
            with open(path, "w", encoding="utf-8") as f:
                f.write("MIT License\n\nCopyright (c) 2026 acct\n\nacct elsewhere\n")
            hits = cn.check(path, "LICENSE", terms, "github.com/acct/vordr")
            self.assertEqual(len(hits), 1, hits)
            self.assertIn("LICENSE:5", hits[0])
            manifest = os.path.join(d, "manifest.json")
            with open(manifest, "w", encoding="utf-8") as f:
                f.write('"codeowners": ["@acct"],\n"issue_tracker": "https://github.com/acct/vordr/issues",\n')
            self.assertEqual(cn.check(manifest, "manifest.json", terms, "github.com/acct/vordr"), [])


if __name__ == "__main__":
    unittest.main()
