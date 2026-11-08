---
id: forge-pipeline-stage-routing-by-ticket-type
title: Forge pipeline gives tasks all 8 stages (incl. spec/design); only bug tickets skip them
scope: forge
type: truth
status: candidate
evidence:
  - path: src/tk/pipelines.ts
    note: >
      Defines actual stage routing — tasks receive all 8 stages including
      spec and design, mirroring features, at every risk level. Bug-type
      tickets go directly backlog → triage → implement.
  - path: using-forge/SKILL.md
    note: >
      "Pipeline Rules" section incorrectly states "Bugs and tasks skip spec
      and design" — stale relative to pipelines.ts.
sources:
  - session: e2ddfd9c-2736-4a53-b061-e27d77fb67e9
    project: forge
    date: 2026-04-08
    role: >
      Session found the documented pipeline rule (bugs and tasks both skip
      spec/design) contradicted the actual routing logic in pipelines.ts
      (only bugs skip), and expanded a sweep ticket's scope to fix the doc.
related: []
contradicts:
  - path: using-forge/SKILL.md
    note: Pipeline Rules section states tasks skip spec/design; this is false per pipelines.ts.
verified_at: 2026-08-15
status: candidate
extracted_at: 2026-08-15T10:55:13
extracted_by: claude:sonnet
---

## Claim

In the forge pipeline (`src/tk/pipelines.ts`), only `bug`-type tickets skip the spec and design stages (backlog → triage → implement). `task`-type tickets go through all 8 stages, identical to `feature`-type tickets, regardless of risk level.

## Why it matters

An agent relying on `using-forge/SKILL.md`'s stated rule will wrongly expect task tickets to skip spec/design, leading it to either misroute a task ticket or misdiagnose why a task ticket is stuck at the spec stage as a bug rather than expected behavior.

## How to verify

Run from the forge repo root:

```
grep -n "bug\|task\|feature" src/tk/pipelines.ts
grep -n "skip spec and design" using-forge/SKILL.md
```

Compare the stage list defined per ticket type in `pipelines.ts` against the prose in `using-forge/SKILL.md`; if they still disagree, the doc is stale.

## Notes

Doc fix was folded into ticket `forge/sweep-stale-triage-f25b` on the reasoning that it shares the same root file and routing-fix context (commit `c97d`) as that ticket's original scope.