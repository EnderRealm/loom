# Cursor session summaries

The summarizer reads `received/cursor-cli/<project>/<session>.jsonl`, the
`cursor-store-v1` journal described in [Cursor capture](cursor-cli-capture.md).
The runtime and agent identity are both `cursor-cli`. Children under
`<session>/subagents/` are separate sessions with the same project identity.

The mapping was checked against Cursor CLI **2026.09.10-fd3934a** and the
[shipped parent/child evidence](cursor-cli-capture-evidence.json). Protobuf
field numbers come from that installation's `agent.v1` definitions. The
parser does not load or execute Cursor code. Original bytes remain in the
received journal; the summary database is disposable.

## Mapping

The parser replays changed rows and deletions, then follows `meta[0]`'s
`latestRootBlobId`. That metadata is hex-encoded JSON inside the journal's
base64 value. A blob reference is the hexadecimal form of its 32-byte ID.

- `agentId` supplies session identity; `subagentInfo.parentAgentId` and
  `toolCallId` supply the immediate parent and dispatch identity.
- `meta.json` supplies cwd and observed creation/update bounds. No cwd is
  inferred from arbitrary transcript text. The receiver's sidecar supplies
  project identity for children whose Cursor source has no cwd sidecar.
- `ConversationStateStructure.turns` (field 8) orders turns. The referenced
  `AgentConversationTurnStructure` names its user message, steps and request
  ID. `UserMessage` supplies the original prompt and available millisecond
  timestamps, including an external text blob when present.
- `ConversationStep` supplies assistant text, thinking presence/length and
  tool calls. Tool-call fields 57, 59 and 60 carry ID, start and completion
  time. Missing completion does not imply success.
- Structured `TaskSuccess.agent_id` and optional `duration_ms` preserve an
  observed child even before its transcript arrives. Its tool-call ID joins
  it to the dispatching turn; a later child transcript reconciles by identity.
- JSON model messages referenced from the current prompt, summary archives
  and user-message snapshots supply tool names, arguments, complete results
  and `highLevelToolCallResult.isError`. Tool IDs join those records to the
  ordered steps. Conflicting evidence is diagnosed instead of selected by
  blob order. Unreferenced historical roots do not contribute records.
- Assistant `providerOptions.cursor.modelName` is associated with the
  request ID of its user message. The turn's routed model display name is
  used when present. Multiple model names in a turn set the mixed flag.
  Encrypted model selectors are retained in received evidence and are not
  decoded as model identities.
- Summary archive references and `message_count_at_last_compaction` mark
  compaction. Archive IDs are deduplicated; unavailable compaction timestamps
  and before/after billing counts remain absent.
- Lens responses are extracted whole from the ordered assistant steps and
  their joined tool results before result-summary truncation. Each response
  carries its turn, source journal line, origin, timestamp and dispatch ID.
  Compaction summaries and user quotations are not assistant verdicts.

Malformed records, missing references, unknown record/step kinds, new state
fields and conflicting tool evidence appear in `unknown_records`. Run-report
surfaces Cursor parser diagnostics as telemetry gaps. Unsupported shell-only
conversation turns are diagnosed rather than interpreted as user work.

## Measurements and joins

The observed source records context-window `used_tokens` and `max_tokens`.
These describe occupancy, not billable input/output usage; their values are
preserved in the `usage:context_window_only` diagnostic and in the source.
The inspected source does not record per-session CLI version, billing input,
output or cache counters. Model names do not establish a price or cache
accounting convention.

Schema **11** adds `sessions.usage_known` and `parent_tool_call_id`, plus
`tool_calls.child_session_id`, `child_duration_ms` and `child_resume_id`. Cursor
sessions currently have `usage_known = 0`; session and turn billing columns
are NULL. Run-report emits an unavailable `cursor-cli` bucket with
`cache_semantics: "unknown"`, NULL counters and NULL total tokens/cost, while
preserving observed turn/tool/error counts and durations. Cost-report also
emits NULL token counters and cost. The TUI displays unavailable tokens and
cost while retaining observed activity. A mixed-runtime scope with any
unavailable usage has an unavailable total, even if other buckets are known.
No Cursor cost calculation is supported from these journals.

Unobserved tool durations are NULL in storage and have a
`tool_call:duration_unavailable` diagnostic. Run reports count timed and
untimed calls under `tool_time_coverage`; an incomplete total tool duration
is NULL and displays as unavailable. Cost-report matches direct Cursor
children through unique parent dispatch IDs, preserves their observed count
and duration, and reports unavailable child usage as NULL cost with a warning.
Task creation and resume calls retain separate dispatch IDs while counting
the child transcript once when all calls belong to the same invocation.
Resumes spanning invocations remain explicitly unresolved. Multiple dispatch
durations do not establish a session duration; resumed children require their
own transcript bounds for that measurement.
An observed child without a transcript has a missing-transcript gap, remains
counted as an execution, and contributes no invented activity or usage.
Missing transcripts also set `tool_time_unavailable`: their scopes' tool-time
totals are NULL, separately from the count of observed untimed tool calls.

After upgrading, run `loom summarize --rebuild` to recreate the database.
The watch sweep then includes Cursor parents and children automatically.

Explicit [execution records](execution-records.md) use the existing IDs,
rounds, attempts and completed/failed/stopped outcomes with runtime
`cursor-cli`. Each `/work` invocation is a separate run. An explicit record
claims its matching invocation; it does not hide other invocations in that
session. For historical Cursor children, `parent_tool_call_id` resolves the
dispatching turn. Missing or ambiguous dispatch evidence remains unresolved;
project name or temporal proximity never chooses a parent invocation.

An explicit run whose root session contains invocations but whose start
does not resolve to one keeps `invocation_unresolved` and an attribution
diagnostic. Its ambiguous session metrics are excluded, including stage
records reusing that root transcript; historical invocations remain visible.

The portable fixtures derive from the shipped source and add compaction,
multiple invocations and long review responses; their provenance is recorded
under `internal/parse/cursorparse/testdata/README.md`. Integration tests run
the received-tree sweep through storage, execution import, reports and TUI
readers, including repeat imports and a full rebuild.
