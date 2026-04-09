---
id: ticket-cli-vs-mcp-init-divergence
title: CLI sets ID/Status/Stage before store.Create but MCP handler doesn't — initialization divergence
scope: ticket
type: truth
status: validated
sources:
  - session: 2e9a386c
    project: ticket
    date: 2026-02-27
verified_at: 2026-04-09
---

## Claim

The CLI `create` command sets ticket ID, Status, and Stage fields before calling `store.Create()`. The MCP `ticket_create` handler passes the ticket to `store.Create()` without setting these fields, relying on downstream validation to catch it. This initialization divergence means tickets created via MCP may fail validation that CLI-created tickets pass.

## How to verify

Run from the ticket repo root. Compare the two create paths:

```
grep -n "store.Create\|ID\|Status\|Stage" cmd/create.go
```

```
grep -n "store.Create\|ID\|Status\|Stage" mcp.go serve.go
```

The CLI path should set ID, Status, and Stage before the store call. The MCP handler should be missing one or more of those assignments.
