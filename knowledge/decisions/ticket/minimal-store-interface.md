---
id: ticket-minimal-store-interface
title: Define Store interface as 5 methods; keep filesystem ops on FileStore only
scope: ticket
type: decision
status: validated
tag: auto
sources:
  - session: 91d979db-8c94-4f38-999b-90b028c5b543
    project: ticket
    date: 2026-03-25
    role: decided during Store interface extraction from FileStore
related:
  - store-interface-five-methods.md
recorded_at: 2026-04-09
---

## Choice

Store interface defines exactly Get, List, Create, Update, Delete. Filesystem-specific methods (Resolve, EnsureDir, MoveTicket) stay on `*FileStore` only.

## Alternatives

Include Resolve and EnsureDir on the interface. Would force non-filesystem backends (MultiStore, future database store) to implement filesystem concepts.

## Rationale

Code reviewer identified that Resolve and EnsureDir are filesystem implementation details. The interface should represent the minimal contract all backends share. FileStore callers (CLI, TUI) keep concrete `*FileStore` references and can access filesystem methods directly.

## Principle

Interface contracts should be the minimum that all backends can implement. Implementation-specific methods belong on the concrete type, not the interface.
