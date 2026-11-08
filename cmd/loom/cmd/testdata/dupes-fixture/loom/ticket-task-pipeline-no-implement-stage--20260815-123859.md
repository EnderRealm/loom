---
id: ticket-task-pipeline-no-implement-stage
title: task-type tickets follow backlog→triage→done with no coding stages at any risk tier
scope: ticket
type: truth
status: candidate
evidence:
  - path: internal/tk/pipelines.go
    note: "pipeline definition table for the 'task' ticket type; confirm via grep -rn '\"task\"' --include='*.go'"
sources:
  - session: a220bca2-256d-476a-9c91-fde91425ad68
    project: ticket
    date: 2026-03-25
    role: A coding-intent ticket was filed as type `task` and dead-ended before reaching any implement stage, surfacing that the task pipeline has no coding stages at any risk level.
related: []
contradicts: []
verified_at: 2026-08-15
status: candidate
extracted_at: 2026-08-15T12:38:59
extracted_by: claude:sonnet
---

## Claim

`tk` task-type tickets follow the pipeline `backlog → triage → done` at every risk level (default/low/normal/high/critical). There is no implement, review, test, or verify stage for this type — only `feature` and other coding-oriented types carry those stages.

## Why it matters

Filing coding work as a `task` ticket silently strands it: the ticket can be triaged and then only advances straight to `done`, with no gate ever requiring code to be written, reviewed, or tested. An agent or human unaware of this will file coding work under the wrong type and either lose track of it or mark it done without any implementation happening.

## How to verify

Run from the ticket repo root:

```
grep -rn '"task"' --include="*.go" internal/tk/
grep -n -B2 -A20 "task" internal/tk/pipelines.go
```

Confirm the stage list for the `task` type contains only `backlog`, `triage`, `done` (and `closed`) with no `implement`/`review`/`test`/`verify` entries at any risk tier.

## Notes

Workaround in use: convert task-type tickets to `feature` type when they require code changes. A follow-up ticket (`auto-convert-tasks-fb27`) was filed to automate this conversion at triage/spec/design time, but was itself created in the wrong repo due to a separate bug (see `ticket-create-repo-param-local-store-only`).