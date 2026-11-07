#!/usr/bin/env python3
"""Sweep every validated artifact in the knowledge store, a human judging each truth.

Walks truths/ and decisions/ as walkArtifacts does, offers each truth's `## How to
verify` blocks through verify.py, one confirmation per block, then shows what the
truth expects and asks whether it holds. Only that answer decides an outcome: exit
codes are printed as facts. An explicit `fails` demotes the truth to `deprecated`
in place, with what ran recorded on it. No flag skips a confirmation or answers for
the person at the terminal, and nothing unattended may call this: not the
extraction sweep, a daemon or a hook. See docs/transcript-trust-and-redaction.md.

Every artifact gets exactly one outcome, so the counts sum to the store's validated
total. Scope liveness is reported beside the outcome and never changes it.

Ctrl-C stops the sweep. The artifact in progress gets no outcome and nothing is
written for it, the report covers what was reached, the rest are counted as not
reached, and the exit status is 1. A demotion already under way finishes first, so
the report can say what became of it. The exit status is also 1 when a demotion
did not land cleanly.

Usage:
    ./sweep.py
"""
from __future__ import annotations

import argparse
import glob
import os
import re
import signal
import subprocess
import sys
import termios
import time
from collections import Counter
from dataclasses import dataclass
from datetime import date
from functools import cache
from pathlib import Path

from knowledge_store import StoreWriteError, apply_changes, knowledge_root, write_change
from redact import redact
from resolve_project import NAME_PATTERN
from verify import (FRONTMATTER_KEY, VERIFY_HEADING, checkout, offer, read_frontmatter,
                    refuse, require_terminal, verify_blocks, visible)

ARTIFACT_DIRS = ("truths", "decisions")

PASS = "pass"
FAILED = "failed"
EVIDENCE_MISSING = "evidence-missing"
UNVERIFIABLE = "unverifiable"
UNDECIDED = "undecided"
SKIPPED = "skipped"
OUTCOMES = (PASS, FAILED, EVIDENCE_MISSING, UNVERIFIABLE, UNDECIDED, SKIPPED)

HOLDS = "holds"
FAILS = "fails"

LIVE = "live"
INERT = "inert"
UNKNOWN = "unknown"
LIVENESS_DAYS = 60
GIT_TIMEOUT = 10

# Same reading as evidencePath in internal/knowledge/knowledge.go. ASCII because
# RE2's \s is.
EVIDENCE_PATH = re.compile(r"^\s*-\s*path:\s*(.*)$", re.ASCII)
# A value the extractor left a trailing note on, as truths/_schema.md's template does.
YAML_COMMENT = re.compile(r"\s+#.*$")
# A placeholder is not a verification. Leading, as parseSourceDate reads a date.
VERIFIED_DATE = re.compile(r"\d{4}-\d{2}-\d{2}")

# Characters kept of one stream across a demoted artifact's whole record, shared
# out among the commands that ran. The record is committed and pushed; a test
# suite's output is not what a reader of it needs.
RECORD_LIMIT = 8000

# Starts every recorded line, so nothing a command printed can be a fence, a
# heading or a `---` to anything that reads the file. The readers differ in what
# they honour: claimSection ignores fences altogether, so a fence alone would not do.
RECORD_GUTTER = "| "


@dataclass
class Artifact:
    path: Path
    plural: str
    scope: str
    data: bytes
    lines: list[str]
    fields: dict[str, str]
    body_start: int
    problem: str = ""  # why this file's contents may not be acted on, if anything

    @property
    def label(self) -> str:
        return f"{self.plural}/{self.scope}/{self.path.name}"


@dataclass
class Result:
    label: str
    outcome: str
    liveness: str
    detail: str = ""
    ran: tuple = ()  # (block number, block, stdout, stderr, ending) of each that ran
    blocks: int = 0
    demotion_problem: str = ""  # why a FAILED truth's demotion did not land cleanly


def load_artifact(path: Path, plural: str, scope: str, linked: bool) -> Artifact | None:
    """The file as a validated artifact, or None when Load would not read it as
    one. Bytes rather than read_text(), for the reason load_truth gives. A file
    that is not UTF-8 still counts, since the Go loader counts it, but nothing in
    it can be shown or rewritten faithfully."""
    try:
        data = path.read_bytes()
    except OSError as exc:
        print(f"[sweep] cannot read {visible(str(path))}, not counted: {visible(str(exc))}")
        return None
    # The Go loader follows a link, so it counts; its target's place in the store
    # is not this one, and the store refuses to write through it.
    problem = "is reached through a link, so its place here says nothing about its target" if linked else ""
    try:
        text = data.decode("utf-8")
    except UnicodeDecodeError:
        text = data.decode("utf-8", "replace")
        problem = "is not valid UTF-8"
    lines = text.split("\n")
    fields, body_start = read_frontmatter(lines)
    # parseArtifact defaults a file under truths/ or decisions/ to validated, so
    # one with no status is in the store's count though nothing here will run it.
    if not fields.get("id") or fields.get("status", "") not in ("", "validated"):
        return None
    return Artifact(path, plural, scope, data, lines, fields, body_start, problem)


def validated_artifacts() -> list[Artifact]:
    """Every artifact validated by its location under truths/ and decisions/, so
    not a _candidates/ file that says status: validated, by walkArtifacts' file
    rule: scope directories without a leading _ (a link to one is not a directory
    to it), and .md files in them with no leading _ that are not README.md and
    carry an id. A FIFO or a directory is passed over rather than read, which would
    block or fail."""
    found = []
    root = knowledge_root()
    for plural in ARTIFACT_DIRS:
        base = root / plural
        if not base.is_dir():
            continue
        home = root.resolve() / plural
        for scope in sorted(os.scandir(base), key=lambda e: e.name):
            if scope.name.startswith("_") or not scope.is_dir(follow_symlinks=False):
                continue
            for entry in sorted(os.scandir(scope.path), key=lambda e: e.name):
                name = entry.name
                if not name.endswith(".md") or name.startswith("_") or name == "README.md":
                    continue
                if not entry.is_file():
                    continue
                path = base / scope.name / name
                # Whole, because truths/ itself may be the link. The root may
                # legitimately be one, so it is resolved on both sides.
                linked = path.resolve() != home / scope.name / name
                artifact = load_artifact(path, plural, scope.name, linked)
                if artifact:
                    found.append(artifact)
    return found


def evidence_paths(artifact: Artifact) -> list[str]:
    """The `- path:` values of the evidence block, read as parseArtifact reads them
    and then unquoted, since the extractor writes both forms. A `- commit:` is not
    a path and takes no part."""
    paths = []
    key = ""
    for line in artifact.lines[1:artifact.body_start - 1]:
        if line.startswith((" ", "\t", "-")):
            m = EVIDENCE_PATH.match(line)
            if key == "evidence" and m:
                value = YAML_COMMENT.sub("", m[1]).strip().strip("\"'")
                if value:
                    paths.append(value)
            continue
        m = FRONTMATTER_KEY.match(line)
        if m:
            key = m[1]
    return paths


def evidence_exists(value: str, root: Path) -> bool:
    """Whether an evidence path resolves: project-relative, or ~ or absolute per
    truth-extractor.md rule 2. A pattern is a real form (`agents/*.md`), and is
    tried after the literal so a name holding [ or * still counts as itself."""
    path = os.path.expanduser(value)
    pattern = os.path.join(glob.escape(str(root)), path)
    return os.path.exists(root / path) or bool(glob.glob(pattern, recursive=True))


@cache
def liveness(root: Path | None) -> str:
    """Whether the checkout has had a commit on any ref in LIVENESS_DAYS, or UNKNOWN
    when that can't be read. The ceiling keeps a plain directory inside some other
    repo from reporting that repo's age, and an empty repo exits 0 with no output."""
    if root is None:
        return UNKNOWN
    env = dict(os.environ, GIT_CEILING_DIRECTORIES=str(root.parent))
    try:
        proc = subprocess.run(["git", "-C", str(root), "log", "-1", "--all", "--format=%ct"],
                              stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
                              encoding="utf-8", errors="replace", env=env, timeout=GIT_TIMEOUT)
        committed = int(proc.stdout)
    except (OSError, subprocess.TimeoutExpired, ValueError):
        return UNKNOWN
    if proc.returncode != 0:
        return UNKNOWN
    return INERT if time.time() - committed > LIVENESS_DAYS * 86400 else LIVE


def unverifiable_reason(artifact: Artifact, root: Path | None, root_reason: str, blocks: list[str]) -> str:
    """Why nothing of this artifact may be offered, or "". Location, status and
    scope are load_truth's rule, held here without its refusal: a truth directly at
    truths/<scope>/ with status validated, and no other kind of artifact."""
    if artifact.problem:
        return artifact.problem
    if artifact.plural != "truths":
        return "a decision: only a truth's checks are ever run"
    if artifact.fields.get("status") != "validated":
        return "no frontmatter status: validated"
    if not NAME_PATTERN.match(artifact.scope):
        return f"{artifact.scope} is not a scope (must match {NAME_PATTERN.pattern})"
    if not VERIFIED_DATE.match(artifact.fields.get("verified_at", "")):
        return "no verified_at"
    if root is None:
        return root_reason
    if not blocks:
        return f"no code blocks under {VERIFY_HEADING}"
    return ""


def show_expected(prose: list[str]):
    """Print what the truth expects. Store text is untrusted, so it is escaped and
    carries show()'s gutter, which keeps it from passing for this program's own."""
    text = "\n".join(prose).strip("\n")
    print(f"\n[sweep] {VERIFY_HEADING} says, around its commands:")
    for line in text.split("\n"):
        print("    | " + visible(line, keep="\t"))


def verdict() -> str:
    # Flushed for the reason confirmed() flushes: a y typed during the last
    # command must not land here.
    termios.tcflush(sys.stdin, termios.TCIFLUSH)
    try:
        return input(f"[sweep] does this truth still hold? {HOLDS}, {FAILS}, anything else is undecided: ").strip()
    except EOFError:
        return ""


def sweep_artifact(artifact: Artifact, number: int, total: int) -> Result:
    """Offer one artifact's checks and take the human's answer. Writes nothing:
    a `fails` comes back as a FAILED result for settle() to demote."""
    root, root_reason = checkout(artifact.scope)
    live = liveness(root)
    flag = f", scope {INERT}: no commits in {LIVENESS_DAYS} days" if live == INERT else ""
    print(f"\n[sweep] {number} of {total}: {visible(artifact.label)}{flag}")

    blocks, prose, unclosed = verify_blocks(artifact.lines[artifact.body_start:])
    gone = [] if root is None else [p for p in evidence_paths(artifact) if not evidence_exists(p, root)]
    detail = f"evidence gone: {', '.join(gone)}" if gone else ""
    if gone:
        print(f"[sweep] {visible(detail)}")

    reason = unverifiable_reason(artifact, root, root_reason, blocks)
    if reason:
        print(f"[sweep] unverifiable, nothing offered: {visible(reason)}")
        return Result(artifact.label, UNVERIFIABLE, live, f"{reason}; {detail}" if detail else reason)
    if unclosed:
        print(f"[sweep] a code fence under {VERIFY_HEADING} never closes; that block is not offered")

    ran = []
    for block_number, block in enumerate(blocks, 1):
        output = offer(block, root, block_number, len(blocks))
        if output:
            ran.append((block_number, block, *output))
    if not ran:
        return Result(artifact.label, EVIDENCE_MISSING if gone else SKIPPED, live, detail)

    show_expected(prose)
    answer = verdict()
    if answer == FAILS:
        outcome = FAILED
    elif gone:
        outcome = EVIDENCE_MISSING
    else:
        outcome = PASS if answer == HOLDS else UNDECIDED
    return Result(artifact.label, outcome, live, detail, tuple(ran), len(blocks))


def recorded(text: str, limit: int) -> list[str]:
    """`text` as lines safe to commit to the store. Redacted first, whole: a cut
    could take a credential below its pattern's minimum length. Escaped next, so a
    control character neither reaches a reader's terminal nor splits a line, and
    then bounded, by the middle because a failure tends to be at the end and its
    cause at the start."""
    safe = visible(redact(text), keep="\t\n")
    if len(safe) > limit:
        safe = f"{safe[:limit // 2]}\n[{len(safe) - limit} characters cut]\n{safe[-limit // 2:]}"
    return [RECORD_GUTTER + line for line in safe.split("\n")]


def demotion_section(ran: tuple, blocks: int) -> list[str]:
    """The section explaining a demotion. Every command that ran is recorded rather
    than one, because nothing here can tell which of them showed the failure: that
    is the human's reading, and exit codes do not get to pick."""
    limit = RECORD_LIMIT // len(ran)
    lines = ["## Demoted", "",
             f"Deprecated {date.today().isoformat()}. A human ran this truth's checks with "
             "`extractors/sweep.py` and judged that it no longer holds.", ""]
    if len(ran) < blocks:
        lines += [f"{len(ran)} of its {blocks} commands ran; the rest were declined.", ""]
    lines += ["What ran, with output redacted and bounded:", ""]
    for number, block, stdout, stderr, ending in ran:
        lines += ["```", f"{RECORD_GUTTER}command {number} of {blocks}:",
                  *recorded(block, limit), *recorded(ending, limit),
                  f"{RECORD_GUTTER}stdout:", *recorded(stdout.decode("utf-8", "backslashreplace"), limit),
                  f"{RECORD_GUTTER}stderr:", *recorded(stderr.decode("utf-8", "backslashreplace"), limit),
                  "```", ""]
    return lines + ["The sections below are the original record, unchanged."]


def demoted_text(artifact: Artifact, section: list[str]) -> str:
    """The file with its effective top-level status deprecated and `section` above
    the original body. The last status line is the effective one to both parsers;
    FRONTMATTER_KEY reads no indented line, so a contradicts entry's is left alone.
    Everything else is the file's own text, CRs included."""
    lines = list(artifact.lines)
    head = range(1, artifact.body_start - 1)
    status = max(i for i in head if (m := FRONTMATTER_KEY.match(lines[i])) and m[1] == "status")
    lines[status] = lines[status].replace("validated", "deprecated", 1)
    body = lines[artifact.body_start:]
    gap = [""] if body and body[0].strip() else []
    return "\n".join(lines[:artifact.body_start] + ["", *section, *gap] + body)


def demote(artifact: Artifact, result: Result) -> str:
    """Demote the truth through the store and say what kept that from landing
    cleanly, or "". The file is compared with what was read because a command the
    human confirmed, or anything else in the time since, may have changed it, and
    rewriting from the stale copy would undo that silently."""
    try:
        if artifact.path.read_bytes() != artifact.data:
            return "the file changed during the sweep, so it was left alone"
    except OSError as exc:
        return f"cannot re-read the file: {exc}"
    body = demoted_text(artifact, demotion_section(result.ran, result.blocks))
    try:
        reason = apply_changes(f"demote truth {artifact.scope}/{artifact.fields['id']}",
                               [write_change(artifact.path, body)])
    except StoreWriteError as exc:
        return f"store write failed, treat it as still validated: {exc}"
    return f"deprecated on disk but not committed: {reason}" if reason else ""


def demotion_note(result: Result) -> str:
    if result.demotion_problem:
        return f"demotion did not land cleanly: {visible(result.demotion_problem)}"
    return "demoted to deprecated"


def settle(artifact: Artifact, result: Result, results: list[Result]):
    """Demote if the answer was fails, and record the result. SIGINT is ignored
    across both, and the ignore is inherited by the writer, so that a Ctrl-C cannot
    leave a demotion the report does not know about, or kill the writer halfway.
    apply_changes bounds how long this can last."""
    signal.signal(signal.SIGINT, signal.SIG_IGN)
    try:
        if result.outcome == FAILED:
            result.demotion_problem = demote(artifact, result)
            print(f"[sweep] {demotion_note(result)}")
        results.append(result)
    finally:
        signal.signal(signal.SIGINT, signal.default_int_handler)


def report(results: list[Result], total: int):
    print("\n[sweep] report")
    for r in results:
        notes = [demotion_note(r)] if r.outcome == FAILED else []
        if r.detail:
            notes.append(visible(r.detail))
        suffix = f" ({'; '.join(notes)})" if notes else ""
        print(f"[sweep] {r.outcome:<16} scope {r.liveness:<7} {visible(r.label)}{suffix}")
    outcomes = Counter(r.outcome for r in results)
    counts = [f"{o}: {outcomes[o]}" for o in OUTCOMES]
    if len(results) < total:
        counts.append(f"not reached: {total - len(results)}")
    print(f"[sweep] {', '.join(counts)}")
    print(f"[sweep] validated artifacts in the store: {total}")
    troubled = sum(1 for r in results if r.demotion_problem)
    print(f"[sweep] demoted: {outcomes[FAILED] - troubled}, demotion did not land cleanly: {troubled}")
    scopes = Counter(r.liveness for r in results)
    print(f"[sweep] scope liveness: {LIVE}: {scopes[LIVE]}, "
          f"{INERT} (no commits in {LIVENESS_DAYS} days): {scopes[INERT]}, {UNKNOWN}: {scopes[UNKNOWN]}")


def main():
    p = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    p.parse_args()

    # Output is arbitrary text and the terminal's encoding is whatever it is.
    sys.stdout.reconfigure(errors="backslashreplace")

    require_terminal()
    try:
        artifacts = validated_artifacts()
    except OSError as exc:
        refuse(f"cannot walk the knowledge store: {visible(str(exc))}")
    print(f"[sweep] {visible(str(knowledge_root()))}: {len(artifacts)} validated artifacts")

    results: list[Result] = []
    interrupted = False
    try:
        for number, artifact in enumerate(artifacts, 1):
            settle(artifact, sweep_artifact(artifact, number, len(artifacts)), results)
    except KeyboardInterrupt:
        interrupted = True
        print("\n[sweep] interrupted: the artifact in progress gets no outcome and nothing "
              "was written for it, though a command it offered may have run")

    report(results, len(artifacts))
    if interrupted or any(r.demotion_problem for r in results):
        sys.exit(1)


if __name__ == "__main__":
    main()
