---
id: tracker-reject-fast-f1-library
title: Rejected Fast-F1 library adoption for data collection
scope: tracker
type: decision
status: validated
tag: human
sources:
  - session: aaa967e8
    project: tracker
    date: 2026-03-26
related: []
recorded_at: 2026-04-09
---

## Choice

Did not adopt the Fast-F1 Python library for F1 data collection. Continued with direct API calls via httpx.

## Alternatives

Adopt Fast-F1 as the data layer. Provides convenient Python API for F1 data.

## Rationale

Fast-F1 only covers F1 (25% of tracked series — also need F2, F3, F1 Academy). Heavy dependency chain (~200MB) for data obtainable with ~50 lines of httpx. Does not justify the weight for partial coverage.
