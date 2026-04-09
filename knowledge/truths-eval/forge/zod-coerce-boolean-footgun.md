---
id: forge-zod-coerce-boolean-footgun
title: z.coerce.boolean() treats any non-empty string as true including "false"
scope: forge
type: truth
status: validated
sources:
  - session: c7dbbcca
    project: forge
    date: 2026-04-07
verified_at: 2026-04-09
---

## Claim

Zod's `z.coerce.boolean()` coerces any non-empty string to `true`, including the string `"false"`. For MCP tool parameters where JSON booleans arrive as strings, explicit string matching via `z.preprocess()` is required: `"true"/"1" -> true`, `"false"/"0" -> false`.

## How to verify

Run from the forge repo root:

```
grep -n "booleanParam\|preprocess.*bool" src/tk/mcp.ts
```

Look for a `z.preprocess()` wrapper that explicitly maps string values to booleans instead of using `z.coerce.boolean()`.
