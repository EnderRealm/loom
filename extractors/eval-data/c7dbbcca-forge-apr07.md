---
project: forge
session_id: c7dbbcca-cceb-452b-aafd-993eb791e230
date: 2026-04-07
branch: main
tickets: []
message_count: 649
tool_uses: 235
files_touched: 17
input_tokens: 884
output_tokens: 134697
cache_read_tokens: 84707076
cache_creation_tokens: 2950670
processed: false
---

<!-- BEGIN_SESSION_SUMMARY -->

### Ticket(s)
- forge/evaluate-dolt-query-33a3
- forge/evaluate-graphify-knowledge-1538
- forge/ticket-review-agent-28b9
- forge/nightly-job-monitoring-ba5c
- forge/port-tk-commit-4ae2
- forge/redesign-forge-client-f605 (referenced, V2 epic)
- forge/merge-tk-forge-9f11 (referenced, confirmed done)

### Overview
Session covered idea evaluation (Dolt, Graphify), a major backlog cleanup (~273→~257 tickets), fixing a blocking MCP integer/boolean coercion bug, creating tickets for nightly job monitoring and tk commit journal porting, and progressing the commit journal ticket through triage → spec → design stage. The MCP coercion fix was committed and pushed, unblocking bulk priority edits across ~25 tickets.

### Decisions
- **Dolt fits as query layer over forge-data, not as ticket store** — Tickets stay as co-located markdown in git history; Dolt would lose that. Migration shape: JSON stays source of truth, Dolt becomes query layer. (human)
- **Graphify experiment over forge-data** — Complements Dolt (semantic/structural queries vs tabular). (human)
- **Fix MCP coercion bug before continuing cleanup** — Bug was blocking all priority edits (~25 tickets). Option A (fix first) chosen over B (skip priorities) or C (bash escape hatch). (human)
- **Use `numericParam` and `booleanParam` helpers with `.preprocess()` instead of naive `z.coerce`** — `z.coerce.boolean()` treats any non-empty string as `true` (including `"false"`), so a `.preprocess()` with explicit string matching was required for booleans. (auto)
- **P0 inflation cleanup: 14 P0→P2, 4 P0→P3, 7 non-V2 epics→P3** — Restores P0 to mean "urgent" during V2 push. (human)
- **Nightly job monitoring ticket created as V2 child (P2)** — Covers version drift detection and one-click reinstall from forge console. (human)
- **Tk commit journal: run sync loop inside each process lifetime (Hono, MCP stdio, future CLI)** — Mirrors Go `cmd/serve.go` architecture exactly. Old Go tk ran sync goroutine inside `tk serve` subprocess, no standalone daemon needed. (human)
- **Three process scenarios must be supported**: Forge console (long-running), Claude Code MCP (per-session), future CLI (one-shot synchronous sync). CLI adds `--no-sync` flag for batch scripting. (human)
- **Risk bumped from normal to high for commit journal** — Critical-path infrastructure running on every ticket write across three processes; mistakes corrupt forge-data history. Spec-builder pushed back on initial assessment. (auto, confirmed by human)
- **Callback-injected logger pattern for journal** — Solves MCP stdout contamination problem where console.log on stdio transport would corrupt the JSON-RPC stream. (auto)

### Problems
- **MCP SDK serializes integers as strings, Zod rejects them** — All 18 MCP tools with numeric/boolean params affected. Fixed with `numericParam`/`booleanParam` helpers using `z.preprocess()`. (resolved) (buggy_code)
- **`z.coerce.boolean()` footgun** — Any non-empty string including `"false"` coerces to `true`. Required explicit string matching in preprocess. (resolved) (buggy_code)
- **tsc --noEmit OOMed on project-wide check** — Used test suite (294 pass) as verification instead. (resolved) (env_constraint)
- **Running MCP server had stale code after fix** — Plugin loaded at session start couldn't use the fix until reload. Worked around by recording review verdicts via notes instead of `ticket_review` tool. (resolved) (env_constraint)
- **Design-builder agent response truncated** — Design document too large for agent return message. Solved by having agent write design to a file instead. (resolved) (env_constraint)
- **`ticket_edit` for priority field failed 26 times during cleanup** — Same coercion bug, discovered during bulk operations. (resolved) (buggy_code)
- **forge-data ticket changes never auto-committed or pushed** — Go tk's commit journal was explicitly deferred during TS merge. Ticket writes from any process sit uncommitted, causing nightly pipeline rebase failures. (unresolved — ticket created: port-tk-commit-4ae2)

### Discoveries
- **Go tk ran sync loop inside `tk serve` subprocess** (`cmd/serve.go:42`) — goroutine with 5s ticker, dies when session ends. No standalone daemon. Simple idempotent `syncCentralStore` function.
- **Go sync is remarkably simple** — No enqueue, no debounce, no FileStore hook. 5s ticker runs `git add tickets/ && git diff --cached --quiet && git commit && git push` with rebase retry on conflict, `.tk-sync-blocked` marker on unresolvable conflict.
- **Two writers to forge-data exist** — Long-running Hono server AND per-session `src/tk/serve.ts` MCP stdio process. Only Hono has lifecycle for long-lived background loop; MCP stdio needs its own sync.
- **No forge CLI exists yet** — Referenced in merge-tk-forge design notes as "built incrementally as needed" but no `src/cli/` or `bin` entry. Journal design must be ready for it.
- **Nightly pipeline reinstall is manual** — `install-nightly.sh --install` must be run after updates that change plist structure/env vars. No git hook, no post-pull hook, no plugin reload tie-in. Silent drift risk.
- **Backlog had 273 tickets with severe P0 inflation** — Many tickets marked P0 that were standard priority, diluting the meaning of "urgent."
- **Test count is 294, not 94** — Memory file was stale (94 from 2026-03-28). Updated.
- **MCP stdout contamination risk** — `console.log` on MCP stdio transport corrupts JSON-RPC stream. Journal needs callback-injected logger pattern.

### Incomplete Work
- **forge/port-tk-commit-4ae2 at design stage** — Design document was being written/reviewed when session was truncated. Has spec with 20 ACs, risk high, covers all three process contexts. Design-builder agent wrote design to file. Next: complete design review, advance to implement.
- **forge/evaluate-dolt-query-33a3 in backlog** — Evaluate Dolt as query layer for forge-data. Needs triage.
- **forge/evaluate-graphify-knowledge-1538 in backlog** — Evaluate graphify as knowledge graph over forge-data. Needs triage.
- **forge/nightly-job-monitoring-ba5c in backlog** — Nightly job monitoring, drift detection, one-click reinstall in Console. V2 child ticket, needs triage.
- **Backlog priority hygiene steps 6-7 completed** but the remaining ~257 tickets could benefit from further cleanup passes as V2 progresses.

### Outcome
mostly_achieved
Fixed a blocking MCP coercion bug (committed+pushed), completed major backlog cleanup (15 tickets killed, ~25 priority edits), created 4 new tickets for identified gaps, and progressed the commit journal feature through spec to design stage, but design was still in review when session ended.

### Metrics
- Files touched: 3 (src/tk/mcp.ts, tests/tk/mcp.test.ts, MEMORY.md)
- Estimated scope: large

<!-- END_SESSION_SUMMARY -->
