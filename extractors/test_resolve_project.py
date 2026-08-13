#!/usr/bin/env python3
"""Tests for the .loom-project resolver.

Run with: python3 -m unittest

Fixtures are real directory trees rather than mocks — the resolver's whole job
is filesystem shape (where .git is, where a marker is, how far up the walk
goes), which a mocked filesystem would assert nothing about.
"""
import os
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from resolve_project import (
    MARKER_NAME,
    SOURCE_BASENAME,
    SOURCE_MARKER,
    VALUE_ECHO_LIMIT,
    resolve_project,
)


def snapshot(root: Path) -> dict:
    """Identity of every path under root, sensitive to creation, deletion and
    modification. Read access is deliberately not captured — atime is excluded."""
    out = {}
    for path in sorted(root.rglob("*")):
        st = path.lstat()
        out[str(path)] = (st.st_mode, st.st_size, st.st_mtime_ns,
                          path.read_bytes() if path.is_file() else None)
    return out


class ResolveProjectTest(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        # Resolved: macOS hands out /var/folders/... symlinked to /private/var,
        # and the resolver reports real paths.
        self.root = Path(tmp.name).resolve()
        self.home = self.root / "home"
        self.home.mkdir()
        # An isolated HOME keeps the real tk registry out of the assertions.
        env = mock.patch.dict(os.environ, {"HOME": str(self.home)})
        env.start()
        self.addCleanup(env.stop)

    def make_repo(self, name="repo", git_as_file=False):
        repo = self.root / name
        repo.mkdir(parents=True)
        if git_as_file:
            (repo / ".git").write_text("gitdir: /elsewhere/.git/worktrees/repo\n")
        else:
            (repo / ".git").mkdir()
        return repo

    def write_registry(self, *names):
        store = self.root / "store"
        store.mkdir()
        body = "projects:\n"
        for name in names:
            body += f"    {name}:\n        store: central\n        auto_link: false\n"
        (store / "config.yaml").write_text(body)
        ticket_dir = self.home / ".ticket"
        ticket_dir.mkdir()
        (ticket_dir / "config.yaml").write_text(f"default_project: {names[0]}\ncentral_root: {store}\n")

    def assertWarns_containing(self, result, needle):
        self.assertTrue(any(needle in w for w in result.warnings),
                        f"no warning containing {needle!r} in {result.warnings}")

    def test_marker_at_repo_root(self):
        repo = self.make_repo()
        (repo / MARKER_NAME).write_text("loom\n")

        result = resolve_project(repo)

        self.assertEqual(result.name, "loom")
        self.assertEqual(result.source, SOURCE_MARKER)
        self.assertEqual(result.marker_path, repo / MARKER_NAME)

    def test_marker_found_from_subdirectory(self):
        repo = self.make_repo()
        (repo / MARKER_NAME).write_text("loom\n")
        sub = repo / "internal" / "extract"
        sub.mkdir(parents=True)

        result = resolve_project(sub)

        self.assertEqual(result.name, "loom")
        self.assertEqual(result.marker_path, repo / MARKER_NAME)

    def test_marker_from_file_path(self):
        repo = self.make_repo()
        (repo / MARKER_NAME).write_text("loom\n")
        source = repo / "main.go"
        source.write_text("package main\n")

        self.assertEqual(resolve_project(source).name, "loom")

    def test_nearest_marker_wins(self):
        repo = self.make_repo()
        (repo / MARKER_NAME).write_text("outer\n")
        sub = repo / "vendored"
        sub.mkdir()
        (sub / MARKER_NAME).write_text("inner\n")

        result = resolve_project(sub)

        self.assertEqual(result.name, "inner")
        self.assertEqual(result.marker_path, sub / MARKER_NAME)

    def test_marker_below_the_repo_root_warns(self):
        repo = self.make_repo()
        sub = repo / "vendored"
        sub.mkdir()
        (sub / MARKER_NAME).write_text("inner\n")

        result = resolve_project(sub)

        self.assertEqual(result.name, "inner")
        self.assertWarns_containing(result, f"is below the repo root {repo}")

    def test_git_file_counts_as_repo_root(self):
        repo = self.make_repo(git_as_file=True)
        (repo / MARKER_NAME).write_text("loom\n")

        result = resolve_project(repo)

        self.assertEqual(result.name, "loom")
        self.assertEqual(result.source, SOURCE_MARKER)

    def test_comment_led_marker(self):
        repo = self.make_repo()
        (repo / MARKER_NAME).write_text("# canonical project name — see SCHEMA.md\n\nloom\n")

        self.assertEqual(resolve_project(repo).name, "loom")

    def test_marker_above_repo_root_is_ignored(self):
        (self.root / MARKER_NAME).write_text("captured\n")
        repo = self.make_repo()

        result = resolve_project(repo)

        self.assertEqual(result.name, "repo")
        self.assertEqual(result.source, SOURCE_BASENAME)

    def test_unsafe_marker_value_falls_back(self):
        repo = self.make_repo()
        (repo / MARKER_NAME).write_text("../escape\n")

        result = resolve_project(repo)

        self.assertEqual(result.name, "repo")
        self.assertEqual(result.source, SOURCE_BASENAME)
        self.assertWarns_containing(result, "unusable project name '../escape'")

    def test_undecodable_marker_falls_back(self):
        repo = self.make_repo()
        (repo / MARKER_NAME).write_bytes(b"\xff\xfe not text\n")

        result = resolve_project(repo)

        self.assertEqual(result.name, "repo")
        self.assertEqual(result.source, SOURCE_BASENAME)
        self.assertWarns_containing(result, "unreadable")

    def test_symlinked_marker_falls_back_without_echoing_the_target(self):
        repo = self.make_repo()
        secret = self.home / "id_ed25519"
        secret.write_text("-----BEGIN OPENSSH PRIVATE KEY-----\n")
        (repo / MARKER_NAME).symlink_to(secret)

        result = resolve_project(repo)

        self.assertEqual(result.name, "repo")
        self.assertEqual(result.source, SOURCE_BASENAME)
        self.assertWarns_containing(result, "is a symlink")
        self.assertFalse(any("OPENSSH" in w for w in result.warnings), result.warnings)

    def test_dangling_symlinked_marker_falls_back(self):
        repo = self.make_repo()
        (repo / MARKER_NAME).symlink_to(self.root / "gone")

        result = resolve_project(repo)

        self.assertEqual(result.name, "repo")
        self.assertEqual(result.source, SOURCE_BASENAME)
        self.assertWarns_containing(result, "is a symlink")

    def test_unusable_marker_value_is_truncated_in_the_warning(self):
        repo = self.make_repo()
        secret = "x" * (VALUE_ECHO_LIMIT * 2)
        (repo / MARKER_NAME).write_text(f"{secret}!\n")

        result = resolve_project(repo)

        self.assertEqual(result.name, "repo")
        self.assertWarns_containing(result, "x" * VALUE_ECHO_LIMIT + "...")
        self.assertFalse(any(secret in w for w in result.warnings), result.warnings)

    def test_empty_marker_falls_back(self):
        repo = self.make_repo()
        (repo / MARKER_NAME).write_text("# nothing but a comment\n")

        result = resolve_project(repo)

        self.assertEqual(result.name, "repo")
        self.assertWarns_containing(result, "holds no project name")

    def test_no_marker_warns_where_to_add_one(self):
        repo = self.make_repo()

        result = resolve_project(repo)

        self.assertEqual(result.name, "repo")
        self.assertEqual(result.source, SOURCE_BASENAME)
        self.assertIsNone(result.marker_path)
        self.assertWarns_containing(result, str(repo / MARKER_NAME))

    def test_unsafe_repo_basename_is_unresolvable(self):
        repo = self.make_repo(name="Repo Name")

        result = resolve_project(repo)

        self.assertIsNone(result.name)
        self.assertWarns_containing(result, "not a usable project name")

    def test_no_repository_is_unresolvable(self):
        loose = self.root / "loose"
        loose.mkdir()

        result = resolve_project(loose)

        self.assertIsNone(result.name)
        self.assertEqual(result.source, "")
        self.assertWarns_containing(result, "no git repository")

    def test_registered_name_passes_validation(self):
        self.write_registry("loom", "ticket")
        repo = self.make_repo()
        (repo / MARKER_NAME).write_text("loom\n")

        result = resolve_project(repo)

        self.assertEqual(result.name, "loom")
        self.assertEqual(result.warnings, [])

    def test_unregistered_name_warns_without_changing_the_name(self):
        self.write_registry("ticket")
        repo = self.make_repo()
        (repo / MARKER_NAME).write_text("loom\n")

        result = resolve_project(repo)

        self.assertEqual(result.name, "loom")
        self.assertWarns_containing(result, "not a registered tk project")

    def test_missing_registry_skips_validation(self):
        repo = self.make_repo()
        (repo / MARKER_NAME).write_text("loom\n")

        result = resolve_project(repo)

        self.assertEqual(result.name, "loom")
        self.assertWarns_containing(result, "tk registry check skipped")

    def test_undecodable_ticket_config_skips_validation(self):
        self.write_registry("loom")
        (self.home / ".ticket" / "config.yaml").write_bytes(b"central_root: \xff\xfe\n")
        repo = self.make_repo()
        (repo / MARKER_NAME).write_text("loom\n")

        result = resolve_project(repo)

        self.assertEqual(result.name, "loom")
        self.assertWarns_containing(result, "tk registry check skipped")

    def test_undecodable_registry_skips_validation(self):
        self.write_registry("loom")
        (self.root / "store" / "config.yaml").write_bytes(b"projects:\n    \xff\xfe:\n")
        repo = self.make_repo()
        (repo / MARKER_NAME).write_text("loom\n")

        result = resolve_project(repo)

        self.assertEqual(result.name, "loom")
        self.assertWarns_containing(result, "tk registry check skipped")

    def test_resolution_writes_nothing(self):
        self.write_registry("loom")
        repo = self.make_repo()
        (repo / MARKER_NAME).write_text("loom\n")
        sub = repo / "internal"
        sub.mkdir()
        bare = self.make_repo(name="bare")
        loose = self.root / "loose"
        loose.mkdir()

        before = snapshot(self.root)
        for path in (repo, sub, bare, loose, repo / MARKER_NAME):
            resolve_project(path)

        self.assertEqual(snapshot(self.root), before)


if __name__ == "__main__":
    unittest.main()
