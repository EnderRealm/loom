---
id: ticket-task-pipeline-no-implement-stage
title: Task-type tickets have no implement/test stage in any risk-level pipeline
scope: ticket
type: truth
status: candidate
evidence:
  - note: Pipeline configuration for ticket type "task" defines the stage sequence backlog → triage → done at every risk level; no implement or test stage exists for this type, unlike feature/bug pipelines.
sources:
  - session: cb3fe3ba-627d-40f5-8c0c-19f6d0d07072
    project: ticket
    date: 2026-03-25
    role: Coding work filed as a task ticket stalled at triage because the pipeline had nowhere to route implementation; workaround was converting the ticket type to feature, and a follow-up ticket (auto-convert-tasks-fb27) was filed to automate that conversion rather than add stages to the task pipeline.
related: []
contradicts: []
verified_at: 2026-08-15
status: candidate
extracted_at: 2026-08-15T08:43:08
extracted_by: claude:sonnet
---

## Claim

In tk's pipeline configuration, tickets of type `task` follow a fixed `backlog → triage → done` stage sequence at every risk level. There is no `implement` or `test` stage for this type — it was designed for non-coding work (docs, process changes), not code changes.

## Why it matters

Any agent or workflow that files coding work as a `task`-type ticket will see it stall at `triage` with nowhere to advance to implementation. This is not a bug in the specific ticket — it is a structural gap in the type's pipeline definition. Until the pipeline itself is changed (tracked separately in `auto-convert-tasks-fb27`), coding work must be filed as `feature` or `bug`, or converted from `task` during triage.

## How to verify

Run from the ticket repo root (`~/code/ticket`):

```
grep -rn "task" --include="*.go" -i . | grep -i "pipeline\|stage"
grep -rn "backlog\|triage\|implement" --include="*.go" -i .
```

Or inspect behavior directly: create a `task`-type ticket, advance it through `triage`, and confirm `ticket_advance`/`tk audit` shows no `implement` or `test` stage available regardless of the ticket's risk level.

## Notes

The follow-up ticket `auto-convert-tasks-fb27` addresses this by auto-converting task tickets to `feature` during triage — it does not add stages to the task pipeline itself. This truth survives that fix: the task pipeline's stage list remains `backlog → triage → done`.