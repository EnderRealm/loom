#!/usr/bin/env python3
"""Tests for routing a candidate to the scope it declares.

Run with: python3 -m unittest

Fixtures are real directory trees rather than mocks — the routing gate's whole
job is whether `truths/<scope>/` exists in the store, which a mocked filesystem
would assert nothing about. `emit_candidates` returns the writes rather than
performing them, so the plan is asserted over directly and no store is touched.
"""
import io
import tempfile
import unittest
from collections import Counter
from contextlib import redirect_stderr
from pathlib import Path

from extract import (
    SCOPE_ECHO_LIMIT,
    SCOPE_NAME_LIMIT,
    echo_scope,
    emit_candidates,
    parse_truth,
    route_candidate_scope,
    run_label,
)

SESSION = "5a28d3d6-cfeb-40ea-872f-15c0b87ea541"

# Credential-shaped, and fake: matches redact.py's `github-token` row
# (gh[pousr]_ plus 36+ alphanumerics) without being anyone's token.
FAKE_TOKEN = "ghp_" + "0123456789abcdefghijklmnopqrstuvwxyz"
# The same, in a shape NAME_PATTERN accepts, so it reaches the other rejection
# branch: redact.py's `anthropic-key` row is all lowercase and hyphens.
FAKE_KEY = "sk-ant-" + "0123456789abcdefghijkl"


def snapshot(root: Path) -> dict:
    """Identity of every path under root, sensitive to creation, deletion and
    modification. Read access is deliberately not captured — atime is excluded."""
    out = {}
    for path in sorted(root.rglob("*")):
        st = path.lstat()
        out[str(path)] = (st.st_mode, st.st_size, st.st_mtime_ns,
                          path.read_bytes() if path.is_file() else None)
    return out


def candidate(cid: str, scope: str, extra: str = "") -> dict:
    """A parsed candidate declaring `scope`, in the shape the model emits."""
    scope_line = f"scope: {scope}\n" if scope else ""
    return parse_truth(
        f"---\nid: {cid}\ntitle: An example truth\n{scope_line}{extra}"
        "sources:\n  - session: hallucinated-slug\n---\n\n"
        "## Claim\n\nSomething is true.\n\n## How to verify\n\nRun the thing.\n"
    )


class RouteCandidateScopeTest(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        # Resolved: macOS hands out /var/folders/... symlinked to /private/var.
        self.root = Path(tmp.name).resolve()
        self.truths = self.root / "truths"
        (self.truths / "loom").mkdir(parents=True)
        (self.truths / "ticket").mkdir()

    def test_no_declaration_stays_under_the_requested_scope(self):
        self.assertEqual(route_candidate_scope("", "loom", self.truths), ("loom", "", ""))

    def test_declaration_matching_the_request_says_nothing(self):
        self.assertEqual(route_candidate_scope("loom", "loom", self.truths), ("loom", "", ""))

    def test_declared_scope_with_a_directory_routes(self):
        scope, note, mismatch = route_candidate_scope("ticket", "loom", self.truths)
        self.assertEqual(scope, "ticket")
        self.assertIn("filed under ticket", note)
        # Filed under what it declared, so there is nothing to flag.
        self.assertEqual(mismatch, "")

    def test_declared_scope_without_a_directory_stays_and_is_flagged(self):
        scope, note, mismatch = route_candidate_scope("warp", "loom", self.truths)
        self.assertEqual(scope, "loom")
        self.assertIn("truths/warp/", note)
        self.assertIn("needs re-scoping", note)
        self.assertEqual(mismatch, "warp")

    def test_a_file_at_the_scope_path_is_not_a_scope(self):
        (self.truths / "weft").write_text("not a directory\n")

        scope, note, mismatch = route_candidate_scope("weft", "loom", self.truths)

        self.assertEqual(scope, "loom")
        self.assertIn("needs re-scoping", note)
        self.assertEqual(mismatch, "weft")

    def test_unusable_names_stay_and_touch_nothing(self):
        before = snapshot(self.root)

        for declared in ("../evil", "../../ticket", "Loom", " ", ".", "a/b", "-lead"):
            with self.subTest(declared=declared):
                scope, note, _ = route_candidate_scope(declared, "loom", self.truths)
                self.assertEqual(scope, "loom")
                self.assertIn("not a usable scope name", note)

        self.assertEqual(snapshot(self.root), before)

    def test_a_credential_declared_as_a_scope_is_not_echoed(self):
        for declared in (FAKE_TOKEN, FAKE_KEY):
            with self.subTest(declared=declared):
                scope, note, mismatch = route_candidate_scope(declared, "loom", self.truths)
                self.assertEqual(scope, "loom")
                self.assertIn("needs re-scoping", note)
                self.assertNotIn(declared, note)
                # Nor the head of it: redaction runs before the truncation.
                self.assertNotIn(declared[:20], note)
                self.assertIn("redacted", note)
                self.assertEqual(mismatch, "redacted")

    def test_a_long_declaration_is_bounded_in_the_note(self):
        declared = "a" * 200

        scope, note, mismatch = route_candidate_scope(declared, "loom", self.truths)

        self.assertEqual(scope, "loom")
        self.assertIn("needs re-scoping", note)
        self.assertIn("a" * SCOPE_ECHO_LIMIT, note)
        self.assertNotIn("a" * (SCOPE_ECHO_LIMIT + 1), note)
        self.assertEqual(mismatch, "a" * SCOPE_ECHO_LIMIT + "...")

    def test_a_cut_echo_says_it_was_cut(self):
        short = "a" * SCOPE_ECHO_LIMIT

        self.assertEqual(echo_scope(short), short)
        self.assertEqual(echo_scope(short + "a"), short + "...")

    def test_a_declaration_past_the_filesystem_limit_is_rejected_not_looked_up(self):
        # Over NAME_MAX (255 on APFS and ext4) and a clean NAME_PATTERN match, so
        # without the bound the lookup raises ENAMETOOLONG and kills the run.
        declared = "a" * 300

        scope, note, mismatch = route_candidate_scope(declared, "loom", self.truths)

        self.assertEqual(scope, "loom")
        self.assertIn("not a usable scope name", note)
        self.assertEqual(mismatch, "a" * SCOPE_ECHO_LIMIT + "...")

    def test_a_name_at_the_limit_is_still_looked_up(self):
        (self.truths / ("b" * SCOPE_NAME_LIMIT)).mkdir()

        scope, _, mismatch = route_candidate_scope("b" * SCOPE_NAME_LIMIT, "loom", self.truths)

        self.assertEqual(scope, "b" * SCOPE_NAME_LIMIT)
        self.assertEqual(mismatch, "")

    def test_a_lookup_that_errors_keeps_the_candidate_and_flags_it(self):
        # The errors the bound cannot cover: it holds the declaration, not the
        # store root the declaration is joined to, so an over-long root makes the
        # lookup raise however short the declaration is.
        unreachable = self.root / ("z" * 300) / "truths"

        scope, note, mismatch = route_candidate_scope("ticket", "loom", unreachable)

        self.assertEqual(scope, "loom")
        self.assertIn("could not be checked", note)
        self.assertIn("needs re-scoping", note)
        self.assertEqual(mismatch, "ticket")


class EmitCandidatesRoutingTest(unittest.TestCase):
    """The wiring: one destination per candidate, and a flag on what can't move."""

    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name).resolve()
        self.truths = self.root / "truths"
        (self.truths / "loom").mkdir(parents=True)
        (self.truths / "ticket").mkdir()
        self.base = self.root / "_candidates" / "truths"

    def emit(self, candidates):
        err = io.StringIO()
        with redirect_stderr(err):
            changes, routed = emit_candidates(candidates, self.base, "loom", "codex",
                                              "gpt-5", "low", SESSION, [], self.truths)
        return changes, routed, err.getvalue()

    def test_candidates_are_filed_under_the_scope_they_declare(self):
        onboarded = candidate("ticket-create-requires-a-store", "ticket")
        unknown = candidate("warp-renders-agents-md", "warp")
        home = candidate("loom-extracts-per-session", "loom")

        changes, routed, err = self.emit([onboarded, unknown, home])

        self.assertEqual([Path(ch["path"]).parent for ch in changes],
                         [self.base / "ticket", self.base / "loom", self.base / "loom"])
        self.assertEqual(routed, Counter({"loom": 2, "ticket": 1}))
        # The plan is not the write: nothing reached the filesystem.
        self.assertFalse(self.base.exists())
        self.assertIn("filed under ticket", err)

    def test_only_the_unroutable_candidate_carries_scope_mismatch(self):
        onboarded = candidate("ticket-create-requires-a-store", "ticket")
        unknown = candidate("warp-renders-agents-md", "warp")

        changes, _, _ = self.emit([onboarded, unknown])

        self.assertNotIn("scope_mismatch", changes[0]["body"])
        self.assertIn("\nscope_mismatch: warp\n", changes[1]["body"])
        # And the note reaches --json-out through the candidate's warnings.
        self.assertTrue(any("needs re-scoping" in w for w in unknown["warnings"]))
        self.assertFalse(any("needs re-scoping" in w for w in onboarded["warnings"]))

    def test_a_credential_declared_as_a_scope_reaches_no_output(self):
        leaky = candidate("loom-example", FAKE_TOKEN)

        changes, _, err = self.emit([leaky])

        self.assertNotIn(FAKE_TOKEN, err)
        self.assertNotIn(FAKE_TOKEN, "\n".join(leaky["warnings"]))
        self.assertNotIn(FAKE_TOKEN, changes[0]["body"])
        self.assertIn("\nscope_mismatch: redacted\n", changes[0]["body"])

    def test_a_model_emitted_scope_mismatch_never_survives(self):
        clean = candidate("loom-example", "ticket", extra="scope_mismatch: warp\n")
        flagged = candidate("warp-renders-agents-md", "warp",
                            extra="scope_mismatch: ticket\n")

        changes, _, _ = self.emit([clean, flagged])

        self.assertNotIn("scope_mismatch", changes[0]["body"])
        self.assertNotIn("scope_mismatch: ticket", changes[1]["body"])
        self.assertEqual(changes[1]["body"].count("scope_mismatch:"), 1)
        self.assertIn("\nscope_mismatch: warp\n", changes[1]["body"])

    def test_a_declaration_past_the_filesystem_limit_is_filed_not_raised(self):
        long_name = candidate("loom-example", "a" * 300)

        changes, routed, err = self.emit([long_name])

        self.assertEqual(Path(changes[0]["path"]).parent, self.base / "loom")
        self.assertEqual(routed, Counter({"loom": 1}))
        self.assertIn(f"\nscope_mismatch: {'a' * SCOPE_ECHO_LIMIT}...\n", changes[0]["body"])
        self.assertIn("not a usable scope name", err)

    def test_a_traversing_declaration_cannot_escape_the_candidates_directory(self):
        traversal = candidate("loom-example", "../../evil")

        changes, routed, _ = self.emit([traversal])

        self.assertEqual(Path(changes[0]["path"]).parent, self.base / "loom")
        self.assertEqual(routed, Counter({"loom": 1}))
        # Echoed for the reviewer, but as one conservative token.
        self.assertIn("\nscope_mismatch: .._.._evil\n", changes[0]["body"])


class RunLabelTest(unittest.TestCase):
    def test_a_single_scope_reads_as_it_always_has(self):
        self.assertEqual(
            run_label("truth", "loom", "92118425-cfeb-40ea", 6, Counter({"loom": 6})),
            "extract 92118425 | loom | 6 truth candidate(s)")

    def test_a_re_scoped_candidate_is_named_in_the_label(self):
        self.assertEqual(
            run_label("truth", "loom", "92118425-cfeb-40ea", 6,
                      Counter({"loom": 5, "ticket": 1})),
            "extract 92118425 | loom | 6 truth candidate(s) (1 → ticket)")


if __name__ == "__main__":
    unittest.main()
