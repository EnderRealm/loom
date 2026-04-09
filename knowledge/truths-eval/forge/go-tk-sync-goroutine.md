---
id: forge-go-tk-sync-goroutine
title: Go tk ran commit sync as a 5s goroutine inside tk serve — no standalone daemon
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

The original Go tk implementation ran its commit sync loop as a goroutine with a 5-second ticker inside the `tk serve` subprocess. It ran `git add tickets/ && git diff --cached --quiet && git commit && git push` with rebase retry on conflict and a `.tk-sync-blocked` marker file for unresolvable conflicts. No standalone daemon — sync died when the session ended.

## How to verify

This describes the Go implementation which may no longer be the active code. Check git history or the Go tk source for `syncCentralStore` and the 5-second ticker:

```
git log --all --oneline -- '*.go' | head -20
```

If Go source is still present:

```
grep -rn "syncCentralStore\|5.*time.Second\|tk-sync-blocked" .
```
