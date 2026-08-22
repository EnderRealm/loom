#!/usr/bin/env python3
"""Tests for the knowledge-store commit an extraction run leaves behind.

Run with: python3 -m unittest

Fixtures are real git repos in temp directories rather than mocks — what this
module has to get right is git's own behaviour (what a pathspec matches, what
`commit --only` leaves alone, where `rev-parse --show-toplevel` lands), and a
mocked git would assert nothing about that. Identity, signing and hooks are all
pinned per-repo so the fixtures don't depend on the host's global config — a
developer with commit.gpgsign or core.hooksPath set globally would otherwise get
a signing prompt or someone else's hook on every fixture commit.
"""
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from knowledge_git import (
    MESSAGE_MAX,
    REASON_MAX,
    EnclosingRepoError,
    NoGitRepoError,
    knowledge_git_log_path,
    record_knowledge_commit,
    sanitize_record,
)

EXTRACTORS = str(Path(__file__).resolve().parent)
SESSION = "5a28d3d6-cfeb-40ea-872f-15c0b87ea541"

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


def git(repo: Path, *args: str) -> str:
    return subprocess.run(["git", "-C", str(repo), *args], check=True,
                          capture_output=True, text=True).stdout


def init_repo(path: Path, *init_args: str) -> Path:
    path.mkdir(parents=True, exist_ok=True)
    git(path, "init", "-q", *init_args)
    git(path, "config", "user.email", "test@example.invalid")
    git(path, "config", "user.name", "loom test")
    git(path, "config", "commit.gpgsign", "false")
    git(path, "config", "core.hooksPath", str(path / ".git" / "no-hooks"))
    return path


def require_ref_format_files() -> None:
    """Skip when git predates --ref-format (2.45). The git helper runs with
    check=True, so without this the fixture dies inside init_repo with a
    CalledProcessError that reads as a failure of the code under test."""
    with tempfile.TemporaryDirectory() as probe:
        try:
            git(Path(probe), "init", "-q", "--ref-format=files")
        except subprocess.CalledProcessError:
            raise unittest.SkipTest("git does not support --ref-format")


class KnowledgeCommitTest(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        # Resolved: macOS hands out /var/folders/... symlinked to /private/var,
        # and git reports the physical path.
        self.root = Path(tmp.name).resolve()
        self.home = self.root / "home"
        env = mock.patch.dict(os.environ, {"LOOM_HOME": str(self.home)})
        env.start()
        self.addCleanup(env.stop)
        self.store = self.root / "knowledge"

    def bootstrap(self, store: Path, *init_args: str) -> Path:
        """A store with a tracked log.md and one commit, as the live store has."""
        init_repo(store, *init_args)
        (store / "log.md").write_text("# Knowledge log\n")
        git(store, "add", "-A")
        git(store, "commit", "-q", "-m", "bootstrap")
        return store

    def write_candidate(self, store: Path, name: str) -> Path:
        path = store / "_candidates" / "truths" / "loom" / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(f"---\nid: {name}\n---\n\n## Claim\n\nSomething.\n")
        return path

    def subjects(self, store: Path) -> list[str]:
        return git(store, "log", "--pretty=%s").splitlines()

    def log_lines(self) -> list[str]:
        return knowledge_git_log_path().read_text().splitlines()

    def test_run_commits_its_paths_and_leaves_unrelated_dirt_dirty(self):
        """The acceptance criterion, end to end at the module: after a run, git
        log shows exactly one new commit, the paths the run touched are clean,
        and working-tree dirt the run did not make is untouched."""
        store = self.bootstrap(self.store)
        (store / "notes.md").write_text("tracked\n")
        git(store, "add", "-A")
        git(store, "commit", "-q", "-m", "notes")
        # Dirt this run did not make: a tracked edit and an untracked file.
        (store / "notes.md").write_text("edited by hand\n")
        (store / "stray.md").write_text("untracked\n")

        first = self.write_candidate(store, "one.md")
        second = self.write_candidate(store, "two.md")
        log = store / "log.md"
        log.write_text(log.read_text() + "\n## [2026-08-22] extract 5a28d3d6 | loom | 2 truth candidate(s)\n")

        message = "extract 5a28d3d6 | loom | 2 truth candidate(s)"
        reason = record_knowledge_commit(store, [first, second, log], message)

        self.assertEqual(reason, "")
        self.assertEqual(self.subjects(store), [message, "notes", "bootstrap"])
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
        relative. Git runs with -C root, so a pathspec passed on unresolved
        would be re-resolved against the store rather than the cwd the caller
        built it against, and the add would match nothing."""
        store = self.bootstrap(self.store)
        candidate = self.write_candidate(store, "one.md")
        cwd = os.getcwd()
        self.addCleanup(os.chdir, cwd)
        os.chdir(self.root)

        message = "extract abc | loom | 1 truth candidate(s)"
        reason = record_knowledge_commit(Path("knowledge"),
                                         [candidate.relative_to(self.root)], message)

        self.assertEqual(reason, "")
        self.assertEqual(self.subjects(store), [message, "bootstrap"])

    def test_not_a_repo_degrades_with_a_logged_reason(self):
        self.store.mkdir(parents=True)
        candidate = self.write_candidate(self.store, "one.md")

        reason = record_knowledge_commit(self.store, [candidate], "extract abc | loom | 1 truth candidate(s)")

        self.assertEqual(reason, NoGitRepoError.reason)
        self.assertEqual(len(self.log_lines()), 1)
        self.assertIn(NoGitRepoError.reason, self.log_lines()[0])
        self.assertIn("extract abc | loom | 1 truth candidate(s)", self.log_lines()[0])

    def test_store_inside_another_repo_gets_its_own_reason(self):
        enclosing = self.bootstrap(self.root / "dotfiles")
        store = enclosing / "knowledge"
        candidate = self.write_candidate(store, "one.md")

        reason = record_knowledge_commit(store, [candidate], "extract abc | loom | 1 truth candidate(s)")

        self.assertEqual(reason, EnclosingRepoError.reason)
        self.assertEqual(self.subjects(enclosing), ["bootstrap"])
        self.assertIn(str(enclosing), self.log_lines()[0])

    def test_path_that_neither_exists_nor_is_tracked_is_dropped(self):
        store = self.bootstrap(self.store)
        candidate = self.write_candidate(store, "one.md")
        removed = store / "_candidates" / "truths" / "loom" / "gone.md"

        reason = record_knowledge_commit(store, [candidate, removed], "extract abc | loom | 1 truth candidate(s)")

        self.assertEqual(reason, "")
        self.assertEqual(git(store, "show", "--name-only", "--pretty=format:").split(),
                         ["_candidates/truths/loom/one.md"])

    def test_no_matching_paths_leaves_no_commit(self):
        store = self.bootstrap(self.store)
        missing = store / "_candidates" / "truths" / "loom" / "gone.md"

        reason = record_knowledge_commit(store, [missing], "extract abc | loom | 1 truth candidate(s)")

        self.assertEqual(reason, "no tracked paths to commit")
        self.assertEqual(self.subjects(store), ["bootstrap"])

    def test_a_commit_git_rejects_logs_in_full_and_leaves_nothing_staged(self):
        """A commit git itself refuses. It has to refuse at the commit, not
        before it: an empty .git/index.lock — the obvious forcing mechanism —
        fails the `git add` first and never reaches the cleanup this test is
        about. A stale lock on the branch ref leaves `git add` alone and rejects
        the commit that follows it, with the multi-line output the log has to
        keep whole. The lock path is the loose-files ref backend's, so the
        fixture pins that backend rather than inheriting the host's default —
        skipped rather than errored on a git too old to know the flag."""
        require_ref_format_files()
        store = self.bootstrap(self.store, "--ref-format=files")
        branch = git(store, "symbolic-ref", "--short", "HEAD").strip()
        (store / ".git" / "refs" / "heads" / f"{branch}.lock").write_text("")
        candidate = self.write_candidate(store, "one.md")

        reason = record_knowledge_commit(store, [candidate], "extract abc | loom | 1 truth candidate(s)")

        self.assertLessEqual(len(reason), REASON_MAX)
        self.assertTrue(reason.endswith("…"))
        self.assertNotIn("Another git process", reason)
        self.assertEqual(self.subjects(store), ["bootstrap"])
        self.assertEqual(len(self.log_lines()), 1)
        self.assertIn("cannot lock ref", self.log_lines()[0])
        self.assertIn("remove the file manually to continue", self.log_lines()[0])
        # The run must not be left staged for the next commit to absorb.
        self.assertEqual(git(store, "diff", "--cached", "--name-only"), "")

    def test_a_hook_in_the_store_does_not_run(self):
        """The store's hooks stay out of an unattended sweep: one that rejected
        or blocked would wedge the run with nobody there to answer it."""
        store = self.bootstrap(self.store)
        git(store, "config", "core.hooksPath", str(store / ".git" / "hooks"))
        hook = store / ".git" / "hooks" / "pre-commit"
        hook.write_text("#!/bin/sh\nexit 1\n")
        hook.chmod(0o755)
        candidate = self.write_candidate(store, "one.md")

        message = "extract abc | loom | 1 truth candidate(s)"
        reason = record_knowledge_commit(store, [candidate], message)

        self.assertEqual(reason, "")
        self.assertEqual(self.subjects(store), [message, "bootstrap"])

    def test_control_characters_in_the_message_cannot_forge_a_second_line(self):
        store = self.bootstrap(self.store)
        candidate = self.write_candidate(store, "one.md")

        reason = record_knowledge_commit(
            store, [candidate],
            "extract abc\nSigned-off-by: nobody‮ | lo om | 1 truth candidate(s)")

        self.assertEqual(reason, "")
        self.assertEqual(git(store, "log", "-1", "--pretty=%B").strip(),
                         "extract abc Signed-off-by: nobody  | lo om | 1 truth candidate(s)")

    def test_an_over_long_message_is_bounded_in_runes(self):
        bounded = sanitize_record("é" * (MESSAGE_MAX + 50))

        self.assertEqual(len(bounded), MESSAGE_MAX)
        self.assertTrue(bounded.endswith("…"))


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
                   LOOM_HOME=str(self.root / "home"))
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

    def test_a_run_that_emits_nothing_leaves_no_commit(self):
        self.run_extract("NO_TRUTHS\n")

        self.assertEqual(git(self.store, "log", "--pretty=%s").splitlines(), ["bootstrap"])


if __name__ == "__main__":
    unittest.main()
