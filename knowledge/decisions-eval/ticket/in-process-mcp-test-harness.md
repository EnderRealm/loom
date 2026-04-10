---
id: ticket-in-process-mcp-test-harness
title: Use in-process MCP test harness over replacing installed binary
scope: ticket
type: decision
status: validated
tag: human
sources:
  - session: 2e9a386c
    project: ticket
    date: 2026-02-27
related: []
recorded_at: 2026-04-09
---

## Choice

Created in-process MCP test harness using Go SDK's `NewInMemoryTransports` for client/server pair testing without stdio framing.

## Alternatives

Replace the installed tk binary with the development build for testing. Would break service availability for other users/agents during test runs.

## Rationale

Preserves service availability. In-process testing avoids stdio overhead and doesn't interfere with production consumers of the same binary.
