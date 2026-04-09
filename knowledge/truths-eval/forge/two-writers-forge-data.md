---
id: forge-two-writers-forge-data
title: Two independent processes write to forge-data — Hono server and per-session MCP stdio
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

Two separate processes can write tickets to forge-data: the long-running Hono web server and the per-session `src/tk/serve.ts` MCP stdio process. Only the Hono server has a lifecycle suitable for a long-running background sync loop. The MCP stdio process starts and dies with each Claude Code session, requiring its own sync strategy.

## How to verify

Run from the forge repo root:

```
grep -rn "serve\|hono\|stdio" src/tk/ src/server/
```

Look for both entry points: the Hono server setup and the MCP stdio serve entry point in `src/tk/`.
