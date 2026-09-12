# Execution records

One `/work` run is more than one transcript. The orchestrating session
dispatches Claude subagents, those dispatch Codex children, the security lens
is routed through `codex-lens.sh` into a throwaway directory, and a Weft run
drives the whole thing through work and review stages, retrying a stage that
fails. Each of those is its own session file, shipped and summarized on its
own, and nothing in the transcripts says which run it belongs to: two runs in
one session, or two runs of the same ticket at the same time, look alike from
the outside. A summary built by project or by time attributes the wrong work
to the wrong invocation, or drops it.

The producer knows. This is the contract by which it says so, out of band,
the way `docs/attribution-stamps.md` records a throwaway cwd's checkout.

## The contract

Version 1. A producer appends one JSON object per line to
`$LOOM_HOME/executions.jsonl` (`~/.loom/executions.jsonl` by default), at or
before the event it describes. Every record carries `"v": 1` and a `"kind"`.
Two kinds exist.

### `run`

One `/work` invocation, or one Weft run. Emitted when the run starts, and
again when it ends.

| Field | Meaning |
| --- | --- |
| `run_id` | Producer-generated, unique per invocation — a UUID. Two invocations in one session get two ids. Required. |
| `ticket` | Qualified ticket id, `project/slug`. |
| `runtime` | `claude-code`, `codex-cli` or `weft`. |
| `agent`, `session_id` | The originating transcript, identified the way loom ships it: the agent name and the session id in the file name. Optional for `weft`, which has no transcript of its own. |
| `producer` | Who wrote the record and at what version, e.g. `warp/work@1.4.0`. |
| `started_at`, `ended_at` | RFC 3339. `ended_at` is absent while the run is in progress. |
| `outcome` | `completed`, `failed` or `stopped`. Absent while the run is in progress. |
| `reporting_cutoff` | RFC 3339, optional. The moment after which nothing more is attributed to the run. |
| `recorded_at` | RFC 3339. When the producer wrote the record. |

### `execution`

One unit of execution attributable to a run. Emitted when the execution
starts, and again when it ends.

| Field | Meaning |
| --- | --- |
| `execution_id` | Unique across all runs. Required. |
| `run_id` | The run this execution belongs to. Required. |
| `parent_execution_id` | The execution that created this one. Absent on the run's root execution and nowhere else. |
| `execution_kind` | `root` (the run's own session), `subagent` (a dispatched agent), `lens` (a review lens), `stage` (a Weft stage attempt) or `command` (a process with no transcript, such as a test gate). |
| `agent`, `session_id` | The transcript this execution *is*, when it has one. A `command` has none. |
| `dispatch_id` | The parent's tool call that created this execution — Claude's `tool_use_id`, Codex's `call_id`. Optional. |
| `stage`, `stage_occurrence` | For a `stage`: which stage (`work`, `review`) and which pass through it, counting from 1. |
| `lens`, `round` | For a `lens`: which lens and which review round, counting from 1. |
| `attempt` | Which try this is at the same stage occurrence or lens round, counting from 1. A retry is a new execution with the next attempt. |
| `started_at`, `ended_at`, `outcome`, `producer`, `recorded_at` | As for `run`. |

## Ids and merging

Every id is a single string. The importer trusts nothing about its shape
beyond non-empty: a UUID, a tool call id, a producer's own scheme all work,
and only equality matters. The `run_id` has to reach every producer that
contributes to the run — a lens router and a Weft stage both write it — and
how it travels between them is theirs to arrange; loom joins on the ids
alone.

A record may be emitted more than once for the same id: once at start, once
at the end. The importer merges by id. A later record's non-empty field
replaces the row's value for that field; an absent or empty field leaves it
alone. So the terminal `run` record need carry only `run_id`, `ended_at`,
`outcome` and whatever else changed, and a late child that names a run
already imported attaches to it. Replaying the whole file is idempotent:
importing it twice, or after a restart, creates no second row and no second
edge.

An `execution_id` belongs to the run that first declared it. A later record
naming it under another `run_id` is rejected with an `identity_conflict`
diagnostic and the original execution is preserved, fields and run alike;
nothing from the rejected record is merged.

Nothing in a record may be a transcript body or a credential. A record is
metadata about an execution, not its content.

## What loom does with it

The shipper lists the file as one session under the agent `loom-executions`,
with the host name as its project and `executions` as its session id, and
ships it through the same capture → staging → receiver path as a transcript.
It lands at `received/loom-executions/<host>/executions.jsonl`. The file is
read by byte offset like any source, so it is append-only: truncating it, as
`attribution.jsonl` permits, desynchronizes the cursor.

`loom summarize` folds every shipped registry into `runs` and `executions`
in `summaries.db` (schema 8), skipping a file whose size and mtime are
unchanged, and `internal/runs` reads the tree back: `Load(run_id)` for one
run, `List(since, until)` for a range. A run known only from transcripts —
history nobody instrumented — is still recognized from its `/work`
invocation and reported with the origin `transcript`, its children drawn
only from evidence the transcripts carry (the parent's subagent rows, a
Codex session naming its parent thread). A Codex session whose parent thread
holds more than one recognized run is listed unresolved under each of them
with an `ambiguous_parent` diagnostic rather than placed by time. A session
with a `run` record is not re-recognized: the record wins.

**Rejected records.** A record with an unknown `v`, an unknown `kind`, a
missing required id, a value outside an enum, or a timestamp that is not RFC
3339 is skipped and a diagnostic written to `execution_diagnostics` with the
codes `unsupported_version`, `unsupported_kind`, `missing_id`,
`invalid_value` or, for a line that is not JSON, `malformed_json`. An
`execution` record reusing an `execution_id` under a different `run_id` is
skipped with `identity_conflict`, whose detail names the run that holds it. A
diagnostic carries the source path, the 1-based line number, the ids and
kinds the record named and the field at fault — never a field's value and
never the record.

**Unresolved associations.** An execution naming a `run_id` no `run` record
declared, or a `parent_execution_id` no `execution` in the same run declared,
is stored and queryable — it appears under the run's `unresolved` list rather
than in the tree — and a diagnostic names it: `unresolved_run` or
`unresolved_parent`. A parent belongs to the child's run: an execution
declared under another `run_id` does not resolve it. The record that resolves
it can arrive later, from any file; the diagnostics are recomputed on every
import and clear once it does. A parentless execution the run cannot take as
its root — a second `root`, or a parentless execution of another kind — is
likewise listed unresolved, with an `unresolved_root` diagnostic the reader
adds when it builds the tree. An execution naming itself as parent is skipped
with `invalid_value`; executions whose `parent_execution_id` chain returns to
them are each listed unresolved as their own top, with a `cyclic_parent`
diagnostic, so the tree never holds a back-reference.

## Example

One Claude `/work` run under Weft: the orchestrating session, a subagent it
dispatched, a Codex child that subagent spawned, the routed security lens,
Weft's stage attempts — the work stage failing once before it completes, the
review stage stopped — and a command gate with no transcript. The `run`
record appears twice, at the start and at the end. This block is
`internal/runs/testdata/executions.jsonl` verbatim; the tests import it from
here, so it cannot drift from what the importer accepts.

```json
{"v":1,"kind":"run","run_id":"0f4c3a6e-2d1b-4b7e-9c8a-5e2f1d0a9b31","ticket":"loom/persist-execution-identities-2149","runtime":"claude-code","agent":"claude-code","session_id":"195f819e-1e11-4e08-8c16-a340f512f892","producer":"warp/work@1.4.0","started_at":"2026-09-10T17:02:11Z","recorded_at":"2026-09-10T17:02:11Z"}
{"v":1,"kind":"execution","execution_id":"root-195f819e","run_id":"0f4c3a6e-2d1b-4b7e-9c8a-5e2f1d0a9b31","execution_kind":"root","agent":"claude-code","session_id":"195f819e-1e11-4e08-8c16-a340f512f892","producer":"warp/work@1.4.0","started_at":"2026-09-10T17:02:11Z","recorded_at":"2026-09-10T17:02:11Z"}
{"v":1,"kind":"execution","execution_id":"agent-a0e0c89b977fd6273","run_id":"0f4c3a6e-2d1b-4b7e-9c8a-5e2f1d0a9b31","parent_execution_id":"root-195f819e","execution_kind":"subagent","agent":"claude-code","session_id":"agent-a0e0c89b977fd6273","dispatch_id":"toolu_015BC3bRz7V19vyVDAMXf5FX","producer":"warp/work@1.4.0","started_at":"2026-09-10T17:05:40Z","ended_at":"2026-09-10T17:09:02Z","outcome":"completed","recorded_at":"2026-09-10T17:09:02Z"}
{"v":1,"kind":"execution","execution_id":"codex-01a0029b","run_id":"0f4c3a6e-2d1b-4b7e-9c8a-5e2f1d0a9b31","parent_execution_id":"agent-a0e0c89b977fd6273","execution_kind":"subagent","agent":"codex-cli","session_id":"01a0029b-b39f-7802-8b5f-56ffe644403b","dispatch_id":"call_7Hq2mK","producer":"warp/work@1.4.0","started_at":"2026-09-10T17:06:00Z","ended_at":"2026-09-10T17:08:30Z","outcome":"completed","recorded_at":"2026-09-10T17:08:30Z"}
{"v":1,"kind":"execution","execution_id":"lens-security-r1-a1","run_id":"0f4c3a6e-2d1b-4b7e-9c8a-5e2f1d0a9b31","parent_execution_id":"root-195f819e","execution_kind":"lens","agent":"codex-cli","session_id":"01a0029c-4d61-7f0e-a2b3-9c7d5e1f2a44","dispatch_id":"toolu_01Wq9LensSecurityR1","lens":"security","round":1,"attempt":1,"producer":"warp/codex-lens.sh@1.4.0","started_at":"2026-09-10T17:12:00Z","ended_at":"2026-09-10T17:15:20Z","outcome":"completed","recorded_at":"2026-09-10T17:15:20Z"}
{"v":1,"kind":"execution","execution_id":"stage-work-1-1","run_id":"0f4c3a6e-2d1b-4b7e-9c8a-5e2f1d0a9b31","parent_execution_id":"root-195f819e","execution_kind":"stage","stage":"work","stage_occurrence":1,"attempt":1,"agent":"claude-code","session_id":"2b7d4c1e-8f3a-4e5b-9d6c-1a2b3c4d5e6f","producer":"weft@0.3.0","started_at":"2026-09-10T17:20:00Z","ended_at":"2026-09-10T17:24:10Z","outcome":"failed","recorded_at":"2026-09-10T17:24:10Z"}
{"v":1,"kind":"execution","execution_id":"stage-work-1-2","run_id":"0f4c3a6e-2d1b-4b7e-9c8a-5e2f1d0a9b31","parent_execution_id":"root-195f819e","execution_kind":"stage","stage":"work","stage_occurrence":1,"attempt":2,"agent":"claude-code","session_id":"3c8e5d2f-9a4b-4f6c-8e7d-2b3c4d5e6f7a","producer":"weft@0.3.0","started_at":"2026-09-10T17:25:00Z","ended_at":"2026-09-10T17:33:40Z","outcome":"completed","recorded_at":"2026-09-10T17:33:40Z"}
{"v":1,"kind":"execution","execution_id":"stage-review-1-1","run_id":"0f4c3a6e-2d1b-4b7e-9c8a-5e2f1d0a9b31","parent_execution_id":"root-195f819e","execution_kind":"stage","stage":"review","stage_occurrence":1,"attempt":1,"agent":"claude-code","session_id":"4d9f6e3a-0b5c-4a7d-9f8e-3c4d5e6f7a8b","producer":"weft@0.3.0","started_at":"2026-09-10T17:34:00Z","ended_at":"2026-09-10T17:36:00Z","outcome":"stopped","recorded_at":"2026-09-10T17:36:00Z"}
{"v":1,"kind":"execution","execution_id":"cmd-go-test-1","run_id":"0f4c3a6e-2d1b-4b7e-9c8a-5e2f1d0a9b31","parent_execution_id":"stage-work-1-2","execution_kind":"command","producer":"weft@0.3.0","started_at":"2026-09-10T17:33:41Z","ended_at":"2026-09-10T17:33:58Z","outcome":"completed","recorded_at":"2026-09-10T17:33:58Z"}
{"v":1,"kind":"run","run_id":"0f4c3a6e-2d1b-4b7e-9c8a-5e2f1d0a9b31","ended_at":"2026-09-10T17:40:00Z","outcome":"completed","reporting_cutoff":"2026-09-10T17:40:00Z","producer":"warp/work@1.4.0","recorded_at":"2026-09-10T17:40:00Z"}
```

## Writing a record

Any producer can. The write is best effort and must not be able to fail the
run it is describing:

```sh
LOOM_DIR="${LOOM_HOME:-$HOME/.loom}"
if mkdir -p "$LOOM_DIR" 2>/dev/null; then
  printf '{"v":1,"kind":"run","run_id":"%s","ticket":"%s","runtime":"claude-code","agent":"claude-code","session_id":"%s","producer":"warp/work@%s","started_at":"%s","recorded_at":"%s"}\n' \
    "$RUN_ID" "$TICKET" "$SESSION_ID" "$WORK_VERSION" "$NOW" "$NOW" \
    >> "$LOOM_DIR/executions.jsonl" 2>/dev/null || true
fi
```

Values with a `"` or a backslash in them would need escaping; ids and ticket
names carry neither, and a record loom cannot parse is skipped with a
diagnostic rather than fatal.
