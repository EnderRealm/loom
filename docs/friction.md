# Harness friction

A friction row is one moment the harness got in the way of a session: a hook
asked or refused, a permission was denied, a tool call came back as an error,
or the user cut the turn short. The rows exist because this is the one class
of finding knowledge extraction structurally cannot see — extraction skips
sessions whose repo has no `truths/<scope>/`, the fix for a signature usually
lives in a repo other than the one the session stood in, and the question is
arithmetic ("how often, what shape") rather than something to mine a
transcript for. So the summarizer counts, and a human reads the count.

## What is stored

`friction` in `~/.loom/summaries.db` (schema v10) holds one row per event,
keyed by `(agent, session_id, seq)`.

| Column | Meaning |
| --- | --- |
| `turn_idx` | The parent turn the event belongs to; for a folded subagent event, the turn that dispatched it (`-1` when unattributed). |
| `ts` | The record's timestamp. |
| `project`, `git_remote`, `cwd` | The repo the session stood in, stamped on every row so the view needs no join. |
| `kind` | One of the six below. |
| `signature` | The normalized message the view groups by. |
| `tool` | The tool name where one applies: the tool a hook gated, the tool whose result was an error. |
| `detail` | The raw first line, cut at 800 characters. |
| `agent_type` | The subagent type when the event came from a dispatched subagent transcript, else NULL. |

Rows are written for every session the summarizer folds. There is no
knowledge-scope gate: a session from a repo with no `truths/<scope>/` lands
its rows like any other.

## Kinds

Exactly six. `bash.repeat` was measured at 2,073 events, dominated by `echo
idle` and `git status` polling, and is excluded on purpose.

| Kind | Source in the Claude transcript | Signature |
| --- | --- | --- |
| `hook.ask` | `attachment` of type `hook_success` whose stdout JSON carries `hookSpecificOutput.permissionDecision: "ask"`. | `<hookName>: <permissionDecisionReason>` |
| `hook.deny` | The same attachment with `permissionDecision: "deny"`, the older top-level `decision: "block"`, or `exitCode: 2`. | `<hookName>: <reason>` — `permissionDecisionReason`, else `reason`, else the first line of stderr |
| `permission.classifier_denied` | A `tool_result` with `is_error` whose text contains `denied by the Claude Code auto mode classifier`. | The text after `Reason:`, else the first line |
| `permission.denied_by_user` | A `tool_result` with `is_error` whose text opens `The user doesn't want to proceed with this tool use` or `User rejected tool use`. | The tool name |
| `tool.error` | Any other `tool_result` with `is_error`. | `<tool>: <first non-empty line>` |
| `user.interrupt` | A `text` block on a user record reading `[Request interrupted by user…]`. Meta records are skipped; a tool-result carrier that holds the interrupt beside a rejected result yields both events. | The bracketed text |

An `allow` decision, a stdout that is not a decision and a clean exit with no
decision are not events. Hook records that are inline sidechain copies in the
parent stream are skipped; the subagent's own transcript is folded instead,
and its events land on the parent session with `agent_type` set. The Codex
parser emits none.

## Normalization

`Normalize` in `internal/parse/friction` — the leaf the parsers import for
the kinds and this function; the ranked view is `internal/friction` — takes
the first line, then replaces in this order: uuids (8-4-4-4-12 hex, any
case) with `<uuid>`; `pid` followed by optional space, `=` or `:` and digits
with `pid <pid>`; absolute paths — a `/` or `~/` opening a token, running to
the next space, quote or `)` — with `<path>`; runs of three or more digits
with `<n>`. Whitespace runs collapse to one space; the result is trimmed and
capped at 300 characters. Uuids and paths go before digits so their digit
runs are not split first.

## Ranking

`loom friction` groups rows by `(kind, signature)` and sorts by events per
active day, then events, then signature. An active day is a distinct UTC
calendar date on which the signature fired. Absolute count is the wrong
sort: it is dominated by chronic noise already tolerated. Density separates
a new spike (rm-gate: 96 events, 4 days) from a chronic signature (ripgrep
not found: 24 events, 10 days) from a self-resolved burst (30 events, one
day). Each row shows total events, first and last seen, active-day count
and sessions affected; `--sessions` lists the session ids under each row,
`--top` bounds the rows and `--since` (a `YYYY-MM-DD` date or RFC3339
timestamp, as on the reports) drops events before that time.

## Nothing is automatic

There is no threshold, nothing triggers extraction, and nothing calls a
model. Every threshold picked today would be a guess against a baseline
that does not exist; the view is meant to be read for a couple of weeks
first.
