---
project: ticket
session_id: 2e9a386c-23bb-4d33-9003-756680c39d9b
date: 2026-02-27
branch: master
tickets: []
message_count: 1034
tool_uses: 326
files_touched: 36
input_tokens: 741
output_tokens: 60082
cache_read_tokens: 47840856
cache_creation_tokens: 644731
processed: false
---

<!-- BEGIN_SESSION_SUMMARY -->

### Ticket(s)
- f072 (feature: Encouraging messages on empty listing)
- mcp-ticket-create-0f07 (bug: MCP ticket_create fails with "ticket ID is required")
- gate-checks-require-0621 (bug: Gates check body sections but ticket_edit writes frontmatter)
- mcp-ticket-edit-0405 (bug: MCP ticket_edit silently drops body fields)
- mcp-ticket-add-310d (bug: MCP ticket_add_note splits text on \n\n into multiple notes)

### Overview
Completed a feature (encouraging messages for empty listing commands) and shipped a Homebrew release. Fixed one critical MCP bug (ticket_create not setting ID/Status/Stage) with a reusable in-process test harness. Discovered and diagnosed three additional MCP/gate workflow bugs that remain unresolved.

### Decisions
- Use external `messages.txt` file for encouraging messages instead of embedding in code — easier maintenance (human)
- Replace all listing command empty states with consistent encouraging messages regardless of filter/empty reason — simpler behavior (human)
- Create in-process MCP test harness (`NewInMemoryTransports`) instead of replacing installed binary — preserves service availability for other users/agents (human)
- Add behavioral rules to global CLAUDE.md: ask before creating bugs, check for duplicates, answer questions before taking action (human)
- Create separate bug tickets for gate/edit mismatch vs. note duplication vs. body field drops — different root causes (human)

### Problems
- MCP `ticket_create` not setting ID/Status/Stage before validation — (resolved) (buggy_code)
- Conversational skill's `ticket_edit` calls for acceptance/design fields fail gate checks because gates expect markdown body sections but `ticket_edit` writes YAML frontmatter — (unresolved) (wrong_approach)
- MCP `ticket_add_note` duplicates notes due to faulty timestamp pattern matching in `parseNotes` (treats `**text**` markdown as potential timestamp) — (diagnosed, fix in progress) (buggy_code)
- User had to clarify three times: Claude should ask before taking action on questions, search for duplicate tickets before creating new ones, and stop to ask about bugs instead of silently working around them — (resolved via CLAUDE.md rules) (misunderstood_intent)
- Gates require `## Acceptance Criteria`, `## Design`, `## Test Results` sections in markdown body, but MCP tools have no way to write these sections — (unresolved, affects multiple workflows) (wrong_approach)

### Discoveries
- MCP in-process testing via `NewInMemoryTransports` — creates client/server pair without stdio framing overhead
- Difference in ticket initialization: CLI sets ID/Status/Stage before `store.Create`, but MCP handler doesn't
- Root cause of note duplication: `parseNotes` regex `HasPrefix("**") && HasSuffix("**")` matches markdown formatting, not just timestamps
- Gates check markdown body sections but MCP edit tools write YAML frontmatter — fundamental architecture mismatch affecting multiple features
- Established pattern: MCP bugs should have in-process test harness to avoid breaking other service users

### Incomplete Work
- mcp-ticket-add-310d: Fix for note duplication identified (improve timestamp parsing pattern), test written, likely ready for commit but not finalized in transcript
- gate-checks-require-0621: Root cause identified (gate/edit mismatch) but fix design not yet sketched — may require MCP tool changes or gate changes
- mcp-ticket-edit-0405: Body field loss tracked but not investigated — need to trace ticket_edit flow to find where fields are dropped

### Outcome
mostly_achieved — Feature f072 shipped and released via Homebrew, one critical MCP bug fixed with test harness, but uncovered three additional MCP/workflow bugs that block future development until resolved.

### Metrics
- Files touched: ~16 (README.md, release.yml, cmd/{messages.txt, empty.go, ls.go, ready.go, blocked.go, inbox.go, closed.go, pipeline.go, next.go, mcp.go}, CLAUDE.md global/project, .tickets/* for bug tickets)
- Estimated scope: large (feature + release + bug discovery + test infrastructure + process docs)

<!-- END_SESSION_SUMMARY -->
