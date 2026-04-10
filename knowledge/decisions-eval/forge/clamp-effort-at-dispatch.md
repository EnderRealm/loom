---
id: forge-clamp-effort-at-dispatch
title: Clamp effort:max to high in orchestrator dispatch rather than per-agent definitions
scope: forge
type: decision
status: validated
tag: auto
sources:
  - session: 0916c546
    project: forge
    date: 2026-03-12
related: []
recorded_at: 2026-04-09
---

## Choice

The orchestrator clamps `effort: "max"` to `"high"` at the dispatch layer, rather than editing all 8 agent definition files to use a supported effort level.

## Rationale

Runtime constraints from the execution environment (Claude.ai subscriber limits) should be handled once at the dispatch layer, not replicated across each consumer.
