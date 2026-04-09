---
id: forge-run-store-frontmatter-json
title: Orchestration runs persist as frontmatter+JSON markdown; active statuses map to terminal on reload
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

`run-store.ts` persists orchestration runs as frontmatter+JSON markdown files. On reload, any run with an active status (running, pending) is mapped to a terminal status — "idle" runs with successful dispatches become "completed", others become "failed". This prevents zombie runs after crashes.

## How to verify

Run from the forge repo root:

```
grep -n "idle\|completed\|failed\|loadRun" src/server/services/run-store.ts
```

Look for the reload path that maps active statuses to terminal statuses, with special handling for "idle" runs that had successful dispatches.
