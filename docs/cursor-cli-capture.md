# Cursor CLI capture

The transport captures Cursor CLI **2026.09.10-fd3934a** from
`~/.cursor/chats/<workspace-hash>/<session-uuid>/store.db`. This is the CLI's
SQLite store, not the editor's conversation database or its rendered
`agent-transcripts` export. The installed CLI's SQLite store declares
`user_version=1`, `blobs(id TEXT PRIMARY KEY, data BLOB)` and
`meta(key TEXT PRIMARY KEY, value TEXT)`. The `meta` row keyed `0` is
hex-encoded JSON. Its `agentId` must match the directory's UUID.

Sessions with a database are discovered regardless of completion state.
An empty chat with only `meta.json` has no transcript to capture yet. The
sidecar's `cwd` provides the project identity. A child database carries
`subagentInfo.parentAgentId`, `rootParentAgentId`, `toolCallId` and `typeName`
in its metadata. Children may have no `meta.json`; their cwd is resolved
through the parent in the same workspace directory. A missing project or
unreadable parent is diagnosed, never attributed by guessing from the hash.
The immediate parent determines the existing Loom subagent path and sidecar;
all original relationship fields remain in the archived metadata.

## Lossless journal

Each consistent SQLite read transaction supplies every row from both tables.
The capture also retains the complete bytes of `meta.json` and
`prompt_history.json` when present. Each source record becomes one JSONL record:

```json
{"format":"cursor-store-v1","kind":"blobs","key":"blob-id","value_type":"blob","value":"AAEC"}
```

`kind` is `blobs`, `meta`, or `file`; `key` is the original table key or
filename. `value_type` preserves SQLite's `text`, `blob`, or `null` type;
files use `blob`. `value` is base64 of the original bytes, or JSON null for
SQL NULL. Empty values and nulls remain distinct through `value_type`.
The capture does not parse, summarize, decrypt, truncate, or discard blob
content. Unknown rows and metadata fields travel whole. Cursor's
`blobEncryptionKey` travels with the metadata because it is required to
interpret the encrypted blobs later; the archive has the same private,
unredacted treatment as other raw transcripts.

The journal appends new and changed values for each `(kind,key)`. A removed
record gets a `deleted:true` entry; earlier values stay in the journal. To
reconstruct the latest captured database, replay each key's values in order,
delete keys carrying that marker, base64-decode each remaining value, and
insert it into its original table with its original SQLite type. Write `file`
records back under their filenames. Earlier values are also recoverable.
This preserves logical source content, not SQLite page layout or WAL bytes.

Staging is the capture checkpoint. An unchanged snapshot appends nothing,
including after a process restart. Capture fsyncs a replacement containing
the old prefix and its delta, then atomically renames it and syncs the
directory. An interrupted pre-rename write leaves the previous journal
intact. There is no separate source-offset checkpoint for mutable stores.
Shipping uses the existing byte-offset protocol and receiver replay handling;
captured sessions keep shipping even after Cursor deletes their sources.
Claude, Codex and execution registries retain their byte-delta capture paths.

Unknown database versions, table/column layouts, malformed identity metadata,
unreadable sources and invalid JSON sidecars produce the shipper's existing
`fail stage=capture agent=cursor-cli ...` diagnostics. A bad session does not
prevent other discovered sessions from being captured. Sidecar files are read
separately from SQLite; no cross-file atomic snapshot is claimed. As with other
polled sources, content removed before its first capture cannot be recovered.

## Real transport evidence

On 2026-09-14 at 06:19 UTC, the locally built shipper sent an authenticated
Cursor parent and child through the running receiver at
`http://127.0.0.1:8765`. Both read a synthetic `evidence.txt` through file-read
tools. The parent's stream recorded the child dispatch and returned child ID.
No installed binary or daemon configuration was replaced.

- Parent: `963ac97f-c33f-46de-9624-3e5946b61a0a`; 22 blobs, 24 total records,
  57,580 source-value bytes, 80,244 received JSONL bytes.
- Child: `5aabb54a-05c7-403c-9721-1bf0448847c1`; 20 blobs, 21 total records,
  46,516 source-value bytes, 65,088 received JSONL bytes.

Every source key, SQLite value type and decoded value matched the received
journal; staging and received files were byte-identical. The child's received
parent and tool-call IDs matched `subagentInfo`. The checked-in
[comparison manifest](cursor-cli-capture-evidence.json) records every source
value's size and SHA-256 plus the full journal hashes. The private raw source
snapshots, stream output, shipping log and comparison are retained under
`~/.loom/capture-evidence/cursor-cli-60ab/`; the receiver and staging retain the
journals under `cursor-cli/` with the paths in the manifest.

The inspected source exposes no child cwd sidecar and no per-blob timestamp
or CLI-version column. The installed CLI version was measured separately.
The transport retains message, model and tool payloads inside blobs whole.
The summarizer decodes the conversation graph separately; see
[Cursor session summaries](cursor-session-records.md) for mappings,
measurement coverage and reporting semantics.

`go test ./transport/...` covers capture through the receiver with live WAL
writes, a fresh shipper process per pass, child project inheritance, large
binary/tool content, changed and deleted records, source removal, receiver
replays, interrupted staging writes, and unsupported/unreadable formats.
The daemon adopts the adapter when a release containing this change is
installed; pushing an unreleased commit does not deploy it (see
[releasing](releasing.md)).
