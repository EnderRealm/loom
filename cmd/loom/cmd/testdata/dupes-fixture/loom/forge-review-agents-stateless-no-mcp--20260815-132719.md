---
id: forge-review-agents-stateless-no-mcp
title: Review agents have no tk MCP access; the dispatching skill must inline ticket content
scope: forge
type: truth
status: candidate
evidence:
  - path: agents/design-reviewer.md
    note: explicit instruction "Do NOT search for .tickets/ files or try to read ticket data from the filesystem"
sources:
  - session: 8674746a-bd60-4aa3-9071-5bb97f5cb6d1
    project: forge
    date: 2026-04-08
    role: Diagnosed review subagents receiving nothing to review; reversed an initial fix plan (add MCP tools to agents) after confirming agents are intentionally stateless, and fixed the dispatching /review skill's prompt construction instead.
related: []
contradicts: []
verified_at: 2026-08-15
status: candidate
extracted_at: 2026-08-15T13:27:19
extracted_by: claude:sonnet
---

## Claim

None of the six forge review agents (spec-builder, design-reviewer, impl-reviewer, code-reviewer, plan-reviewer, test-runner) has any `mcp__plugin_forge_tk__*` tool in its allowlist, and `agents/design-reviewer.md` explicitly forbids reading ticket data from the filesystem. Review agents are stateless text processors by design; all ticket content (description, acceptance criteria) must be inlined into the prompt by the skill that dispatches the agent.

## Why it matters

A skill that dispatches a review agent by passing only a ticket ID or reference — without inlining the full ticket content — leaves the agent nothing to review, and it will fabricate a verdict or silently pass. This is an architectural constraint on every current and future review agent, not a bug in one skill: the fix belongs in the dispatching skill's prompt construction, never in giving agents MCP tool access.

## How to verify

Run from the forge repo root:

```
grep -L "mcp__plugin_forge_tk" agents/*.md
```

This should list all six review agents (spec-builder, design-reviewer, impl-reviewer, code-reviewer, plan-reviewer, test-runner). Also confirm:

```
grep -n "Do NOT search for .tickets" agents/design-reviewer.md
```

## Notes

An initial fix attempt in this session proposed adding MCP tools to the agents; that was reversed after reading agent source and recognizing the stateless design was intentional, not a gap.