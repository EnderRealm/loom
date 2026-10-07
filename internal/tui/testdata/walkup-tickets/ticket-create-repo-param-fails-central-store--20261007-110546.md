---
id: ticket-create-repo-param-fails-central-store
title: ticket_create `repo` parameter fails for projects in a central store
scope: ticket
type: ticket
destination: ticket
ticket_type: bug
status: candidate
evidence:
  - path: mcp.go
    note: registerCreate handler resolves the `repo` parameter by walking up from the repo path looking for `.tickets/`
sources:
  - session: 91d979db-8c94-4f38-999b-90b028c5b543
    project: ticket
    date: 2026-03-25
    role: ticket_create with `repo` targeting a central-store project filed the ticket in the wrong repo
verified_at: 2026-10-07
status: candidate
extracted_at: 2026-10-07T11:05:46
extracted_by: claude:sonnet
---

## Problem

`ticket_create` with the `repo` parameter locates the target store by walking up the directory tree looking for `.tickets/`. Projects served from a central store have no `.tickets/` directory, so the lookup fails. The session's ticket `auto-convert-tasks-fb27` was meant for the forge repo and was created in the ticket repo instead. `ticket_create` should resolve the target store for central-store projects, or return an error rather than filing the ticket in the wrong repo.

## Impact

Cross-repo ticket creation is unreliable for central-store projects, and tickets can end up in the wrong project. This also affects the multi-project serving work (`support-multi-project-791b`).

## How to reproduce

Call `ticket_create` with `repo` set to a project that lives only in the central store (no `.tickets/` directory). Observe the failure or misplaced ticket.