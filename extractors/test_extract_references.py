#!/usr/bin/env python3
"""Tests for which truths extract.py takes as reference examples.

Run with: python3 -m unittest

Fixtures are real directory trees rather than mocks. The deprecated fixture is
written by hand in the shape a demotion leaves on disk: test_sweep.py's WiringTest
holds that nothing outside its allowlist names the sweep.
"""
import io
import tempfile
import unittest
from contextlib import redirect_stderr
from pathlib import Path

from extract import TYPE_CONFIG, build_prompt, load_reference_truths_from

# Credential-shaped, and fake: matches redact.py's `github-token` row.
FAKE_TOKEN = "ghp_" + "0123456789abcdefghijklmnopqrstuvwxyz"
DEMOTED_RECORD = "The demotion record of a truth judged false."


def truth(cid: str, claim: str, status_line: str = "status: validated\n", record: str = "") -> str:
    return (f"---\nid: {cid}\ntitle: An example truth\nscope: loom\n{status_line}---\n\n{record}"
            f"## Claim\n\n{claim}\n\n## How to verify\n\nRun the thing.\n")


class ReferenceStatusTest(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.scope = Path(tmp.name) / "truths" / "loom"
        self.scope.mkdir(parents=True)
        (self.scope / "loom-kept.md").write_text(truth("loom-kept", "A claim that holds."))

    def demote(self, name: str, claim: str):
        (self.scope / f"{name}.md").write_text(
            truth(name, claim, "status: deprecated\n", f"## Demoted\n\n{DEMOTED_RECORD}\n\n"))

    def test_a_demoted_truth_is_neither_a_reference_nor_in_the_prompt(self):
        self.demote("loom-demoted", "A claim judged false.")

        refs = load_reference_truths_from(self.scope)

        self.assertEqual([r["id"] for r in refs], ["loom-kept"])
        prompt = build_prompt(TYPE_CONFIG["truth"]["prompt"].read_text(), refs, "input", "2026-11-07")
        self.assertIn("A claim that holds.", prompt)
        self.assertNotIn("A claim judged false.", prompt)
        self.assertNotIn(DEMOTED_RECORD, prompt)

    def test_only_a_status_of_exactly_validated_is_kept(self):
        cases = {
            "candidate": truth("loom-other", "Left out.", "status: candidate\n"),
            "no status": truth("loom-other", "Left out.", ""),
            "empty status": truth("loom-other", "Left out.", "status:\n"),
            "another case": truth("loom-other", "Left out.", "status: Validated\n"),
            "a nested status only": truth("loom-other", "Left out.",
                                          "contradicts:\n  - id: x\n    status: validated\n"),
            "no frontmatter": "## Claim\n\nLeft out.\n",
        }
        for where, text in cases.items():
            with self.subTest(where=where):
                (self.scope / "loom-other.md").write_text(text)

                refs = load_reference_truths_from(self.scope)

                self.assertEqual([r["id"] for r in refs], ["loom-kept"])

    def test_the_redaction_log_counts_only_what_was_kept(self):
        """The line reports what went toward the provider, so a secret in a truth
        that was dropped is not in it."""
        (self.scope / "loom-kept.md").write_text(truth("loom-kept", f"It ran with {FAKE_TOKEN}."))
        self.demote("loom-demoted", f"It ran with {FAKE_TOKEN}.")
        err = io.StringIO()

        with redirect_stderr(err):
            refs = load_reference_truths_from(self.scope)

        self.assertNotIn(FAKE_TOKEN, refs[0]["raw"])
        self.assertIn(f"1 span(s), {len(FAKE_TOKEN)} chars: github-token×1", err.getvalue())


if __name__ == "__main__":
    unittest.main()
