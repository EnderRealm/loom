---
id: forge-stages-represent-completion
title: Pipeline stages represent completion state, not pending work
scope: forge
type: decision
status: validated
tag: auto
sources:
  - session: b9b4c0be-4180-40ec-b1e3-b0dc77b0669b
    project: forge
    date: 2026-03-14
    role: clarified during backlog stage design discussion
related: []
recorded_at: 2026-04-09
---

## Choice

A ticket AT a stage means that stage's work has been done. "At triage" = triage is complete, not "triage needs to happen." `ticket_advance` moves the ticket OUT of the completed stage.

## Alternatives

Stages as pending work (ticket AT triage = triage needs to happen). More intuitive naming but creates ambiguity about whether work is done.

## Rationale

Completion semantics let the orchestrator make clean decisions: if a ticket is at backlog, backlog is done but nothing else has happened — reject orchestration. If at triage, triage is done — advance to spec. No ambiguity about whether to run the current stage or the next one.

## Principle

State labels should describe what IS complete, not what NEEDS to happen. This eliminates off-by-one ambiguity in state machines.
