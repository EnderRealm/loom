#!/usr/bin/env python3
"""Tests for the cwd every claude -p call runs in.

Run with: python3 -m unittest

The subprocess is mocked — the contract is the cwd handed to it, and a real
claude call would file a transcript under ~/.claude/projects/. The directory is
real: that call_claude creates it is half of what is asserted. HOME and
LOOM_HOME point into a temp dir, so the real ~/.loom is never touched.
"""
import os
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest import mock

import extract


class CallClaudeCwdTest(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name).resolve()

    def call(self) -> dict:
        done = subprocess.CompletedProcess([], 0, stdout="ok", stderr="")
        with mock.patch.object(extract.subprocess, "run", return_value=done) as run:
            self.assertEqual(extract.call_claude("prompt", "haiku"), "ok")
        return run.call_args.kwargs

    def test_default_loom_home_runs_in_its_machinery_directory(self):
        with mock.patch.dict(os.environ, {"HOME": str(self.root)}):
            os.environ.pop("LOOM_HOME", None)
            kwargs = self.call()

        want = self.root / ".loom" / "machinery"
        self.assertEqual(kwargs["cwd"], want)
        self.assertTrue(want.is_dir())

    def test_loom_home_names_the_machinery_directory(self):
        home = self.root / "elsewhere"
        with mock.patch.dict(os.environ, {"HOME": str(self.root / "user"),
                                          "LOOM_HOME": str(home)}):
            kwargs = self.call()

        self.assertEqual(kwargs["cwd"], home / "machinery")
        self.assertTrue((home / "machinery").is_dir())
        # Not the default, which LOOM_HOME replaces rather than joins.
        self.assertFalse((self.root / "user").exists())

    def test_an_existing_directory_is_reused(self):
        home = self.root / "home"
        (home / "machinery").mkdir(parents=True)
        with mock.patch.dict(os.environ, {"LOOM_HOME": str(home)}):
            kwargs = self.call()

        self.assertEqual(kwargs["cwd"], home / "machinery")


if __name__ == "__main__":
    unittest.main()
