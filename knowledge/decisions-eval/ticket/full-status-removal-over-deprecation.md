---
id: ticket-full-status-removal-over-deprecation
title: Full removal of legacy status field rather than phased deprecation
scope: ticket
type: decision
status: validated
tag: human
sources:
  - session: 5bc59345
    project: ticket
    date: 2026-03-12
related: []
recorded_at: 2026-04-09
---

## Choice

Completely removed the legacy `status` field from all four layers (core library, CLI, MCP server, TUI), replacing all logic with the modern `stage` field. 41 files changed, net reduction of 541 lines.

## Alternatives

Phased deprecation where status is derived from stage. Would keep the field alive longer and require maintenance of the mapping.

## Rationale

User chose clean break. Since `stage` fully replaces `status` semantics, keeping both creates confusion about which is authoritative.
