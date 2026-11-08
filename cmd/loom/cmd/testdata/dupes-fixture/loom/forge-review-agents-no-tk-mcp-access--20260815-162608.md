---
id: forge-review-agents-no-tk-mcp-access
title: forge review agents have no tk MCP tools; ticket content must be inlined into their prompts
scope: forge
type: truth
status: candidate
evidence:
  - path: agents/design-reviewer.md
    note: 'instructs the agent explicitly: "Do NOT search for .tickets/ files or try to read ticket data from the filesystem"'
sources:
  - session: 12130886-9305-4755-99ea-eff964acdb4a
    project: forge
    date: 2026-04-08
    role: Surfaced while extracting truths from a prior forge session summary that traced a review-skill bug back to this architectural constraint.
related: []
contradicts: []
verified_at: 2026-08-15
status: candidate
extracted_at: 2026-08-15T16:26:08
extracted_by: claude:sonnet
---

## Claim

None of the six forge review agents (spec-builder, design-reviewer, impl-reviewer, code-reviewer, plan-reviewer, test-runner) have tk MCP tools in their allowlist, and they do not read `.tickets/` files from disk either. Any ticket content a review agent needs (title, description, acceptance criteria, prior review notes) must be inlined into the prompt text by the skill that dispatches the agent.

## Why it matters

A skill that invokes a review agent without inlining ticket content produces an agent that either hallucinates ticket state or silently reviews nothing — there is no fallback path (no MCP tool call, no filesystem read) that would let the agent recover the missing context on its own. This is the architectural reason such bugs manifest as "the review agent doesn't seem to know about the ticket" rather than a tool-call error.

## How to verify

Run from the forge repo root:

```
grep -l "mcp__plugin_forge_tk" agents/*.md
```

Expected: empty output (no matches) — confirming none of the review agents carry tk MCP tools in their frontmatter allowlist.

```
grep -n "Do NOT search for\|.tickets/" agents/design-reviewer.md
```

## Notes

This is a design constraint, not a bug — any skill dispatching one of these six agents is responsible for inlining ticket content; fixing a specific skill's omission doesn't change the underlying fact that the agents are stateless with respect to tk.