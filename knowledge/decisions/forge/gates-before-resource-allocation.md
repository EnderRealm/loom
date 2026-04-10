---
id: forge-gates-before-resource-allocation
title: Place admission gates in startOrchestration() before worktree/run creation
scope: forge
type: decision
status: validated
tag: auto
sources:
  - session: b9b4c0be-4180-40ec-b1e3-b0dc77b0669b
    project: forge
    date: 2026-03-14
    role: code reviewer caught worktree leak from original placement
related:
  - backlog-blocks-orchestration.md
recorded_at: 2026-04-09
---

## Choice

Moved the backlog admission gate into `startOrchestration()` before any run record or worktree is created. Originally placed in `orchestrate()` after run setup.

## Alternatives

Keep the gate in `orchestrate()` and add cleanup for rejected runs — more complex, still leaks resources on the error path.

## Rationale

Code reviewer identified that the original placement caused worktree leaks: a run record and worktree were created, then the gate rejected the ticket, but no cleanup ran. Moving the gate earlier means rejected tickets never allocate resources.

## Principle

Admission gates must run before resource allocation. A gate that fires after resources are allocated is a cleanup problem, not a gate.
