#!/usr/bin/env python3
"""Write the durable knowledge store through loom's own entry point.

A thin client over `loom knowledge write`: it builds the JSON plan and hands it
to the binary, which applies the changes and commits what they touched. There is
deliberately no git here — the store's rules (path-scoped commits, an untouched
dirty tree, the non-repo and enclosing-repo reasons, record sanitization) live in
internal/knowledge/store, in the repo that owns the store, and a second
implementation on this side is what this module exists to remove. The plan's op
vocabulary is cmd/loom/cmd/knowledge.go's; see docs/knowledge-store-writes.md.

There is no fallback to writing the files directly: a run that cannot reach the
binary fails loudly rather than leaving the store further from its own history,
which is the state a second writer produces.
"""
from __future__ import annotations

import json
import os
import shutil
import subprocess
import sys
from pathlib import Path

# WRITE_TIMEOUT bounds the child, which runs several git invocations under its
# own per-command bound. Generous by comparison: what this guards is a loom that
# never returns at all, not a slow git. The push is the one of those invocations
# that realistically runs to its full 10s bound, since it talks to a remote.
WRITE_TIMEOUT = 90


class StoreWriteError(Exception):
    """A write that did not land: the binary could not be run, or it refused or
    failed the plan. Fatal to the caller — the store did not get what the run
    produced, and there is nowhere else to put it."""


def knowledge_root() -> Path:
    """The store this run writes, resolved as the Go side resolves it
    (internal/knowledge.Root over internal/config.Home): LOOM_KNOWLEDGE_ROOT,
    else $LOOM_HOME/knowledge, else ~/.loom/knowledge. One rule for both sides —
    a default here that ignored LOOM_HOME would build paths under a store the Go
    entry point never resolved, and every one of them would be refused as outside
    the store. Neither value is expanded: the Go side takes both verbatim, and a
    literal ~ expanded here alone would put the two sides exactly one step out of
    agreement, which is the failure this exists to prevent."""
    root = os.environ.get("LOOM_KNOWLEDGE_ROOT")
    if root:
        return Path(root)
    home = os.environ.get("LOOM_HOME")
    return (Path(home) if home else Path.home() / ".loom") / "knowledge"


def write_change(path: Path | str, body: str) -> dict:
    """One file written, its parent directories created."""
    return {"op": "write", "path": str(path), "body": body}


def append_change(path: Path | str, text: str) -> dict:
    """Text appended to an existing file. The store never creates one here:
    log.md is bootstrapped at store init, and a run pointed at a wrong root must
    not scatter one."""
    return {"op": "append", "path": str(path), "text": text}


def apply_changes(message: str, changes: list[dict]) -> str:
    """Apply one unit of work and return the reason its commit did not land, or
    "" when it did. A failed commit is a warning, not a failure: the writes are
    on disk, and the whole failure is in ~/.loom/knowledge-git.log. A failed
    write raises.

    A commit that landed but was not pushed is reported on stderr here rather
    than returned: the record exists and the next unit of work's push carries it
    when the failure was transient, so it is not the caller's decision to make —
    but it must not be silent. A remote that has diverged rejects every later
    push the same way, and stays unpublished until a human pulls."""
    binary = _loom_bin()
    plan = json.dumps({"message": message, "changes": changes})
    try:
        # The encoding is pinned rather than left to the locale: the LaunchAgent
        # runs under C/POSIX, where a non-ASCII candidate in the plan or in the
        # reason coming back would raise UnicodeDecodeError — neither OSError nor
        # TimeoutExpired, so it would escape these handlers.
        proc = subprocess.run([binary, "knowledge", "write"], input=plan,
                              stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                              encoding="utf-8", errors="replace",
                              timeout=WRITE_TIMEOUT)
    except (OSError, subprocess.TimeoutExpired) as exc:
        # A binary that could not be run and a child killed at the deadline are
        # the same outcome here: the plan did not land, and the type names which.
        raise StoreWriteError(f"{binary} knowledge write: {type(exc).__name__}: {exc}") from exc
    if proc.returncode != 0:
        detail = (proc.stderr or proc.stdout or "").strip().replace("\n", " ")
        raise StoreWriteError(f"{binary} knowledge write: exit status {proc.returncode}: {detail}")
    warn, not_pushed = _outcomes(proc.stdout)
    if not_pushed:
        print(f"knowledge store not pushed: {not_pushed}", file=sys.stderr)
    return warn


def _loom_bin() -> str:
    """The binary to write through: LOOM_BIN — which internal/extract pins to the
    loom that spawned this run, so a sweep writes through the same build it was
    launched from — else `loom` on PATH."""
    env = os.environ.get("LOOM_BIN", "").strip()
    if env:
        return env
    found = shutil.which("loom")
    if found:
        return found
    raise StoreWriteError("no loom binary: LOOM_BIN is unset and loom is not on PATH")


def _outcomes(stdout: str) -> tuple[str, str]:
    """The commit's outcome and the push's, as the subcommand reports them, read
    from one parse so the unreadable case is handled once. A response we cannot
    read is reported as the commit's reason rather than raised — the writes
    landed, and the caller's contract is that only a failed write is fatal — and
    never as the push's, which would name a step we have no answer about."""
    try:
        parsed = json.loads(stdout.strip() or "{}")
    except json.JSONDecodeError:
        parsed = None
    # Parsing is not enough: valid JSON that is not an object — `null`, a number —
    # has no .get, and an AttributeError here would escape as neither a
    # StoreWriteError nor a reason.
    if not isinstance(parsed, dict):
        return f"unreadable response from loom knowledge write: {stdout.strip()[:80]}", ""
    return str(parsed.get("warn", "")), str(parsed.get("push_warn", ""))
