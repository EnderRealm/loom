---
id: ticket-create-repo-param-fails-central-store
title: ticket_create repo parameter fails for central-store projects
scope: ticket
type: ticket
destination: ticket
ticket_type: bug
status: candidate
evidence:
  - path: pkg/ticket/mcp.go
    note: "registerCreate handler builds a separate FileStore for the repo parameter by walking up looking for .tickets/ (file path inferred from the session's mcp.go; confirm with grep -rn registerCreate)"
sources:
  - session: 91d979db-8c94-4f38-999b-90b028c5b543
    project: ticket
    date: 2026-03-25
    role: ticket_create with repo targeting a central-store project created the ticket in the wrong repo
verified_at: 2026-10-07
status: candidate
extracted_at: 2026-10-07T11:05:47
extracted_by: claude:sonnet
---

## Problem

`ticket_create` with the `repo` parameter locates the target store by walking up from the repo path looking for `.tickets/`. Central-store projects have no `.tickets/` directory, so the lookup does not find their store. The session's ticket `auto-convert-tasks-fb27` was meant for the forge project but was created in the ticket repo instead. The tool should resolve the central store for the named project, or return an error rather than filing elsewhere.

## Impact

Tickets for central-store projects can be filed against the wrong project without any error, so they are lost from the intended project's backlog.

## How to reproduce

Call `ticket_create` with `repo` set to a project whose tickets live in a central store (no `.tickets/` in the repo). Observe that the ticket lands in the calling repo's store, not the target project's.