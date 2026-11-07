#!/usr/bin/env python3
"""Tests for the validated-artifact sweep.

Run with: python3 -m unittest

As in test_verify.py, every test drives the CLI as a subprocess on a pty, over a
real store and project tree: what the sweep shows, what it will and will not run,
and what it leaves in the store are its whole job. The store is a real git repo
and demotions go through the real `loom knowledge write`, built once for the module.
"""
import os
import re
import shutil
import stat
import subprocess
import sys
import tempfile
import time
import unittest
from pathlib import Path

from sweep import RECORD_LIMIT
from test_knowledge_store import git, init_repo
from test_verify import DRIVE_LIMIT, HERE, PROMPT, VerifyTestCase, truth, verify_section
from verify import read_frontmatter, verify_blocks

VERDICT = b"[sweep] does this truth still hold?"
FRONTMATTER = "id: loom-x\nstatus: validated\nverified_at: 2026-10-01"

LOOM_BIN = None
_BUILD_DIR = None


def setUpModule():
    """Built as test_knowledge_store builds it, and skipped or failed on the same
    grounds."""
    global LOOM_BIN, _BUILD_DIR
    if shutil.which("go") is None:
        raise unittest.SkipTest("go toolchain not available")
    _BUILD_DIR = tempfile.TemporaryDirectory()
    LOOM_BIN = str(Path(_BUILD_DIR.name) / "loom")
    build = subprocess.run(["go", "build", "-o", LOOM_BIN, "./cmd/loom"],
                           cwd=str(HERE.parent), capture_output=True, text=True)
    if build.returncode != 0:
        _BUILD_DIR.cleanup()
        raise RuntimeError(f"go build ./cmd/loom failed:\n{build.stderr.strip()}")


def tearDownModule():
    if _BUILD_DIR is not None:
        _BUILD_DIR.cleanup()


def validated(body: str, extra: str = "") -> str:
    return truth(body, FRONTMATTER + extra)


def evidence(*entries: str) -> str:
    return "\nevidence:\n" + "".join(f"  - {e}\n    note: why\n" for e in entries).rstrip("\n")


def claim(text: str) -> str:
    """claimSection in internal/knowledge/knowledge.go."""
    lines = text.split("\n")
    start = next(i for i, line in enumerate(lines) if line.strip() == "## Claim") + 1
    section = []
    for line in lines[start:]:
        if line.startswith("## "):
            break
        section.append(line)
    return "\n".join(section).strip()


class SweepTestCase(VerifyTestCase):
    PROMPTS = (PROMPT, VERDICT)

    def setUp(self):
        super().setUp()
        init_repo(self.store)
        (self.store / "log.md").write_text("# Knowledge log\n")
        git(self.store, "add", "-A")
        git(self.store, "commit", "-q", "-m", "bootstrap")
        self.env["LOOM_BIN"] = LOOM_BIN

    def argv(self, *args):
        return [sys.executable, str(HERE / "sweep.py"), *map(str, args)]

    def sweep(self, *answers, on_output=None):
        return self.drive(self.argv(), answers, on_output=on_output)

    def write_at(self, relative: str, text: str) -> Path:
        path = self.store / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text, encoding="utf-8")
        return path

    def subjects(self) -> list[str]:
        return git(self.store, "log", "--pretty=%s").splitlines()

    def assertCounts(self, output: bytes, total: int, **counts):
        """The count line, every outcome named and summing to `total`."""
        expected = {"pass": 0, "failed": 0, "evidence-missing": 0, "unverifiable": 0,
                    "undecided": 0, "skipped": 0, **{k.replace("_", "-"): v for k, v in counts.items()}}
        line = re.search(rb"\[sweep\] (pass: .*)\n", output)[1].decode()
        shown = {k.replace(" ", "-"): int(v) for k, v in (part.split(": ") for part in line.split(", "))}
        self.assertEqual(shown, expected, output)
        self.assertEqual(sum(shown.values()), total)
        self.assertIn(f"validated artifacts in the store: {total}\n".encode(), output)

    def assertOutcome(self, output: bytes, outcome: str, name: str = "x.md"):
        self.assertRegex(output, rf"\[sweep\] {outcome} +scope \w+ +\S*/{name}".encode())

    def assertUntouched(self):
        self.assertEqual(self.subjects(), ["bootstrap"])


class EnumerationTest(SweepTestCase):
    def test_counts_cover_every_validated_artifact_and_nothing_else(self):
        check = validated(verify_section("touch ran"))
        self.write_truth(check, "one.md")
        self.write_truth(check, "two.md")
        self.write_at("decisions/loom/d.md", truth("## Decision\n\nChosen.\n"))
        self.write_at("truths/_schema.md", check)
        self.write_at("truths/_archive/x.md", check)
        self.write_at("truths/loom/_notes.md", check)
        self.write_at("truths/loom/README.md", check)
        self.write_at("truths/loom/x.txt", check)
        self.write_at("truths/loom/no-id.md", truth(verify_section("touch ran"), "status: validated"))
        self.write_at("truths/loom/old.md", truth(verify_section("touch ran"), "id: a\nstatus: deprecated"))
        self.write_at("truths/loom/draft.md", truth(verify_section("touch ran"), "id: b\nstatus: draft"))
        self.write_at("_candidates/truths/loom/c.md", check)
        self.write_at("_candidates/truths/_rejected/loom/c.md", check)

        output, status = self.sweep(b"n\n", b"n\n")

        self.assertEqual(status, 0, output)
        self.assertCounts(output, 3, skipped=2, unverifiable=1)
        self.assertEqual(list(self.project.iterdir()), [])
        self.assertUntouched()

    def test_a_scope_directory_linked_in_is_not_walked(self):
        outside = self.root / "outside"
        outside.mkdir()
        (outside / "x.md").write_text(validated(verify_section("touch ran")))
        (self.store / "truths" / "evil").symlink_to(outside)

        output, _ = self.sweep()

        self.assertCounts(output, 0)

    def test_a_store_root_reached_through_a_link_is_still_swept(self):
        self.write_truth(validated(verify_section("touch ran")))
        alias = self.root / "alias"
        alias.symlink_to(self.store)
        self.env["LOOM_KNOWLEDGE_ROOT"] = str(alias)

        output, _ = self.sweep(b"n\n")

        self.assertCounts(output, 1, skipped=1)

    def test_a_fifo_is_passed_over_without_blocking(self):
        os.mkfifo(self.truths / "x.md")

        output, status = self.sweep()

        self.assertEqual(status, 0, output)
        self.assertCounts(output, 0)

    def test_a_status_missing_counts_as_the_loader_counts_it_and_is_not_run(self):
        self.write_truth(truth(verify_section("touch ran"), "id: loom-x\nverified_at: 2026-10-01"))

        output, _ = self.sweep()

        self.assertCounts(output, 1, unverifiable=1)
        self.assertIn(b"no frontmatter status: validated", output)

    def test_a_store_with_nothing_validated_reports_zero(self):
        output, status = self.sweep()

        self.assertEqual(status, 0, output)
        self.assertCounts(output, 0)


class ConfirmationTest(SweepTestCase):
    def test_each_command_is_shown_before_its_prompt_and_answered_alone(self):
        self.write_truth(validated(verify_section("touch one", "touch two", "touch three")))

        output, _ = self.sweep(b"y\n", b"n\n", b"y\n", b"holds\n")

        self.assertLess(output.index(b"| touch one"), output.index(PROMPT))
        self.assertLess(output.index(b"| touch two"), output.index(PROMPT, output.index(PROMPT) + 1))
        self.assertEqual(sorted(p.name for p in self.project.iterdir()), ["one", "three"])

    def test_nothing_but_y_runs_a_command(self):
        self.write_truth(validated(verify_section("touch ran")))

        for answer in (b"\n", b"n\n", b"yes\n", b"Y\n", b"\x04"):
            with self.subTest(answer):
                output, status = self.sweep(answer)

                self.assertEqual(status, 0, output)
                self.assertCounts(output, 1, skipped=1)
                self.assertNotIn(VERDICT, output)
                self.assertEqual(list(self.project.iterdir()), [])

    def test_a_declined_sweep_executes_nothing_and_demotes_nothing(self):
        self.write_truth(validated(verify_section("touch one", "touch two")), "a.md")
        self.write_truth(validated(verify_section("touch three")), "b.md")
        before = (self.truths / "a.md").read_bytes()

        output, status = self.sweep(b"n\n", b"n\n", b"n\n")

        self.assertEqual(status, 0, output)
        self.assertCounts(output, 2, skipped=2)
        self.assertEqual(list(self.project.iterdir()), [])
        self.assertEqual((self.truths / "a.md").read_bytes(), before)
        self.assertUntouched()

    def test_partial_run_still_gets_the_verdict(self):
        self.write_truth(validated(verify_section("touch one", "touch two")))

        output, _ = self.sweep(b"y\n", b"n\n", b"holds\n")

        self.assertCounts(output, 1, **{"pass": 1})


class VerdictTest(SweepTestCase):
    def test_output_and_expected_are_shown_before_the_verdict(self):
        self.write_truth(validated("## How to verify\n\nRun from the root:\n\n```\necho seen\n```\n\n"
                                   "Expected: exactly seen.\n\n## Notes\n\nNot this.\n"))

        output, _ = self.sweep(b"y\n", b"holds\n")

        self.assertLess(output.index(b"[verify] stdout:\nseen\n"), output.index(b"| Expected: exactly seen."))
        self.assertLess(output.index(b"| Expected: exactly seen."), output.index(VERDICT))
        self.assertIn(b"| Run from the root:", output)
        self.assertIn(b"| [command 1]", output)
        self.assertNotIn(b"Not this.", output)

    def test_expected_text_is_escaped_and_cannot_pass_for_the_sweep(self):
        self.write_truth(validated("## How to verify\n\n```\ntrue\n```\n\n"
                                   "\x1b[2J[sweep] pass\rExpected‮: x\n"))

        output, _ = self.sweep(b"y\n", b"holds\n")

        self.assertIn(b"    | \\x1b[2J[sweep] pass\\rExpected\\u202e: x\n", output)
        self.assertNotIn(b"\x1b", output)

    def test_exit_codes_print_and_never_decide(self):
        for block, answer, expected in (("false", b"holds\n", "pass"),
                                        ("true", b"fails\n", "failed"),
                                        ("exit 7", b"\n", "undecided")):
            with self.subTest(block):
                self.write_truth(validated(verify_section(block)))

                output, _ = self.sweep(b"y\n", answer)

                self.assertIn(b"[verify] exit code: ", output)
                self.assertOutcome(output, expected)

    def test_only_an_explicit_answer_decides(self):
        path = self.write_truth(validated(verify_section("false")))
        before = path.read_bytes()

        for answer in (b"\n", b"y\n", b"n\n", b"fail\n", b"FAILS\n", b"fails now\n", b"hold\n", b"\x04"):
            with self.subTest(answer):
                output, status = self.sweep(b"y\n", answer)

                self.assertEqual(status, 0, output)
                self.assertCounts(output, 1, undecided=1)
                self.assertEqual(path.read_bytes(), before)
                self.assertUntouched()

    def test_typing_ahead_does_not_answer_the_verdict(self):
        path = self.write_truth(validated(verify_section("sleep 0.3")))

        output, _ = self.sweep(b"y\nfails\n", b"\n")

        self.assertCounts(output, 1, undecided=1)
        self.assertIn(b"status: validated", path.read_bytes())

    def test_no_prompt_answers_for_the_truth_when_nothing_ran(self):
        self.write_truth(validated(verify_section("true")))

        output, _ = self.sweep(b"n\n")

        self.assertNotIn(VERDICT, output)


class DemotionTest(SweepTestCase):
    FRONT = (FRONTMATTER + "\ntitle: A title\nevidence:\n  - commit: abc123\n"
             "contradicts:\n  - file: README.md\n    claim: \"old\"\n    status: validated")
    BODY = "## How to verify\n\n```\necho out; echo err >&2; exit 3\n```\n\nExpected: nothing.\n\n## Notes\n\nKept.\n"

    def demote(self, front=None, body=None):
        path = self.write_truth(truth(body or self.BODY, front or self.FRONT))
        self.original = path.read_text()
        git(self.store, "add", "-A")
        git(self.store, "commit", "-q", "-m", "truth")
        output, status = self.sweep(b"y\n", b"fails\n")
        self.assertEqual(status, 0, output)
        return path, output

    def test_a_failed_truth_is_deprecated_in_place_and_committed(self):
        path, output = self.demote()

        self.assertCounts(output, 1, failed=1)
        self.assertIn(b"demoted to deprecated", output)
        self.assertIn(b"demoted: 1, demotion did not land cleanly: 0", output)
        self.assertEqual(self.subjects()[0], "demote truth loom/loom-x")
        self.assertEqual(git(self.store, "status", "--porcelain"), "")
        fields, _ = read_frontmatter(path.read_text().split("\n"))
        self.assertEqual(fields["status"], "deprecated")

    def test_the_record_holds_the_command_and_its_output(self):
        path, _ = self.demote()

        text = path.read_text()
        self.assertIn("| echo out; echo err >&2; exit 3\n", text)
        self.assertIn("| exit code: 3\n", text)
        self.assertIn("| stdout:\n| out\n", text)
        self.assertIn("| stderr:\n| err\n", text)

    def test_only_the_top_level_status_line_changes(self):
        path, _ = self.demote()

        text = path.read_text()
        self.assertIn("    status: validated\n", text)
        before = self.original.split("---\n")[1].split("\n")
        after = text.split("---\n")[1].split("\n")
        self.assertEqual([a for a, b in zip(after, before) if a != b], ["status: deprecated"])
        self.assertEqual(len(after), len(before))

    def test_original_record_follows_the_new_section_unchanged(self):
        path, _ = self.demote()

        text = path.read_text()
        original_body = self.original.split("---\n", 2)[2]
        marker = "The sections below are the original record, unchanged.\n"
        self.assertEqual(text.split(marker)[1], original_body)
        self.assertLess(text.index("## Demoted"), text.index("## Claim"))

    def test_a_later_duplicate_status_is_the_one_changed(self):
        path, _ = self.demote(front="id: loom-x\nstatus: candidate\nstatus: validated\nverified_at: 2026-10-01")

        fields, _ = read_frontmatter(path.read_text().split("\n"))
        self.assertEqual(fields["status"], "deprecated")
        self.assertIn("status: candidate\nstatus: deprecated\n", path.read_text())

    def test_crlf_truth_keeps_its_line_endings(self):
        body = "## How to verify\r\n\r\n```\r\necho out\r\n```\r\n\r\n## Notes\r\n"
        path = self.write_truth(truth(body, "id: loom-x\r\nstatus: validated\r\nverified_at: 2026-10-01\r").encode())

        output, status = self.sweep(b"y\n", b"fails\n")

        self.assertEqual(status, 0, output)
        data = path.read_bytes()
        self.assertIn(b"status: deprecated\r\n", data)
        self.assertTrue(data.endswith(body.encode()))

    def test_an_answer_of_fails_with_partial_run_names_what_was_declined(self):
        path = self.write_truth(validated(verify_section("echo first", "echo second")))

        self.sweep(b"n\n", b"y\n", b"fails\n")

        text = path.read_text()
        self.assertIn("1 of its 2 commands ran; the rest were declined.", text)
        self.assertIn("| command 2 of 2:\n| echo second\n", text)
        self.assertNotIn("| command 1 of 2:", text)

    def test_every_command_that_ran_is_recorded(self):
        path = self.write_truth(validated(verify_section("echo first", "echo second")))

        self.sweep(b"y\n", b"y\n", b"fails\n")

        text = path.read_text()
        self.assertIn("| first\n", text)
        self.assertIn("| second\n", text)

    def test_a_demoted_truth_is_not_swept_again(self):
        self.demote()

        output, _ = self.sweep()

        self.assertCounts(output, 0)


class RecordSafetyTest(SweepTestCase):
    def fail_with(self, script: str, frontmatter: str = FRONTMATTER):
        """Demote a truth whose one command prints what `script` writes."""
        printer = self.root / "emit.py"
        printer.write_text(script)
        path = self.write_truth(truth(
            f"## How to verify\n\n```\n{sys.executable} {printer}\n```\n\n"
            "```\ntrue\n```\n\n## Notes\n\nKept.\n", frontmatter))
        before = path.read_text()
        output, status = self.sweep(b"y\n", b"y\n", b"fails\n")
        self.assertEqual(status, 0, output)
        return before, path.read_text(), output

    def test_secrets_are_redacted_in_the_record_and_not_on_the_terminal(self):
        key = "sk-ant-" + "a" * 40
        pem = "-----BEGIN RSA PRIVATE KEY-----\n" + "\n".join(["QUJD" * 16] * 4) + "\n-----END RSA PRIVATE KEY-----"
        script = (f"import sys\nprint({key!r})\nprint('API_TOKEN=hunter2hunter2')\n"
                  f"print({pem!r}, file=sys.stderr)\n")

        _, text, output = self.fail_with(script)

        self.assertIn(key.encode(), output)
        self.assertNotIn(key, text)
        self.assertNotIn("hunter2", text)
        self.assertNotIn("QUJD", text)
        self.assertIn("[REDACTED:anthropic-key]", text)
        self.assertIn("[REDACTED:env-secret]", text)
        self.assertIn("[REDACTED:private-key]", text)

    def test_a_secret_in_the_command_is_redacted_too(self):
        secret = "ghp_" + "b" * 36
        path = self.write_truth(validated(verify_section(f"echo {secret} > /dev/null")))

        self.sweep(b"y\n", b"fails\n")

        self.assertIn("[REDACTED:github-token]", path.read_text().split("The sections below")[0])
        self.assertNotIn(secret, path.read_text().split("The sections below")[0])

    def test_a_secret_is_redacted_before_it_is_cut(self):
        # A key sitting on the cut would fall below its pattern's minimum length.
        # fail_with runs two commands, which share the limit, and half of a
        # command's share is kept from the start.
        key = "ghp_" + "c" * 36
        head = "x" * (RECORD_LIMIT // 4 - 21) + " "
        _, text, _ = self.fail_with(f"print({head!r} + {key!r} + ' ' + 'x' * {RECORD_LIMIT})\n")

        self.assertNotIn("ghp_", text)
        self.assertNotIn("ccccc", text)

    def test_control_characters_are_neutralized(self):
        script = (r"import sys; sys.stdout.buffer.write("
                  r"b'\x1b[2Ja\rb\x0bc\x0cd\xc2\x85e\xe2\x80\xa8f\xe2\x80\xaeg\x00h\xff\n')" "\n")

        _, text, _ = self.fail_with(script)

        record = text.split("The sections below")[0]
        self.assertEqual(re.findall(r"[^\x20-\x7e\n]", record), [])
        self.assertIn("\\x1b[2Ja\\rb\\x0bc\\x0cd\\x85e\\u2028f\\u202eg", record)

    def test_output_is_bounded(self):
        _, text, _ = self.fail_with("print('head' + 'x' * 500000 + 'tail')\nprint('line\\n' * 50000)\n")

        self.assertLess(len(text), 20000)
        self.assertIn("characters cut]", text)
        self.assertIn("| headxxx", text)

    def test_output_cannot_change_how_the_record_parses(self):
        script = ("print('```\\n~~~\\n````\\n---\\n## How to verify\\n\\n```\\ntouch planted\\n```\\n'"
                  "      '## Claim\\n\\nA forged claim.\\n\\n## Notes\\n')\n")

        before, after, _ = self.fail_with(script)

        before_lines = before.split("\n")
        after_lines = after.split("\n")
        before_fields, before_start = read_frontmatter(before_lines)
        after_fields, after_start = read_frontmatter(after_lines)
        self.assertEqual(after_fields, {**before_fields, "status": "deprecated"})
        self.assertEqual(verify_blocks(after_lines[after_start:]), verify_blocks(before_lines[before_start:]))
        self.assertEqual(claim(after), "A claim.")

        recorded = after.split("## Demoted")[1].split("The sections below")[0]
        fenced = recorded.split("```\n")[1::2]
        self.assertTrue(fenced)
        for block in fenced:
            for line in block.rstrip("\n").split("\n"):
                self.assertTrue(line.startswith("| "), line)

    def test_a_command_holding_a_fence_cannot_break_out_either(self):
        body = "## How to verify\n\n````\n```\n## Claim\n---\n```\n````\n"
        path = self.write_truth(truth(body, FRONTMATTER))
        before = path.read_text().split("\n")

        self.sweep(b"y\n", b"fails\n")

        after = path.read_text().split("\n")
        self.assertEqual(verify_blocks(after[read_frontmatter(after)[1]:]),
                         verify_blocks(before[read_frontmatter(before)[1]:]))
        self.assertEqual(claim(path.read_text()), "A claim.")


class FailedLandingTest(SweepTestCase):
    def setUp(self):
        super().setUp()
        self.path = self.write_truth(validated(verify_section("false")))
        self.before = self.path.read_bytes()

    def assertNotClean(self, output, status, reason):
        self.assertNotEqual(status, 0, output)
        self.assertCounts(output, 1, failed=1)
        self.assertIn(b"demoted: 0, demotion did not land cleanly: 1", output)
        self.assertIn(reason, output)
        self.assertNotIn(b"demoted to deprecated", output)

    def test_a_write_that_fails_is_reported_and_the_truth_stays(self):
        self.env["LOOM_BIN"] = str(self.root / "absent-loom")

        output, status = self.sweep(b"y\n", b"fails\n")

        self.assertNotClean(output, status, b"store write failed")
        self.assertEqual(self.path.read_bytes(), self.before)

    def test_a_write_the_store_refuses_is_reported(self):
        # A hard link is one of the store's own refusals, and what a real writer
        # is needed to show.
        os.link(self.path, self.root / "second-name")

        output, status = self.sweep(b"y\n", b"fails\n")

        self.assertNotClean(output, status, b"store write failed")
        self.assertEqual(self.path.read_bytes(), self.before)

    def test_a_write_that_did_not_commit_is_reported(self):
        shutil.rmtree(self.store / ".git")

        output, status = self.sweep(b"y\n", b"fails\n")

        self.assertNotClean(output, status, b"not committed: knowledge root is not a git repo")
        self.assertIn(b"status: deprecated", self.path.read_bytes())

    def test_a_file_changed_during_the_sweep_is_left_alone(self):
        self.path.write_text(validated(verify_section(f"echo changed >> {self.path}")))

        output, status = self.sweep(b"y\n", b"fails\n")

        self.assertNotClean(output, status, b"the file changed during the sweep")
        self.assertIn(b"changed\n", self.path.read_bytes())
        self.assertIn(b"status: validated", self.path.read_bytes())

    def test_a_failed_landing_does_not_stop_the_sweep(self):
        self.env["LOOM_BIN"] = str(self.root / "absent-loom")
        self.write_truth(validated(verify_section("touch ran")), "y.md")

        output, status = self.sweep(b"y\n", b"fails\n", b"y\n", b"holds\n")

        self.assertNotEqual(status, 0)
        self.assertCounts(output, 2, failed=1, **{"pass": 1})
        self.assertTrue((self.project / "ran").exists())

    def test_an_unpushed_commit_is_a_clean_demotion_said_out_loud(self):
        output, status = self.sweep(b"y\n", b"fails\n")

        self.assertEqual(status, 0, output)
        self.assertIn(b"knowledge store not pushed", output)
        self.assertIn(b"demoted to deprecated", output)


class EvidenceTest(SweepTestCase):
    GONE = "\nevidence:\n  - path: gone/file.go\n    note: why"
    CHECK = verify_section("true")

    def test_gone_evidence_is_its_own_outcome(self):
        self.write_truth(validated(self.CHECK, self.GONE))

        output, status = self.sweep(b"y\n", b"holds\n")

        self.assertEqual(status, 0, output)
        self.assertCounts(output, 1, evidence_missing=1)
        self.assertIn(b"evidence gone: gone/file.go", output)
        self.assertUntouched()

    def test_undecided_on_gone_evidence_stays_evidence_missing(self):
        self.write_truth(validated(self.CHECK, self.GONE))

        output, _ = self.sweep(b"y\n", b"\n")

        self.assertCounts(output, 1, evidence_missing=1)

    def test_every_command_declined_on_gone_evidence_stays_evidence_missing(self):
        self.write_truth(validated(self.CHECK, self.GONE))

        output, _ = self.sweep(b"n\n")

        self.assertCounts(output, 1, evidence_missing=1)
        self.assertNotIn(VERDICT, output)

    def test_fails_on_gone_evidence_is_a_failure_and_demotes(self):
        path = self.write_truth(validated(self.CHECK, self.GONE))

        output, _ = self.sweep(b"y\n", b"fails\n")

        self.assertCounts(output, 1, failed=1)
        self.assertIn("status: deprecated", path.read_text())

    def test_gone_evidence_alone_demotes_nothing(self):
        path = self.write_truth(validated(self.CHECK, self.GONE))
        before = path.read_bytes()

        self.sweep(b"y\n", b"holds\n")

        self.assertEqual(path.read_bytes(), before)

    def assertPresent(self, *entries):
        self.write_truth(validated(self.CHECK, evidence(*entries)))
        output, _ = self.sweep(b"y\n", b"holds\n")
        self.assertCounts(output, 1, **{"pass": 1})

    def test_each_form_a_path_takes_is_read(self):
        (self.project / "agents" / "deep" / "er").mkdir(parents=True)
        (self.project / "agents" / "a.md").write_text("")
        (self.project / "agents" / "deep" / "er" / "b.md").write_text("")
        (self.project / "[id].tsx").write_text("")
        (self.home / "in-home").write_text("")
        (self.root / "absolute").write_text("")

        self.assertPresent("path: agents/a.md", "path: agents/", "path: agents/*.md", "path: agents/**/b.md",
                           "path: [id].tsx", 'path: "agents/a.md"', "path: 'agents/a.md'", "path: agents/a.md   # a note",
                           "path: ~/in-home", 'path: "~/in-home"', f"path: {self.root / 'absolute'}")

    def test_a_pattern_is_read_under_a_checkout_holding_a_glob_character(self):
        checkout = self.root / "check[out]"
        (checkout / "agents").mkdir(parents=True)
        (checkout / "agents" / "a.md").write_text("")
        self.register(f"    loom:\n        path: {checkout}\n")

        self.assertPresent("path: agents/*.md")

    def test_commit_evidence_is_not_a_path(self):
        self.assertPresent("commit: abc123")

    def test_a_note_or_a_later_block_is_not_evidence(self):
        self.write_truth(validated(self.CHECK, "\nevidence:\n  - commit: abc123\n    note: path: gone.go\n"
                                                "related:\n  - path: gone-too.go"))

        output, _ = self.sweep(b"y\n", b"holds\n")

        self.assertCounts(output, 1, **{"pass": 1})

    def test_a_pattern_matching_nothing_is_gone(self):
        self.write_truth(validated(self.CHECK, evidence("path: agents/*.md")))

        output, _ = self.sweep(b"y\n", b"holds\n")

        self.assertCounts(output, 1, evidence_missing=1)

    def test_one_path_gone_among_several_is_enough(self):
        (self.project / "here.go").write_text("")
        self.write_truth(validated(self.CHECK, evidence("path: here.go", "path: gone.go")))

        output, _ = self.sweep(b"y\n", b"holds\n")

        self.assertCounts(output, 1, evidence_missing=1)
        self.assertIn(b"evidence gone: gone.go\n", output)

    def test_an_escape_in_a_path_reaches_the_terminal_spelled_out(self):
        self.write_truth(validated(self.CHECK, evidence("path: gone\x1b[2J.go")))

        output, _ = self.sweep(b"y\n", b"holds\n")

        self.assertNotIn(b"\x1b", output)
        self.assertIn(b"gone\\x1b[2J.go", output)


class UnverifiableTest(SweepTestCase):
    def assertUnverifiable(self, reason, total=1):
        output, status = self.sweep()

        self.assertEqual(status, 0, output)
        self.assertCounts(output, total, unverifiable=total)
        self.assertIn(reason, output)
        self.assertNotIn(PROMPT, output)
        self.assertEqual(list(self.project.iterdir()), [])
        return output

    def test_no_verified_at(self):
        self.write_truth(truth(verify_section("touch ran")))

        self.assertUnverifiable(b"no verified_at")

    def test_a_verified_at_with_no_date_in_it(self):
        for value in ("", "<YYYY-MM-DD>", "never"):
            with self.subTest(value):
                self.write_truth(truth(verify_section("touch ran"),
                                       f"id: loom-x\nstatus: validated\nverified_at: {value}"))

                self.assertUnverifiable(b"no verified_at")

    def test_no_verify_section(self):
        self.write_truth(validated("## Notes\n\n```\ntouch ran\n```\n"))

        self.assertUnverifiable(b"no code blocks")

    def test_a_section_of_prose(self):
        self.write_truth(validated("## How to verify\n\nRead `go.mod`.\n"))

        self.assertUnverifiable(b"no code blocks")

    def test_an_unclosed_block_is_no_block(self):
        self.write_truth(validated("## How to verify\n\n```\ntouch ran\n\n## Notes\n"))

        self.assertUnverifiable(b"no code blocks")

    def test_universal_has_no_checkout(self):
        self.write_at("truths/universal/x.md", validated(verify_section("touch ran")))

        self.assertUnverifiable(b"no projects.universal.path")

    def test_a_checkout_that_is_not_a_directory(self):
        self.register(f"    loom:\n        path: {self.root / 'gone'}\n")
        self.write_truth(validated(verify_section("touch ran")))

        output, status = self.sweep()

        self.assertEqual(status, 0, output)
        self.assertCounts(output, 1, unverifiable=1)
        self.assertIn(b"is not an existing directory", output)

    def test_a_decision_is_never_run_even_shaped_like_a_truth(self):
        self.write_at("decisions/loom/x.md", validated(verify_section("touch ran")))

        self.assertUnverifiable(b"a decision")

    def test_a_link_to_a_candidate_claiming_validated_is_never_run(self):
        candidate = self.write_at("_candidates/truths/loom/c.md", validated(verify_section("touch ran")))
        (self.truths / "x.md").symlink_to(candidate)

        self.assertUnverifiable(b"through a link")

    def link_truths(self, target):
        shutil.rmtree(self.store / "truths")
        (self.store / "truths").symlink_to(target, target_is_directory=True)

    def test_truths_linked_into_the_candidates_is_never_run(self):
        self.write_at("_candidates/truths/loom/c.md", validated(verify_section("touch ran")))
        self.link_truths("_candidates/truths")

        self.assertUnverifiable(b"through a link")

    def test_truths_linked_outside_the_store_is_never_run(self):
        outside = self.root / "outside"
        (outside / "loom").mkdir(parents=True)
        (outside / "loom" / "x.md").write_text(validated(verify_section("touch ran")))
        self.link_truths(outside)

        self.assertUnverifiable(b"through a link")

    def test_undecodable_file_counts_and_is_not_run(self):
        self.write_truth(validated(verify_section("touch ran")).encode() + b"\xff\xfe\n")

        self.assertUnverifiable(b"not valid UTF-8")

    def test_a_scope_that_is_not_a_name(self):
        self.write_at("truths/Odd/x.md", validated(verify_section("touch ran")))
        self.register(f"    Odd:\n        path: {self.project}\n")

        self.assertUnverifiable(b"is not a scope")

    def test_gone_evidence_is_still_named_on_an_unverifiable_truth(self):
        self.write_truth(truth("## Notes\n", "id: loom-x\nstatus: validated\nevidence:\n  - path: gone.go"))

        output = self.assertUnverifiable(b"no verified_at")

        self.assertIn(b"evidence gone: gone.go", output)

    def test_reason_is_reported_per_artifact(self):
        self.write_truth(truth(verify_section("touch ran")))

        output, _ = self.sweep()

        self.assertRegex(output, rb"\[sweep\] unverifiable +scope \w+ +truths/loom/x\.md \(no verified_at\)")


class LivenessTest(SweepTestCase):
    def commit(self, repo: Path, days_ago: float):
        stamp = f"{int(time.time() - days_ago * 86400)} +0000"
        env = dict(os.environ, GIT_AUTHOR_DATE=stamp, GIT_COMMITTER_DATE=stamp)
        subprocess.run(["git", "-C", str(repo), "commit", "-q", "--allow-empty", "-m", "c"],
                       check=True, capture_output=True, env=env)

    def run_one(self, *answers):
        output, _ = self.sweep(*answers)
        return output

    def test_a_scope_idle_past_the_limit_is_flagged_and_not_demoted(self):
        init_repo(self.project)
        self.commit(self.project, 61)
        path = self.write_truth(validated(verify_section("true")))
        before = path.read_bytes()

        output = self.run_one(b"y\n", b"holds\n")

        self.assertIn(b"scope inert: no commits in 60 days", output)
        self.assertCounts(output, 1, **{"pass": 1})
        self.assertRegex(output, rb"\[sweep\] pass +scope inert +truths/loom/x\.md")
        self.assertIn(b"inert (no commits in 60 days): 1", output)
        self.assertEqual(path.read_bytes(), before)
        self.assertUntouched()

    def test_a_declined_artifact_is_still_flagged(self):
        init_repo(self.project)
        self.commit(self.project, 61)
        self.write_truth(validated(verify_section("true")))

        output = self.run_one(b"n\n")

        self.assertCounts(output, 1, skipped=1)
        self.assertIn(b"inert (no commits in 60 days): 1", output)

    def test_a_scope_committed_to_inside_the_limit_is_live(self):
        init_repo(self.project)
        self.commit(self.project, 59)
        self.write_truth(validated(verify_section("true")))

        output = self.run_one(b"n\n")

        self.assertRegex(output, rb"\[sweep\] skipped +scope live +truths/loom/x\.md")
        self.assertIn(b"live: 1, inert (no commits in 60 days): 0, unknown: 0", output)

    def test_a_newer_commit_on_another_branch_keeps_the_repo_live(self):
        init_repo(self.project)
        self.commit(self.project, 90)
        git(self.project, "branch", "parked")
        git(self.project, "checkout", "-q", "-b", "newer")
        self.commit(self.project, 1)
        git(self.project, "checkout", "-q", "parked")
        self.write_truth(validated(verify_section("true")))

        output = self.run_one(b"n\n")

        self.assertIn(b"live: 1,", output)

    def assertUnknown(self, output):
        self.assertIn(b"live: 0, inert (no commits in 60 days): 0, unknown: 1", output)

    def test_a_checkout_that_is_no_repo_is_unknown(self):
        self.write_truth(validated(verify_section("true")))

        self.assertUnknown(self.run_one(b"n\n"))

    def test_a_repo_with_no_commits_is_unknown(self):
        init_repo(self.project)
        self.write_truth(validated(verify_section("true")))

        self.assertUnknown(self.run_one(b"n\n"))

    def test_a_plain_directory_in_a_live_repo_is_unknown(self):
        init_repo(self.root)
        self.commit(self.root, 1)
        self.write_truth(validated(verify_section("true")))

        self.assertUnknown(self.run_one(b"n\n"))

    def test_no_checkout_is_unknown(self):
        self.write_at("truths/universal/x.md", validated(verify_section("true")))

        self.assertUnknown(self.run_one())


class InterruptTest(SweepTestCase):
    def test_interrupt_at_a_prompt_reports_what_was_reached(self):
        self.write_truth(validated(verify_section("touch one")), "a.md")
        self.write_truth(validated(verify_section("touch two")), "b.md")
        self.write_truth(validated(verify_section("touch three")), "c.md")

        output, status = self.sweep(b"n\n", b"\x03")

        self.assertNotEqual(status, 0)
        self.assertIn(b"[sweep] interrupted", output)
        self.assertCounts(output, 3, skipped=1, not_reached=2)
        self.assertEqual(list(self.project.iterdir()), [])

    def test_interrupt_at_the_verdict_writes_nothing_and_says_a_command_ran(self):
        path = self.write_truth(validated(verify_section("touch ran")))
        before = path.read_bytes()

        output, status = self.sweep(b"y\n", b"\x03")

        self.assertNotEqual(status, 0)
        self.assertIn(b"may have run", output)
        self.assertCounts(output, 1, not_reached=1)
        self.assertEqual(path.read_bytes(), before)
        self.assertUntouched()

    def test_a_demotion_already_applied_is_still_reported(self):
        first = self.write_truth(validated(verify_section("false")), "a.md")
        self.write_truth(validated(verify_section("touch two")), "b.md")

        output, status = self.sweep(b"y\n", b"fails\n", b"\x03")

        self.assertNotEqual(status, 0)
        self.assertIn("status: deprecated", first.read_text())
        self.assertCounts(output, 2, failed=1, not_reached=1)
        self.assertRegex(output, rb"\[sweep\] failed +scope \w+ +truths/loom/a\.md \(demoted to deprecated\)")
        self.assertIn(b"demoted: 1, demotion did not land cleanly: 0", output)

    def test_interrupt_during_a_command_stops_the_sweep(self):
        self.write_truth(validated(verify_section("echo $$ > shell; sleep 300", "touch ran")))
        shell_file = self.project / "shell"
        sent = []

        def interrupt(master, output):
            if not sent and b"y\r\n" in output.partition(PROMPT)[2]:
                while not shell_file.exists() or not shell_file.read_text():
                    time.sleep(0.01)
                sent.append(True)
                os.write(master, b"\x03")

        output, status = self.sweep(b"y\n", on_output=interrupt)

        self.assertNotEqual(status, 0)
        self.assertCounts(output, 1, not_reached=1)
        self.assertFalse((self.project / "ran").exists())

    def test_interrupt_during_the_write_cannot_lose_the_demotion(self):
        # The writer is slowed so the Ctrl-C provably lands while it runs. It
        # reaches the writer as well as the sweep: a terminal signals the whole
        # foreground group.
        started = self.root / "writer-started"
        slow = self.root / "slow-loom"
        slow.write_text(f"#!/bin/sh\ntouch {started}\nsleep 0.5\nexec {LOOM_BIN} \"$@\"\n")
        slow.chmod(slow.stat().st_mode | stat.S_IXUSR)
        self.env["LOOM_BIN"] = str(slow)
        path = self.write_truth(validated(verify_section("false")))
        sent = []

        def interrupt(master, output):
            if not sent and b"fails\r\n" in output.partition(VERDICT)[2]:
                while not started.exists():
                    time.sleep(0.01)
                sent.append(True)
                os.write(master, b"\x03")

        output, status = self.sweep(b"y\n", b"fails\n", on_output=interrupt)

        self.assertEqual(status, 0, output)
        self.assertTrue(sent)
        self.assertIn("status: deprecated", path.read_text())
        self.assertEqual(self.subjects()[0], "demote truth loom/loom-x")
        self.assertIn(b"demoted to deprecated", output)
        self.assertCounts(output, 1, failed=1)


class TerminalTest(SweepTestCase):
    def setUp(self):
        super().setUp()
        self.write_truth(validated(verify_section("touch ran")))

    def test_piped_stdin_is_refused(self):
        result = self.piped(stdin="y\nfails\n")

        self.assertNotEqual(result.returncode, 0)
        self.assertIn("not both a terminal", result.stderr)
        self.assertFalse((self.project / "ran").exists())

    def test_closed_stdin_is_refused(self):
        result = subprocess.run(self.argv(), stdin=subprocess.DEVNULL, capture_output=True,
                                text=True, env=self.env, timeout=DRIVE_LIMIT)

        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((self.project / "ran").exists())

    def test_no_flag_skips_a_confirmation_or_answers_for_the_human(self):
        for flag in ("--yes", "-y", "--force", "--no-confirm", "--non-interactive",
                     "--fail", "--fails", "--holds", "--demote"):
            with self.subTest(flag):
                result = self.piped(flag, stdin="y\n")
                self.assertEqual(result.returncode, 2)
                self.assertFalse((self.project / "ran").exists())


class WiringTest(unittest.TestCase):
    def test_nothing_in_the_repo_invokes_the_sweep(self):
        repo = HERE.parent
        # POSIX classes: git's ERE on macOS has no \\b and matches nothing with it.
        word = "[^[:alnum:]_]"
        listed = subprocess.run(["git", "-C", str(repo), "grep", "--untracked", "-lE",
                                 rf"(^|{word})sweep\.py({word}|$)|(import|from) sweep({word}|$)",
                                 "--", "*.go", "*.py", "*.sh", "Makefile", "*.plist", "*.yml"],
                                capture_output=True, text=True, timeout=DRIVE_LIMIT)

        # Exact, so a pattern that stops matching fails here. verify.py names it in
        # prose, and test_verify.py in its own allowlist.
        self.assertEqual(set(listed.stdout.split()),
                         {"extractors/sweep.py", "extractors/test_sweep.py",
                          "extractors/verify.py", "extractors/test_verify.py"})


if __name__ == "__main__":
    unittest.main()
