---
id: ticket-create-repo-param-central-store
title: ticket_create `repo` parameter fails for central-store projects
scope: ticket
type: ticket
destination: ticket
ticket_type: bug
status: candidate
evidence:
  - path: pkg/ticket/mcp.go
    note: registerCreate resolves the `repo` parameter by walking up looking for `.tickets/`, which central-store projects do not have
sources:
  - session: 91d979db-8c94-4f38-999b-90b028c5b543
    project: ticket
    date: 2026-03-25
    role: ticket_create with `repo` targeted a central-store project and the ticket landed in the wrong repo
verified_at: 2026-10-07
status: candidate
extracted_at: 2026-10-07T11:05:56
extracted_by: claude:sonnet
---

## Problem

`ticket_create` with the `repo` parameter locates the target store by walking up looking for a `.tickets/` directory. Central-store projects keep tickets elsewhere, so the lookup fails and the ticket is created in the wrong repo. It should resolve central-store projects, or return an error instead of filing elsewhere.

## Impact

Cross-repo ticket creation is unreliable for central-store projects. In the session, `auto-convert-tasks-fb27` was created in the ticket repo instead of the forge repo.

## How to reproduce

Call `ticket_create` with `repo` set to a project whose tickets live in the central store, with no `.tickets/` directory in the repo tree.