---
id: forge-review-agents-no-tk-mcp-access
title: Forge review-pipeline agents have no tk MCP tools; ticket content must be inlined by the dispatcher
scope: forge
type: truth
status: candidate
evidence:
  - path: agents/design-reviewer.md
    line: 22
    note: explicit instruction "do NOT search for `.tickets/` files or try to read ticket data from the filesystem"
  - path: agents/*.md
    note: 'grep "^tools:" across all six review agents shows no mcp__plugin_forge_tk__* tool listed'
sources:
  - session: c094969e-649e-4c8d-beab-39ff7ef5749d
    project: forge
    date: 2026-04-08
    role: A review-subagent failure was initially attributed to missing agent tool access; reading agent source showed agents are deliberately stateless, and the real fix belonged in the /review skill's prompt-dispatch logic.
related: []
contradicts: []
verified_at: 2026-08-15
status: candidate
extracted_at: 2026-08-15T11:42:55
extracted_by: claude:sonnet
---

## Claim

None of forge's six review-pipeline agents (spec-builder, design-reviewer, impl-reviewer, code-reviewer, plan-reviewer, test-runner) have `mcp__plugin_forge_tk__*` tools in their allowlist, and at least one (`design-reviewer`) explicitly instructs itself not to read ticket data from the filesystem. These agents are stateless text processors: any ticket content they need to act on must be inlined into their prompt by the skill that dispatches them.

## Why it matters

A skill that dispatches a review agent with only a bare ticket ID (instead of inlined ticket content) will produce a review agent that has no way to see what it's reviewing, and the failure will look like an agent-capability problem when it's actually a prompt-construction problem in the dispatching skill. Future fixes for "review agent doesn't have the context it needs" belong in the skill layer, not in the agent's tool allowlist.

## How to verify

Run from the forge repo root:

```
grep -n "do NOT search\|do not read ticket data" agents/design-reviewer.md
grep -n "^tools:" agents/*.md
```

Expect no `mcp__plugin_forge_tk__` entries in any agent's `tools:` line.

## Notes

This held even after the fix was relocated to `/review`'s SKILL.md with explicit prompt templates — the agents' tool access was intentionally left unchanged, confirming the stateless-processor design is the intended architecture, not an oversight.