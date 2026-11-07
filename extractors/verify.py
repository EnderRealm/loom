#!/usr/bin/env python3
"""Run a validated truth's verification commands, each on a human's confirmation.

Takes one file directly under truths/<scope>/ in the knowledge store, shows each
fenced block of its `## How to verify` section, and runs a block only when the
person at the terminal answers y to it. Promotion reviewed the truth's claim, not
its text as a command (docs/transcript-trust-and-redaction.md), so that answer is
the only thing that authorizes a run: there is no flag to skip it, and only a
command a human started may reach this code. That is this one or
extractors/sweep.py; the unattended extraction sweep, a daemon or a hook never does.

Output is dumped for a human to read. Nothing here decides whether a truth still
holds, and the exit status says only whether the file was refused.

Usage:
    ./verify.py <truth-file>
"""
from __future__ import annotations

import argparse
import os
import re
import signal
import subprocess
import sys
import termios
from pathlib import Path
from typing import NoReturn

from knowledge_store import knowledge_root
from resolve_project import NAME_PATTERN, project_path

VERIFY_HEADING = "## How to verify"

# Bounds one block, with its background children. Sized for a project's whole
# test suite: what it guards is a block that never returns, not a slow one.
COMMAND_TIMEOUT = 300

# Same reading as parseArtifact in internal/knowledge/knowledge.go.
FRONTMATTER_KEY = re.compile(r"^([a-z_]+):\s*(.*)$")

# CommonMark's opening fence, whose backtick form may not hold another backtick:
# ```a``` on one line is inline code.
FENCE_OPEN = re.compile(r"^ {0,3}(`{3,}(?=[^`]*$)|~{3,})")


def refuse(reason: str) -> NoReturn:
    sys.exit(f"[verify] refused, nothing ran: {reason}")


def visible(text: str, keep: str = "") -> str:
    """`text` with every character that does not print as itself spelled as a
    backslash escape, except those in `keep`. Covers more than C0: a bidi
    override or a zero-width character misleads a reader as well as an ESC or a
    CR does, and U+2028 breaks a line."""
    return "".join(c if c.isprintable() or c in keep else c.encode("unicode_escape").decode("ascii")
                   for c in text)


def read_frontmatter(lines: list[str]) -> tuple[dict[str, str], int]:
    """Top-level frontmatter keys and the index the body starts at, as
    parseArtifact reads them: indented and list lines belong to the key above
    them, so a contradicts entry's `status:` is not the artifact's, and the last
    duplicate wins. Empty when the file has no frontmatter block."""
    if len(lines) < 2 or lines[0].strip() != "---":
        return {}, 0
    end = next((i for i in range(1, len(lines)) if lines[i].strip() == "---"), None)
    if end is None:
        return {}, 0
    fields = {}
    for line in lines[1:end]:
        if line.startswith((" ", "\t", "-")):
            continue
        m = FRONTMATTER_KEY.match(line)
        if m:
            fields[m[1]] = m[2].strip()
    return fields, end + 1


def load_truth(arg: str) -> tuple[Path, str, list[str]]:
    """Path, scope and body lines of the validated truth `arg` names, or a
    refusal. Location and status are both required, the location read from the
    resolved path so a link cannot lend a candidate a truth's place. What counts
    as an artifact file at all is walkArtifacts' rule in internal/knowledge:
    .md, no leading _, not README.md, and an id."""
    try:
        path = Path(arg).expanduser().resolve()
        root = knowledge_root().resolve()
    except (OSError, RuntimeError) as exc:
        refuse(f"cannot resolve {visible(arg)}: {visible(str(exc))}")
    shown = visible(str(path))
    if not path.is_relative_to(root):
        refuse(f"{shown} is outside the knowledge store {visible(str(root))}")
    parts = path.relative_to(root).parts
    if len(parts) != 3 or parts[0] != "truths":
        refuse(f"{shown} is not directly at truths/<scope>/ in the knowledge store")
    _, scope, name = parts
    if not NAME_PATTERN.match(scope):
        refuse(f"{visible(scope)} is not a scope (must match {NAME_PATTERN.pattern})")
    if not name.endswith(".md") or name.startswith("_") or name == "README.md":
        refuse(f"{shown} is not an artifact file")
    if not path.is_file():
        refuse(f"{shown} is not a regular file")
    try:
        # Bytes rather than read_text(): universal newlines would turn a lone CR
        # into a line break the Go reader does not see, so a `status:` hidden
        # behind one would count here alone.
        text = path.read_bytes().decode("utf-8")
    except (OSError, UnicodeDecodeError) as exc:
        refuse(f"cannot read {shown}: {visible(str(exc))}")
    lines = text.split("\n")
    fields, body_start = read_frontmatter(lines)
    if not fields.get("id"):
        refuse(f"{shown} has no frontmatter id")
    if fields.get("status") != "validated":
        refuse(f"{shown} does not have frontmatter status: validated")
    return path, scope, lines[body_start:]


def checkout(scope: str) -> tuple[Path | None, str]:
    """The scope's registered checkout, or None and the reason there is none.
    Absolute as well as existing: a relative value would resolve against wherever
    this was started."""
    path, reason = project_path(scope)
    if not path:
        return None, reason
    root = Path(path)
    if not root.is_absolute() or not root.is_dir():
        return None, f"{path}, registered for {scope}, is not an existing directory"
    return root, ""


def project_root(scope: str) -> Path:
    """The scope's registered checkout, or a refusal."""
    root, reason = checkout(scope)
    if root is None:
        refuse(visible(reason))
    return root


def verify_blocks(body: list[str]) -> tuple[list[str], list[str], bool]:
    """The fenced blocks of the verify section, the prose around them, and whether
    a block in it never closes. Fences are tracked from the top of the body
    because a `## ` line inside one is text: a shell comment must not end the
    section, nor a quoted heading start it. An unclosed block is left out, since
    it has swallowed everything to the end of the file, the following sections
    included. The prose holds a `[command N]` line where block N stood, because
    what a truth expects is usually written after the command it is about."""
    blocks: list[str] = []
    prose: list[str] = []
    in_section = False
    close = None
    current: list[str] = []
    for line in body:
        if close:
            if close.fullmatch(line):
                if in_section and "\n".join(current).strip():
                    blocks.append("\n".join(current))
                    prose.append(f"[command {len(blocks)}]")
                close, current = None, []
            else:
                current.append(line)
            continue
        m = FENCE_OPEN.match(line)
        if m:
            fence = re.escape(m[1])
            close = re.compile(rf" {{0,3}}{fence}{re.escape(m[1][0])}*\s*")
        elif in_section and line.startswith("## "):
            break
        elif line.strip() == VERIFY_HEADING:
            in_section = True
        elif in_section:
            prose.append(line)
    return blocks, prose, in_section and close is not None


def show(block: str, cwd: Path, number: int, total: int):
    """Print a block as it will run. The gutter keeps a line of the block from
    passing for one of this program's own."""
    lines = block.split("\n")
    print(f"\n[verify] command {number} of {total}, {len(lines)} line(s), cwd: {visible(str(cwd))}")
    for line in lines:
        print("    | " + visible(line, keep="\t"))
    if visible(block, keep="\t\n") != block:
        print("[verify] this command holds non-printing characters, shown above as backslash escapes")


def confirmed() -> bool:
    # Anything already typed was typed before this command was on screen, and a
    # y left over from the wait on the last one must not answer for this one.
    termios.tcflush(sys.stdin, termios.TCIFLUSH)
    try:
        return input("[verify] run this command? y runs it, anything else skips: ").strip() == "y"
    except EOFError:
        return False


def kill_group(proc: subprocess.Popen):
    try:
        os.killpg(proc.pid, signal.SIGKILL)
    except (ProcessLookupError, PermissionError):
        pass  # Already gone; Darwin answers EPERM for a group of only zombies.


def run(block: str, cwd: Path) -> tuple[bytes, bytes, str]:
    """Run one block and return its stdout, stderr and how it ended. Its own
    session gives it no controlling terminal, so it cannot prompt on /dev/tty or
    push input at the next confirmation, and gives the deadline a group to kill:
    the shell alone would leave the block's children running. Output is captured,
    not inherited, so a child that outlives the shell has no way onto the screen
    during a later command's confirmation.

    The timeout path takes the partial output from the exception rather than a
    second communicate(), which a child that left the group could hold open."""
    try:
        with subprocess.Popen(block, shell=True, cwd=cwd, stdin=subprocess.DEVNULL,
                              stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                              start_new_session=True) as proc:
            try:
                stdout, stderr = proc.communicate(timeout=COMMAND_TIMEOUT)
            except subprocess.TimeoutExpired as exc:
                kill_group(proc)
                return exc.stdout or b"", exc.stderr or b"", f"timed out, killed after {COMMAND_TIMEOUT}s"
            except KeyboardInterrupt:
                kill_group(proc)
                raise
    except (OSError, ValueError) as exc:
        # ValueError is a NUL byte, which no argv can carry.
        return b"", b"", f"did not start: {exc}"
    return stdout, stderr, f"exit code: {proc.returncode}"


def dump(label: str, data: bytes):
    """Print captured output with its text and line structure intact. Control
    characters are spelled out rather than passed: output sent raw could conceal
    the next command's display, rewrite this one's, or draw a terminal reply that
    lands on the next prompt."""
    print(f"[verify] {label}:")
    text = visible(data.decode("utf-8", "backslashreplace"), keep="\t\n")
    if text:
        print(text, end="" if text.endswith("\n") else "\n")


def offer(block: str, cwd: Path, number: int, total: int) -> tuple[bytes, bytes, str] | None:
    """Show one block and, on confirmation, run it and dump what came back: its
    stdout, stderr and how it ended, or None when it was declined."""
    show(block, cwd, number, total)
    if not confirmed():
        print("[verify] skipped")
        return None
    stdout, stderr, ending = run(block, cwd)
    dump("stdout", stdout)
    dump("stderr", stderr)
    print(f"[verify] {visible(ending)}")
    return stdout, stderr, ending


def require_terminal():
    # stdout as well as stdin: a command shown into a pipe was not shown.
    if not (sys.stdin.isatty() and sys.stdout.isatty()):
        refuse("each command needs a human's confirmation, and stdin and stdout are not both a terminal")


def main():
    p = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    p.add_argument("truth", help="truth file under truths/<scope>/ in the knowledge store")
    args = p.parse_args()

    # Output is arbitrary text and the terminal's encoding is whatever it is.
    sys.stdout.reconfigure(errors="backslashreplace")

    path, scope, body = load_truth(args.truth)
    cwd = project_root(scope)
    blocks, _, unclosed = verify_blocks(body)
    if unclosed:
        print(f"[verify] a code fence under {VERIFY_HEADING} never closes; that block is not offered")
    if not blocks:
        print(f"[verify] no code blocks under {VERIFY_HEADING} in {visible(str(path))}; nothing to run")
        return
    require_terminal()

    print(f"[verify] {visible(str(path))}")
    try:
        for number, block in enumerate(blocks, 1):
            offer(block, cwd, number, len(blocks))
    except KeyboardInterrupt:
        sys.exit("\n[verify] interrupted")


if __name__ == "__main__":
    main()
