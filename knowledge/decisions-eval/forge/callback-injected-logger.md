---
id: forge-callback-injected-logger
title: Use callback-injected logger pattern for MCP stdio processes to avoid stdout corruption
scope: forge
type: decision
status: validated
tag: auto
sources:
  - session: c7dbbcca
    project: forge
    date: 2026-04-07
related: []
recorded_at: 2026-04-09
---

## Choice

MCP stdio servers use a callback-injected logger that routes diagnostic output to stderr or a file, never stdout.

## Rationale

`console.log` on MCP stdio transport writes to the same stdout used for JSON-RPC messages, corrupting the protocol stream.
