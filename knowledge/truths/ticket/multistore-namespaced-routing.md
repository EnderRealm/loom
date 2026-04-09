---
id: ticket-multistore-namespaced-routing
title: MultiStore routes by namespaced ID (project/ticket-id); bare IDs search all projects
scope: ticket
type: truth
status: validated
evidence:
  - path: pkg/ticket/multistore.go
    line: 15-17
    note: "type MultiStore struct { rootDir string }" — discovers projects as subdirs
  - path: pkg/ticket/multistore.go
    line: 27-32
    note: "func (m *MultiStore) Get(id)" — splits on ParseNamespacedID, routes to project store
sources:
  - session: 91d979db-8c94-4f38-999b-90b028c5b543
    project: ticket
    date: 2026-03-25
    role: implemented during multi-project MCP serving design
verified_at: 2026-04-09
related:
  - store-interface-five-methods.md
---

## Claim

`MultiStore` routes operations by parsing the ticket ID into `project/ticket-id` form via `ParseNamespacedID`. When a project prefix is present, the operation targets that project's `FileStore` directly. When the ID is bare (no prefix), MultiStore searches all project stores: it returns the unique match or errors when the ID is ambiguous across projects.

`MultiStore.Create` requires a namespaced ID — a bare-ID create would be ambiguous about which project store to write to.

## Why it matters

Any MCP tool handler or CLI command that takes a ticket ID must decide whether to accept bare IDs (convenience, but ambiguous in multi-project setups) or require namespaced IDs (explicit, but verbose). The current design accepts both for reads and requires namespacing for writes. Callers that assume all IDs are project-scoped will break in single-project mode; callers that assume bare IDs are unambiguous will break in multi-project mode.

## How to verify

Run from the ticket repo root:

```
grep -n "ParseNamespacedID\|func.*MultiStore" pkg/ticket/multistore.go | head -15
```

Expected: Get, List, Create, Update, Delete methods that all call ParseNamespacedID early in the function body.

```
grep -n "func ParseNamespacedID" pkg/ticket/id.go
```

Expected: a function that splits on `/` and returns (project, ticketID).
