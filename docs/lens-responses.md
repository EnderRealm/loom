# Lens responses

A `/work` run fans out to three review lenses — contract, quality, security —
and each answers with one fenced `json` verdict block: `lens`, `verdict`,
`summary`, `context`, `criteria[]`, `findings[]`. The block reaches the
orchestrator's transcript by several routes: a task notification on the user
side for a background reviewer — or, since the notification stopped quoting
the report, the subagent hand-back that precedes it — the subagent call's own
tool result for a synchronous one, the `codex-lens.sh` call's output for the
routed security lens (or nothing, when the run redirected that output to a
file and read it back with a later `cat`), and the assistant's own text — or
a file it wrote whole — on a runtime that inlines its passes. The summary
tables cut every tool result at 800 characters, which lands inside most
verdicts' criteria lists. This is the store that keeps them whole.

## What is stored

`lens_responses` holds one row per lens block, read off the full record text
by both parsers before any truncation. Every fenced `json` block whose body
names a `lens` field is a row, whatever its shape: a response that does not
parse is evidence of what the orchestrator saw and is kept as such, never
dropped and never promoted.

| Column | Meaning |
| --- | --- |
| `agent`, `session_id`, `seq` | The session the block was read from and the block's position among its lens rows. |
| `response_id` | The position-derived identity below. |
| `turn_idx` | The turn the record belongs to. |
| `origin` | Where the block landed: `task_notification`, `tool_result`, `assistant`, `file_write` or `user`. `task_notification` is the envelope the harness posts when a background dispatch finishes — a user record, or a `queued_command` attachment prompt, whose text opens with `<task-notification>` past any leading `<system-reminder>` blocks, and that is not a compaction summary (`isCompactSummary`) — or a subagent hand-back: an `origin` of kind `peer` with `handback` set, the harness relaying a background agent's final report in `origin.body`, every line indented under its frame. It rides on a meta user record, or, when it lands while the assistant is mid-turn, on the `queued_command` attachment that injects it, the attachment carrying the `origin`. The notification that follows it names the dispatch but no longer quotes the report. A hand-back opens no turn and lands in the turn in progress; the same hand-back — the same agent and report — is read once whichever record carried it, so a verdict delivered twice cannot answer twice. An `origin` that does not decode is not a hand-back. `user` is any other Claude user message, one that merely contains the marker included — a pasted note or a compaction summary quoting a whole notification, dispatch id and all, is stored with no dispatch to answer; a Codex user item is not read for blocks. `file_write` is a file the agent wrote whole through a Codex `item_completed` FileChange item — the `add` change's content, which is the whole file as written, since the `/work` render has the inlined passes write their verdicts to files for `verdict-merge.sh` and a file written through `apply_patch` lands in no message. An `update` carries a unified diff and a `delete` the removed file's content — the run's cleanup deletes every verdict file at once — and neither is read. |
| `dispatch_id` | The tool call the response answers — a tool result's `tool_use_id` / `call_id`, a task notification's `<tool-use-id>`, a hand-back's Agent call: the one whose tool result's `toolUseResult.agentId` names the agent in `origin.from`, when that result is its carrier's only one — or NULL where the record names none, a hand-back from an agent the session did not launch included. |
| `source_path`, `source_line` | The transcript file and the 1-based line of the record. |
| `ordinal` | The block's index among the lens blocks in that record. |
| `at` | The record's timestamp. |
| `lens`, `verdict`, `summary` | The decoded fields. A malformed block still carries the `lens` and `summary` that precede the cut, recovered by pattern. |
| `status`, `malformed_reason` | `parsed` for a JSON object naming a known lens and a known verdict whose `context`, `criteria` and `findings`, where supplied, fit the verdict schema — a context object carrying both a known `state` and a `received` array of strings, and arrays of the schema's items with a known status or severity; a missing `received`, or a `null` where an array belongs, does not fit. Else `malformed` with the reason: `unterminated`, `invalid_json`, `not_object`, `unknown_lens`, `unknown_verdict`, `invalid_context`, `invalid_criteria`, `invalid_findings`. A response with no `context` key predates the field and is parsed without it. |
| `context_kind`, `context_state`, `context_received` | `structured` with the state and the received list when the block carries a `context` object with a known state; `historical` with both NULL when it carries none — it predates the field, or was cut before it, since nothing is recovered past a block that is not JSON. |
| `criteria_json`, `findings_json` | The arrays exactly as returned. |
| `raw` | The whole fenced body. |

`lens_criteria` and `lens_findings` hold the arrays normalized, one row per
item keyed by `response_id` and position, for a `parsed` response. A
malformed response leaves its JSON on the response row and writes no items:
what the lens returned is kept either way, and only a response that fits the
schema is counted.

Ordinary subagent transcripts are not read for lens blocks. The parent holds
the same response as a notification or a tool result, and a second copy would
count twice. A recorded routed lens is the exception: when its parent attempt
has no response, the final assistant verdict in the execution record's
declared child session answers that exact execution. A parent-session response
still wins when both exist.

## Response identity

`response_id` is the hex SHA-256 of
`agent|session_id|source_line|origin|dispatch_id|ordinal`. Nothing in it comes
from the response's content or from the wall clock, so a re-fold of the same
transcript computes the same id, and the session's rows — response, criteria,
findings — are deleted and rewritten under the same ids rather than
duplicated. A transcript that grows keeps its earlier ids: the lines already
read do not move.

## The attempt model

`workreport.Lenses` reads a run's rows back as attempts: one per try at one
lens in one review round. The run's turns are walked in order.

- A commitment line — `dispatching (<ticket> round N): contract, quality,
  security` — makes round N current and records the lenses it named.
- A lens dispatch — a subagent row whose key argument reads as a lens
  dispatch (it says `lens` or `review`, the same test the fan-out count
  applies) and names a lens, or a `codex-lens.sh --lens <name>` call, or
  any tool row a lens execution record names by `dispatch_id` — opens the
  next attempt for that lens in the current round, `dispatched`; a router
  call's attempt is `routed`. A dispatch whose own result is an error and
  carries no verdict is `failed`. An attempt's `provenance` says where its
  paired response came from when the walk could attribute it to the router
  — `router_result` for a lone router call's own result, `read_back` for an
  exclusive read of the router's redirect, or `child_session` for the final
  assistant verdict in a routed execution's declared transcript — and is
  empty otherwise: for a subagent's notification, an inlined pass, or no
  response.
- A response pairs with its attempt by dispatch id. One naming a known lens
  other than the attempt's — a contract dispatch coming back with a quality
  verdict — is the dispatch answered wrong, not a verdict of either lens:
  the attempt is `responded` with the mismatch as its reason (`lens
  mismatch: response names quality, dispatch named contract`), located by
  the response's id and source but carrying none of its verdict, and it is
  never successful. A mismatched response for a dispatch that already
  answered places nothing — it neither supersedes the standing answer nor
  opens a further attempt — and stays in the store as evidence. A `codex-lens.sh` call's
  own result pairs only when the command is the router alone: past the shell
  wrapper, one invocation — an env-assignment prefix and a `2> <path>`
  stderr redirect aside — with no `>`/`>>` stdout redirect and no `;`, `&&`,
  `||`, `|` or newline joining another command. A redirected or compound
  router command's result is not the router's output: what the other
  command printed can hold a block shaped like a verdict, so its blocks are
  stored and place nothing, and the attempt stays `dispatched` until a read
  of the redirect path pairs it, as below. A redirect path is recorded for
  the dispatch only when the command is that lone router invocation followed
  by exactly one `> <path>` and nothing else, a `2> <path>` on either side
  of it aside; a compound command records none, whichever command its
  redirect belongs to — `codex-lens.sh …; cat README.md > notes.txt` would
  otherwise hand the README's quoted verdict to the dispatch on a later read
  of the notes file — and an append (`>> <path>`) records none either, since
  the file keeps what it already held and a read of it would deliver an
  earlier round's verdict first, answering the later attempt with the wrong
  block; both attempts stay `dispatched`, never paired by a read-back.
  Anything else the command
  cannot be classified as fails closed. The key argument is cut at 200
  chars — a router command is kept whole to 2000, since the walk reads its
  `--lens` off it, but rows folded before that bound stand as stored — and
  a router row cut before `--lens` reads as no
  lens dispatch on its own: it joins its execution record by time window,
  as below, and takes lens, round and attempt from the record, while
  verdict pairing from that row's own result still fails closed, the
  attempt `dispatched` until the child session's response fills it. A
  verdict a shell call
  read back — a `cat` of the file the router's output was redirected to —
  answers the unanswered dispatched attempt whose `codex-lens.sh` command
  redirected its stdout (`> <path>`) to the one path the reading command
  reads, and nothing else: past the shell wrapper, the command must be `cat`
  (bare, or a `/`-path ending in `/cat`), an optional `--`, and exactly that
  path (`cat <path>`, `cat -- <path>`), so any other program — `rg
  --passthru <path>`, whose output is a search's — any other flag, a second
  operand, a chain or a pipeline, whose output cannot be attributed to the
  file alone, is stored and places nothing, as is a shell result naming no
  such path, since any document the run reads can hold a block shaped like a
  verdict. An inlined pass — an `assistant` row on a runtime that inlines
  its passes — answers the latest unanswered dispatched attempt of its lens
  in the current round, else opens an undispatched attempt there. A
  `file_write` row places the same way, on a runtime that inlines its
  passes only, in its position among the turn's tool rows by time — ahead
  of the first timed call it does not follow, after the last call otherwise
  — and applies the next commitment line first when its lens already
  answered in the current round, as a re-dispatch of that lens does. A paired
  response makes the attempt `parsed` or, when malformed, `responded`. A
  `task_notification` row places only through its dispatch id: one for a
  dispatch that is not one of the run's lens attempts is not placed, since
  it answers something else, and one naming no dispatch — user text quoting
  the `<task-notification>` marker with no `<tool-use-id>`, a pasted ticket
  or a compaction summary — is not placed either, so it can neither answer
  an outstanding attempt nor open one that supersedes the real answer. A
  `user` row — a Claude user message that is not a
  task notification — is never placed: no lens answers as a plain user
  message, so the block is quoted material (a compaction summary reproducing
  a verdict, a human pasting one), stored as evidence only.
- A recorded run's lens execution records (docs/execution-records.md) join
  its dispatches, each record to at most one dispatch and each dispatch to
  at most one record: a record naming a `dispatch_id` joins the row with
  that call id, whatever the row's key argument reads as; a record naming
  none — `codex-lens.sh` writes its own record and cannot know its
  tool_use_id — joins a `codex-lens.sh` call of its lens whose window holds
  the record's start, from five seconds before the call began to five
  seconds after it ended (unbounded after, when the call's duration is
  unknown), the earliest such record first — of any lens, when the call's
  key argument was cut before `--lens` and names none, the router the last
  command before the cut. A subagent row joins by
  dispatch id alone. A joined record is the attempt's identity: the attempt
  is of the record's lens, in the record's round and at the record's
  attempt number where the record carries them, over the key-argument
  heuristic, the commitment line's round and the next number in it — the
  record is the producer's word, and the transcript walk is the fallback
  for rows without one. Two records naming the same (lens, round, attempt)
  are both kept, the later placed superseding the earlier. The attempt
  carries the record's `execution_id`, and the run report fills that
  attempt with the record's outcome and metrics rather than looking one up
  by (lens, round, attempt).
- After the parent transcript walk, an unanswered attempt carrying a recorded
  execution id takes the final assistant lens response from that execution's
  declared `(agent, session_id)`. No response is looked up by project, time or
  lens name alone. A response naming a different known lens is retained as a
  mismatched `responded` attempt, not promoted to a verdict; a missing child
  response leaves the attempt `dispatched`.
- A turn whose assistant text holds no commitment line — a Claude transcript
  since Claude Code 2.1.268 carries no text block from an assistant message
  that also called a tool, so no row holds the line — takes its round from
  the records its dispatches joined: one line per joined row naming the
  record's round and no lenses, applied the way a text line is, so the
  turn's subagent dispatches ahead of the router call land in that round
  too, and no `missing` attempt is derived from a record. A text line,
  where one exists, still governs the walk's current round for every row
  that joined no record and for the inlined passes, and alone decides what
  was committed to — the `missing` derivation reads only the lines.
- After the walk, a lens a commitment line named with no attempt in that
  round gets one `missing` attempt; every attempt with a later attempt for
  the same lens and round is `superseded`; a response that landed after the
  next round's commitment line, or after the next `/work` invocation, is
  `late`.

Round 0 holds attempts neither a commitment line nor a joined record placed.
Within a turn the order is
the user-side responses that open it, then its tool rows in sequence, then
its assistant text with commitment lines and inlined blocks in text order. A
`task_notification` row whose dispatch was not on record when the turn opened
— one that landed mid-turn, and in a run no human message interrupts, the
whole run is one turn — is placed among the tool rows by time once its
dispatch is on record, ahead of the first timed call it does not follow, and
after the tool rows otherwise: a lens that answered before its next round's
dispatch is answered there, so that dispatch applies the next line. A
tool row has no position among the turn's commitment lines, so the first line
is applied at the turn's first lens dispatch and each later one when a lens
that already answered in the current round is dispatched again; a retry
follows a failed, malformed or unanswered attempt and does not open a round.
Inlined blocks and `file_write` rows are placed only on a runtime that
inlines its passes (Codex, Cursor); on Claude the assistant quoting a
verdict, in its text or in a file it wrote, is not a lens answering.
A file-reading tool's result is not paired either: a diff or a doc it reads
can quote verdict blocks that answer nothing. The redirect path is read off
`tool_calls.key_arg`, the command cut at 200 characters (2000 for a router
command); a redirect past the cut is not seen, and the attempt stays
`dispatched` rather than pairing on a guess. A router row cut before
`--lens` still joins its execution record by time window and takes lens,
round and attempt from it; only the pairing from the row's own result fails
closed.

`contaminated` is the structured `context.state` where the response carries
one, and otherwise the summary's prose — the historical reading, labelled as
such by `context_kind`.

## Status vocabulary

| Status | Meaning |
| --- | --- |
| `missing` | Committed to in a round's line; nothing dispatched or answered. |
| `dispatched` | On the record, unanswered — redirected to a file and never read back, or still running when the transcript ended. |
| `failed` | The dispatch's own result was an error and carried no verdict. |
| `responded` | A response landed but was malformed, or named a known lens other than the one dispatched. |
| `parsed` | A whole verdict naming a known lens and verdict landed. |

An attempt is **successful** when it is `parsed` and not `superseded`: a
whole response that no later attempt replaced. The compliance report's
`contamination_reports` counts parsed responses reporting contamination once
per response id, superseded or not, because each was reported; its
`criteria_unverified` reads the last successful contract attempt's criteria;
and its router evidence — the `codex-lens.sh` call a Codex run stands on, or
the routed security lens beside a Claude run's subagents — is a `routed`
attempt that `parsed` off a response with router `provenance`, superseded or
not: the dispatch happened and the router's own result or a read-back of its
redirect answered it. The command naming the router is transcript content an
`echo` can reproduce, a chained command's result can carry any file's quoted
verdict, and an inlined verdict pairing with a redirected router call that
was never read back is the in-context pass answering, so none counts.

## What this is not

A stored response is evidence of what the orchestrator saw at that line of
the transcript. It is not proof the review passed, not proof the lens ran in
a clean context beyond what the response itself claims, and not a receipt: a
transcript can hold anything, and the store keeps it as it was.
