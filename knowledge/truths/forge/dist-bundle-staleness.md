---
id: forge-dist-bundle-staleness
title: Forge src/tk fixes are invisible to plugin consumers until .claude-plugin/dist/serve.js is rebuilt
scope: forge
type: truth
status: validated
evidence:
  - path: .claude-plugin/dist/serve.js
    note: built artifact that plugin consumers actually run
  - path: src/tk/mcp.ts
    note: source that must be bundled into dist/serve.js
  - commit: 6ab96e5
    note: only commit ever to touch dist/serve.js prior to 2026-04-08
  - commit: 747ddef
    note: numericParam/booleanParam coercion fix that was undeployed for ~2h until bundle rebuild
sources:
  - session: 88f1615b-69cc-4330-9c8b-3affd4494229
    project: forge
    date: 2026-04-08
    role: root cause for 3 P0 MCP tool bugs
verified_at: 2026-04-08
---

## Claim

The forge plugin runs `.claude-plugin/dist/serve.js`, not `src/tk/serve.ts`. Source-level fixes in `src/tk/**` do not reach plugin consumers until the dist bundle is rebuilt and committed. Git history of `src/` does **not** reflect runtime behavior.

## Why it matters

A class of bug looks like: source diff is correct, commit landed, behavior unchanged. The natural diagnosis ("I must have the wrong fix") is wrong — the actual cause is that the bundle is older than the source. This wastes investigation time and produces ghost-bug tickets (fixes get re-implemented or duplicate-filed).

## How to verify

Run from the forge repo root.

Compare bundle mtime to source commit time:

```
stat -f "%Sm %N" .claude-plugin/dist/serve.js
git log -1 --format='%ai %H' src/tk/
```

If the bundle mtime is **older** than the most recent `src/tk/` commit, the bundle is stale.

For a specific suspected fix, grep both source and dist for the new symbol:

```
grep -c booleanParam src/tk/mcp.ts
grep -c booleanParam .claude-plugin/dist/serve.js
```

Counts should be roughly equal. A non-zero source count with a zero dist count is the smoking gun.

## How to remediate

From the forge repo root:

```
bun run build:mcp
```

Then **restart Claude Code** so the plugin's MCP server reconnects with the rebuilt bundle. The MCP client does not hot-reload.

## Notes

- The same staleness blocks plugin consumers via the marketplace cache (path `~/.claude/plugins/marketplaces/forge-dev/.claude-plugin/dist/serve.js` outside the project — the only absolute path in this file because it's not inside the forge repo). That copy is propagated by plugin install/update flows, not by `bun run build:mcp` in the working tree alone.
- Regression prevention proposed in session: pre-commit hook that refuses `src/tk/**` changes without a matching bundle rebuild. Not yet implemented as of 2026-04-08.
- Related: see `mcp-coercion-cluster.md` — multiple MCP tool failures with the same root cause are a signal to check the bundle before filing per-symptom tickets.
