---
id: forge-mcp-coercion-cluster
title: Forge tk MCP coercion failures cluster — when one appears, others are already present
scope: forge
type: truth
status: validated
evidence:
  - commit: 747ddef
    note: single fix touched ticket_edit.priority, ticket_list.{priority,offset,limit}, ticket_create.priority, ticket_advance.force, ticket_review.{approve,reject}
sources:
  - session: 88f1615b-69cc-4330-9c8b-3affd4494229
    project: forge
    date: 2026-04-08
    role: 3 P0 ticket_* tools failed in same session before root cause identified
related:
  - forge-dist-bundle-staleness
verified_at: 2026-04-08
---

## Claim

The forge tk MCP server stringifies JSON-encoded primitive params (numbers, booleans) at the transport layer. The fix is centralized in `src/tk/mcp.ts` via `numericParam()` and `booleanParam()` helpers. **Every tk tool that takes a numeric or boolean parameter is on the same code path.** When one such param is rejected as `"Expected number, received string"` or equivalent, all of them are broken. Filing per-symptom tickets is wasted work.

## Why it matters

In the 2026-04-08 session, three separate P0 bug tickets were filed for what turned out to be one root cause:

- `forge/ticket-edit-mcp-dfd5` — `ticket_edit` rejects numeric `priority`
- `forge/plugin-dist-bundle-3097` — root cause (stale dist bundle)
- (would-be third) `ticket_review` could not record `approve: true`

All three were the same bug in the same coercion path. Filing the first two before finding the root cause produced ticket churn that later had to be reconciled with "superseded by" notes.

## How to verify

From the forge repo root, identify all params currently going through the centralized coercion helpers:

```
grep -n "numericParam\|booleanParam" src/tk/mcp.ts
```

Any tool listed in those calls is part of the cluster. As of 2026-04-08 the set is:

- `ticket_edit.priority`
- `ticket_list.priority`, `ticket_list.offset`, `ticket_list.limit`
- `ticket_create.priority`
- `ticket_advance.force`
- `ticket_review.approve`, `ticket_review.reject`

## Triage rule

If you hit a coercion failure on any tk tool param, **before filing**:

1. Verify the dist bundle is current (see `dist-bundle-staleness.md`).
2. If the bundle is current, check whether the failing param goes through `numericParam`/`booleanParam`. If yes, the fix lives at the helper, not at the tool.
3. If the bundle is stale, file/update the bundle ticket — do not file per-tool tickets.

## Notes

- This is the "if you find a cluster, fix the cluster" pattern. The deeper truth (`dist-bundle-staleness.md`) is what makes the cluster appear simultaneously rather than one-at-a-time.
