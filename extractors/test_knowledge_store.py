#!/usr/bin/env python3
"""Tests for the knowledge-store write an extraction run leaves behind.

Run with: python3 -m unittest

The writing itself is loom's: this module owns the client — building the plan,
finding the binary, reporting what came back — and the end-to-end result of a run
against a real store. The rules that produce that result (path-scoped commits,
the non-repo and enclosing-repo reasons, record sanitization) live in
internal/knowledge/store and are tested there; what is asserted here is that a
run of extract.py ends with the store holding the right commit.

Fixtures are real git repos in temp directories rather than mocks, and the binary
is the real one, built once for the module — a stub would assert nothing about
either side of the boundary this module exists to cross. Identity, signing and
hooks are all pinned per-repo so the fixtures don't depend on the host's global
config.
"""
import contextlib
import io
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from knowledge_store import (StoreWriteError, apply_changes, append_change,
                             knowledge_root, write_change)

EXTRACTORS = str(Path(__file__).resolve().parent)
LOOM_ROOT = Path(EXTRACTORS).parent
SESSION = "5a28d3d6-cfeb-40ea-872f-15c0b87ea541"

# The bounds loom applies to a record, restated here because they are what a
# caller observes: internal/knowledge/store/git.go owns them (MessageMax,
# gitReasonMax).
MESSAGE_MAX = 200

# Built once for the module: a `go build` per test would dominate the run.
LOOM_BIN = None
_BUILD_DIR = None

CANDIDATE_OUTPUT = """---
id: loom-example-truth
title: An example truth
scope: loom
type: truth
status: validated
---

## Claim

Something is true.

## How to verify

Run the thing.

===END-OF-TRUTH===
"""

SUMMARY_INPUT = f"""---
project: loom
session_id: {SESSION}
date: 2026-08-22
---

### Discoveries

- Something is true.
"""

# Drives extract.main() in a subprocess with the LLM call stubbed from a file,
# so the end-to-end path runs with no network and no CLI.
RUNNER = """
import sys
sys.path.insert(0, sys.argv[1])
output = open(sys.argv[2]).read()
import extract
extract.call_llm = lambda *a, **k: output
sys.argv = ["extract.py"] + sys.argv[3:]
extract.main()
"""


def setUpModule():
    """Build the loom the client writes through. Skipped, not failed, without a
    go toolchain: the Python side is a client, and a machine that cannot build
    its server has nothing to say about the client's behaviour."""
    global LOOM_BIN, _BUILD_DIR
    if shutil.which("go") is None:
        raise unittest.SkipTest("go toolchain not available")
    _BUILD_DIR = tempfile.TemporaryDirectory()
    LOOM_BIN = str(Path(_BUILD_DIR.name) / "loom")
    build = subprocess.run(["go", "build", "-o", LOOM_BIN, "./cmd/loom"],
                           cwd=str(LOOM_ROOT), capture_output=True, text=True)
    if build.returncode != 0:
        raise unittest.SkipTest(f"go build failed: {build.stderr.strip()}")


def tearDownModule():
    if _BUILD_DIR is not None:
        _BUILD_DIR.cleanup()


def git(repo: Path, *args: str) -> str:
    return subprocess.run(["git", "-C", str(repo), *args], check=True,
                          capture_output=True, text=True).stdout


def init_repo(path: Path) -> Path:
    path.mkdir(parents=True, exist_ok=True)
    git(path, "init", "-q")
    git(path, "config", "user.email", "test@example.invalid")
    git(path, "config", "user.name", "loom test")
    git(path, "config", "commit.gpgsign", "false")
    git(path, "config", "core.hooksPath", str(path / ".git" / "no-hooks"))
    return path


class StoreClientTest(unittest.TestCase):
    """The client's own contract: what a caller gets back from a write."""

    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        # Resolved: macOS hands out /var/folders/... symlinked to /private/var,
        # and git reports the physical path.
        self.root = Path(tmp.name).resolve()
        self.home = self.root / "home"
        self.store = self.root / "knowledge"
        env = mock.patch.dict(os.environ, {
            "LOOM_HOME": str(self.home),
            "LOOM_KNOWLEDGE_ROOT": str(self.store),
            "LOOM_BIN": LOOM_BIN,
        })
        env.start()
        self.addCleanup(env.stop)

    def bootstrap(self) -> Path:
        """A store with a tracked log.md and one commit, as the live store has."""
        init_repo(self.store)
        (self.store / "log.md").write_text("# Knowledge log\n")
        git(self.store, "add", "-A")
        git(self.store, "commit", "-q", "-m", "bootstrap")
        return self.store

    def candidate_change(self, name: str) -> tuple[Path, dict]:
        path = self.store / "_candidates" / "truths" / "loom" / name
        return path, write_change(path, f"---\nid: {name}\n---\n\n## Claim\n\nSomething.\n")

    def subjects(self) -> list[str]:
        return git(self.store, "log", "--pretty=%s").splitlines()

    def log_lines(self) -> list[str]:
        return (self.home / "knowledge-git.log").read_text().splitlines()

    def test_a_run_writes_and_commits_its_paths(self):
        """The acceptance criterion, end to end at the module: the client's
        changes land on disk and are recorded as one commit, and working-tree
        dirt the run did not make is left alone."""
        store = self.bootstrap()
        (store / "notes.md").write_text("tracked\n")
        git(store, "add", "-A")
        git(store, "commit", "-q", "-m", "notes")
        # Dirt this run did not make: a tracked edit and an untracked file.
        (store / "notes.md").write_text("edited by hand\n")
        (store / "stray.md").write_text("untracked\n")

        first, one = self.candidate_change("one.md")
        second, two = self.candidate_change("two.md")
        message = "extract 5a28d3d6 | loom | 2 truth candidate(s)"
        entry = append_change(store / "log.md",
                              f"\n## [2026-08-22] {message}\n")

        reason = apply_changes(message, [one, two, entry])

        self.assertEqual(reason, "")
        self.assertTrue(first.exists() and second.exists())
        self.assertEqual(self.subjects(), [message, "notes", "bootstrap"])
        committed = git(store, "show", "--name-only", "--pretty=format:").split()
        self.assertEqual(sorted(committed), sorted([
            "_candidates/truths/loom/one.md",
            "_candidates/truths/loom/two.md",
            "log.md",
        ]))
        status = git(store, "status", "--porcelain")
        self.assertNotIn("_candidates", status)
        self.assertNotIn("log.md", status)
        self.assertIn(" M notes.md", status)
        self.assertIn("?? stray.md", status)

    def test_a_relative_root_commits_the_run(self):
        """The root arrives as LOOM_KNOWLEDGE_ROOT verbatim, which may be
        relative, and the paths a caller builds from it are relative with it."""
        store = self.bootstrap()
        cwd = os.getcwd()
        self.addCleanup(os.chdir, cwd)
        os.chdir(self.root)
        with mock.patch.dict(os.environ, {"LOOM_KNOWLEDGE_ROOT": "knowledge"}):
            message = "extract abc | loom | 1 truth candidate(s)"
            relative = Path("knowledge") / "_candidates" / "truths" / "loom" / "one.md"

            reason = apply_changes(message, [write_change(relative, "---\nid: one\n---\n")])

        self.assertEqual(reason, "")
        self.assertEqual(self.subjects(), [message, "bootstrap"])
        self.assertEqual(git(store, "show", "--name-only", "--pretty=format:").split(),
                         ["_candidates/truths/loom/one.md"])

    def test_not_a_repo_is_a_warning_not_a_failure(self):
        """The writes landed; only the record is missing. A store that is not
        under version control says so rather than relaying a git failure."""
        self.store.mkdir(parents=True)
        path, change = self.candidate_change("one.md")

        reason = apply_changes("extract abc | loom | 1 truth candidate(s)", [change])

        self.assertEqual(reason, "knowledge root is not a git repo")
        self.assertTrue(path.exists())
        self.assertEqual(len(self.log_lines()), 1)
        self.assertIn("extract abc | loom | 1 truth candidate(s)", self.log_lines()[0])

    def test_an_unpushed_commit_is_reported_but_not_returned(self):
        """A commit the store could not publish — the fixture has no upstream —
        is the caller's to hear about but not to act on: the record landed, and
        the next unit of work's push carries it."""
        self.bootstrap()
        _, change = self.candidate_change("one.md")
        captured = io.StringIO()

        with contextlib.redirect_stderr(captured):
            reason = apply_changes("extract abc | loom | 1 truth candidate(s)", [change])

        self.assertEqual(reason, "")
        self.assertIn("knowledge store not pushed: no upstream to push to", captured.getvalue())
        self.assertEqual(self.subjects()[0], "extract abc | loom | 1 truth candidate(s)")

    def test_store_inside_another_repo_gets_its_own_reason(self):
        """A store that merely sits inside a repo — a git-managed home directory,
        a dotfiles checkout — must not push the run into that repo's history."""
        enclosing = init_repo(self.root / "dotfiles")
        git(enclosing, "commit", "-q", "--allow-empty", "-m", "bootstrap")
        with mock.patch.dict(os.environ, {"LOOM_KNOWLEDGE_ROOT": str(enclosing / "knowledge")}):
            self.store = enclosing / "knowledge"
            # The store directory exists before anything writes to it: loom refuses
            # a root it cannot open rather than materializing one.
            self.store.mkdir()
            _, change = self.candidate_change("one.md")

            reason = apply_changes("extract abc | loom | 1 truth candidate(s)", [change])

        self.assertEqual(reason, "knowledge root is inside another git repo")
        self.assertEqual(git(enclosing, "log", "--pretty=%s").splitlines(), ["bootstrap"])
        self.assertIn(str(enclosing), self.log_lines()[0])

    def test_a_hook_in_the_store_does_not_run(self):
        """The store's hooks stay out of an unattended sweep: one that rejected
        or blocked would wedge the run with nobody there to answer it."""
        store = self.bootstrap()
        git(store, "config", "core.hooksPath", str(store / ".git" / "hooks"))
        hook = store / ".git" / "hooks" / "pre-commit"
        hook.write_text("#!/bin/sh\nexit 1\n")
        hook.chmod(0o755)
        _, change = self.candidate_change("one.md")

        message = "extract abc | loom | 1 truth candidate(s)"
        reason = apply_changes(message, [change])

        self.assertEqual(reason, "")
        self.assertEqual(self.subjects(), [message, "bootstrap"])

    def test_control_characters_in_the_message_cannot_forge_a_second_line(self):
        store = self.bootstrap()
        _, change = self.candidate_change("one.md")

        reason = apply_changes(
            "extract abc\nSigned-off-by: nobody‮ | lo om | 1 truth candidate(s)",
            [change])

        self.assertEqual(reason, "")
        self.assertEqual(git(store, "log", "-1", "--pretty=%B").strip(),
                         "extract abc Signed-off-by: nobody  | lo om | 1 truth candidate(s)")

    def test_an_over_long_message_is_bounded_in_runes(self):
        store = self.bootstrap()
        _, change = self.candidate_change("one.md")

        reason = apply_changes("é" * (MESSAGE_MAX + 50), [change])

        self.assertEqual(reason, "")
        subject = git(store, "log", "-1", "--pretty=%s").strip()
        self.assertEqual(len(subject), MESSAGE_MAX)
        self.assertTrue(subject.endswith("…"))

    def test_an_append_to_a_missing_file_fails_the_write(self):
        """log.md is bootstrapped at store init; a run pointed at a wrong root
        must not scatter one, and a write that did not land is fatal."""
        self.bootstrap()
        missing = self.store / "elsewhere" / "log.md"

        with self.assertRaises(StoreWriteError) as caught:
            apply_changes("extract abc | loom | 1 truth candidate(s)",
                          [append_change(missing, "\n## entry\n")])

        self.assertIn("log.md", str(caught.exception))
        self.assertFalse(missing.exists())

    def test_the_store_default_follows_loom_home(self):
        """The store is resolved the way the Go entry point resolves it:
        LOOM_KNOWLEDGE_ROOT, else $LOOM_HOME/knowledge. A default that ignored
        LOOM_HOME would build paths under a store loom never resolved, and every
        one of them would come back refused as outside the store."""
        home = self.root / "state"
        with mock.patch.dict(os.environ, {"LOOM_HOME": str(home)}):
            os.environ.pop("LOOM_KNOWLEDGE_ROOT", None)

            self.assertEqual(knowledge_root(), home / "knowledge")

        with mock.patch.dict(os.environ, {"LOOM_HOME": str(home),
                                          "LOOM_KNOWLEDGE_ROOT": str(self.store)}):
            self.assertEqual(knowledge_root(), self.store)

    def test_no_binary_names_what_was_tried(self):
        """There is no fallback to writing the files directly — a second writer
        is what the single entry point exists to remove — so a missing binary
        fails the run, saying what it looked for."""
        self.bootstrap()
        _, change = self.candidate_change("one.md")
        with mock.patch.dict(os.environ, {"LOOM_BIN": "", "PATH": str(self.root / "empty")}):
            with self.assertRaises(StoreWriteError) as caught:
                apply_changes("extract abc | loom | 1 truth candidate(s)", [change])

        self.assertIn("LOOM_BIN", str(caught.exception))
        self.assertIn("PATH", str(caught.exception))
        self.assertEqual(self.subjects(), ["bootstrap"])

    def test_an_unrunnable_binary_names_it(self):
        self.bootstrap()
        _, change = self.candidate_change("one.md")
        with mock.patch.dict(os.environ, {"LOOM_BIN": str(self.root / "absent-loom")}):
            with self.assertRaises(StoreWriteError) as caught:
                apply_changes("extract abc | loom | 1 truth candidate(s)", [change])

        self.assertIn("absent-loom", str(caught.exception))


class ExtractRunTest(unittest.TestCase):
    """extract.py's own wiring: one run of main() against a temp store."""

    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name).resolve()
        self.store = self.root / "knowledge"
        init_repo(self.store)
        (self.store / "log.md").write_text("# Knowledge log\n")
        git(self.store, "add", "-A")
        git(self.store, "commit", "-q", "-m", "bootstrap")
        self.input = self.root / "session.md"
        self.input.write_text(SUMMARY_INPUT)

    def run_extract(self, llm_output: str):
        out = self.root / "llm-output.txt"
        out.write_text(llm_output)
        env = dict(os.environ,
                   LOOM_KNOWLEDGE_ROOT=str(self.store),
                   LOOM_HOME=str(self.root / "home"),
                   LOOM_BIN=LOOM_BIN)
        return subprocess.run(
            [sys.executable, "-c", RUNNER, EXTRACTORS, str(out),
             "--input", str(self.input), "--scope", "loom"],
            check=True, capture_output=True, text=True, env=env)

    def test_a_run_that_emits_candidates_leaves_one_commit(self):
        self.run_extract(CANDIDATE_OUTPUT)

        subjects = git(self.store, "log", "--pretty=%s").splitlines()
        self.assertEqual(subjects,
                         [f"extract {SESSION[:8]} | loom | 1 truth candidate(s)", "bootstrap"])
        committed = git(self.store, "show", "--name-only", "--pretty=format:").split()
        self.assertIn("log.md", committed)
        self.assertEqual(len([p for p in committed if p.startswith("_candidates/truths/loom/")]), 1)
        self.assertNotIn("_candidates", git(self.store, "status", "--porcelain"))

    def test_a_run_that_emits_nothing_writes_and_commits_nothing(self):
        self.run_extract("NO_TRUTHS\n")

        self.assertEqual(git(self.store, "log", "--pretty=%s").splitlines(), ["bootstrap"])
        self.assertEqual(git(self.store, "status", "--porcelain"), "")


class BuildWikiTest(unittest.TestCase):
    """The worked example: a writer added after the entry point existed carries
    no commit code of its own."""

    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name).resolve()
        self.store = self.root / "knowledge"
        truth = self.store / "truths" / "loom" / "one.md"
        truth.parent.mkdir(parents=True)
        truth.write_text("---\nid: loom-one\ntitle: One\n---\n\n## Claim\n\nSomething.\n")
        init_repo(self.store)
        git(self.store, "add", "-A")
        git(self.store, "commit", "-q", "-m", "bootstrap")

    def test_build_wiki_commits_the_index(self):
        env = dict(os.environ,
                   LOOM_KNOWLEDGE_ROOT=str(self.store),
                   LOOM_HOME=str(self.root / "home"),
                   LOOM_BIN=LOOM_BIN)
        subprocess.run([sys.executable, str(Path(EXTRACTORS) / "build-wiki.py")],
                       check=True, capture_output=True, text=True, env=env)

        self.assertEqual(git(self.store, "log", "--pretty=%s").splitlines(),
                         ["build-wiki index.md", "bootstrap"])
        self.assertIn("index.md", git(self.store, "show", "--name-only", "--pretty=format:"))
        self.assertEqual(git(self.store, "status", "--porcelain"), "")


if __name__ == "__main__":
    unittest.main()
