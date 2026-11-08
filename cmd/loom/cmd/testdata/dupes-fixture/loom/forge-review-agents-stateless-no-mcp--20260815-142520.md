---
id: forge-review-agents-stateless-no-mcp
title: Forge review/spec subagents have no ticket-reading tools by design
scope: forge
type: truth
status: candidate
evidence:
  - path: design-reviewer.md
    line: 22
    note: "explicitly states: \"Do NOT search for `.tickets/` files or try to read ticket data from the filesystem — ticket files may not exist in your working directory.\""
  - path: spec-builder.md
    line: 23
    note: uses "You will receive [content] in your prompt" phrasing
  - path: impl-reviewer.md
    line: 17
    note: same stateless-prompt-input pattern
  - path: code-reviewer.md
    line: 17
    note: same stateless-prompt-input pattern
  - path: plan-reviewer.md
    line: 17
    note: same stateless-prompt-input pattern
  - path: test-runner.md
    line: 17
    note: same stateless-prompt-input pattern
sources:
  - session: 6c964191-afba-41d3-9643-36d548994734
    project: forge
    date: 2026-04-08
    role: Initially misdiagnosed a review-dispatch failure as "agents lack MCP tools" and planned to add tool grants; corrected after reading the agent files directly, which forbid filesystem/ticket lookups by design.
related: []
contradicts: []
verified_at: 2026-08-15
status: candidate
extracted_at: 2026-08-15T14:25:20
extracted_by: claude:sonnet
---

## Claim

Forge's review and spec subagents (`spec-builder`, `design-reviewer`, `impl-reviewer`, `code-reviewer`, `plan-reviewer`, `test-runner`) are intentionally stateless text processors with no `tk` MCP tool access and no filesystem access to ticket data. They are designed to operate only on content inlined into their dispatch prompt by the calling skill.

## Why it matters

If a subagent returns "UNABLE TO REVIEW" or otherwise fails to find ticket context, the correct fix is almost never granting it MCP tools or filesystem access — that would contradict the deliberate design and introduce non-determinism (different agents seeing different live ticket state at review time vs. spec time). The correct fix is auditing whether the dispatching skill inlined the required fields into the prompt.

## How to verify

Run from the forge repo root:

```
find . -iname "design-reviewer.md" -o -iname "spec-builder.md" -o -iname "impl-reviewer.md" -o -iname "code-reviewer.md" -o -iname "plan-reviewer.md" -o -iname "test-runner.md"
grep -n "Do NOT search for\|You will receive\|ticket files may not exist" $(find . -iname "design-reviewer.md")
grep -rln "mcp__plugin_forge_tk\|mcp__tk__" $(find . -iname "*-reviewer.md" -o -iname "spec-builder.md" -o -iname "test-runner.md")
```

The last grep should return no matches — confirming none of these agent definitions are granted `tk` MCP tools.