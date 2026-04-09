---
id: forge-human-stages-block-auto-advance
title: Forge orchestrator stops auto-advance at requiresHuman stages; hands off to human
scope: forge
type: truth
status: validated
evidence:
  - path: src/server/services/orchestrator.ts
    line: 121
    note: requiresHuman boolean in AgentStageConfig interface
  - path: src/server/services/orchestrator.ts
    line: 127-136
    note: AGENT_CONFIGS — backlog, triage, verify are requiresHuman:true; spec through test are false
  - path: src/server/services/orchestrator.ts
    line: 894-896
    note: "if (config.requiresHuman && run.auto) return { result: 'needs_handoff' }" — this is the actual stop condition
sources:
  - session: b9b4c0be-4180-40ec-b1e3-b0dc77b0669b
    project: forge
    date: 2026-03-14
    role: discovered during backlog stage implementation
verified_at: 2026-04-09
related:
  - review-gates-require-fresh-approval.md
---

## Claim

Three forge pipeline stages are marked `requiresHuman: true` in the orchestrator's agent config: `backlog`, `triage`, and `verify`. When the orchestrator reaches one of these stages in auto mode, it returns `needs_handoff` instead of running an agent, stopping the auto-advance loop. The human must take action before orchestration can continue.

Stages without `requiresHuman` (spec, design, design-review, implement, code-review, test) have agent generators or gate agents that run automatically.

## Why it matters

A session that kicks off auto-orchestration will run through spec → design → implement → test unattended, but will **stop** at verify and hand off. This is intentional: verify is the human sign-off point. If a workflow expects fully-automatic end-to-end orchestration, it will stall at the first human stage with no error — just a handoff event. The handoff is silent unless the session is watching events.

Similarly, backlog and triage require human judgment. A ticket created via `/idea` (landing in backlog) cannot be auto-advanced to spec — it needs `/triage` first.

## How to verify

Run from the forge repo root:

```
grep -n "requiresHuman" src/server/services/orchestrator.ts
```

Expected: the `AgentStageConfig` interface definition, the AGENT_CONFIGS array with true/false per stage, and the guard in `runStage` that returns `needs_handoff`.

```
grep -n "requiresHuman: true" src/server/services/orchestrator.ts
```

Expected: backlog, triage, verify.

## Notes

- The `requiresHuman` flag and the `review_approved` gate (see `review-gates-require-fresh-approval.md`) are independent mechanisms. A stage can be `requiresHuman: false` and still have a review gate (e.g. `design-review`). The gate blocks advancement until review approves; `requiresHuman` blocks advancement until a human acts. Both can block; they use different state.
- Pipeline stages represent completion state: a ticket AT "triage" means triage is done, not pending. This is implicit in how `ticket_advance` works — it moves the ticket OUT of the completed stage.
