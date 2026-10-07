---
id: ticket-create-repo-param-central-store
title: ticket_create repo parameter fails for central-store projects
scope: ticket
type: ticket
destination: ticket
ticket_type: bug
status: candidate
evidence:
  - path: mcp.go
    note: "registerCreate resolves the repo parameter by walking up looking for .tickets/, which central-store projects do not have"
sources:
  - session: 91d979db-8c94-4f38-999b-90b028c5b543
    project: ticket
    date: 2026-03-25
    role: ticket_create with repo targeting a central-store project filed the ticket in the wrong repo
verified_at: 2026-10-07
status: candidate
extracted_at: 2026-10-07T11:05:43
extracted_by: claude:sonnet
---

## Problem

`ticket_create` with the `repo` parameter locates the target store by walking up from the repo path looking for a `.tickets/` directory. Central-store projects have no such directory, so the lookup fails. The ticket `auto-convert-tasks-fb27` was created in the ticket repo instead of forge. `repo` should resolve central-store projects, or the call should return an error rather than file the ticket somewhere else.

## Impact

Cross-repo ticket creation is unreliable for central-store projects. Tickets can land in the wrong project without any error.

## How to reproduce

Call `ticket_create` with `repo` set to a project whose tickets live in a central store (no `.tickets/` in its tree). The ticket does not appear in that project's store.