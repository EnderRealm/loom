#!/usr/bin/env python3
"""Re-apply attribution stamps to Codex identity sidecars already on disk.

Codex sessions run in a throwaway cwd and captured before attribution stamps
existed (docs/attribution-stamps.md) carry the tmp directory in their
`.meta.json` sidecars, and nothing rewrites those: the capture pass writes
identity only alongside new transcript bytes. This walks the staging and
received trees for codex-cli, resolves each sidecar whose cwd is ephemeral
through `$LOOM_HOME/attribution.jsonl` with the same rule
transport/internal/source/attribution.go applies at capture, and rewrites the
sidecar with the stamped project's identity.

Dry run by default; --apply writes. Nothing else is touched: not the
transcripts, cursors, offsets, ~/.codex/sessions or the summary DB.
"""

import argparse
import json
import os
import re
import subprocess
import sys
from datetime import datetime, timezone

AGENT = "codex-cli"
ATTRIBUTION_FILE = "attribution.jsonl"

# Mirrors ephemeralRoots in attribution.go.
EPHEMERAL_ROOTS = [
    "/tmp/",
    "/private/tmp/",
    "/var/folders/",
    "/private/var/folders/",
    "/var/tmp/",
    "/private/var/tmp/",
]

# Go's time.Parse(time.RFC3339, ...) accepts fractional seconds and either
# "Z" or a numeric offset; datetime.fromisoformat accepts far more, so the
# shape is pinned first.
RFC3339 = re.compile(r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$")

# Zero time: orders an unparseable stamp before every dated record.
ZERO_TIME = datetime.min.replace(tzinfo=timezone.utc)


def parse_rfc3339(s):
    if not isinstance(s, str) or not RFC3339.match(s):
        return ZERO_TIME
    try:
        return datetime.fromisoformat(s.replace("Z", "+00:00"))
    except ValueError:
        return ZERO_TIME


def load_stamps(home):
    """loadStamps: {normpath(work_root): [(at, project_cwd), ...]} oldest-first."""
    idx = {}
    try:
        with open(os.path.join(home, ATTRIBUTION_FILE), encoding="utf-8", errors="replace") as f:
            lines = f.readlines()
    except OSError:
        return idx
    for line in lines:
        line = line.strip()
        if not line:
            continue
        try:
            rec = json.loads(line)
        except ValueError:
            continue
        if not isinstance(rec, dict):
            continue
        work_root = rec.get("work_root")
        project_cwd = rec.get("project_cwd")
        if not isinstance(work_root, str) or not isinstance(project_cwd, str):
            continue
        if work_root == "" or project_cwd == "":
            continue
        at = parse_rfc3339(rec.get("stamped_at"))
        idx.setdefault(os.path.normpath(work_root), []).append((at, project_cwd))
    for group in idx.values():
        group.sort(key=lambda s: s[0])
    return idx


def project_cwd_for(idx, cwd, start):
    """stampIndex.projectCwd: newest stamp at or before start; newest when
    start is unknown; "" when every stamp postdates the session."""
    group = idx.get(os.path.normpath(cwd))
    if not group:
        return ""
    if start is None:
        return group[-1][1]
    for at, project_cwd in reversed(group):
        if at <= start:
            return project_cwd
    return ""


def under(path, d):
    if d in ("", "/"):
        return False
    return path.startswith(d + os.sep)


def is_ephemeral_cwd(cwd):
    if not cwd:
        return False
    clean = os.path.normpath(cwd)
    tmp = os.environ.get("TMPDIR", "")
    if tmp and under(clean, os.path.normpath(tmp)):
        return True
    return any(under(clean, os.path.normpath(root)) for root in EPHEMERAL_ROOTS)


def read_session_start(jsonl_path):
    """readCodexMeta's start: the session_meta record's timestamp, else the
    payload's. Returns (start or None, reason when None)."""
    try:
        with open(jsonl_path, "rb") as f:
            line = f.readline()
    except OSError:
        return None, "jsonl missing"
    if not line.endswith(b"\n"):
        return None, "first line unterminated"
    try:
        meta = json.loads(line)
    except ValueError:
        return None, "first line unparseable"
    if not isinstance(meta, dict) or meta.get("type") != "session_meta":
        return None, "first record is not session_meta"
    start = parse_rfc3339(meta.get("timestamp"))
    if start == ZERO_TIME:
        payload = meta.get("payload")
        if isinstance(payload, dict):
            start = parse_rfc3339(payload.get("timestamp"))
    if start == ZERO_TIME:
        return None, "session_meta carries no usable timestamp"
    return start, ""


def resolve_git_remote(cwd):
    if not cwd:
        return ""
    try:
        out = subprocess.run(
            ["git", "-C", cwd, "remote", "get-url", "origin"],
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
            check=True,
        ).stdout
    except (OSError, subprocess.CalledProcessError):
        return ""
    return out.decode("utf-8", errors="replace").strip()


def encode_identity(git_remote, cwd, root_slug):
    """Same field order and omitempty shape as wire.ProjectIdentity, compact."""
    identity = {}
    if git_remote:
        identity["git_remote"] = git_remote
    if cwd:
        identity["cwd"] = cwd
    if root_slug:
        identity["root_slug"] = root_slug
    return json.dumps(identity, separators=(",", ":"), ensure_ascii=False).encode("utf-8")


def write_sidecar(path, data):
    tmp = path + ".tmp"
    fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "wb") as f:
        f.write(data)
    os.rename(tmp, path)


def sidecars(tree_root):
    if not os.path.isdir(tree_root):
        return
    for slug in sorted(os.listdir(tree_root)):
        slug_dir = os.path.join(tree_root, slug)
        if not os.path.isdir(slug_dir):
            continue
        for name in sorted(os.listdir(slug_dir)):
            if name.endswith(".meta.json"):
                yield os.path.join(slug_dir, name)


def main():
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--loom-home", default=os.environ.get("LOOM_HOME") or os.path.expanduser("~/.loom"))
    ap.add_argument("--apply", action="store_true", help="write repaired sidecars (default: dry run)")
    args = ap.parse_args()
    home = args.loom_home

    stamps = load_stamps(home)
    trees = {
        "staging": os.path.join(home, "transport", "staging", AGENT),
        "received": os.path.join(home, "received", AGENT),
    }
    counts = {tree: {"repaired": 0, "unmatched": 0, "skipped": 0, "failed": 0} for tree in trees}
    unmatched = {}
    verb = "repaired" if args.apply else "would repair"

    for tree, root in trees.items():
        for path in sidecars(root):
            sid = os.path.basename(path)[: -len(".meta.json")]
            try:
                with open(path, encoding="utf-8") as f:
                    meta = json.load(f)
            except (OSError, ValueError) as e:
                print(f"{tree}\t{sid}\t?\tskipped: unreadable sidecar: {e}", file=sys.stderr)
                counts[tree]["skipped"] += 1
                continue
            if not isinstance(meta, dict):
                counts[tree]["skipped"] += 1
                continue
            cwd = meta.get("cwd") or ""
            if not is_ephemeral_cwd(cwd):
                counts[tree]["skipped"] += 1
                continue

            start, reason = read_session_start(path[: -len(".meta.json")] + ".jsonl")
            note = f" (start unknown: {reason}; newest stamp wins)" if start is None else ""
            work_root = os.path.normpath(cwd)
            project_cwd = project_cwd_for(stamps, cwd, start)
            if project_cwd == "":
                counts[tree]["unmatched"] += 1
                unmatched.setdefault(work_root, []).append(sid)
                if work_root in stamps:
                    result = f"unmatched: every stamp postdates start {start.isoformat()}"
                else:
                    result = f"unmatched: no stamp for {work_root}"
                print(f"{tree}\t{sid}\t{cwd}\t{result}{note}")
                continue

            remote = resolve_git_remote(project_cwd)
            if args.apply:
                try:
                    write_sidecar(path, encode_identity(remote, project_cwd, meta.get("root_slug") or ""))
                except OSError as e:
                    print(f"{tree}\t{sid}\t{cwd}\tfailed: {e}", file=sys.stderr)
                    counts[tree]["failed"] += 1
                    continue
            counts[tree]["repaired"] += 1
            print(f"{tree}\t{sid}\t{cwd}\t{verb} -> {project_cwd} ({remote}){note}")

    print()
    print(f"mode: {'apply' if args.apply else 'dry run'}")
    for tree in trees:
        c = counts[tree]
        print(f"{tree}: {verb} {c['repaired']}, unmatched {c['unmatched']}, skipped {c['skipped']}, failed {c['failed']}")
    if unmatched:
        print("unmatched by work root:")
        for work_root in sorted(unmatched):
            print(f"  {work_root}")
            for sid in unmatched[work_root]:
                print(f"    {sid}")
    return 1 if any(c["failed"] for c in counts.values()) else 0


if __name__ == "__main__":
    sys.exit(main())
