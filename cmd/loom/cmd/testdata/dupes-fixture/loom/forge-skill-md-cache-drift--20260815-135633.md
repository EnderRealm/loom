---
id: forge-skill-md-cache-drift
title: Plugin-cached SKILL.md files drift independently from the working tree and the dist bundle
scope: forge
type: truth
status: candidate
evidence:
  - path: skills/triage/SKILL.md
    note: working-tree copy contains the "advance twice" wording fix from commit c97d
  - path: ~/.claude/plugins/marketplaces/forge-dev/skills/triage/SKILL.md
    note: "lives outside the forge project root, under the Claude plugin marketplace cache directory (not git-tracked); still read \"advance once\" as of 2026-04-08 despite the working-tree fix"
  - commit: c97d
    note: fix that updated working-tree skills/triage/SKILL.md wording from "advance once" to "advance twice"
sources:
  - session: 76e61663-73fb-4c2f-8af7-52acc67049e2
    project: forge
    date: 2026-04-08
    role: Direct grep comparison between working-tree SKILL.md and the plugin-cached copy showed the cache still read pre-fix wording; confirmed bun run build:mcp rebuilds only dist/serve.js, not cached SKILL.md files.
related: []
contradicts: []
verified_at: 2026-08-15
status: candidate
extracted_at: 2026-08-15T13:56:33
extracted_by: claude:sonnet
---

## Claim

Forge skills have two independently-stale downstream artifacts: `.claude-plugin/dist/serve.js` (rebuilt by `bun run build:mcp`, governs MCP tool logic) and per-skill `SKILL.md` files cached under the Claude plugin marketplace directory (governs skill invocation instructions). No build step propagates working-tree `SKILL.md` edits into the plugin cache — `build:mcp` only targets the dist bundle.

## Why it matters

An agent that greps the working tree to confirm a skill-wording fix landed can be misled: the plugin Claude Code actually loads may still be serving the stale cached copy. The dist bundle and the plugin-cached SKILL.md go stale on different schedules and via different remediation paths, so a single "is it fixed" check covering one does not confirm the other.

## How to verify

Run from the forge repo root:

```
diff skills/triage/SKILL.md ~/.claude/plugins/marketplaces/forge-dev/skills/triage/SKILL.md
```

A non-empty diff confirms the plugin cache has drifted from the working tree.

Confirm the build step's scope:

```
grep -rn "SKILL.md" package.json Makefile scripts/ 2>/dev/null
```

Expect no reference to syncing `skills/*/SKILL.md` into the plugin cache — `build:mcp` should only reference `.claude-plugin/dist/serve.js`.

## Notes

This is a distinct staleness surface from the dist-bundle-staleness truth (which concerns `.claude-plugin/dist/serve.js` vs. `src/tk/**`). The two must be checked and remediated separately since no single build command covers both.