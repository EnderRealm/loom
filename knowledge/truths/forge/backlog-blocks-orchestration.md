---
id: forge-backlog-blocks-orchestration
title: Forge orchestrator hard-rejects runs on backlog tickets; must triage first
scope: forge
type: truth
status: validated
evidence:
  - path: src/server/services/orchestrator.ts
    line: 1266-1272
    note: "Hard gate: cannot orchestrate a backlog ticket" — sets run to failed, emits error
  - path: src/server/services/orchestrator.ts
    line: 1724-1741
    note: second check in startAgentRun — same gate, same error message
sources:
  - session: b9b4c0be-4180-40ec-b1e3-b0dc77b0669b
    project: forge
    date: 2026-03-14
    role: discovered during backlog stage implementation
verified_at: 2026-04-09
related:
  - human-stages-block-auto-advance.md
---

## Claim

The forge orchestrator has a hard gate that rejects any run (orchestration or agent) when the ticket is at the `backlog` stage. The run is immediately set to `"failed"` with the error message: *"Cannot start run on a backlog ticket. Triage the ticket first (/triage)."* This check runs in both `startOrchestration()` and `startAgentRun()`, before any worktree or resource allocation.

## Why it matters

If an automated workflow creates a ticket via `/idea` and immediately tries to orchestrate it, the run fails. The ticket must be triaged (`/triage`) out of backlog before any agent can work on it. This is a deliberate intake gate: backlog means "captured, not assessed." No resources are spent until a human (or the triage skill) confirms the ticket is workable.

Because the check runs **before** worktree creation, a rejected backlog run does not leak resources — no partial worktrees, no orphaned run records.

## How to verify

Run from the forge repo root:

```
grep -n "backlog" src/server/services/orchestrator.ts | head -20
```

Expected: multiple hits including the hard gate checks at two call sites, both containing "Cannot start run on a backlog ticket."

```
grep -c "Cannot start run on a backlog" src/server/services/orchestrator.ts
```

Expected: 2 (one in startOrchestration, one in startAgentRun).
