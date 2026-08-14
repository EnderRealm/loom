#!/usr/bin/env python3
"""Resolve a filesystem path to the canonical project name of its repo.

The name comes from a `.loom-project` marker: a one-line plain text file at the
repo root holding the project name. The marker is owned by the project and is
read-only to loom and tk. Resolution walks up from the given path to the repo
root and the first usable marker wins; with no usable marker the repo
directory's basename is used instead. The resolved name is checked against tk's
project registry, which never changes it — an unregistered name is a warning.

Pure read: this module opens files and stats directories. It never creates or
modifies a marker, the registry, or anything else.

Usage:
    ./resolve_project.py [path]
"""
from __future__ import annotations

import argparse
import re
import sys
from dataclasses import dataclass, field
from pathlib import Path

MARKER_NAME = ".loom-project"

# How much of a rejected value a warning may echo. A rejected value is
# arbitrary file content and the warning lands in a transcript loom ships, so
# it can't carry a whole line of someone's file; long enough to read back a
# typo'd project name, which is what the warning is for.
VALUE_ECHO_LIMIT = 40

# Same shape rule as scopePattern in internal/extract/scope.go: the resolved
# name becomes both a --scope argument and a directory under the knowledge
# store, so it must be exactly one safe path segment. Requiring a leading
# alphanumeric is what rejects "." and "..", which a path join would otherwise
# clean into the store's parent.
# Exact-case, unlike the Go path: summaries.NormalizeRemote and wantedScopes in
# internal/extract/backfill.go lowercase before matching, so "Loom" reaches
# scopePattern as "loom" but is rejected here. A marker is a human declaration
# — lowercasing it silently would hide the typo.
# `\Z`, not `$`: `$` would also match before a trailing newline, which RE2's
# does not — and a repo basename may legally end in one.
NAME_PATTERN = re.compile(r"^[a-z0-9][a-z0-9._-]*\Z")

SOURCE_MARKER = "marker"
SOURCE_BASENAME = "repo-basename"


@dataclass
class Resolution:
    """One resolution attempt. `name` is None when nothing usable was found;
    callers decide how to report `warnings` — the library never prints."""
    name: str | None = None
    source: str = ""
    marker_path: Path | None = None
    warnings: list[str] = field(default_factory=list)


def resolve_project(path: str | Path) -> Resolution:
    """Resolve `path` to a project name via the marker chain."""
    warnings: list[str] = []
    start = Path(path).expanduser().resolve()
    if start.is_file():
        start = start.parent

    repo_root = _find_repo_root(start)
    if repo_root is None:
        warnings.append(f"no git repository at or above {start} — nothing to resolve a project name from")
        return Resolution(warnings=warnings)

    # Bounded at the repo root: a stray marker in $HOME must never capture an
    # unrelated repo.
    chain = [start, *start.parents]
    for directory in chain[:chain.index(repo_root) + 1]:
        marker = directory / MARKER_NAME
        # Ahead of the is_file() guard: a dangling symlink and a symlink to a
        # directory both fail is_file(), and skipping those silently leaves the
        # operator with only the generic fallback warning.
        if marker.is_symlink():
            # A marker arrives from a repo this host didn't author, and both
            # is_file() and read_text() follow links: a symlinked marker is a
            # read-anything channel whose target this resolver would then echo
            # into a warning (~/.ssh/id_ed25519 fails NAME_PATTERN verbatim).
            warnings.append(f"{marker} is a symlink — ignoring it")
            continue
        if not marker.is_file():
            continue
        try:
            text = marker.read_text(encoding="utf-8")
        except (OSError, UnicodeDecodeError) as e:
            # A marker arrives from a repo this host didn't author, so bytes
            # that aren't UTF-8 are an expected input, not a crash. The
            # encoding is pinned because the marker's byte format is a
            # documented contract, not a property of this host's locale.
            warnings.append(f"{marker} unreadable ({e}) — ignoring it")
            continue
        value = _marker_value(text)
        if value is None:
            warnings.append(f"{marker} holds no project name — ignoring it")
            continue
        if not NAME_PATTERN.match(value):
            echoed = value[:VALUE_ECHO_LIMIT]
            if len(value) > VALUE_ECHO_LIMIT:
                echoed += "..."
            warnings.append(f"{marker} holds unusable project name {echoed!r} "
                            f"(must match {NAME_PATTERN.pattern}) — ignoring it")
            continue
        if directory != repo_root:
            # Nearest-wins is what makes invocation from a subdirectory work,
            # but the marker is a repo-root convention: one below the root is
            # as likely a vendored subtree's own declaration as the project's,
            # and that difference has to be visible in the log.
            warnings.append(f"{marker} is below the repo root {repo_root} — using it, but the "
                            f"marker belongs at {repo_root / MARKER_NAME}")
        return _validated(Resolution(value, SOURCE_MARKER, marker, warnings))

    warnings.append(f"no usable {MARKER_NAME} for {start} — falling back to the repo "
                    f"basename; add {repo_root / MARKER_NAME} holding the project name")
    name = repo_root.name
    if not NAME_PATTERN.match(name):
        warnings.append(f"repo basename {name!r} is not a usable project name "
                        f"(must match {NAME_PATTERN.pattern})")
        return Resolution(warnings=warnings)
    return _validated(Resolution(name, SOURCE_BASENAME, None, warnings))


def describe(result: Resolution) -> str:
    """One line accounting for how the name was reached, for a caller's log."""
    if result.source == SOURCE_MARKER:
        return f"{result.name} from {result.marker_path}"
    if result.source == SOURCE_BASENAME:
        return f"{result.name} from repo basename"
    return "unresolved"


def _find_repo_root(start: Path) -> Path | None:
    """First ancestor of `start` (inclusive) holding a `.git` entry. Worktrees
    and submodules carry a `.git` file rather than a directory, so both count."""
    for directory in [start, *start.parents]:
        if (directory / ".git").exists():
            return directory
    return None


def _marker_value(text: str) -> str | None:
    """First non-empty, non-comment line of a marker file, stripped."""
    for line in text.splitlines():
        line = line.strip()
        if line and not line.startswith("#"):
            return line
    return None


def _validated(result: Resolution) -> Resolution:
    """Warn when the name isn't in tk's registry, or when the registry can't be
    consulted. Validation never changes the resolved name."""
    names, reason = _registry_projects()
    if reason:
        result.warnings.append(f"tk registry check skipped: {reason}")
    elif result.name not in names:
        result.warnings.append(f"{result.name!r} is not a registered tk project — "
                               f"register it with tk or correct the marker")
    return result


def _registry_projects() -> tuple[set[str], str]:
    """Project names from tk's registry, or an empty set and the reason it
    couldn't be read. loom carries no YAML dependency (see the Go precedent in
    internal/config/ticket.go), so both files are scanned for the keys that
    matter rather than parsed as documents."""
    ticket_config = Path.home() / ".ticket" / "config.yaml"
    try:
        central_root = _central_root(ticket_config.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError) as e:
        # Same reason the marker read catches it: a registry that can't be read
        # is a warning, never a failure, and non-UTF-8 bytes are a way for it to
        # be unreadable — an uncaught UnicodeDecodeError would crash the caller.
        return set(), f"cannot read {ticket_config} ({e})"
    if not central_root:
        return set(), f"no central_root in {ticket_config}"

    registry = Path(central_root).expanduser() / "config.yaml"
    try:
        names = _project_keys(registry.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError) as e:
        return set(), f"cannot read {registry} ({e})"
    if not names:
        return set(), f"no projects in {registry}"
    return names, ""


def _central_root(text: str) -> str:
    for line in text.splitlines():
        if line.startswith((" ", "\t")):
            continue  # nested key, not the top-level central_root
        stripped = line.strip()
        if stripped.startswith("central_root:"):
            return stripped[len("central_root:"):].strip()
    return ""


def _project_keys(text: str) -> set[str]:
    """Top-level keys nested under the registry's `projects:` block.

    Block style only, which is what tk's writer emits. An inline mapping
    (`projects: {loom: ...}`), or a trailing comment on the `projects:` line
    that stops it matching and so skips the whole block, is deliberately not
    recognized: the cost of missing either is a spurious "not a registered tk
    project" warning, and the resolved name is never changed by validation."""
    names: set[str] = set()
    in_projects = False
    key_indent = None
    for line in text.splitlines():
        stripped = line.strip()
        if not stripped or stripped.startswith("#"):
            continue
        indent = len(line) - len(line.lstrip())
        if indent == 0:
            in_projects = stripped == "projects:"
            continue
        if not in_projects:
            continue
        if key_indent is None:
            key_indent = indent
        if indent == key_indent and stripped.endswith(":"):
            names.add(stripped[:-1].strip())
    return names


def main():
    p = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    p.add_argument("path", nargs="?", default=".", help="path to resolve (default: cwd)")
    args = p.parse_args()

    result = resolve_project(args.path)
    for warning in result.warnings:
        print(f"[resolve-project] {warning}", file=sys.stderr)
    if result.name is None:
        sys.exit(f"[resolve-project] cannot resolve a project name for {args.path}")
    print(f"[resolve-project] {describe(result)}", file=sys.stderr)
    print(result.name)


if __name__ == "__main__":
    main()
