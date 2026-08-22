#!/usr/bin/env python3
"""Commit the knowledge store's own git repo for what an extraction run wrote.

Python counterpart to internal/tui/knowledgegit.go, which does the same for the
promote and reject gestures. The extractor is the store's highest-volume writer:
without this every sweep leaves candidate files and a log.md entry as
working-tree state only, and anything reading the repo's history sees the corpus
stand still.

The unit is the run, not the file — one commit per sweep, matching the one entry
log.md already records — so a backlog of sweeps reads back as a list of
extraction events rather than a file-by-file diff.
"""
from __future__ import annotations

import os
import subprocess
import unicodedata
from datetime import datetime
from pathlib import Path

# GIT_TIMEOUT bounds every git invocation. This runs unattended on the
# LaunchAgent, so a child that blocks — on an index lock, a credential or a
# signing prompt — would wedge the sweep with nobody there to answer it; there
# is no interactive TUI to starve either, so the bound is looser than the TUI's.
GIT_TIMEOUT = 30

# REASON_MAX bounds the git failure text handed back to the caller, which prints
# it as one [extract] line. It bounds nothing that is written down:
# knowledge-git.log keeps git's whole output.
REASON_MAX = 60

# MESSAGE_MAX bounds the commit subject, which is also the body of the
# knowledge-git log record.
MESSAGE_MAX = 200


class NoGitRepoError(Exception):
    """A knowledge root that is not under version control, so the run can say
    the store isn't a repo rather than relay a git failure."""

    reason = "knowledge root is not a git repo"


class EnclosingRepoError(Exception):
    """A knowledge root that is itself untracked but sits inside a repo — a
    git-managed home directory, a dotfiles checkout. It degrades exactly like
    NoGitRepoError but needs its own reason, because the user can falsify "not a
    git repo" by running git status in the store."""

    reason = "knowledge root is inside another git repo"


def knowledge_git_log_path() -> Path:
    """Canonical log file for knowledge-store commits that could not be
    recorded. Mirrors internal/config.Home()."""
    home = os.environ.get("LOOM_HOME")
    return (Path(home) if home else Path.home() / ".loom") / "knowledge-git.log"


def _run_git(root: Path, *args: str) -> tuple[str, int | str]:
    """Run one git command in the knowledge store, returning its combined output
    and either git's exit status or, when git could not be run at all, a string
    describing why. Callers must keep the two apart: only an exit status means
    git ran and answered. Signing and hooks are both pinned off for the same
    reason: a sweep runs unattended, so a signing passphrase prompt or a hook
    that blocks would wedge it until GIT_TIMEOUT on every run. The store is a
    data store loom's bootstrap creates, not a project checkout whose hooks
    anyone meant to run here. core.hooksPath=/dev/null names a location git
    finds no hooks in — it is a file rather than a directory, and git treats
    that as no hooks rather than an error. The encoding is pinned rather than
    left to the locale: the LaunchAgent runs under C/POSIX, where a non-ASCII
    candidate name in git's output would raise UnicodeDecodeError — neither
    OSError nor TimeoutExpired, so it would escape this function's handlers and
    be recorded as a decode error instead of the git failure it hid."""
    full = ["git", "-C", str(root), "-c", "commit.gpgsign=false",
            "-c", "core.hooksPath=/dev/null", *args]
    try:
        proc = subprocess.run(full, stdout=subprocess.PIPE,
                              stderr=subprocess.STDOUT,
                              encoding="utf-8", errors="replace",
                              timeout=GIT_TIMEOUT)
    except OSError as exc:
        # A missing binary: nothing ran, so there is nothing it printed.
        return "", f"{type(exc).__name__}: {exc}"
    except subprocess.TimeoutExpired as exc:
        # A child killed at the deadline often named its own cause before it
        # blocked — a git waiting on index.lock says so — so keep what it wrote,
        # which arrives as bytes (or None) whatever the decoding args say.
        out = exc.output or b""
        if isinstance(out, bytes):
            out = out.decode(errors="replace")
        return out.strip(), f"{type(exc).__name__}: {exc}"
    return proc.stdout.strip(), proc.returncode


def _cause(out: str, err: int | str) -> str:
    """Pair git's output with what ended the child, so the two failures that
    print nothing — a git that could not be run, a child killed at GIT_TIMEOUT —
    still record a cause."""
    detail = f"exit status {err}" if isinstance(err, int) else err
    if not out:
        return detail
    return f"{detail}: {out}"


def commit_knowledge(root: Path, paths: list[Path], message: str) -> None:
    """Record the paths one extraction run wrote as a single commit in the
    knowledge store, raising on failure. The commit is path-scoped — `git add`
    over those paths, then `commit --only` with the same pathspec — because the
    store's working tree is routinely dirty with untracked candidates and edits
    this run did not make, and a whole-tree commit would absorb them into the
    record. Failures carry git's whole output, because the useful part is rarely
    the first line — a rejected commit leads with "On branch main" and names the
    cause below it — and the caller decides what one printed line can show."""
    top, err = _run_git(root, "rev-parse", "--show-toplevel")
    if err != 0:
        # rev-parse fails both for a store that is not a repo and for a git we
        # could not run at all. Blaming the store's layout for anything but an
        # exit status would send the user to fix a healthy repo.
        if not isinstance(err, int):
            raise RuntimeError(f"git rev-parse: {_cause(top, err)}")
        raise NoGitRepoError(f"{NoGitRepoError.reason}: {_cause(top, err)}")
    # rev-parse walks up the tree, so a store that is merely *inside* a repo
    # resolves to that ancestor and the commit would land in history nobody
    # pointed us at. The toplevel has to be the store itself. Both sides are
    # resolved first: git reports an absolute physical path, while the root
    # arrives as LOOM_KNOWLEDGE_ROOT verbatim, and on macOS a store under
    # /var/folders comes back as /private/var/folders — the raw strings would
    # never compare equal either way.
    if Path(top).resolve() != Path(root).resolve():
        raise EnclosingRepoError(f"{EnclosingRepoError.reason}: enclosing repo at {top}")
    # A path matters to git only if it exists or was tracked; the store carries
    # uncommitted candidates, and naming one that is neither is a fatal "did not
    # match any files" that would sink the whole commit.
    pathspec = []
    for p in paths:
        # Resolved for the same reason the toplevel is: git runs with -C root,
        # so a path the caller built against the process cwd — a relative
        # LOOM_KNOWLEDGE_ROOT gives relative paths — would be re-resolved
        # against the store, match nothing, and sink the commit.
        full = str(Path(p).resolve())
        if not Path(full).exists():
            _, err = _run_git(root, "ls-files", "--error-unmatch", "--", full)
            if err != 0:
                continue
        pathspec.append(full)
    if not pathspec:
        raise RuntimeError("no tracked paths to commit")
    out, err = _run_git(root, "add", "--", *pathspec)
    if err != 0:
        raise RuntimeError(f"git add: {_cause(out, err)}")
    out, err = _run_git(root, "commit", "--only", "-m", message, "--", *pathspec)
    if err != 0:
        # A failed commit leaves the run staged, and the next commit anyone
        # makes in the store would absorb it — the mirror of the absorption the
        # pathspec scoping exists to prevent.
        _run_git(root, "reset", "-q", "--", *pathspec)
        raise RuntimeError(f"git commit: {_cause(out, err)}")


def record_knowledge_commit(root: Path, paths: list[Path], message: str) -> str:
    """Commit one extraction run and return "" on success or a short reason on
    failure. Every exception is caught, anticipated or not: an unattended sweep
    must not fail the extraction because the commit failed. A failure is never
    silent — it is appended to knowledge_git_log_path() in full, and its head
    handed back for the caller to print."""
    message = sanitize_record(message)
    try:
        commit_knowledge(root, paths, message)
        return ""
    except Exception as exc:
        # Flattened but not bounded: the log is the debugging mechanism for
        # these commits, so it keeps every line the failure produced.
        _append_knowledge_git_log(f"{message}: {_flatten_record(str(exc))}")
        # Both no-repo shapes degrade the same way; the sentinel text is the
        # whole printed reason, since the enclosing path belongs in the log.
        if isinstance(exc, (NoGitRepoError, EnclosingRepoError)):
            return type(exc).reason
        return _short_git(str(exc))


def sanitize_record(message: str) -> str:
    """Flatten a run's message into a single bounded record — the commit
    subject, which is also the body of the knowledge-git log line. The subject
    names a scope resolved from --scope/resolve_project and a session id read
    out of the input file's frontmatter, so this is defence in depth at the
    boundary that writes the record rather than a check on any one caller."""
    flat = _flatten_record(message)
    # Python slices by code point, so a non-ASCII id near the limit is never cut
    # mid-character into the very record _flatten_record keeps readable.
    if len(flat) <= MESSAGE_MAX:
        return flat
    return flat[:MESSAGE_MAX - 1] + "…"


def _flatten_record(s: str) -> str:
    """Map every character that could break a one-line record onto a space:
    control characters (Cc), the Unicode format characters (Cf — bidi overrides
    and isolates, which reorder a record without appearing in it) and the line
    and paragraph separators (Zl, Zp). Unfiltered, one of these would forge a
    commit-message trailer, an extra knowledge-git.log line, or a misleading
    rendering of either — corrupting the audit trail these records exist to be."""
    return "".join(
        " " if unicodedata.category(c) in ("Cc", "Cf", "Zl", "Zp") else c
        for c in s
    ).strip()


def _append_knowledge_git_log(line: str) -> None:
    """Append one timestamped line to the knowledge-git log. A log that cannot
    be written is not worth failing a sweep over — and this runs inside
    record_knowledge_commit's handler, so anything escaping here would fail the
    extraction over a failed commit, which is the one outcome that path exists
    to prevent. The guard is Exception rather than OSError because resolving the
    default root raises RuntimeError when there is no home directory to find,
    where internal/config.Home() falls back instead of raising."""
    path = knowledge_git_log_path()
    try:
        # Modes mirror the Go writer's: the TUI gestures append to this same
        # file, and whichever side creates it sets what the other inherits.
        path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_APPEND, 0o600)
        with os.fdopen(fd, "a") as f:
            f.write(f"{datetime.now().astimezone().replace(microsecond=0).isoformat()} {line}\n")
    except Exception:
        return


def _short_git(out: str) -> str:
    """Collapse a failure to its first line, bounded, so it fits one printed
    line. This is the display boundary only — the whole text is logged first."""
    first = out.split("\n", 1)[0]
    if len(first) <= REASON_MAX:
        return first
    return first[:REASON_MAX - 1] + "…"
