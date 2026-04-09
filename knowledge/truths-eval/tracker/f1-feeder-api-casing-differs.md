---
id: tracker-f1-feeder-api-casing-differs
title: F1 API uses lapsCompleted (string), feeder series use LapsCompleted (integer) — casing and type differ
scope: tracker
type: truth
status: validated
sources:
  - session: aaa967e8
    project: tracker
    date: 2026-03-26
verified_at: 2026-04-09
---

## Claim

The F1 API returns `lapsCompleted` as a string in `raw_json`, while feeder series APIs (F2, F3, F1 Academy) use `LapsCompleted` as an integer. Both field name casing and type differ between the main series and feeder APIs. Parsing code must handle both variants.

## How to verify

Run from the tracker repo root:

```
grep -rn "lapsCompleted\|LapsCompleted" agents/results.py
```

Look for handling of both the camelCase string variant (F1) and PascalCase integer variant (feeder series).
