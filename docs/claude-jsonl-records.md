# Claude Code Session JSONL — Record Catalog

Ground-truth catalog of the record shapes that appear in Claude Code session transcripts
(`~/.claude/projects/<sanitized-cwd>/<session-uuid>.jsonl`).

Each line is a single JSON object. Records are discriminated by a top-level `type` field.
This catalog is the reference any downstream parser, router, or extractor in loom should
validate against.

Enumerated against every `.jsonl` file under `~/.claude/projects/` on this host (tens of
thousands of records across 40+ projects). Verification commands are at the bottom.

---

## Top-level `type` values (12)

| `type` | Purpose |
|---|---|
| `user` | User-side message — human input OR a tool-result carrier |
| `assistant` | Model response — text, thinking, and/or tool_use blocks |
| `system` | System-generated annotation (errors, hook output, compact boundaries, …) |
| `attachment` | Structured side-channel payload (diagnostics, skill listings, plan mode, …) |
| `progress` | Streaming output from a long-running tool or background agent |
| `file-history-snapshot` | Tracked-file checkpoint bound to a message |
| `permission-mode` | Current session permission mode |
| `queue-operation` | Internal orchestration — task queue events |
| `last-prompt` | Most recent user prompt (for session resume) |
| `agent-name` | Session agent identity |
| `custom-title` | User-provided session title override |
| `pr-link` | GitHub PR reference attached to the session |

---

## Conversation records

### `user`

Human input OR the carrier for a tool result returned to the model.

Key fields:
- `message.role` — always `user`
- `message.content` — either a `string` (plain typed message) or an array of content blocks
- `toolUseResult` — present when this record is a tool result (discriminator)
- `isMeta` — `true` for system-injected reminders
- `isSidechain` — `true` inside a Task subagent conversation
- `promptId` — present when the user typed a new prompt

Observed variants:
- plain user text (`content` is a string, no `toolUseResult`)
- tool-result carrier (`toolUseResult` present, `content` is a block array with `tool_result`)
- meta / system-injected (`isMeta=true`)
- sidechain (`isSidechain=true`)

### `assistant`

Model turn. The embedded `message` mirrors the Anthropic Messages API response shape.

Key fields:
- `message.model` — e.g. `claude-opus-4-6`
- `message.content[]` — any subset of `{text, thinking, tool_use}`
- `message.stop_reason` — `end_turn`, `tool_use`, `stop_sequence`, or `null` (partial / interrupted / still streaming)
- `message.usage` — token counts
- `requestId` — API request correlation ID

---

## System records

### `system`

Annotations emitted by the CLI itself. `subtype` is the discriminator.

| `subtype` | Meaning |
|---|---|
| `api_error` | API call failure (level = `error`) |
| `away_summary` | Summary of activity while user was away |
| `bridge_status` | Remote-control / bridge connection status |
| `compact_boundary` | Marks the boundary where conversation history was compacted (carries `compactMetadata`, `logicalParentUuid`) |
| `informational` | Generic info notice (often `level: warning`) |
| `local_command` | Local command (slash command) invocation trace |
| `stop_hook_summary` | Summary produced by a Stop hook (level = `suggestion`) |
| `turn_duration` | Timing info for the prior turn |

Other fields: `content` (string body), `level` (`info` / `warning` / `error` / `suggestion` / null), `isMeta`, `url`, `upgradeNudge`.

---

## Side-channel records

### `attachment`

Structured payloads that accompany the conversation but aren't model messages. The inner
`attachment.type` is the discriminator.

| `attachment.type` | Meaning |
|---|---|
| `command_permissions` | Per-command permission grants (newer CLI) |
| `companion_intro` | Companion/pet intro metadata (name, species) |
| `date_change` | Calendar-day rollover marker |
| `deferred_tools_delta` | Deferred-tool availability changes (`addedNames`, `removedNames`) |
| `diagnostics` | LSP / build diagnostics snapshot |
| `edited_text_file` | Record of an externally-edited file |
| `hook_success` | Hook execution succeeded (newer CLI) |
| `plan_mode` | Plan-mode state change |
| `queued_command` | Command queued for later execution (newer CLI) |
| `skill_listing` | Available skills listing |
| `task_reminder` | Task-tracker reminder injection |

### `progress`

Streaming progress from a long-running tool (Bash `run_in_background`, Task agents, MCP,
etc.). Bound to a `toolUseID`. `data.type` / `data.hookEvent` discriminates the payload
kind.

Observed `data.type` values:
`agent_progress`, `bash_progress`, `hook_progress`, `mcp_progress`, `query_update`,
`search_results_received`.

### `queue-operation`

Task-queue orchestration events. `operation` is one of `enqueue`, `dequeue`, `remove`.
`content` is a `<task-notification>` XML-ish blob with status, exit code, output-file path.

---

## Sparse-header metadata records

These records carry almost none of the common header — just `type`, `sessionId`, and their
payload field. Treat them as session-scoped metadata, not conversation events.

| `type` | Payload field | Shape |
|---|---|---|
| `agent-name` | `agentName` | `{type, agentName, sessionId}` |
| `custom-title` | `customTitle` | `{type, customTitle, sessionId}` |
| `last-prompt` | `lastPrompt` | `{type, lastPrompt, sessionId}` |
| `permission-mode` | `permissionMode` | `{type, permissionMode, sessionId}` |
| `pr-link` | `prNumber` / `prUrl` / `prRepository` | `{type, sessionId, prNumber, prUrl, prRepository, timestamp}` |
| `file-history-snapshot` | `snapshot` | `{type, messageId, snapshot:{messageId, trackedFileBackups, timestamp}, isSnapshotUpdate}` — **no sessionId either** |

---

## Common header fields (conversation + most side-channel records)

Present on `user`, `assistant`, `system`, `attachment`, `progress`, `queue-operation`:

| Field | Notes |
|---|---|
| `uuid` | Record ID |
| `parentUuid` | Prior record in the thread; `null` on the first record |
| `logicalParentUuid` | Only on `system` records across compact boundaries |
| `sessionId` | Session UUID (matches filename) |
| `timestamp` | ISO-8601 UTC |
| `cwd` | Working directory at time of record |
| `version` | Claude Code CLI version (e.g. `2.1.91`) |
| `gitBranch` | Git branch at time of record |
| `userType` | `external` / `internal` |
| `entrypoint` | `cli` / … (not always present) |
| `isSidechain` | `true` inside Task subagent conversations |
| `agentId` | Agent identity |
| `slug` | Short agent slug |
| `promptId` | `user` records only — present when user typed a prompt |
| `requestId` | `assistant` records only — API request ID |
| `toolUseID`, `parentToolUseID` | `progress` records — bind to tool invocation |
| `sourceToolAssistantUUID` | `user` tool-result records — links back to the assistant's `tool_use` block |

---

## Verification

Re-enumerate against live data:

```sh
# Top-level types
find ~/.claude/projects -name '*.jsonl' -exec jq -r '.type' {} \; | sort -u

# system subtypes
find ~/.claude/projects -name '*.jsonl' -exec jq -rc 'select(.type=="system")|.subtype' {} \; | sort -u

# attachment subtypes
find ~/.claude/projects -name '*.jsonl' -exec jq -rc 'select(.type=="attachment")|.attachment.type' {} \; | sort -u

# progress payload kinds
find ~/.claude/projects -name '*.jsonl' -exec jq -rc 'select(.type=="progress")|.data.type // .data.hookEvent' {} \; | sort -u

# queue-operation operations
find ~/.claude/projects -name '*.jsonl' -exec jq -rc 'select(.type=="queue-operation")|.operation' {} \; | sort -u
```

If any of these surfaces a value not listed in this catalog, this document is stale —
update it before the parser ships a silent fallback.
