---
id: forge-advance-button-always-targets-done
title: Orchestrator Advance button sends targetStage "done"; single-stage is enforced internally
scope: forge
type: truth
status: validated
sources:
  - session: 0916c546
    project: forge
    date: 2026-03-12
verified_at: 2026-04-09
---

## Claim

The dashboard's Advance button always sends `targetStage: "done"` with `auto: false`. Single-stage advancement is enforced by the orchestrator internally, not by the UI target. Stage transition labels must be derived from `advancing` events and `resolvedStage`, not from `targetStage`.

## How to verify

Run from the forge repo root:

```
grep -n "targetStage.*done\|effectiveTarget\|resolvedStage" src/server/services/orchestrator.ts src/client/components/RunQueue.tsx
```

The UI component should show `targetStage: "done"` in its advance call. The orchestrator should show internal logic that resolves to a single-stage advance regardless of the target.
