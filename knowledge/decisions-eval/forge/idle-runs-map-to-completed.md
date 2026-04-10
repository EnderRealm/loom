---
id: forge-idle-runs-map-to-completed
title: Map idle runs with successful dispatches to completed on reload, not failed
scope: forge
type: decision
status: validated
tag: auto
sources:
  - session: 0916c546
    project: forge
    date: 2026-03-12
related: []
recorded_at: 2026-04-09
---

## Choice

`run-store.ts` maps "idle" runs to "completed" (not "failed") on disk reload when they have successful dispatch records.

## Rationale

Idle is a valid terminal state for manual-mode runs (auto=false). Mapping to "failed" misrepresents the outcome and creates false alarms in dashboard history.
