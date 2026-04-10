---
id: forge-file-root-cause-not-symptoms
title: File one root-cause ticket instead of per-symptom tickets for clustered bugs
scope: forge
type: decision
status: validated
tag: auto
sources:
  - session: 88f1615b-69cc-4330-9c8b-3affd4494229
    project: forge
    date: 2026-04-08
    role: decided after 3 P0 tickets traced to stale dist bundle
related: []
recorded_at: 2026-04-09
---

## Choice

Filed `forge/plugin-dist-bundle-3097` as the single root-cause ticket for multiple MCP coercion failures, superseding per-symptom tickets (`ticket-edit-mcp-dfd5`, and the would-be `ticket-review` failure ticket).

## Alternatives

Continue filing per-symptom tickets as each failure surfaces. Would have produced 3-4 tickets for what was one bug in one coercion path.

## Rationale

All symptoms traced to the same stale dist bundle — the source fix (commit 747ddef) already existed but was never deployed. Per-symptom tickets created reconciliation churn ("superseded by" notes) and diluted priority signal.

## Principle

When multiple failures cluster in one session, investigate the root cause before filing. If they share a mechanism, file one ticket for the mechanism and close or supersede the symptom tickets.
