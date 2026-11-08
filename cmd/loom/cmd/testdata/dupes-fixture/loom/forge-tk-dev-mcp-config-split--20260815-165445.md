---
id: forge-tk-dev-mcp-config-split
title: forge runs two tk MCP servers: plugin-bundled `tk` and gitignored per-dev `tk-dev`
scope: forge
type: truth
status: candidate
evidence:
  - path: .mcp.json
    note: gitignored per .gitignore:18; when present locally, registers a `tk-dev` server running `bun run src/tk/serve.ts --central` against the working tree source
  - path: .claude-plugin/plugin.json
    note: registers `tk`, pointing at `${CLAUDE_PLUGIN_ROOT}/.claude-plugin/dist/serve.js`, the bundled dist build served to all projects via the plugin cache
  - commit: 6613876
    note: "Untrack .mcp.json so plugin stops shipping redundant tk-dev server" — removed the tk-dev definition from version control, not the server concept itself
sources:
  - session: 03ec2cb9-3036-48b2-9c29-8e1d2d02a256
    project: forge
    date: 2026-04-08
    role: Session initially misdiagnosed tk-dev's disappearance as a rename-only event; user corrected that tk-dev is a separate, intentionally-coexisting server that needed to be manually recreated locally.
related: []
contradicts: []
verified_at: 2026-08-15
status: candidate
extracted_at: 2026-08-15T16:54:45
extracted_by: claude:sonnet
---

## Claim

forge intentionally exposes two separate MCP tk servers side by side: `plugin:forge:tk`, built from `.claude-plugin/dist/serve.js` and served to every project through the plugin cache, and a per-developer `tk-dev`, defined in a gitignored root `.mcp.json` that runs `bun run src/tk/serve.ts --central` directly against the working tree. Because `.mcp.json` is gitignored, a fresh clone or a reset home directory has no `tk-dev` server registered until a developer manually recreates the file.

## Why it matters

An agent that sees `tk` connected and no `tk-dev` should not conclude `tk-dev` was renamed or removed — it may simply never have been created locally. Chasing this via git history on `.mcp.json` is a dead end for the "where did it go" question; the actual fix is recreating the gitignored file, which requires knowing its intended content (`tk-dev` → `bun run src/tk/serve.ts --central`).

## How to verify

Run from the forge repo root:
```
git check-ignore .mcp.json        # should print .mcp.json — confirms it's ignored
grep -n "mcp.json" .gitignore
grep -n '"tk"' .claude-plugin/plugin.json
git log --all --oneline -- .mcp.json   # shows the untrack/rename history, not a deletion of the concept
```

## Notes

If `.mcp.json` doesn't exist locally, `claude mcp list` will show only `plugin:forge:tk` connected — this is expected default state for a fresh checkout, not a regression.