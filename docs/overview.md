# Memory-First Reset: Working Summary

## Core shift

The system is not failing because of orchestration.

It is failing because:

- memory is noisy
- memory is duplicated
- memory is not trusted
- memory is not compressed into reusable form

Fix memory first.

Everything else is downstream.

---

## Hard principles (do not break these)

### 1. Ideas are not tickets

- Brainstorming stays in markdown
- Tickets are only for work you are about to do

### 2. Memory is separate from workflow

- Knowledge is not a workflow artifact
- Tickets are not knowledge containers
- Sessions are not memory

### 3. Regeneration over accumulation

- Derived artifacts must be recomputed, not appended
- Append-only systems drift and corrupt (you've already seen this)

### 4. Compression is the goal

- Raw artifacts are input
- Knowledge is compressed output
- If it isn't reusable, it isn't knowledge

---

## What you already have (important)

You are not starting from zero.

From forge-data:

- 500+ session summaries
- 1000+ tickets
- patterns and rollups (currently corrupted or drifting)

This is training data, not the final system.

---

## The actual problem

Your current system mixes:

- memory
- workflow
- orchestration
- control logic

Result:

- artifacts do too many jobs
- system becomes heavy
- model spends time maintaining the system

Example failure mode:

- correct implementation blocked by tool bug
- ticket becomes procedural recovery document
- workflow dominates the work

---

## The reset strategy

Do not rebuild the system.

Build a memory layer first.

---

## The durable knowledge model

There are only a few real durable assets:

### 1. Truths (start here)

Reusable facts about how systems behave.

### 2. Decisions

What you chose and why.

### 3. Runbooks

Repeatable procedures.

### 4. Mental models

How to think about systems.

Everything else feeds into these.

---

## What is NOT durable

Do not treat these as memory:

- sessions → raw input
- tickets → current work state
- rollups → summaries of summaries
- patterns (current form) → corrupted unless regenerated

---

## Truths (the first thing to build)

A truth is:

- reusable
- specific
- evidence-backed
- independent of a single task

Examples from your data:

- summary frontmatter is written in two places
- stale runtime/plugin state can block progress even after code is fixed

These are the kinds of things that matter.

---

## Truth lifecycle

### 1. Extract

From:

- session summaries (Discoveries, Problems)
- tickets (blocked states, debugging notes)

### 2. Enrich

Convert into:

- clear claim
- why it matters
- supporting evidence

Remove narrative.

### 3. Validate

Lightweight:

- seen multiple times
- confirmed in code or behavior
- human-reviewed

### 4. Promote

Move into `knowledge/truths/` only when:

- reusable
- understandable out of context
- not task-specific

Everything else stays as raw material.

---

## Project vs universal truths

**Project truths**

- scoped to one system (Forge, Loom, etc.)
- most truths live here

**Universal truths**

- abstracted from repeated project truths
- rare
- must be rewritten, not copied

**Rule:** Start everything as project truth. Promote only when clearly generalizable.

---

## Minimal structure (do not overbuild)

Start with:

```
knowledge/
  truths/
  _candidates/truths/

sessions/
tickets/
notes/
```

That is enough.

---

## First system to build

Not a service.
Not loom.
Not orchestration.

Just:

**A simple truth extractor**

Input:

- session markdown
- ticket markdown

Output:

- candidate truth files

Steps:

1. parse sections like Discoveries / Problems / Blocked
2. extract candidate statements
3. normalize into short truth drafts
4. dedupe
5. write to `_candidates/truths/`

No auto-promotion.

---

## Promotion workflow

Manual.

Flow:

- script generates candidates
- you review
- you promote good ones to `knowledge/truths/`

This keeps the system clean.

---

## What success looks like

You know this is working when:

A fresh session can:

- pick up a random ticket
- load a small set of truths
- complete the work correctly

In under ~20 messages.

If that works:

- you don't need complex orchestration
- you don't need large prompts
- you don't need most of Forge

---

## What NOT to do next

Do NOT:

- build loom yet
- build a distributed system
- design MCP surfaces
- redesign tickets
- create large backlogs

That is the old pattern.

---

## What to do next (concrete)

### Step 1

Write `knowledge/truths/truth.md` (you did this)

### Step 2

Pick 5 real artifacts:

- 2 sessions
- 2 tickets
- 1 pattern

Manually extract truths.

### Step 3

Create 5–10 truth files.

### Step 4

Refine structure based on reality.

### Step 5

Write a simple extraction script.

---

## Final mental model

```
Sessions → raw history
Tickets  → current intent
Truths   → durable memory
```

Everything flows upward.

---

## Bottom line

You don't need a better system.

You need:

- fewer artifacts
- smaller artifacts
- higher-quality memory

Start with truths.

Everything else can wait.
