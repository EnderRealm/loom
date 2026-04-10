---
id: ticket-auto-migrate-legacy-on-parse
title: Auto-migrate legacy tickets with status but no stage on Parse()
scope: ticket
type: decision
status: validated
tag: auto
sources:
  - session: 5bc59345
    project: ticket
    date: 2026-03-12
related: []
recorded_at: 2026-04-09
---

## Choice

`Parse()` transparently converts tickets that have a status field but no stage field using a `StatusToStage` mapping. The Go struct keeps the Status field for YAML parse compatibility but never writes it back via `Serialize()`.

## Rationale

Allows old ticket files to be read without error while ensuring all downstream logic works with stage. Migration is invisible to callers.
