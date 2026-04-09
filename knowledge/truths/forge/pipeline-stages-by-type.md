---
id: forge-pipeline-stages-by-type
title: Forge pipeline stages depend on both ticket type AND risk level
scope: forge
type: truth
status: validated
evidence:
  - path: src/tk/pipelines.ts
    line: 49-86
    note: CONFIG.pipelines — authoritative stage list per (type, risk)
  - path: src/tk/pipelines.ts
    line: 64
    note: explicit comment "Design decision: task mirrors feature at all risk levels"
sources:
  - session: 88f1615b-69cc-4330-9c8b-3affd4494229
    project: forge
    date: 2026-04-08
    role: discovered while sweep ticket dead-ended at spec gate
contradicts:
  - file: skills/using-forge/SKILL.md
    claim: "Bugs and tasks skip spec and design. Chores skip spec, design, test, and verify."
    status: stale doc, ticketed for removal in forge/sweep-stale-triage-f25b AC5
verified_at: 2026-04-08
---

## Claim

Forge pipeline stages are computed from `(type, risk)`, not type alone. The authoritative table (`src/tk/pipelines.ts:50-86`) is:

| Type     | default     | low                                  | normal      | high       | critical   |
|----------|-------------|--------------------------------------|-------------|------------|------------|
| feature  | full 10     | 8 (no design-review, no code-review) | full        | full       | full       |
| **bug**  | **7 (skips spec, design, design-review)** | **5 (also skips code-review, test)** | **7** | **full 10** | **full 10** |
| task     | full 10     | 8                                    | full        | full       | full       |
| chore    | full 10     | 8                                    | full        | full       | full       |
| epic     | 5 (terminates at design)             | same        | same       | same       | same       |

Key facts:

1. **Bugs at default/low/normal risk** skip spec, design, and design-review — they go `triage → implement` directly.
2. **Bugs at high or critical risk** get the **full** 10-stage pipeline including spec/design. Risk escalation restores the design stages.
3. **Tasks mirror features** at every risk level. Tasks NEVER skip spec/design.
4. **Chores mirror features** at every risk level. Chores NEVER skip spec/design (and never skip test/verify).
5. **Epics** have a unique short pipeline that terminates at `design` without implementing.
6. The "low" variant for feature/task/chore skips the *review* stages (`design-review`, `code-review`), not spec/design.

## Why it matters

The `using-forge/SKILL.md` Pipeline Rules section is wrong about both tasks and chores ("Bugs and tasks skip spec and design. Chores skip spec, design, test, and verify."). Acting on the doc instead of the source code will:

- File task tickets expecting them to skip spec/design — they won't, and the workflow will dead-end
- Skip writing acceptance criteria for tasks because "tasks don't have spec" — the ticket will fail the `spec>design` gate
- Misreport pipeline behavior to users planning work

The source file (`pipelines.ts`) is the only authority. The doc is a stale reflection.

## How to verify

From the forge repo root:

```
sed -n '49,86p' src/tk/pipelines.ts
```

Or via the MCP tool:

```
ticket_pipelines  # if exposed
```

(This MCP tool exists per `mcp__plugin_forge_tk__ticket_pipelines` in the harness; it should return the same table.)

## Notes

- "Design decision: task mirrors feature at all risk levels" is a literal source comment at `pipelines.ts:64`. This is intentional, not an oversight — task and feature are distinguished by intent (planned work vs new capability), not by ceremony.
- The bug pipeline's risk-conditional shape means triage assigning a risk level **changes which gates apply downstream**. Triage is not just metadata — for bugs it's a routing decision.
- Whenever the doc and the source disagree, the source wins. Whenever this truth disagrees with the doc, this truth wins. Until the doc is fixed (tracked in `forge/sweep-stale-triage-f25b` AC5), the doc cannot be cited as evidence.
