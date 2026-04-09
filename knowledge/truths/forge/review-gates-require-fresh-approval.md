---
id: forge-review-gates-require-fresh-approval
title: Forge review_approved gates need a fresh ticket_review call at each stage; stale approvals don't carry
scope: forge
type: truth
status: validated
evidence:
  - path: src/tk/pipelines.ts
    line: 92-101
    note: 7 transitions carry a structural "review_approved" gate (spec>design, design>implement, design-review>implement, code-review>test, verify>done, design>done, and others)
  - path: src/tk/gates.ts
    line: 48-51
    note: checkReviewApproved — single-field check, t.review === "approved" or rejection
  - path: src/tk/workflow.ts
    line: 89
    note: t.review is cleared to "" when the ticket advances into a new stage — prior approvals do not carry over
  - path: src/tk/workflow.ts
    line: 139
    note: second clearing site, same rule
  - path: src/tk/workflow.ts
    line: 166
    note: t.review is set only by the review() function, which is wired to the ticket_review MCP tool
sources:
  - session: 88f1615b-69cc-4330-9c8b-3affd4494229
    project: forge
    date: 2026-04-08
    role: discovered during /spec skill dead-end investigation — /spec called ticket_advance without invoking /review, failed spec>design gate silently
verified_at: 2026-04-08
related:
  - review-agents-stateless.md
---

## Claim

Seven forge pipeline transitions carry a structural `review_approved` gate: `spec>design`, `design>implement`, `design-review>implement`, `code-review>test`, `verify>done`, `design>done`, and `backlog>triage` (indirectly via `priority_set`/`risk_set`). Each gate reads `t.review` and requires it to equal `"approved"`. Because `t.review` is cleared to `""` whenever the ticket enters a new stage, every gated transition requires a **fresh** `ticket_review` approve call at that stage. Approvals do not carry over from prior stages. A skill that calls `ticket_advance` across one of these transitions without first calling `ticket_review` (directly or via a `/review` dispatch) will be rejected at the gate with no side-effect.

## Why it matters

This is exactly how the `/spec` skill dead-ended the pipeline during the 2026-04-08 session: it called `ticket_advance` from `spec` to `design` without triggering any review, and the gate refused the transition. From the skill's perspective the spec stage looked "done"; from the pipeline's perspective the ticket was stuck at `spec` with no error emitted. Downstream work (design, implementation) could not begin.

Any skill authoring `ticket_advance` across a gated transition must do one of:

1. Call `/review` before `ticket_advance` and wait for the approval to land.
2. Call `ticket_review` directly with an agent-driven verdict.
3. Return control to the human with explicit instructions to run `/review`, then continue.

Doing nothing = silent dead-end. The pipeline will not auto-escalate, auto-approve, or emit a warning. The ticket simply stays where it is.

## How to verify

Run from the forge repo root.

Count gated transitions in the pipeline definition:

```
grep -n "review_approved" src/tk/pipelines.ts
```

Expected: multiple hits inside the `gates:` block, plus definition lines in the labels section.

Inspect the gate check function:

```
sed -n '48,51p' src/tk/gates.ts
```

Expected: a single-line check of `t.review !== "approved"`.

Confirm approval clearing on stage transition:

```
grep -n 't\.review = ""' src/tk/workflow.ts
```

Expected: at least one hit inside the advance/transition code path.

Confirm the only writer of `t.review = verdict` is the review function:

```
grep -n 't\.review = ' src/tk/workflow.ts
```

Expected: 2-3 hits — the clearings and one assignment from a `verdict` parameter.

## Notes

- A **second family** of review gates uses different fields: `code_review_approved` and `impl_review_approved`. These scan the historical `t.reviews` array for reviewer identities matching `"code-review"` or `"impl-review"`, so they survive stage transitions (see `src/tk/gates.ts:53-65`). Don't confuse these with the plain `review_approved` gate — they have different semantics and different ways to satisfy them.
- Agents cannot call `ticket_review` directly — no review agent has `mcp__plugin_forge_tk__*` tools in its allowlist. Only the dispatching skill (running in the main session) can call `ticket_review`. This pairs with `review-agents-stateless.md`: agents produce verdict text, skills convert that text into MCP calls.
- A clumsy-but-safe pattern when the skill isn't sure whether approval has landed: call `ticket_advance`, catch the gate rejection, prompt the user to `/review`, then retry. Not elegant, but it avoids silent dead-ends.
- The `t.review` field is per-stage state, not per-ticket history. If you need to know whether *any* review has approved a ticket at any point, check `t.reviews[]` not `t.review`.
