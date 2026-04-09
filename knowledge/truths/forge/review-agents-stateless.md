---
id: forge-review-agents-stateless
title: Forge agents have no tk MCP access; ticket content must be inlined into prompts
scope: forge
type: truth
status: validated
evidence:
  - path: agents/design-reviewer.md
    line: 22
    note: explicit "Do NOT search for `.tickets/` files" rule
  - path: agents/*.md
    note: tools allowlists contain no mcp__plugin_forge_tk tools
sources:
  - session: 88f1615b-69cc-4330-9c8b-3affd4494229
    project: forge
    date: 2026-04-08
    role: discovery during /review subagent fix
verified_at: 2026-04-08
related:
  - review-gates-require-fresh-approval.md
---

## Claim

No forge agent in `agents/*.md` has tk MCP tools in its allowlist. Ticket data is not fetched by the agent — it must be inlined into the prompt by the dispatching skill. `design-reviewer.md:22` makes this explicit: *"All ticket data is provided in the prompt. Do NOT search for `.tickets/` files or try to read ticket data from the filesystem."*

## Why it matters

If a `/review`-style skill passes only a ticket ID to an agent, the agent has nothing to review. The integration point is the **skill**, not the agent. Adding MCP tools to an agent's allowlist to "fix" review failures is architecturally wrong — it injects non-determinism into a path designed to be a pure function from prompt to verdict.

## How to verify

Run from the forge repo root:

```
grep "^tools:" agents/*.md
```

Expected: every line lists only file/search/edit tools (Read, Glob, Grep, Edit, Write, Bash, WebSearch, WebFetch). No `mcp__plugin_forge_tk__*` tool appears.

```
grep -l "mcp__plugin_forge_tk" agents/*.md
```

Expected: empty.

## Notes

- `spec-builder.md` references `mcp__forge_signals__report_verdict` — this is a *different* MCP server (signals, not tk) used for **reporting** verdicts, not fetching ticket data. Doesn't violate the truth.
- The "do NOT search" instruction text is only in `design-reviewer.md`. The other agents enforce the same constraint via their allowlist alone, which is weaker — an agent file could be edited to *try* to search and the harness would simply refuse the tool call. Worth strengthening the convention.
