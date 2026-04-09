---
id: _schema
title: Truth file schema (extracted post-hoc from first 4 truths)
type: meta
status: draft
verified_at: 2026-04-08
---

## Purpose

This document defines the shape of files in `knowledge/truths/`. It was written **after** the first four truths so the schema reflects what actually emerged, not what was planned.

A truth is a reusable, evidence-backed claim about how a system behaves. It is not a ticket, not a runbook, not a session note.

## Directory layout

Truths are scoped by directory, not by filename prefix:

```
knowledge/truths/
  _schema.md                  # this file
  forge/                      # project scope
    review-agents-stateless.md
    dist-bundle-staleness.md
    ...
  loom/                       # project scope (when truths exist)
  universal/                  # cross-project truths (rare)
```

`universal/` sits as a peer to project directories — not nested under a `projects/` parent — because universal isn't a sub-type of project, it's its own scope. The overview's rule applies: start everything as project-scoped, promote to universal only when clearly generalizable.

## Required tests for promotion

A candidate becomes a truth when it passes all four:

1. **Reusable** — the claim applies to more than one task/session
2. **Specific** — the claim is testable; "X is bad" is not specific, "X stringifies booleans on the transport layer" is
3. **Evidence-backed** — at least one file path, commit hash, or code location backs the claim
4. **Independent of a single task** — the claim survives when the originating ticket is closed

## File layout

```yaml
---
id: <scope>-<short-name>        # required, e.g. "forge-review-agents-stateless"
                                #   (id keeps the scope prefix even though the
                                #    file does not, so cross-references survive moves)
title: <one-line summary>       # required, ~80 chars max
scope: <project|universal>      # required, matches the parent directory name
type: truth                     # required, one of: truth | runbook | decision | mental-model
status: validated|candidate|deprecated   # required
evidence:                       # required, ≥1 entry
  - path: <project-relative path>   # relative to the scope's project root
    line: <number>              # optional
    note: <what to look for>
  - commit: <sha>               # alternative form
    note: <what the commit changed>
sources:                        # optional but recommended
  - session: <uuid>
    project: <project name>
    date: <YYYY-MM-DD>
    role: <how this session contributed>
related:                        # optional, just the filename (e.g. "dist-bundle-staleness.md")
  - <sibling-file>
contradicts:                    # optional, for truths that override docs
  - file: <project-relative path>
    claim: "<exact stale wording>"
    status: <how it's being remediated>
verified_at: <YYYY-MM-DD>       # required, last time the claim was checked against reality
---

## Claim

One to three sentences. State the rule. No narrative.

## Why it matters

What goes wrong if a future agent doesn't know this. Operational consequence.

## How to verify

Exact commands or file checks that prove the claim is currently true.
This is the part that distinguishes a truth from a slogan — every truth must
carry its own verification method so a stale truth can be detected and updated.

## Notes (optional)

Edge cases, related observations, things known but not yet generalized.
```

## Conventions that emerged from writing the first 4

- **Filenames** are kebab-case, no scope prefix. Scope is carried by the directory.
- **Paths inside truth files are project-relative**, not absolute. The scope directory implies the base. `agents/design-reviewer.md` (good) vs `/Users/smacbeth/code/forge/agents/design-reviewer.md` (bad — leaks home dir, breaks for other readers, won't survive a repo move).
- **`How to verify` commands assume cwd = the scope's project root.** State this explicitly with a "Run from the X repo root:" line before the first command block. Verification commands should be copy-pasteable into a shell at that location.
- **Evidence must include a verification command** wherever possible — a path alone is weaker than a path + grep.
- **A truth that contradicts a doc** carries a `contradicts:` block citing the exact stale wording. This makes the next reader's "but the docs say..." moment self-resolving.
- **Verification dates matter.** Source files change. A truth verified 6 months ago is a candidate, not a fact. Re-verification should bump `verified_at`.
- **Related truths cross-link by sibling filename.** `mcp-coercion-cluster.md` links to `dist-bundle-staleness.md` because the first is a *symptom pattern* and the second is the *root cause*. Reading either should surface the other. Cross-scope references (rare) should use `<scope>/<filename>` form.

## Exceptions to the project-relative path rule

A few paths legitimately live outside the scope's project root — for example, the marketplace cache at `~/.claude/plugins/marketplaces/<plugin>/...` or system paths. When this happens:

1. Keep the absolute or `~`-relative path (no fake-relativizing).
2. Add an inline note explaining *why* it's absolute, so future readers don't "fix" it.

See `forge/dist-bundle-staleness.md` Notes section for an example.

## What is NOT a truth

- A current bug ("ticket_edit appends instead of replaces") — that's a defect, file a ticket.
- A decision specific to one ticket ("we chose Path B for sweep ticket") — that's a ticket note.
- A pattern observed once with no mechanism explanation — that's a candidate, put it in `_candidates/`.
- A feeling ("the pipeline feels brittle") — that's a brainstorm note.

## Open questions

- Should runbooks live in `truths/` or in their own `runbooks/` directory? The first 4 are all truths, but the session that produced them also yielded one clear runbook (the Path A/B/C analysis pattern). Decide when the second artifact type lands.
- Is `verified_at` enough, or should there be a `verification_log:` list? Defer until a truth needs re-verifying and the answer becomes obvious.
- How are project truths promoted to `universal`? The overview says rewrite, not copy. No mechanism yet — wait until a candidate appears.
