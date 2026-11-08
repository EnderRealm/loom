---
id: ticket-task-pipeline-no-implement-stage
title: task-type ticket pipeline is backlog→triage→done with no coding stages
scope: ticket
type: truth
status: candidate
evidence:
  - path: store.go
    note: pipeline stage configuration per ticket type; task pipeline terminates at done immediately after triage, with no implement/review/test/verify stages present at any risk level
sources:
  - session: dc303a88-0f65-453a-848c-c4287e710a63
    project: ticket
    date: 2026-03-25
    role: session hit this while trying to drive coding work through a task-type ticket and had to work around it by converting the ticket to feature type
related: []
contradicts: []
verified_at: 2026-08-15
status: candidate
extracted_at: 2026-08-15T11:09:28
extracted_by: claude:sonnet
---

## Claim

The `task` ticket type's pipeline is `backlog → triage → done` at every risk level. Unlike `feature` and `bug` pipelines, it has no `implement`, `review`, `test`, or `verify` stages.

## Why it matters

Any coding work filed as a `task` ticket has no stage to host implementation, review, or test — `ticket_advance` will walk it straight to `done` without any of the standard gates. An agent driving a task ticket toward coding work must first convert its type to `feature` (or `bug`), or the work silently skips verification.

## How to verify

Run from the ticket repo root:

```
grep -n "task" store.go | grep -i pipeline
grep -n "backlog\|triage\|implement\|verify" store.go
```

Confirm the `task` pipeline definition lists only `backlog`, `triage`, `done` while `feature`/`bug` pipelines include intermediate coding stages.

## Notes

A per-ticket workaround (convert task → feature during triage) was applied in this session; a general fix was deferred to ticket `auto-convert-tasks-fb27`. This truth survives that fix landing, since the underlying pipeline shape is a config fact independent of whether auto-conversion exists.