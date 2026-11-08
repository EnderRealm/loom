---
id: forge-review-agents-stateless-need-inlined-tickets
title: Forge review agents have no tk MCP tools; ticket content must be inlined by the dispatcher
scope: forge
type: truth
status: candidate
evidence:
  - path: agents/design-reviewer.md
    line: 22
    note: instructs the agent "do NOT search for `.tickets/` files" — confirms agents don't fetch ticket data themselves
  - path: SKILL.md
    note: /review skill rewritten with explicit prompt templates that inline ticket ID, description, and acceptance criteria into the agent prompt
sources:
  - session: 3d1794e2-71bb-4c4c-8f09-19a6416c059a
    project: forge
    date: 2026-04-08
    role: Root-caused a /review failure to missing ticket context in agent prompts, tracing it to the fact that none of the 6 review agents carry tk MCP tools in their allowlist.
related: []
contradicts: []
verified_at: 2026-08-15
status: candidate
extracted_at: 2026-08-15T15:23:33
extracted_by: claude:sonnet
---

## Claim

None of the forge review agents (spec-builder, design-reviewer, impl-reviewer, code-reviewer, plan-reviewer, test-runner) have tk MCP tools in their allowlist, and none fetch ticket data on their own. They are stateless text processors: any ticket content they need — ID, description, acceptance criteria — must be inlined into the prompt by whichever skill dispatches them.

## Why it matters

A skill that passes only a ticket ID string to a review agent (instead of inlining the ticket's content) produces a review with no actual context to evaluate against, and the agent has no way to recover the missing information itself. This applies to every current and future skill that dispatches one of these 6 agents, not just `/review`.

## How to verify

Run from the forge repo root:

```
grep -L "mcp__tk__" agents/spec-builder.md agents/design-reviewer.md agents/impl-reviewer.md agents/code-reviewer.md agents/plan-reviewer.md agents/test-runner.md
```

Expected: all 6 files listed (none reference a tk MCP tool in their allowlist).

```
grep -n "do NOT search for" agents/design-reviewer.md
```

Expected: the line instructing the agent not to look for `.tickets/` files directly.

## Notes

The bug that surfaced this ("`/review` didn't inline ticket content") is fixable independently of this fact — the fact remains true regardless of which dispatching skill is in question.