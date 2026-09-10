# Attribution stamps

An agent run from a throwaway working directory reports that directory as its
cwd, and cwd is what loom keys project identity on. The result is one project
per run: 254 single-session rows from Codex CLI before this existed, all of
them real review work, none of them extractable — a throwaway root carries no
`.loom-project` marker and no git remote, so `resolveScope` files it under
nothing.

The launcher knows the checkout even when the agent deliberately does not.
warp's `scripts/codex-lens.sh` starts each `/work` review lens in an empty
`mktemp -d` precisely so the reviewed repo cannot instruct its own reviewer;
that property is worth keeping, so the association is recorded out of band
instead.

## The contract

A producer that runs an agent in a throwaway directory appends one line to
`$LOOM_HOME/attribution.jsonl` (`~/.loom/attribution.jsonl` by default) before
starting the run:

```json
{"work_root":"/var/folders/x7/jntw/T/tmp.abc1234567","project_cwd":"/Users/steve/code/warp","stamped_at":"2026-09-09T18:22:41Z"}
```

| Field | Meaning |
| --- | --- |
| `work_root` | The directory the agent is pointed at — what it will report as its cwd. |
| `project_cwd` | The checkout the run is about. Absolute path on this host. |
| `stamped_at` | RFC 3339. When the producer wrote the record. |

Append before launching, not after: the shipper can capture a session while it
is still running, and a stamp that lands later has already missed the first
capture of that session.

## What loom does with it

`transport/internal/source/attribution.go` loads the registry once per capture
pass. When a Codex session's reported cwd is a throwaway directory — under
`$TMPDIR`, `/tmp`, `/var/tmp`, `/var/folders`, or the `/private` form of any of
them — the matching record's `project_cwd` becomes the session's identity, and
the capture pass resolves that checkout's git remote for it. Everything
downstream already keys off identity: the staging sidecar, `ProjectIdentity` on
the wire, the receiver's `.meta.json`, `cwd_raw`/`git_remote` in
`summaries.db`, the TUI's remote-over-cwd grouping, and extraction's scope
resolution.

The storage slug is left alone. `received/codex-cli/<slug>/` still names the
directory codex ran in, because the slug is a storage path and
`ProjectIdentity` is the authoritative handle — the split `wire.IngestRequest`
already documents.

**Matching.** `mktemp` names are random but finite, so one work root can carry
several records. The newest record stamped at or before the session's start
time wins, where the start time is the `session_meta` timestamp the adapter
already reads. A session that predates every record for its work root resolves
to nothing rather than to the wrong project. A session with no usable start
time takes the newest record.

**Failure is a no-op.** No state root, no registry, an unreadable file, a
malformed line, a record missing either path, an unparseable `stamped_at`
(usable — it just sorts before every dated record), a cwd that is not
throwaway, or no matching record: all of them mean the session keeps the
identity it reports today. A stamp is an additional source of identity, never
a new way for capture to fail.

## Writing a stamp

Any producer can. The write is best effort and must not be able to fail the
run it is describing:

```sh
LOOM_DIR="${LOOM_HOME:-$HOME/.loom}"
if mkdir -p "$LOOM_DIR" 2>/dev/null; then
  printf '{"work_root":"%s","project_cwd":"%s","stamped_at":"%s"}\n' \
    "$WORK_ROOT" "$PWD" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    >> "$LOOM_DIR/attribution.jsonl" 2>/dev/null || true
fi
```

Paths with a `"` or a backslash in them would need escaping; no producer has
one today, and a record loom cannot parse is skipped rather than fatal.

The registry is a handoff, not an archive: once a session has been captured and
shipped, its record has done its job. Truncating the file loses nothing but the
attribution of runs not yet captured.
