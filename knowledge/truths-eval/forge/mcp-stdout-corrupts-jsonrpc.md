---
id: forge-mcp-stdout-corrupts-jsonrpc
title: console.log on MCP stdio transport corrupts JSON-RPC stream; use callback-injected logger
scope: forge
type: truth
status: validated
sources:
  - session: c7dbbcca
    project: forge
    date: 2026-04-07
verified_at: 2026-04-09
---

## Claim

On an MCP server running over stdio transport, any `console.log` call writes to the same stdout used for JSON-RPC messages, corrupting the protocol stream. MCP stdio servers must use a callback-injected logger pattern that routes diagnostic output to stderr or a file, never stdout.

## How to verify

Run from the forge repo root:

```
grep -n "logger\|stderr\|console" src/tk/serve.ts
```

Look for a logger injection pattern that avoids direct `console.log` calls. There should be no unguarded `console.log` in the stdio serve path.
