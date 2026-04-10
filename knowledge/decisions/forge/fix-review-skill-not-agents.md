---
id: forge-fix-review-skill-not-agents
title: Fix /review skill prompt dispatch instead of adding MCP tools to agents
scope: forge
type: decision
status: validated
tag: auto
sources:
  - session: 88f1615b-69cc-4330-9c8b-3affd4494229
    project: forge
    date: 2026-04-08
    role: reversed during implementation of review subagent fix
related:
  - review-agents-stateless.md
recorded_at: 2026-04-09
---

## Choice

Fixed the `/review` skill to inline ticket content into agent prompts, rather than adding `mcp__plugin_forge_tk__ticket_show` to each review agent's tool allowlist. The skill was the broken component — it said "pass the ticket ID and criteria" but didn't specify HOW, leaving dispatchers to pass just the ID string.

## Alternatives

Option A (add MCP tools to agents) was initially chosen during triage — faster, 6-file touch, trivially verifiable. Reversed after reading agent source files and finding `design-reviewer.md`'s explicit "do NOT search for .tickets/ files" instruction.

## Rationale

Agents are designed as stateless text processors. Adding MCP tools would directly contradict `design-reviewer`'s instruction, couple agent behavior to environment state, and inject non-determinism into review paths. The triage's "Option B for purity, Option A for speed" framing was a false dichotomy — Option A wasn't just less pure, it was architecturally incorrect.

## Principle

When triage frames a choice as "pure vs fast," verify the fast path doesn't violate the component's explicit design contract. Read the source files before committing to a triage recommendation.
