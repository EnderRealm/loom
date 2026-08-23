#!/usr/bin/env python3
"""Tests for ticket-id derivation and citation injection in extract.py.

Run with: python3 -m unittest

Fixtures are real jsonl files rather than mocks — the derivation's whole job is
to survive the shape of a session transcript (which records carry tool_use,
which carry tool_result, how result content is encoded, what git actually
prints), and a mocked reader would assert nothing about that.
"""
import io
import json
import tempfile
import unittest
from contextlib import redirect_stderr
from pathlib import Path

from extract import (
    MAX_SOURCE_TICKETS,
    emit_candidates,
    extract_ticket_ids,
    inject_source_tickets,
    parse_truth,
)

TICKET = "loom/embed-source-ticket-4932"
OTHER_TICKET = "loom/define-loom-project-2288"


def tool_use(tool_id: str, name: str = "Bash") -> dict:
    return {"type": "assistant", "message": {"content": [
        {"type": "tool_use", "id": tool_id, "name": name, "input": {"command": "git commit"}},
    ]}}


def tool_result(tool_id: str, content) -> dict:
    return {"type": "user", "message": {"content": [
        {"type": "tool_result", "tool_use_id": tool_id, "content": content},
    ]}}


def commit_line(ticket: str | None, subject: str = "Do the thing",
                branch: str = "main", sha: str = "2bbeb99") -> str:
    marker = f"[{ticket}] " if ticket else ""
    return f"[{branch} {sha}] {marker}{subject}"


class ExtractTicketIdsTest(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name)

    def write_jsonl(self, records, name="session.jsonl") -> Path:
        path = self.root / name
        path.write_text("".join(json.dumps(r) + "\n" for r in records))
        return path

    def bash_commit(self, tool_id: str, output) -> list[dict]:
        return [tool_use(tool_id), tool_result(tool_id, output)]

    def test_single_commit_yields_its_ticket_id(self):
        path = self.write_jsonl(self.bash_commit("t1", commit_line(TICKET)))

        self.assertEqual(extract_ticket_ids(path), [TICKET])

    def test_multiple_tickets_in_first_seen_order_deduplicated(self):
        records = (
            self.bash_commit("t1", commit_line(TICKET))
            + self.bash_commit("t2", commit_line(OTHER_TICKET))
            + self.bash_commit("t3", commit_line(TICKET, subject="Follow-up", sha="ab12cd3"))
        )
        path = self.write_jsonl(records)

        self.assertEqual(extract_ticket_ids(path), [TICKET, OTHER_TICKET])

    def test_two_commits_in_one_bash_result(self):
        output = "\n".join([commit_line(TICKET), " 1 file changed",
                            commit_line(OTHER_TICKET, sha="ab12cd3"), " 2 files changed"])
        path = self.write_jsonl(self.bash_commit("t1", output))

        self.assertEqual(extract_ticket_ids(path), [TICKET, OTHER_TICKET])

    def test_non_bash_tool_result_is_ignored(self):
        # A Read of a file quoting a commit line is not proof a commit landed.
        records = [tool_use("t1", name="Read"), tool_result("t1", commit_line(TICKET))]
        path = self.write_jsonl(records)

        self.assertEqual(extract_ticket_ids(path), [])

    def test_unmatched_tool_use_id_is_ignored(self):
        records = [tool_use("t1"), tool_result("t9", commit_line(TICKET))]
        path = self.write_jsonl(records)

        self.assertEqual(extract_ticket_ids(path), [])

    def test_git_own_branch_bracket_is_not_a_marker(self):
        # "[main 2bbeb99] Release v1.2.2" — the only bracket is git's own.
        path = self.write_jsonl(self.bash_commit("t1", commit_line(None, subject="Release v1.2.2")))

        self.assertEqual(extract_ticket_ids(path), [])

    def test_commit_without_marker_is_skipped(self):
        records = (
            self.bash_commit("t1", commit_line(None, subject="Fix typo"))
            + self.bash_commit("t2", commit_line(TICKET))
        )
        path = self.write_jsonl(records)

        self.assertEqual(extract_ticket_ids(path), [TICKET])

    def test_bare_ticket_mention_in_output_is_not_a_citation(self):
        # A transcript mentions ids constantly; only git's confirmation line
        # proves a commit landed, so a bare `[id]` token never cites.
        output = (f"Working on [{TICKET}] next.\n"
                  f"[{TICKET}] is the ticket for this change\n"
                  "nothing committed, working tree clean")
        path = self.write_jsonl(self.bash_commit("t1", output))

        self.assertEqual(extract_ticket_ids(path), [])

    def test_hostile_or_malformed_ids_are_rejected(self):
        hostile = [
            "loom/has space",           # whitespace
            "loom/has:colon",           # colon
            "nonamespace",              # not namespaced
            "../escape",                # traversal
            "loom/too/deep",            # more than one segment
            "/leading-slash",           # empty project
            "loom/",                    # empty slug
            "-loom/leading-dash",       # must start alphanumeric
        ]
        records = []
        for i, bad in enumerate(hostile):
            records.extend(self.bash_commit(f"t{i}", commit_line(bad)))
        path = self.write_jsonl(records)

        err = io.StringIO()
        with redirect_stderr(err):
            self.assertEqual(extract_ticket_ids(path), [])

        # A rejected marker is reported, so "no commits" and "markers all
        # rejected" don't read identically in the extractor log.
        for bad in hostile:
            self.assertIn(repr(bad), err.getvalue())

    def test_a_rejected_marker_is_reported_once_not_per_occurrence(self):
        records = []
        for i in range(3):
            records.extend(self.bash_commit(f"t{i}", commit_line("loom/has space")))
        path = self.write_jsonl(records)

        err = io.StringIO()
        with redirect_stderr(err):
            self.assertEqual(extract_ticket_ids(path), [])

        self.assertEqual(err.getvalue().count("malformed ticket marker"), 1)

    def test_newline_in_marker_is_rejected(self):
        # A newline splits the line before the marker can close its bracket.
        output = "[main 2bbeb99] [loom/inject\nnewline-4932] Subject"
        path = self.write_jsonl(self.bash_commit("t1", output))

        self.assertEqual(extract_ticket_ids(path), [])

    def test_list_shaped_tool_result_content_is_normalized(self):
        content = [{"type": "text", "text": commit_line(TICKET)}]
        path = self.write_jsonl(self.bash_commit("t1", content))

        self.assertEqual(extract_ticket_ids(path), [TICKET])

    def test_bare_string_parts_are_normalized_like_preprocess_does(self):
        # preprocess.py's _process_user collects bare string parts too; two
        # readers of the same file shape must not disagree about it.
        content = [commit_line(TICKET), {"type": "text", "text": commit_line(OTHER_TICKET)}]
        path = self.write_jsonl(self.bash_commit("t1", content))

        self.assertEqual(extract_ticket_ids(path), [TICKET, OTHER_TICKET])

    def test_collection_stops_at_the_cap_and_warns(self):
        records = []
        for i in range(MAX_SOURCE_TICKETS + 10):
            records.extend(self.bash_commit(f"t{i}", commit_line(f"loom/flood-{i:04d}")))
        path = self.write_jsonl(records)

        err = io.StringIO()
        with redirect_stderr(err):
            ids = extract_ticket_ids(path)

        self.assertEqual(len(ids), MAX_SOURCE_TICKETS)
        self.assertEqual(ids[0], "loom/flood-0000")
        self.assertIn(f"hit the {MAX_SOURCE_TICKETS}-ticket cap", err.getvalue())

    def test_hook_preamble_before_the_commit_line_still_resolves(self):
        # Why we read the raw jsonl: preprocess.py truncates non-error results
        # to 500 chars, and hook output can push the commit line past that.
        preamble = "\n".join(f"hook: check {i} passed" for i in range(60))
        output = f"{preamble}\n{commit_line(TICKET)}\n 3 files changed, 12 insertions(+)"
        self.assertGreater(len(output), 500)
        path = self.write_jsonl(self.bash_commit("t1", output))

        self.assertEqual(extract_ticket_ids(path), [TICKET])

    def test_summary_markdown_yields_no_ids(self):
        # main() gates the call on the resolved input format; this is the
        # second line of defence — no line of a summary parses as a record.
        path = self.root / "summary.md"
        path.write_text(f"---\nsession_id: abc\n---\n\n{commit_line(TICKET)}\n")

        self.assertEqual(extract_ticket_ids(path), [])

    def test_missing_file_warns_on_stderr_instead_of_failing(self):
        err = io.StringIO()
        with redirect_stderr(err):
            ids = extract_ticket_ids(self.root / "gone.jsonl")

        self.assertEqual(ids, [])
        self.assertIn("could not derive ticket ids", err.getvalue())

    def test_corrupt_jsonl_returns_no_ids_without_raising(self):
        path = self.root / "corrupt.jsonl"
        path.write_bytes(b"not json at all\n\xff\xfe binary\n{\"type\": \"user\"\n")

        self.assertEqual(extract_ticket_ids(path), [])

    def test_undecodable_lines_do_not_lose_the_readable_ones(self):
        path = self.root / "mixed.jsonl"
        good = "".join(json.dumps(r) + "\n" for r in self.bash_commit("t1", commit_line(TICKET)))
        path.write_bytes(b"{ truncated\n\n" + good.encode())

        self.assertEqual(extract_ticket_ids(path), [TICKET])

    def test_unexpected_record_shapes_are_tolerated(self):
        records = [
            "a bare string record",
            {"type": "user", "message": "not a dict"},
            {"type": "assistant", "message": {"content": "not a list"}},
            {"type": "user", "message": {"content": [None, 7]}},
            {"type": "summary", "summary": "whatever"},
        ]
        path = self.write_jsonl(records + self.bash_commit("t1", commit_line(TICKET)))

        self.assertEqual(extract_ticket_ids(path), [TICKET])


TRUTH = """---
id: loom-example
title: An example truth
sources:
  - session: 5a28d3d6-cfeb-40ea-872f-15c0b87ea541
    project: loom
    date: 2026-08-14
    role: surfaced the mechanism
related: []
verified_at: 2026-08-14
---

## Claim

Something is true.
"""


class InjectSourceTicketsTest(unittest.TestCase):
    def test_entries_land_at_the_end_of_the_sources_block(self):
        out = inject_source_tickets(TRUTH, [TICKET, OTHER_TICKET])

        # The entries sit after the block's indented continuation lines and
        # before the next top-level key — not appended to the frontmatter.
        self.assertEqual(out, TRUTH.replace(
            "related: []",
            f"  - ticket: {TICKET}\n  - ticket: {OTHER_TICKET}\nrelated: []",
        ))

    def test_following_top_level_keys_are_not_displaced(self):
        out = inject_source_tickets(TRUTH, [TICKET])

        self.assertIn("\nrelated: []\nverified_at: 2026-08-14\n---\n", out)
        self.assertTrue(out.endswith("## Claim\n\nSomething is true.\n"))

    def test_missing_sources_block_is_created(self):
        raw = "---\nid: loom-example\nverified_at: 2026-08-14\n---\n\n## Claim\n"

        out = inject_source_tickets(raw, [TICKET])

        self.assertEqual(out, "---\nid: loom-example\nverified_at: 2026-08-14\n"
                              f"sources:\n  - ticket: {TICKET}\n---\n\n## Claim\n")

    def test_model_emitted_ticket_entry_is_replaced(self):
        raw = TRUTH.replace("related: []", "  - ticket: loom/hallucinated-0000\nrelated: []")

        out = inject_source_tickets(raw, [TICKET])

        self.assertNotIn("hallucinated", out)
        self.assertEqual(out.count("ticket:"), 1)

    def test_spaced_and_quoted_ticket_keys_are_replaced_too(self):
        # YAML accepts both shapes for the same key, so both have to be caught.
        for entry in ('  - ticket : loom/hallucinated-0000',
                      '  - "ticket": loom/hallucinated-0000'):
            with self.subTest(entry=entry):
                raw = TRUTH.replace("related: []", f"{entry}\nrelated: []")

                out = inject_source_tickets(raw, [TICKET])

                self.assertNotIn("hallucinated", out)
                self.assertEqual(out.count("ticket:"), 1)

    def test_ticket_inside_a_prose_value_is_untouched(self):
        raw = TRUTH.replace("role: surfaced the mechanism",
                            "role: explains why the ticket: key exists")

        out = inject_source_tickets(raw, [TICKET])

        self.assertIn("role: explains why the ticket: key exists", out)

    def test_flush_left_sequence_items_do_not_split_the_block(self):
        # `- session:` at column 0 is valid YAML under `sources:`; our entries
        # must still land after it, not ahead of it.
        raw = TRUTH.replace("  - session:", "- session:").replace(
            "    project:", "  project:").replace(
            "    date:", "  date:").replace("    role:", "  role:")

        out = inject_source_tickets(raw, [TICKET])

        self.assertIn(f"  role: surfaced the mechanism\n  - ticket: {TICKET}\nrelated: []", out)

    def test_no_ticket_entries_and_no_ids_leaves_the_artifact_unchanged(self):
        self.assertEqual(inject_source_tickets(TRUTH, []), TRUTH)

    def test_model_emitted_ticket_entry_is_stripped_even_with_no_derived_ids(self):
        # The common case: a session that landed no commits derives no ids, and
        # a model-emitted id has passed no validation at all.
        raw = TRUTH.replace("related: []", "  - ticket: loom/hallucinated-0000\nrelated: []")

        out = inject_source_tickets(raw, [])

        self.assertEqual(out, TRUTH)

    def test_missing_frontmatter_is_returned_unchanged(self):
        raw = "## Claim\n\nNo frontmatter here.\n"

        self.assertEqual(inject_source_tickets(raw, [TICKET]), raw)


class EmitCandidatesTest(unittest.TestCase):
    """The wiring, not the helpers: override → tickets → frontmatter."""

    def test_candidate_without_a_sources_block_gets_session_and_tickets(self):
        session = "5a28d3d6-cfeb-40ea-872f-15c0b87ea541"
        candidate = {"id": "loom-example", "raw": (
            "---\nid: loom-example\ntitle: An example truth\nstatus: validated\n---\n\n"
            "## Claim\n\nSomething is true.\n\n## How to verify\n\nRun the thing.\n"
        )}

        with tempfile.TemporaryDirectory() as tmp:
            # emit_candidates builds the writes; the store performs them.
            changes = emit_candidates([candidate], Path(tmp), "loom", "codex", "gpt-5",
                                      "low", session, [TICKET, OTHER_TICKET])

            self.assertEqual(len(changes), 1)
            parsed = parse_truth(changes[0]["body"], source=changes[0]["path"])

        self.assertTrue(parsed["valid"])
        self.assertEqual(parsed["source_sessions"], [session])
        # The override appends `sources:`, the tickets land inside it, and the
        # top-level keys land after — so `status:` is last-write-wins.
        self.assertIn(f"sources:\n  - session: {session}\n"
                      f"  - ticket: {TICKET}\n  - ticket: {OTHER_TICKET}\n", parsed["raw"])
        self.assertEqual(parsed["status"], "candidate")

    def test_inline_empty_sources_does_not_capture_the_ticket_entries(self):
        # `sources: []` mimics the template's `related: []`. It carries no
        # `session:`, so the override appends a second block — and the tickets
        # belong in that one, not as items of an already-closed empty list.
        session = "5a28d3d6-cfeb-40ea-872f-15c0b87ea541"
        candidate = {"id": "loom-example", "raw": (
            "---\nid: loom-example\ntitle: An example truth\nsources: []\n---\n\n"
            "## Claim\n\nSomething is true.\n\n## How to verify\n\nRun the thing.\n"
        )}

        with tempfile.TemporaryDirectory() as tmp:
            changes = emit_candidates([candidate], Path(tmp), "loom", "codex", "gpt-5",
                                      "low", session, [TICKET])

            self.assertEqual(len(changes), 1)
            parsed = parse_truth(changes[0]["body"], source=changes[0]["path"])

        self.assertIn(f"sources: []\nsources:\n  - session: {session}\n"
                      f"  - ticket: {TICKET}\n", parsed["raw"])


if __name__ == "__main__":
    unittest.main()
