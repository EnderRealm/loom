#!/usr/bin/env python3
"""Tests for the frontmatter keys extract.py injects into every candidate.

Run with: python3 -m unittest
Strict-YAML coverage needs PyYAML, a test-only dependency:
    uv run --with pyyaml python3 -m unittest

The strict-YAML fixture is a real decision-extractor response, saved verbatim,
rather than a hand-written candidate — what a strict loader rejects is decided
by what the model actually emits, not by what a fixture author expects it to.
"""
import tempfile
import unittest
from pathlib import Path

from extract import emit_candidates, inject_frontmatter, parse_output, parse_truth

try:
    import yaml
except ImportError:
    yaml = None

SESSION = "5c37b3b4-fb29-4e2e-8468-f63119e0d930"
INJECTED = ("status", "extracted_at", "extracted_by")
DECISION_SENTINEL = "===END-OF-DECISION==="
# A Cursor-session decision run's raw model output, byte for byte: two
# decisions, each emitting its own `status: candidate`.
CURSOR_DECISIONS = Path(__file__).parent / "testdata" / "cursor-decisions.raw.txt"

if yaml is not None:
    class UniqueKeyLoader(yaml.SafeLoader):
        """SafeLoader that rejects a duplicate mapping key instead of keeping
        the last one, the way strict YAML loaders do."""

        def construct_mapping(self, node, deep=False):
            seen = set()
            for key_node, _ in node.value:
                key = self.construct_object(key_node, deep=deep)
                if key in seen:
                    raise yaml.constructor.ConstructorError(
                        None, None, f"duplicate key {key!r}", key_node.start_mark)
                seen.add(key)
            return super().construct_mapping(node, deep)


def frontmatter_lines(body: str) -> list[str]:
    return body.split("\n---\n", 1)[0].removeprefix("---\n").split("\n")


def emit(candidates: list[dict], tmp: str) -> list[dict]:
    changes, _ = emit_candidates(candidates, Path(tmp), "loom", "claude", "sonnet",
                                 None, SESSION, [], Path(tmp) / "truths")
    return changes


class InjectFrontmatterTest(unittest.TestCase):
    def test_a_block_valued_key_is_replaced_whole_and_nested_keys_are_untouched(self):
        raw = ("---\nid: x\n\"status\" : |\n  validated\n\n  really\nsources:\n"
               "  - session: s\n    status: nested\n---\n\nstatus: prose\n")

        out = inject_frontmatter(raw, {"status": "candidate"})

        self.assertEqual(out, "---\nid: x\nsources:\n  - session: s\n    status: nested\n"
                              "status: candidate\n---\n\nstatus: prose\n")


class EmitCandidatesFrontmatterTest(unittest.TestCase):
    def test_injected_keys_override_model_emitted_ones_exactly_once(self):
        candidate = parse_truth(
            "---\nid: loom-example\ntitle: An example decision\nstatus: validated\n"
            "extracted_at: 1999-01-01T00:00:00\nextracted_by: model:self\n"
            "sources:\n  - session: hallucinated-slug\n---\n\n"
            "## Choice\n\nDo the thing.\n\n## Rationale\n\nBecause.\n")

        with tempfile.TemporaryDirectory() as tmp:
            changes = emit([candidate], tmp)

        self.assertEqual(len(changes), 1)
        lines = frontmatter_lines(changes[0]["body"])
        for key in INJECTED:
            self.assertEqual(sum(ln.startswith(f"{key}:") for ln in lines), 1, key)
        self.assertIn("status: candidate", lines)
        self.assertIn("extracted_by: claude:sonnet", lines)
        self.assertNotIn("extracted_at: 1999-01-01T00:00:00", lines)
        self.assertEqual(parse_truth(changes[0]["body"])["status"], "candidate")

    @unittest.skipUnless(yaml, "PyYAML not installed; run via `uv run --with pyyaml`")
    def test_real_extractor_output_parses_under_a_strict_yaml_loader(self):
        candidates = parse_output(CURSOR_DECISIONS.read_text(), DECISION_SENTINEL)
        self.assertEqual(len(candidates), 2)
        self.assertTrue(all(c["valid"] for c in candidates))

        with tempfile.TemporaryDirectory() as tmp:
            changes = emit(candidates, tmp)

        self.assertEqual(len(changes), 2)
        for change in changes:
            fm = "\n".join(frontmatter_lines(change["body"]))
            data = yaml.load(fm, Loader=UniqueKeyLoader)
            self.assertEqual(data["status"], "candidate")
            self.assertEqual(data["extracted_by"], "claude:sonnet")
            self.assertIn("extracted_at", data)
            lines = fm.split("\n")
            for key in INJECTED:
                self.assertEqual(sum(ln.startswith(f"{key}:") for ln in lines), 1, key)


if __name__ == "__main__":
    unittest.main()
