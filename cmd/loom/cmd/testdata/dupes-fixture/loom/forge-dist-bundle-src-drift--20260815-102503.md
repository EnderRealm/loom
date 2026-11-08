---
id: forge-dist-bundle-src-drift
title: Plugin dist bundle is a manually built artifact that can silently drift from src
scope: forge
type: truth
status: candidate
evidence:
  - path: .claude-plugin/dist/serve.js
    note: bundle mtime (Apr 6 21:24 UTC) predated commit 747ddef (Apr 6 23:16+ UTC) which landed a transport-layer coercion fix in src/tk/mcp.ts — the distributed bundle did not contain the fix.
  - commit: 747ddef
    note: numericParam/booleanParam coercion fix landed in src/tk/mcp.ts after the dist bundle was last built.
sources:
  - session: f692d941-0280-493e-a164-78eb756d0d02
    project: forge
    date: 2026-04-08
    role: Session compared dist bundle mtime against a source-fix commit timestamp to explain why a fix appeared merged but wasn't live.
related:
  - forge-dist-bundle-staleness
contradicts: []
verified_at: 2026-08-15
status: candidate
extracted_at: 2026-08-15T10:25:03
extracted_by: claude:sonnet
---

## Claim

The forge plugin's distributed bundle (`.claude-plugin/dist/serve.js`) is a built artifact that is not automatically regenerated when `src/` changes. Its mtime can lag behind source commits by an arbitrary window, so a merged source fix is not necessarily live in the running plugin until the bundle is rebuilt.

## Why it matters

An agent debugging "why doesn't this fix take effect" can waste time re-verifying already-correct source code when the real cause is a stale build artifact. Any investigation into plugin runtime behavior must check bundle freshness against source, not just source correctness.

## How to verify

Run from the forge repo root:

```
stat -f "%Sm" .claude-plugin/dist/serve.js
git log -1 --format=%cI -- src/tk/mcp.ts
```

If the bundle mtime predates the latest relevant commit touching `src/tk/`, the dist bundle is stale relative to source. Confirm there is no build step wired into CI/pre-commit that regenerates the bundle automatically:

```
grep -rn "dist/serve.js" package.json .github/workflows/*.yml 2>/dev/null
```

## Notes

The specific staleness window cited (Apr 6, commit 747ddef) is historical evidence for the mechanism, not a claim that the bundle is currently stale — re-run the check above for current state.