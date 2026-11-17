#!/usr/bin/env python3
"""Tests for where --summarize saves its intermediate summary.

Run with: python3 -m unittest

main() is driven end to end with call_llm stubbed, so the files on disk and
the stderr line are the ones a real run produces.
"""
import contextlib
import io
import json
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

import extract

SUMMARY = "### Decisions\n\nKeep the summary beside the raw output.\n"
RAW = "no candidates\n"


class SummarizeOutputPathTest(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name)
        self.input = self.root / "session.jsonl"
        self.input.write_text(json.dumps({"type": "user", "message": {"content": "hello"}}) + "\n")

    def run_main(self, raw_out: Path) -> str:
        calls = []

        def model(prompt, *args):
            calls.append(prompt)
            return SUMMARY if len(calls) == 1 else RAW

        argv = ["extract.py", "--input", str(self.input), "--scope", "loom", "--summarize",
                "--judge", "keyword", "--no-emit-candidates", "--raw-out", str(raw_out)]
        err = io.StringIO()
        with mock.patch.object(extract, "call_llm", model), \
             mock.patch.object(extract, "load_reference_truths_from", return_value=[]), \
             mock.patch.object(sys, "argv", argv), \
             contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(err):
            extract.main()
        self.assertEqual(len(calls), 2)
        return err.getvalue()

    def summary_line_path(self, err: str) -> Path:
        prefix = "[extract] intermediate summary saved to "
        lines = [line for line in err.splitlines() if line.startswith(prefix)]
        self.assertEqual(len(lines), 1)
        return Path(lines[0][len(prefix):])

    def assert_split(self, raw_out: Path, summary_out: Path):
        err = self.run_main(raw_out)

        self.assertNotEqual(raw_out, summary_out)
        self.assertEqual(raw_out.read_text(), RAW)
        self.assertIn("Keep the summary beside the raw output.", summary_out.read_text())
        self.assertEqual(self.summary_line_path(err), summary_out)

    def test_raw_out_without_the_raw_suffix_keeps_the_summary_in_its_own_file(self):
        raw_out = self.root / "raw.txt"
        self.assert_split(raw_out, self.root / "raw.txt.summary.txt")

    def test_raw_out_with_the_raw_suffix_swaps_it_for_the_summary_suffix(self):
        raw_out = self.root / "run.raw.txt"
        self.assert_split(raw_out, self.root / "run.summary.txt")

    def test_only_the_trailing_raw_suffix_is_swapped(self):
        nested = self.root / "a.raw.txt.d"
        nested.mkdir()
        raw_out = nested / "run.raw.txt"
        self.assert_split(raw_out, nested / "run.summary.txt")


if __name__ == "__main__":
    unittest.main()
