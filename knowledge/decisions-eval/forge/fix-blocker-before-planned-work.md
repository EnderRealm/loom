---
id: forge-fix-blocker-before-planned-work
title: Fix blocking MCP coercion bug before continuing backlog cleanup
scope: forge
type: decision
status: validated
tag: human
sources:
  - session: c7dbbcca
    project: forge
    date: 2026-04-07
related: []
recorded_at: 2026-04-09
---

## Choice

Paused the planned backlog priority cleanup (~25 tickets) to fix the MCP integer/boolean coercion bug first.

## Alternatives

Option B: skip priority edits that hit the bug. Option C: use bash escape hatch to set priorities directly.

## Rationale

The bug was blocking all priority edits via MCP tools. Continuing cleanup without fixing it would have meant 25+ manual workarounds. Fix the tool, then use the tool.
