---
id: ticket-task-pipeline-no-implement-stage
title: Task-type tickets in `ticket` follow backlog→triage→done with no implement/test stage
scope: ticket
type: truth
status: candidate
evidence:
  - note: >-
      Source session Discoveries bullet: "Task pipeline configuration:
      backlog → triage → done at all risk levels — no implement/test stages
      for task type tickets."
sources:
  - session: ace9a39d-4f15-4163-81d7-df3fcdb92f42
    project: ticket
    date: 2026-03-25
    role: >-
      Three child tasks under a multi-project epic dead-ended at triage
      because no implement stage exists for the task type, forcing a
      manual retype to feature before implementation work could proceed.
related: [forge-pipeline-stages-by-type]
contradicts: []
verified_at: 2026-08-15
status: candidate
extracted_at: 2026-08-15T08:55:54
extracted_by: claude:sonnet
---

## Claim

In the `ticket` project's pipeline configuration, tickets of type `task` have exactly three reachable stages — `backlog → triage → done` — at every risk level. There is no `spec`, `design`, `implement`, `test`, `verify`, or review stage for this type, unlike `feature` tickets.

## Why it matters

Any coding work filed as a `task` will stall at `triage` with no forward path — an agent or human has to notice the stall and manually retype the ticket to `feature` before implementation can begin. Until this is fixed (or automated, per follow-up ticket `auto-convert-tasks-fb27`), filing coding work as `task` is a silent dead end, not an error.

## How to verify

Run from the `ticket` repo root:

```
grep -rn "triage" --include="*.go" .
grep -rn '"task"' --include="*.go" . | grep -i pipeline
```

Confirm the pipeline/stage table for type `task` lists only `backlog`, `triage`, `done` and that no `implement` or `test` stage appears for that type, in contrast to the `feature` type's stage list.

## Notes

Flagged during extraction as the conceptual opposite of the `forge` scope's `forge-pipeline-stages-by-type` truth, where task and feature pipelines mirror each other exactly — the two codebases diverged on this design point.