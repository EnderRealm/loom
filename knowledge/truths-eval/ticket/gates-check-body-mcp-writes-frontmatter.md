---
id: ticket-gates-check-body-mcp-writes-frontmatter
title: Pipeline gates check markdown body sections but MCP edit tools write YAML frontmatter — architecture mismatch
scope: ticket
type: truth
status: validated
sources:
  - session: 2e9a386c
    project: ticket
    date: 2026-02-27
verified_at: 2026-04-09
---

## Claim

Pipeline gate checks look for specific markdown body sections (`## Acceptance Criteria`, `## Design`, `## Test Results`) in the ticket file. But `ticket_edit` MCP tool writes values to YAML frontmatter fields, not to the markdown body. This is a fundamental architecture mismatch: gates and MCP tools operate on different parts of the same file, so MCP edits never satisfy gate requirements.

## How to verify

Run from the ticket repo root:

```
grep -rn "Acceptance Criteria\|## Design\|## Test Results" .
```

Look for gate check code that inspects the markdown body. Then:

```
grep -rn "frontmatter\|yaml" .
```

Look for the MCP edit handler writing to frontmatter fields. The two should be operating on different parts of the file.
