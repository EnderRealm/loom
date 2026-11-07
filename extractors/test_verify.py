#!/usr/bin/env python3
"""Tests for the truth verification runner.

Run with: python3 -m unittest

Every test drives the CLI as a subprocess over a real store and project tree, on
a pty wherever a terminal is part of what is being asserted: the runner's whole
job is what reaches a person's screen and what it will and will not execute
afterwards. Commands prove they ran or did not by touching files in the project.
"""
import fcntl
import os
import pty
import select
import signal
import subprocess
import sys
import tempfile
import termios
import time
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
PROMPT = b"[verify] run this command?"
# Far above any run here. A read that gets this far is a runner that hung.
DRIVE_LIMIT = 30


def truth(body: str, frontmatter: str = "id: loom-x\nstatus: validated") -> str:
    return f"---\n{frontmatter}\n---\n\n## Claim\n\nA claim.\n\n{body}"


def verify_section(*blocks: str) -> str:
    fenced = "\n\nExpected: something.\n\n".join(f"```\n{b}\n```" for b in blocks)
    return f"## How to verify\n\n{fenced}\n"


class VerifyTestCase(unittest.TestCase):
    PROMPTS = (PROMPT,)

    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        # Resolved: macOS hands out /var/folders/... symlinked to /private/var,
        # and the runner reports real paths.
        self.root = Path(tmp.name).resolve()
        self.store = self.root / "knowledge"
        self.home = self.root / "home"
        self.project = self.root / "checkouts" / "loom"
        self.truths = self.store / "truths" / "loom"
        for directory in (self.home / ".ticket", self.project, self.truths):
            directory.mkdir(parents=True)
        self.register(f"    loom:\n        path: {self.project}\n        auto_link: false\n")
        self.env = {k: v for k, v in os.environ.items() if k != "LOOM_HOME"}
        self.env.update(HOME=str(self.home), LOOM_KNOWLEDGE_ROOT=str(self.store))

    def register(self, entries: str):
        (self.home / ".ticket" / "config.yaml").write_text(
            f"central_root: /elsewhere\nprojects:\n{entries}")

    def write_truth(self, text, name="x.md") -> Path:
        path = self.truths / name
        if isinstance(text, bytes):
            path.write_bytes(text)
        else:
            path.write_text(text, encoding="utf-8")
        return path

    def argv(self, *args):
        return [sys.executable, str(HERE / "verify.py"), *map(str, args)]

    def bounded_argv(self, seconds, path):
        """The CLI with COMMAND_TIMEOUT lowered, for tests of the deadline."""
        code = (f"import sys; sys.path.insert(0, {str(HERE)!r}); import verify; "
                f"verify.COMMAND_TIMEOUT = {seconds}; verify.main()")
        return [sys.executable, "-c", code, str(path)]

    def piped(self, *args, stdin=""):
        return subprocess.run(self.argv(*args), input=stdin, capture_output=True, text=True,
                              env=self.env, timeout=DRIVE_LIMIT)

    def drive(self, argv, answers, on_output=None):
        """Run `argv` on a pty that is its controlling terminal, as a shell would
        start it, writing one answer per prompt it shows. `on_output` is called
        with the master fd and all output so far, ahead of the answer. Returns
        the bytes that reached the terminal, with the pty's own \\n to \\r\\n
        mapping undone, and the exit status."""
        master, slave = pty.openpty()
        proc = subprocess.Popen(argv, stdin=slave, stdout=slave, stderr=slave, env=self.env,
                                start_new_session=True,
                                preexec_fn=lambda: fcntl.ioctl(0, termios.TIOCSCTTY, 0))
        os.close(slave)
        self.addCleanup(proc.kill)
        self.addCleanup(os.close, master)
        answers = list(answers)
        output = b""
        answered = 0
        deadline = time.monotonic() + DRIVE_LIMIT
        while True:
            self.assertLess(time.monotonic(), deadline, f"runner hung:\n{output!r}")
            if not select.select([master], [], [], 0.05)[0]:
                continue
            try:
                chunk = os.read(master, 65536)
            except OSError:  # EIO: the last slave closed
                break
            if not chunk:
                break
            output += chunk
            if on_output:
                on_output(master, output)
            if sum(output.count(p) for p in self.PROMPTS) > answered:
                self.assertTrue(answers, f"prompted past the answers given:\n{output!r}")
                answered += 1
                os.write(master, answers.pop(0))
        self.assertEqual(answers, [], f"answers left over:\n{output!r}")
        return output.replace(b"\r\n", b"\n"), proc.wait(timeout=DRIVE_LIMIT)

    def verify(self, path, *answers):
        return self.drive(self.argv(path), answers)

    def assertRefused(self, path, reason):
        """Refused on a real terminal, so the tty check can't be what stopped
        it, with no prompt and no command run."""
        output, status = self.drive(self.argv(path), [])
        self.assertNotEqual(status, 0, output)
        self.assertIn(reason.encode(), output)
        self.assertNotIn(PROMPT, output)
        self.assertEqual(list(self.project.iterdir()), [])


class ValidatedArtifactTest(VerifyTestCase):
    TOUCH = verify_section("touch ran")

    def test_validated_truth_is_offered_and_runs(self):
        path = self.write_truth(truth(self.TOUCH))

        output, status = self.verify(path, b"y\n")

        self.assertEqual(status, 0, output)
        self.assertTrue((self.project / "ran").exists())

    def test_candidate_is_refused_even_claiming_validated(self):
        scope = self.store / "_candidates" / "truths" / "loom"
        scope.mkdir(parents=True)
        path = scope / "x.md"
        path.write_text(truth(self.TOUCH))

        self.assertRefused(path, "not directly at truths/<scope>/")

    def test_rejected_candidate_is_refused(self):
        scope = self.store / "_candidates" / "truths" / "_rejected" / "loom"
        scope.mkdir(parents=True)
        path = scope / "x.md"
        path.write_text(truth(self.TOUCH))

        self.assertRefused(path, "not directly at truths/<scope>/")

    def test_candidates_directory_inside_truths_is_refused(self):
        scope = self.store / "truths" / "_candidates"
        scope.mkdir()
        path = scope / "x.md"
        path.write_text(truth(self.TOUCH))

        self.assertRefused(path, "is not a scope")

    def test_symlink_into_truths_for_a_candidate_is_refused(self):
        scope = self.store / "_candidates" / "truths" / "loom"
        scope.mkdir(parents=True)
        (scope / "x.md").write_text(truth(self.TOUCH))
        link = self.truths / "x.md"
        link.symlink_to(scope / "x.md")

        self.assertRefused(link, "not directly at truths/<scope>/")

    def test_scope_directory_linked_out_of_the_store_is_refused(self):
        outside = self.root / "outside"
        outside.mkdir()
        (outside / "x.md").write_text(truth(self.TOUCH))
        (self.store / "truths" / "evil").symlink_to(outside)

        self.assertRefused(self.store / "truths" / "evil" / "x.md", "outside the knowledge store")

    def test_link_from_outside_to_a_validated_truth_runs_it(self):
        path = self.write_truth(truth(self.TOUCH))
        link = self.root / "shortcut.md"
        link.symlink_to(path)

        self.verify(link, b"y\n")

        self.assertTrue((self.project / "ran").exists())

    def test_store_reached_through_a_link_is_the_same_store(self):
        path = self.write_truth(truth(self.TOUCH))
        alias = self.root / "alias"
        alias.symlink_to(self.store)
        self.env["LOOM_KNOWLEDGE_ROOT"] = str(alias)

        self.verify(path, b"y\n")

        self.assertTrue((self.project / "ran").exists())

    def test_file_outside_the_store_is_refused(self):
        path = self.root / "x.md"
        path.write_text(truth(self.TOUCH))

        self.assertRefused(path, "outside the knowledge store")

    def test_decision_is_refused(self):
        scope = self.store / "decisions" / "loom"
        scope.mkdir(parents=True)
        path = scope / "x.md"
        path.write_text(truth(self.TOUCH))

        self.assertRefused(path, "not directly at truths/<scope>/")

    def test_file_below_a_scope_is_refused(self):
        sub = self.truths / "sub"
        sub.mkdir()
        path = sub / "x.md"
        path.write_text(truth(self.TOUCH))

        self.assertRefused(path, "not directly at truths/<scope>/")

    def test_file_beside_the_scopes_is_refused(self):
        path = self.store / "truths" / "_schema.md"
        path.write_text(truth(self.TOUCH))

        self.assertRefused(path, "not directly at truths/<scope>/")

    def test_files_the_store_does_not_count_as_artifacts_are_refused(self):
        for name in ("_notes.md", "README.md", "x.txt", "x.MD"):
            with self.subTest(name):
                self.assertRefused(self.write_truth(truth(self.TOUCH), name), "not an artifact file")

    def test_missing_file_is_refused(self):
        self.assertRefused(self.truths / "gone.md", "not a regular file")

    def test_directory_is_refused(self):
        (self.truths / "dir.md").mkdir()

        self.assertRefused(self.truths / "dir.md", "not a regular file")

    def test_fifo_is_refused_without_blocking(self):
        os.mkfifo(self.truths / "x.md")

        self.assertRefused(self.truths / "x.md", "not a regular file")

    def test_undecodable_file_is_refused(self):
        path = self.write_truth(truth(self.TOUCH).encode() + b"\xff\xfe\n")

        self.assertRefused(path, "cannot read")

    def test_status_other_than_validated_is_refused(self):
        for status in ("candidate", "deprecated", "draft", '"validated"', "validated # ok", ""):
            with self.subTest(status):
                path = self.write_truth(truth(self.TOUCH, f"id: loom-x\nstatus: {status}"))
                self.assertRefused(path, "status: validated")

    def test_no_status_is_refused(self):
        self.assertRefused(self.write_truth(truth(self.TOUCH, "id: loom-x")), "status: validated")

    def test_no_id_is_refused(self):
        self.assertRefused(self.write_truth(truth(self.TOUCH, "status: validated")), "no frontmatter id")

    def test_no_frontmatter_is_refused(self):
        self.assertRefused(self.write_truth(self.TOUCH), "no frontmatter id")

    def test_unterminated_frontmatter_is_refused(self):
        path = self.write_truth(f"---\nid: loom-x\nstatus: validated\n\n{self.TOUCH}")

        self.assertRefused(path, "no frontmatter id")

    def test_status_in_a_nested_entry_does_not_count(self):
        path = self.write_truth(truth(self.TOUCH, "id: loom-x\nstatus: deprecated\ncontradicts:\n"
                                                  "  - file: README.md\n    status: validated"))

        self.assertRefused(path, "status: validated")

    def test_nested_status_cannot_stand_in_for_a_missing_one(self):
        path = self.write_truth(truth(self.TOUCH, "id: loom-x\nevidence:\n  status: validated"))

        self.assertRefused(path, "status: validated")

    def test_status_outside_the_frontmatter_does_not_count(self):
        path = self.write_truth(truth("status: validated\n\n" + self.TOUCH, "id: loom-x\nstatus: deprecated"))

        self.assertRefused(path, "status: validated")

    def test_later_duplicate_status_wins_as_it_does_in_the_loader(self):
        path = self.write_truth(truth(self.TOUCH, "id: loom-x\nstatus: validated\nstatus: candidate"))

        self.assertRefused(path, "status: validated")

    def test_later_validated_duplicate_is_accepted_as_it_is_in_the_loader(self):
        path = self.write_truth(truth(self.TOUCH, "id: loom-x\nstatus: candidate\nstatus: validated"))

        self.verify(path, b"y\n")

        self.assertTrue((self.project / "ran").exists())

    def test_status_behind_a_character_that_is_no_line_break_to_the_loader(self):
        for sep in ("\r", "\x0b", "\x0c", "\x85", " "):
            with self.subTest(repr(sep)):
                path = self.write_truth(truth(self.TOUCH, f"id: loom-x\nstatus: candidate{sep}status: validated"))
                self.assertRefused(path, "status: validated")

    def test_refusal_does_not_replay_control_characters_from_the_name(self):
        path = self.write_truth(truth(self.TOUCH, "id: loom-x\nstatus: candidate"), "a\x1b[2Jb.md")

        output, status = self.verify(path)

        self.assertNotEqual(status, 0)
        self.assertNotIn(b"\x1b", output)
        self.assertIn(b"a\\x1b[2Jb.md", output)


class ProjectRootTest(VerifyTestCase):
    def setUp(self):
        super().setUp()
        self.path = self.write_truth(truth(verify_section("touch ran")))

    def assertRefused(self, reason):
        super().assertRefused(self.path, reason)

    def test_unregistered_scope_is_refused(self):
        self.register(f"    other:\n        path: {self.project}\n")

        self.assertRefused("no projects.loom.path")

    def test_conventional_checkout_is_not_guessed(self):
        self.register(f"    other:\n        path: {self.project}\n")
        (self.home / "code" / "loom").mkdir(parents=True)

        self.assertRefused("no projects.loom.path")
        self.assertEqual(list((self.home / "code" / "loom").iterdir()), [])

    def test_registered_project_with_no_path_is_refused(self):
        self.register("    loom:\n        auto_link: false\n")

        self.assertRefused("no projects.loom.path")

    def test_path_of_the_next_project_is_not_borrowed(self):
        self.register(f"    loom:\n        auto_link: false\n    other:\n        path: {self.project}\n")

        self.assertRefused("no projects.loom.path")

    def test_missing_config_is_refused(self):
        (self.home / ".ticket" / "config.yaml").unlink()

        self.assertRefused("cannot read")

    def test_undecodable_config_is_refused(self):
        (self.home / ".ticket" / "config.yaml").write_bytes(b"projects:\n    \xff\xfe:\n")

        self.assertRefused("cannot read")

    def test_missing_directory_is_refused(self):
        self.register(f"    loom:\n        path: {self.root / 'gone'}\n")

        self.assertRefused("is not an existing directory")

    def test_path_naming_a_file_is_refused(self):
        file = self.root / "a-file"
        file.write_text("")
        self.register(f"    loom:\n        path: {file}\n")

        self.assertRefused("is not an existing directory")

    def test_relative_path_is_refused(self):
        self.register("    loom:\n        path: .\n")

        self.assertRefused("is not an existing directory")

    def test_universal_scope_has_no_project_to_run_in(self):
        scope = self.store / "truths" / "universal"
        scope.mkdir()
        self.path = scope / "x.md"
        self.path.write_text(truth(verify_section("touch ran")))

        self.assertRefused("no projects.universal.path")


class TerminalTest(VerifyTestCase):
    def setUp(self):
        super().setUp()
        self.path = self.write_truth(truth(verify_section("touch ran")))

    def test_piped_stdin_is_refused(self):
        result = self.piped(self.path, stdin="y\n")

        self.assertNotEqual(result.returncode, 0)
        self.assertIn("not both a terminal", result.stderr)
        self.assertFalse((self.project / "ran").exists())

    def test_closed_stdin_is_refused(self):
        result = subprocess.run(self.argv(self.path), stdin=subprocess.DEVNULL, capture_output=True,
                                text=True, env=self.env, timeout=DRIVE_LIMIT)

        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((self.project / "ran").exists())

    def test_stdout_away_from_the_terminal_is_refused(self):
        # A command shown into a pipe was shown to nobody, even with a person
        # at the keyboard.
        master, slave = pty.openpty()
        self.addCleanup(os.close, master)
        proc = subprocess.run(self.argv(self.path), stdin=slave, stdout=subprocess.PIPE,
                              stderr=subprocess.PIPE, text=True, env=self.env, timeout=DRIVE_LIMIT)
        os.close(slave)

        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("not both a terminal", proc.stderr)
        self.assertFalse((self.project / "ran").exists())

    def test_no_flag_bypasses_confirmation(self):
        for flag in ("--yes", "-y", "--force", "--no-confirm", "--non-interactive"):
            with self.subTest(flag):
                result = self.piped(flag, self.path, stdin="y\n")
                self.assertEqual(result.returncode, 2)
                self.assertFalse((self.project / "ran").exists())


class ConfirmationTest(VerifyTestCase):
    def test_command_and_cwd_are_shown_before_the_prompt(self):
        path = self.write_truth(truth(verify_section("touch ran")))

        output, _ = self.verify(path, b"n\n")

        self.assertLess(output.index(b"| touch ran"), output.index(PROMPT))
        self.assertLess(output.index(f"cwd: {self.project}".encode()), output.index(PROMPT))

    def test_nothing_but_y_runs_the_command(self):
        path = self.write_truth(truth(verify_section("touch ran")))

        for answer in (b"\n", b"n\n", b"yes\n", b"Y\n", b"yy\n", b"y n\n", b"\x04"):
            with self.subTest(answer):
                output, status = self.verify(path, answer)
                self.assertEqual(status, 0, output)
                self.assertIn(b"[verify] skipped", output)
                self.assertEqual(list(self.project.iterdir()), [])

    def test_each_command_has_its_own_answer(self):
        path = self.write_truth(truth(verify_section("touch one", "touch two", "touch three")))

        self.verify(path, b"y\n", b"n\n", b"y\n")

        self.assertEqual(sorted(p.name for p in self.project.iterdir()), ["one", "three"])

    def test_each_command_is_shown_after_the_last_one_ran(self):
        path = self.write_truth(truth(verify_section("echo first-output", "echo second-output")))

        output, _ = self.verify(path, b"y\n", b"n\n")

        self.assertLess(output.rindex(b"\nfirst-output\n"), output.index(b"| echo second-output"))

    def test_typing_ahead_does_not_answer_the_next_command(self):
        path = self.write_truth(truth(verify_section("sleep 0.3", "touch ran")))

        output, _ = self.verify(path, b"y\ny\n", b"n\n")

        self.assertEqual(output.count(PROMPT), 2)
        self.assertEqual(list(self.project.iterdir()), [])

    def test_multi_line_block_is_one_unit(self):
        path = self.write_truth(truth(verify_section("touch one\ntouch two")))

        output, _ = self.verify(path, b"y\n")

        self.assertEqual(output.count(PROMPT), 1)
        self.assertIn(b"| touch one\n    | touch two\n", output)
        self.assertEqual(sorted(p.name for p in self.project.iterdir()), ["one", "two"])

    def test_control_characters_are_shown_and_still_run(self):
        hiding = [
            ("carriage return", "touch hidden; \rtouch ran", b"\\r"),
            ("erase line", "touch hidden; \x1b[2K\x1b[1Gtouch ran", b"\\x1b[2K\\x1b[1G"),
            ("backspaces", "touch hidden; \b\b\b\b\b\b\b\b\b\b\b\b\btouch ran", b"\\x08"),
            ("osc title", "touch hidden; \x1b]0;t\x07touch ran", b"\\x1b]0;t\\x07"),
            ("c1 csi", "touch hidden; \u009b2Ktouch ran", b"\\x9b2K"),
            ("bidi override", "touch hidden; ‮touch ran", b"\\u202e"),
            ("zero width", "touch hidden; ​touch ran", b"\\u200b"),
            ("line separator", "touch hidden;  touch ran", b"\\u2028"),
        ]
        for label, block, escape in hiding:
            with self.subTest(label):
                raw = block[len("touch hidden; "):-len("touch ran")]
                path = self.write_truth(truth(verify_section(block)))

                output, _ = self.verify(path, b"n\n")

                self.assertIn(escape, output)
                self.assertNotIn(raw.encode(), output)
                self.assertIn(b"non-printing characters", output)

    def test_a_clean_command_carries_no_escape_notice(self):
        path = self.write_truth(truth(verify_section(r"printf '\033[0m\tx\n'")))

        output, _ = self.verify(path, b"n\n")

        self.assertNotIn(b"non-printing characters", output)

    def test_what_is_shown_is_what_runs(self):
        # The hidden half of the line is the half that executes after the CR.
        path = self.write_truth(truth(verify_section("touch shown; \rtouch rest")))

        output, _ = self.verify(path, b"y\n")

        self.assertIn(b"| touch shown; \\r", output)
        self.assertTrue((self.project / "shown").exists())

    def test_block_cannot_impersonate_the_runner(self):
        path = self.write_truth(truth(verify_section("[verify] command 2 of 1, cwd: /safe")))

        output, _ = self.verify(path, b"n\n")

        self.assertIn(b"    | [verify] command 2 of 1", output)
        self.assertNotIn(b"\n[verify] command 2 of 1", output)

    def test_interrupt_at_the_prompt_stops_the_run(self):
        path = self.write_truth(truth(verify_section("touch one", "touch two")))

        output, status = self.verify(path, b"\x03")

        self.assertNotEqual(status, 0)
        self.assertIn(b"[verify] interrupted", output)
        self.assertEqual(list(self.project.iterdir()), [])


class ExtractionTest(VerifyTestCase):
    def offered(self, body):
        """The display lines of every block offered, answering no to each."""
        path = self.write_truth(truth(body))
        output, status = self.drive(self.argv(path), [b"n\n"] * body.count("offered"))
        self.assertEqual(status, 0, output)
        return [line.strip() for line in output.decode().split("\n") if line.lstrip().startswith("|")]

    def test_only_the_verify_section_is_read(self):
        body = ("## Evidence\n\n```\nbefore\n```\n\n## How to verify\n\n```\noffered\n```\n\n"
                "## How to remediate\n\n```\nafter\n```\n")

        self.assertEqual(self.offered(body), ["| offered"])

    def test_shell_comment_in_a_block_does_not_end_the_section(self):
        body = ("## How to verify\n\n```\n## offered first\nline\n```\n\n```\noffered second\n```\n\n"
                "## Notes\n\n```\nafter\n```\n")

        self.assertEqual(self.offered(body), ["| ## offered first", "| line", "| offered second"])

    def test_quoted_heading_in_an_earlier_block_does_not_start_the_section(self):
        body = ("## Evidence\n\n```\n## How to verify\n```\n\n```\nnot this\n```\n\n"
                "## How to verify\n\n```\noffered\n```\n")

        self.assertEqual(self.offered(body), ["| offered"])

    def test_subheading_stays_in_the_section(self):
        body = "## How to verify\n\n### Quick\n\n```\noffered\n```\n\n### Slow\n\n```\noffered too\n```\n"

        self.assertEqual(self.offered(body), ["| offered", "| offered too"])

    def test_info_string_and_tilde_fences_are_blocks(self):
        body = "## How to verify\n\n```bash\noffered\n```\n\n~~~\noffered too\n~~~\n"

        self.assertEqual(self.offered(body), ["| offered", "| offered too"])

    def test_longer_fence_holds_a_shorter_one(self):
        body = "## How to verify\n\n````\n```\noffered\n```\n````\n"

        self.assertEqual(self.offered(body), ["| ```", "| offered", "| ```"])

    def test_inline_code_on_one_line_is_not_a_fence(self):
        body = "## How to verify\n\n```not a fence```\n\n```\noffered\n```\n"

        self.assertEqual(self.offered(body), ["| offered"])

    def test_crlf_truth_keeps_its_fences(self):
        body = "## How to verify\r\n\r\n```\r\noffered\r\n```\r\n\r\n## Notes\r\n\r\n```\r\nafter\r\n```\r\n"

        self.assertEqual(self.offered(body), ["| offered\\r"])

    def assertNothingToRun(self, body):
        output, status = self.drive(self.argv(self.write_truth(truth(body))), [])
        self.assertEqual(status, 0, output)
        self.assertIn(b"nothing to run", output)
        self.assertNotIn(PROMPT, output)
        return output

    def test_section_without_blocks_runs_nothing(self):
        self.assertNothingToRun("## How to verify\n\nRead `go.mod`.\n\n## Notes\n\n```\ntouch ran\n```\n")
        self.assertEqual(list(self.project.iterdir()), [])

    def test_no_section_runs_nothing(self):
        self.assertNothingToRun("## Notes\n\n```\ntouch ran\n```\n")
        self.assertEqual(list(self.project.iterdir()), [])

    def test_empty_block_is_not_a_command(self):
        self.assertNothingToRun("## How to verify\n\n```\n\n```\n")

    def test_no_blocks_is_reported_without_a_terminal(self):
        result = self.piped(self.write_truth(truth("## How to verify\n\nProse.\n")))

        self.assertEqual(result.returncode, 0)
        self.assertIn("nothing to run", result.stdout)

    def test_unclosed_block_is_not_offered(self):
        output = self.assertNothingToRun("## How to verify\n\n```\ntouch ran\n\n## Notes\n\nProse.\n")

        self.assertIn(b"never closes", output)
        self.assertEqual(list(self.project.iterdir()), [])

    def test_blocks_before_an_unclosed_one_are_still_offered(self):
        path = self.write_truth(truth("## How to verify\n\n```\necho closed\n```\n\n```\ntouch ran\n"))

        output, _ = self.verify(path, b"n\n")

        self.assertIn(b"never closes", output)
        self.assertIn(b"| echo closed", output)
        self.assertNotIn(b"| touch ran", output)


class RunningTest(VerifyTestCase):
    def test_runs_in_the_project_root_through_a_shell(self):
        path = self.write_truth(truth(verify_section("pwd | tr a-z A-Z")))

        output, _ = self.verify(path, b"y\n")

        self.assertIn(f"\n{str(self.project).upper()}\n".encode(), output)

    def test_stdout_stderr_and_exit_code_are_dumped(self):
        path = self.write_truth(truth(verify_section("echo to-out; echo to-err >&2; exit 3")))

        output, _ = self.verify(path, b"y\n")

        self.assertIn(b"[verify] stdout:\nto-out\n", output)
        self.assertIn(b"[verify] stderr:\nto-err\n", output)
        self.assertIn(b"[verify] exit code: 3\n", output)

    def test_output_text_is_not_altered(self):
        path = self.write_truth(truth(verify_section(r"printf 'a  b\tc\n\n  d\\n'")))

        output, _ = self.verify(path, b"y\n")

        self.assertIn(b"[verify] stdout:\na  b\tc\n\n  d\\n\n[verify] stderr:", output)

    def test_exit_status_does_not_encode_a_commands_result(self):
        for block in ("true", "false", "exit 7", "kill -9 $$"):
            with self.subTest(block):
                path = self.write_truth(truth(verify_section(block)))

                output, status = self.verify(path, b"y\n")

                self.assertEqual(status, 0, output)

    def test_nothing_reads_as_a_verdict(self):
        path = self.write_truth(truth(verify_section("true", "false", "true")))

        output, _ = self.verify(path, b"y\n", b"y\n", b"n\n")

        lowered = output.lower()
        for word in (b"pass", b"fail", b"success", b"verdict", b"summary"):
            self.assertNotIn(word, lowered)

    def test_control_characters_in_output_are_spelled_out(self):
        # Raw, the first command's output would conceal the second's display.
        path = self.write_truth(truth(verify_section(r"printf '\033[8m\r\377'", "echo next")))

        output, _ = self.verify(path, b"y\n", b"n\n")

        self.assertIn(b"\\x1b[8m\\r\\xff\n", output)
        self.assertNotIn(b"\x1b", output)
        self.assertIn(b"| echo next", output)

    def test_command_gets_no_terminal_to_read(self):
        path = self.write_truth(truth(verify_section("cat; (exec 3</dev/tty) 2>/dev/null; echo tty=$?")))

        output, _ = self.verify(path, b"y\n")

        self.assertIn(b"\ntty=1\n", output)

    def test_block_with_a_nul_byte_reports_and_continues(self):
        path = self.write_truth(truth(verify_section("touch a\x00b", "touch ran")))

        output, status = self.verify(path, b"y\n", b"y\n")

        self.assertEqual(status, 0, output)
        self.assertIn(b"[verify] did not start:", output)
        self.assertTrue((self.project / "ran").exists())

    def test_project_removed_after_the_check_reports_and_continues(self):
        path = self.write_truth(truth(verify_section("touch ran")))

        def remove(master, output):
            if PROMPT in output and self.project.exists():
                self.project.rmdir()

        output, status = self.drive(self.argv(path), [b"y\n"], on_output=remove)

        self.assertEqual(status, 0, output)
        self.assertIn(b"[verify] did not start:", output)

    def test_timeout_is_reported_with_partial_output_and_the_run_continues(self):
        path = self.write_truth(truth(verify_section("echo partial; echo also >&2; sleep 300", "touch ran")))

        output, status = self.drive(self.bounded_argv(1, path), [b"y\n", b"y\n"])

        self.assertEqual(status, 0, output)
        self.assertIn(b"[verify] stdout:\npartial\n", output)
        self.assertIn(b"[verify] stderr:\nalso\n", output)
        self.assertIn(b"[verify] timed out, killed after 1s\n", output)
        self.assertTrue((self.project / "ran").exists())

    def assertDies(self, pid):
        deadline = time.monotonic() + 5
        while time.monotonic() < deadline:
            try:
                os.kill(pid, 0)
            except ProcessLookupError:
                return
            time.sleep(0.05)
        self.fail(f"pid {pid} outlived the runner's kill")

    def test_timeout_kills_the_blocks_children(self):
        path = self.write_truth(truth(verify_section("sleep 300 & echo $! > child; wait")))

        self.drive(self.bounded_argv(1, path), [b"y\n"])

        self.assertDies(int((self.project / "child").read_text()))

    def test_background_child_holding_the_pipes_cannot_outlast_the_timeout(self):
        path = self.write_truth(truth(verify_section("sleep 300 & echo $! > child")))

        output, _ = self.drive(self.bounded_argv(1, path), [b"y\n"])

        self.assertIn(b"timed out", output)
        self.assertDies(int((self.project / "child").read_text()))

    def test_child_that_left_the_group_cannot_hang_the_runner(self):
        escape = ("import os, sys, time\n"
                  "if os.fork() == 0:\n"
                  "    os.setsid()\n"
                  "    open('child', 'w').write(str(os.getpid()))\n"
                  "    time.sleep(20)\n")
        script = self.root / "escape.py"
        script.write_text(escape)
        path = self.write_truth(truth(verify_section(f"{sys.executable} {script}")))

        output, status = self.drive(self.bounded_argv(1, path), [b"y\n"])

        self.assertEqual(status, 0, output)
        self.assertIn(b"timed out", output)
        os.kill(int((self.project / "child").read_text()), signal.SIGKILL)

    def test_interrupt_during_a_command_kills_it_and_stops_the_run(self):
        path = self.write_truth(truth(verify_section("echo $$ > shell; sleep 300", "touch ran")))
        shell_file = self.project / "shell"
        sent = []

        def interrupt(master, output):
            # The y's echo, not the prompt: drive() answers after this returns.
            if not sent and b"y\r\n" in output.partition(PROMPT)[2]:
                while not shell_file.exists() or not shell_file.read_text():
                    time.sleep(0.01)
                sent.append(True)
                os.write(master, b"\x03")

        output, status = self.drive(self.argv(path), [b"y\n"], on_output=interrupt)

        self.assertNotEqual(status, 0)
        self.assertIn(b"[verify] interrupted", output)
        self.assertDies(int(shell_file.read_text()))
        self.assertFalse((self.project / "ran").exists())


class WiringTest(unittest.TestCase):
    def test_nothing_in_the_repo_invokes_the_runner(self):
        repo = HERE.parent
        # POSIX classes: git's ERE on macOS has no \\b and matches nothing with it.
        word = "[^[:alnum:]_]"
        listed = subprocess.run(["git", "-C", str(repo), "grep", "--untracked", "-lE",
                                 rf"(^|{word})verify\.py({word}|$)|(import|from) verify({word}|$)",
                                 "--", "*.go", "*.py", "*.sh", "Makefile", "*.plist", "*.yml"],
                                capture_output=True, text=True, timeout=DRIVE_LIMIT)

        # Exact, so a pattern that stops matching fails here. The sweep is the one
        # other human-started command, and has its own check.
        self.assertEqual(set(listed.stdout.split()),
                         {"extractors/verify.py", "extractors/test_verify.py",
                          "extractors/sweep.py", "extractors/test_sweep.py"})


if __name__ == "__main__":
    unittest.main()
