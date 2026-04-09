---
id: ticket-mcp-in-process-testing
title: Go MCP servers tested in-process via go-sdk NewInMemoryTransports
scope: ticket
type: truth
status: validated
sources:
  - session: 5bc59345
    project: ticket
    date: 2026-03-12
  - session: 2e9a386c
    project: ticket
    date: 2026-02-27
verified_at: 2026-04-09
---

## Claim

The Go MCP SDK provides `NewInMemoryTransports` to create a client/server pair for in-process testing without stdio framing overhead. This is the established pattern for tk MCP server tests — avoids replacing the installed binary and preserves service availability for other consumers.

## How to verify

Run from the ticket repo root:

```
grep -rn "NewInMemoryTransports" .
```

Look for test files that create in-memory transport pairs for MCP server testing.
