---
id: forge-review-agents-stateless-inline-required
title: Forge review agents have no tk MCP tools; ticket content must be inlined by the dispatcher
scope: forge
type: truth
status: candidate
evidence:
  - path: agents/design-reviewer.md
    note: contains explicit instruction "do NOT search for `.tickets/` files or try to read ticket data from the filesystem"
sources:
  - session: be3c05d5-bbeb-4393-9a8e-d5234a780209
    project: forge
    date: 2026-04-08
    role: Session initially planned to add MCP tools to review agents to fix a "can't access ticket data" bug, then reversed course after reading agent source and finding the design was intentional.
related: []
contradicts: []
verified_at: 2026-08-15
status: candidate
extracted_at: 2026-08-15T11:54:49
extracted_by: claude:sonnet
---

## Claim

The forge review agents (spec-builder, design-reviewer, impl-reviewer, code-reviewer, plan-reviewer, test-runner) are stateless text processors with no `mcp__plugin_forge_tk__*` tools in their allowlists and no filesystem access to `.tickets/`. All ticket content they need — body, criteria, context — must be inlined into the prompt by whichever skill dispatches them.

## Why it matters

A skill that spawns a review agent and passes only a bare ticket ID (instead of the full ticket body/criteria) will fail the review silently — the agent has no tool to fetch the missing context, and the failure looks like an agent bug rather than a dispatch-prompt bug.

## How to verify

Run from the forge repo root:
```
grep -L "mcp__plugin_forge_tk__" agents/spec-builder.md agents/design-reviewer.md agents/impl-reviewer.md agents/code-reviewer.md agents/plan-reviewer.md agents/test-runner.md
grep -n "do NOT search for" agents/design-reviewer.md
```
All six agent files should appear in the first command's output (none reference the tk MCP tool prefix).

## Notes

The actual root cause of the "review can't see ticket data" bug traced to earlier in this same session was the `/review` skill's prompt dispatch (passing a bare ID), not a missing agent capability — confirming the agents are stateless by design, not by accident.