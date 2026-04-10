---
id: forge-sync-per-process-lifetime
title: Run commit sync loop inside each writer process lifetime, no standalone daemon
scope: forge
type: decision
status: validated
tag: human
sources:
  - session: c7dbbcca
    project: forge
    date: 2026-04-07
related: []
recorded_at: 2026-04-09
---

## Choice

The tk commit journal runs its sync loop inside each process that writes tickets (Hono server, MCP stdio, future CLI). No standalone sync daemon.

## Alternatives

Standalone sync daemon that watches for changes. Would add operational complexity (another service to manage) without benefit since each writer already knows when it writes.

## Rationale

Mirrors the Go `cmd/serve.go` architecture exactly — goroutine with 5s ticker, dies when process dies. Each writer owns its own sync lifecycle.
