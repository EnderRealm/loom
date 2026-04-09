---
id: ticket-type-dependent-pipelines
title: tk pipelines are type-dependent; chore has 3 stages, feature has 7
scope: ticket
type: truth
status: validated
sources:
  - session: 5bc59345
    project: ticket
    date: 2026-03-12
verified_at: 2026-04-09
---

## Claim

The tk ticket system uses type-dependent pipeline definitions. Chore-type tickets follow a 3-stage pipeline (`triage -> implement -> done`), while feature-type tickets use all 7 stages. `StageIndex()` returns -1 for stages not in a type's pipeline, which affects sort ordering — sort tests must use valid stages per ticket type.

## How to verify

Run from the ticket repo root:

```
grep -rn "StageIndex\|pipeline.*chore\|pipeline.*feature" .
```

Look for pipeline definitions per ticket type and the StageIndex function that returns -1 for invalid stages.
