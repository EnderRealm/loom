---
id: forge-effort-max-api-only
title: Claude Agent SDK effort "max" is API-only; subscribers max at "high"
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

The Claude Agent SDK's `effort: "max"` parameter is only available for API users. Claude.ai subscribers are capped at "high". Passing "max" for a subscriber causes the SDK child process to exit with code 1. The forge orchestrator clamps effort to "high" at the dispatch layer.

## How to verify

Run from the forge repo root:

```
grep -n "effort" src/server/services/orchestrator.ts
```

Look for clamping logic that caps the effort parameter to "high" before dispatching to the Agent SDK.
