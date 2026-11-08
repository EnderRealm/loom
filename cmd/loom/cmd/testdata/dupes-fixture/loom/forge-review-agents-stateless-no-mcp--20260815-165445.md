---
id: forge-review-agents-stateless-no-mcp
title: forge review agents have no MCP tools and must receive ticket content inlined
scope: forge
type: truth
status: candidate
evidence:
  - path: agents/design-reviewer.md
    line: 22
    note: "\"Do NOT search for `.tickets/` files or try to read ticket data from the filesystem... Use the ticket content from the prompt as your source of truth.\""
  - path: agents/spec-builder.md
    note: same "You will receive" pattern; agent tool allowlist is Read, Glob, Grep, WebSearch — no tk MCP tools
sources:
  - session: 03ec2cb9-3036-48b2-9c29-8e1d2d02a256
    project: forge
    date: 2026-04-08
    role: spec-builder invocation returned "UNABLE TO REVIEW — I don't have access to ticket_show... I cannot locate the ticket file on disk", surfacing that the dispatcher had passed only a ticket ID to a stateless agent.
related: []
contradicts: []
verified_at: 2026-08-15
status: candidate
extracted_at: 2026-08-15T16:54:45
extracted_by: claude:sonnet
---

## Claim

forge's six review agents (`spec-builder`, `design-reviewer`, `impl-reviewer`, `code-reviewer`, `plan-reviewer`, `test-runner`, all under `agents/`) are stateless text processors by design: none have tk MCP tools in their allowlist, and at least `design-reviewer.md` explicitly instructs the model not to search the filesystem for ticket data. They depend entirely on the dispatching skill inlining ticket content (e.g. `ticket_show` output) into the prompt.

## Why it matters

Any skill that dispatches one of these agents but passes only a ticket ID (instead of inlined ticket content) will get an "UNABLE TO REVIEW" response that looks like an agent or tool-access bug but is actually a dispatcher wiring bug. Conversely, "fixing" this by adding MCP tools to the agents is wrong: it directly contradicts `design-reviewer.md`'s explicit filesystem instruction and introduces non-deterministic review behavior — the correct fix lives in the dispatching skill's prompt construction.

## How to verify

Run from the forge repo root:
```
grep -n "Do NOT search" agents/*.md
grep -n "You will receive" agents/*.md
grep -n "^tools:" agents/*.md   # confirm no mcp__*tk* entries in any review agent's frontmatter
```