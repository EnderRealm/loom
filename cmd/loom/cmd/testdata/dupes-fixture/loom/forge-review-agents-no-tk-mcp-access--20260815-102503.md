---
id: forge-review-agents-no-tk-mcp-access
title: Forge review agents carry no tk MCP tools; ticket data must be inlined into their prompts
scope: forge
type: truth
status: candidate
evidence:
  - path: agents/design-reviewer.md
    line: 22
    note: "Explicit instruction in the agent body: \"All ticket data is provided in the prompt. Do NOT search for `.tickets/` files.\""
sources:
  - session: f692d941-0280-493e-a164-78eb756d0d02
    project: forge
    date: 2026-04-08
    role: Session confirmed across all six forge review agents (spec-builder, design-reviewer, impl-reviewer, code-reviewer, plan-reviewer, test-runner) that none carry mcp__plugin_forge_tk__* tools.
related:
  - forge-review-agents-stateless
contradicts: []
verified_at: 2026-08-15
status: candidate
extracted_at: 2026-08-15T10:25:03
extracted_by: claude:sonnet
---

## Claim

None of forge's six review agents (`spec-builder`, `design-reviewer`, `impl-reviewer`, `code-reviewer`, `plan-reviewer`, `test-runner`) have `mcp__plugin_forge_tk__*` tools in their allowlists. They cannot fetch ticket data themselves — the dispatching skill must inline all relevant ticket content into the agent's prompt before invocation.

## Why it matters

A skill author who forgets to inline ticket content will produce a review agent that either hallucinates ticket state or fails outright — this is not a bug in the agent, it's the intended stateless design. Debugging "the reviewer doesn't know about X" should start by checking what the dispatching skill inlined into the prompt, not by adding MCP tools to the agent.

## How to verify

Run from the forge repo root:

```
grep -n "^tools:" agents/spec-builder.md agents/design-reviewer.md agents/impl-reviewer.md agents/code-reviewer.md agents/plan-reviewer.md agents/test-runner.md
```

Confirm none list `mcp__plugin_forge_tk__*` entries. Then check the dispatching skill for inlining:

```
grep -rn "ticket_show\|inline" skills/*/SKILL.md
```

## Notes

`spec-builder.md` references `mcp__forge_signals__report_verdict` — a *different* MCP server (`signals`, not `tk`) used only to report a verdict after the agent runs, not to read ticket data beforehand. This is a post-hoc reporting hook, not an exception to the statelessness claim.