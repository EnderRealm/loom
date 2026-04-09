---
id: tracker-f1-api-666-sentinel
title: F1 API uses positionNumber 666 as "not classified" sentinel regardless of completionStatusCode
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

The Formula 1 API returns `positionNumber: 666` as a sentinel value meaning "not classified." This applies to both DNF drivers and lapped-out drivers, regardless of `completionStatusCode` (which may still show "OK"). Code that checks only `dnf=True` will miss lapped-out drivers who received 666. The sentinel must be checked independently.

## How to verify

Run from the tracker repo root:

```
grep -rn "666\|not.classified\|positionNumber" agents/results.py scoring/performance.py
```

Look for handling of the 666 sentinel value independent of the DNF flag.
