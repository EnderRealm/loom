---
id: ticket-store-interface-five-methods
title: Ticket Store interface defines exactly 5 methods; filesystem ops are not part of the contract
scope: ticket
type: truth
status: validated
evidence:
  - path: pkg/ticket/store.go
    line: 11-17
    note: "type Store interface { Get, List, Create, Update, Delete }"
  - path: pkg/ticket/store.go
    line: 19
    note: "var _ Store = (*FileStore)(nil)" — compile-time implementation check
  - path: pkg/ticket/store.go
    line: 22-24
    note: "type FileStore struct { Dir string }" — single-field struct
sources:
  - session: 91d979db-8c94-4f38-999b-90b028c5b543
    project: ticket
    date: 2026-03-25
    role: discovered during Store interface extraction from FileStore for MultiStore support
verified_at: 2026-04-09
related:
  - multistore-namespaced-routing.md
---

## Claim

The `Store` interface in `pkg/ticket/store.go` defines exactly five methods: `Get(id) (*Ticket, error)`, `List() ([]*Ticket, error)`, `Create(*Ticket) error`, `Update(*Ticket) error`, `Delete(id) error`. Filesystem-specific operations (directory resolution, `.tickets/` discovery, `EnsureDir`) are implementation details of `FileStore`, not part of the `Store` contract. Any new backend (database, remote, in-memory) needs to implement only these five methods.

## Why it matters

`FileStore` is a single-field struct (`Dir string`). Creating new instances per call has negligible overhead — this is the pattern used in `MultiStore.storeFor()` and in the `registerCreate` MCP handler for cross-repo ticket creation via the `repo` parameter. Understanding the Store boundary prevents bloating the interface with filesystem concerns that don't generalize.

## How to verify

Run from the ticket repo root:

```
sed -n '11,17p' pkg/ticket/store.go
```

Expected: exactly 5 method signatures inside `type Store interface {}`.

```
grep -c "func (f \*FileStore)" pkg/ticket/store.go
```

Count should exceed 5 — FileStore has additional methods beyond the Store contract (e.g. Resolve, path helpers).
