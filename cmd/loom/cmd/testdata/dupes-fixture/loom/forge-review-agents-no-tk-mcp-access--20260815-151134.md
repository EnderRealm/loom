---
id: forge-review-agents-no-tk-mcp-access
title: Forge review agents have no tk MCP tools; ticket content must be inlined by the caller
scope: forge
type: truth
status: candidate
evidence:
  - path: agents/design-reviewer.md
    line: 22
    note: contains the literal instruction "Do NOT search for `.tickets/` files..."
  - path: agents/
    note: "grep -l for mcp__plugin_forge_tk across all agents/*.md tools: allowlists returns zero matches across all 8 agent files"
sources:
  - session: 3d24557c-89db-41f2-9dc0-92ebd1f7393e
    project: loom
    date: 2026-04-08
    role: Verified while cross-checking a raw-jsonl-derived candidate truth ("entire /review flow is non-functional") against live forge source
related: []
contradicts: []
verified_at: 2026-08-15
status: candidate
extracted_at: 2026-08-15T15:11:34
extracted_by: claude:sonnet
---

## Claim

No agent definition under `agents/*.md` in the forge plugin includes any `mcp__plugin_forge_tk` tool in its `tools:` allowlist. Review and other forge agents cannot query tickets themselves — any ticket content an agent needs (title, description, acceptance criteria, notes) must be inlined into its prompt by whichever skill dispatches it.

## Why it matters

A skill author who assumes an agent can call `ticket_show` or similar to look up context will ship an agent that silently has no ticket data, producing generic or wrong output with no error. `design-reviewer.md:22`'s explicit "Do NOT search for `.tickets/` files" instruction only makes sense once this constraint is known — without it, the instruction reads as an arbitrary restriction rather than a workaround for a real capability gap.

## How to verify

Run from the forge repo root:

```
grep -rn "mcp__plugin_forge_tk" agents/*.md
```

Expected: no matches. Confirm the workaround instruction is still present:

```
sed -n '20,24p' agents/design-reviewer.md
```