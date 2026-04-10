---
id: tracker-dnf-null-not-synthetic
title: Store DNF position as NULL with dnf=True rather than baking synthetic values into data layer
scope: tracker
type: decision
status: validated
tag: auto
sources:
  - session: aaa967e8
    project: tracker
    date: 2026-03-26
related: []
recorded_at: 2026-04-09
---

## Choice

DNF positions stored as NULL in the data layer with a `dnf=True` flag. Synthetic finishing positions (for trajectory scoring) are computed at scoring time, not stored.

## Alternatives

Store synthetic positions (e.g. max_classified + 1) directly in the results table.

## Rationale

Separates data truth (the driver DNF'd) from scoring interpretation (how to penalize DNFs in trajectory). Different scoring models may want different synthetic positions — keeping the data layer clean preserves flexibility.
