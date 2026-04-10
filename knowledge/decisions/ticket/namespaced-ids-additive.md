---
id: ticket-namespaced-ids-additive
title: Use namespaced IDs (project/ticket-id) for cross-project disambiguation
scope: ticket
type: decision
status: validated
tag: human
sources:
  - session: 91d979db-8c94-4f38-999b-90b028c5b543
    project: ticket
    date: 2026-03-25
    role: decided during MultiStore design for multi-project support
related:
  - multistore-namespaced-routing.md
recorded_at: 2026-04-09
---

## Choice

Prefixed ticket IDs with `project/` for cross-project operations. Bare IDs still work for single-project usage — MultiStore searches all projects and returns the unique match.

## Alternatives

Enforce globally unique IDs across all projects. Would require a central registry and break existing single-project workflows.

## Rationale

Namespacing is additive — it extends the ID format without breaking existing behavior. Single-project users never see the prefix. Multi-project users get explicit routing. Ambiguous bare IDs produce a descriptive error listing matches.

## Principle

Prefer additive extensions over breaking changes when introducing multi-tenant features. Existing users should not need to change behavior.
