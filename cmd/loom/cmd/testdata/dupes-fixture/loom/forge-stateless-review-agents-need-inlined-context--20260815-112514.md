---
id: forge-stateless-review-agents-need-inlined-context
title: Forge review/spec agents are stateless; ticket content must be inlined by the dispatching skill
scope: forge
type: truth
status: candidate
evidence:
  - path: agents/design-reviewer.md
    line: 22
    note: agent instructions explicitly state "do NOT search for `.tickets/` files" — agents never fetch ticket data themselves
sources:
  - session: ce558446-3554-41ab-a820-36fb869addbf
    project: forge
    date: 2026-04-08
    role: Session traced a review-subagent failure to this design, then fixed the dispatching /review skill rather than adding MCP tools to the agents
related: []
contradicts: []
verified_at: 2026-08-15
status: candidate
extracted_at: 2026-08-15T11:25:14
extracted_by: claude:sonnet
---

## Claim

All six forge pipeline agents (spec-builder, design-reviewer, impl-reviewer, code-reviewer, plan-reviewer, test-runner) are stateless text processors with no tk MCP tool access and no ability to search the ticket store directly. They can only act on ticket content that the dispatching skill inlines into their prompt.

## Why it matters

A new or modified skill that dispatches one of these agents but forwards only a ticket ID (instead of the full ticket body/acceptance criteria) produces a review that silently runs with no real context — the agent doesn't error, it just reviews nothing meaningful. Any fix for "agent seems to be missing context" belongs in the dispatching skill's prompt construction, not in the agent definition or by granting the agent MCP tools.

## How to verify

Run from the forge repo root:

```
grep -n "do NOT search" agents/design-reviewer.md
grep -L "mcp__tk__" agents/spec-builder.md agents/design-reviewer.md agents/impl-reviewer.md agents/code-reviewer.md agents/plan-reviewer.md agents/test-runner.md
```

The second command should list all six files (i.e., none of them reference a tk MCP tool in their frontmatter/tool grants).

## Notes

This was previously misdiagnosed as "agents need MCP tools" and reversed after reading agent source — the correction itself is evidence the stateless design is intentional, not an oversight.