# Knowledge scopes

A scope is one project's namespace in the knowledge store: `truths/<scope>/`,
`decisions/<scope>/`, and the candidate trees that mirror them under
`_candidates/`. Every extraction is filed under exactly one, and a scope is one
safe path segment — `^[a-z0-9][a-z0-9._-]*$`, the same shape on both sides
(`scopePattern` in `internal/extract/scope.go`, `NAME_PATTERN` in
`extractors/resolve_project.py`).

## The gate

`truths/<scope>/` must exist, or the session is not extracted. The directory is
what `extract.py` loads as few-shot references, and its absence means the store
has no such scope (`scopeInStore` in `internal/extract/scope.go`).

There is deliberately no default scope. A default files a project's truths under
another project's name, and once filed, nothing in the store can tell that
happened later: a truth carries no record of the repo it came from beyond the
directory it sits in. A skipped session is recoverable — the transcript is still
in `received/` and the scope can be created after the fact — where a
misattributed one is not.

## The three derivations

| Derivation | Entry point | Source |
| --- | --- | --- |
| `.loom-project` marker | both | first usable marker from the session's cwd up to the repo root |
| normalized git-remote basename | Go, `internal/extract/scope.go` | `summaries.NormalizeRemote(git_remote)` after the last `/` |
| repo directory basename | Python, `extractors/resolve_project.py` | the repo root's own directory name |

The marker is preferred by both. The fallbacks differ because the inputs differ:
the Go sweep resolves sessions captured on other hosts, where the checkout that
ran them may not exist here, and `git_remote` travels with the session in
`summaries.db`; `resolve_project.py` is handed a live path on the machine it
runs on.

The marker is repo-owned. Neither loom nor tk ever writes one — both only read
it — so declaring one is the project's decision and stays the project's.

### Case

`resolve_project.py` matches a marker's value exact-case and rejects `Loom`: a
marker is a human declaration, and lowercasing it silently would hide the typo.
`markerScope` lowercases instead, because everything on the Go side is lowercase
by construction — `summaries.NormalizeRemote` lowercases, `scopePattern`
requires a leading `[a-z0-9]`, and `wantedScopes` lowercases `--scope` — so an
exact-case rule there would make `loom extract --backfill --scope Loom` stop
matching the sessions it matches today.

## Collisions

The derived name is a basename, so it is not unique, and it is not stable.

- **Two repos, one basename.** `github.com/a/tools` and `github.com/b/tools`
  both derive `tools`, and their truths merge into one namespace. Nothing
  detects it: the store sees one scope with more sessions than expected.
- **Directory basename ≠ remote basename.** A checkout in `~/code/loom-fork`
  whose remote is `github.com/enderrealm/loom` resolves to `loom-fork` under
  `resolve_project.py` and to `loom` under the sweep, so the same repo's truths
  split across two directories depending on which entry point ran.
- **A fork or a rename.** Changing the remote changes the derived scope, and
  what was filed under the old one is orphaned — still in the store, no longer
  reachable from the new name.

The resolution is the marker. It is the only derivation a project controls and
the only one that survives a move, a rename or a fork, so a repo where any of
the three apply declares one:

```
$ cat ~/code/loom-fork/.loom-project
loom
```

Both entry points prefer it, so the two agree by construction rather than by
coincidence.

## Onboarding

```
loom knowledge scope add <name>...
```

Creates `truths/<name>/` under the store the **extractor** resolves — its
persisted tunables (`LOOM_KNOWLEDGE_ROOT`), which may name a store the invoking
shell does not — and commits and pushes it through the store's single write
entry point (`docs/knowledge-store-writes.md`). The directory holds a
`.gitkeep`, because git tracks files and not directories and an empty scope
would otherwise exist only on the machine that created it; it is deliberately
not a `*.md` file, so `internal/knowledge`'s artifact walker never reads it back
as a truth.

Names are validated against the name half of the gate before anything is
written, and one bad name refuses the whole invocation. A name that already has
a directory is reported and is not an error.

`loom status`'s `=== knowledge scopes ===` section is where pending scopes are
visible: every scope this host's sessions resolve to, which of them the store
has a directory for, and how many sessions each un-onboarded one is costing.

### A marker naming a scope the store does not have

The `truths/<scope>/` check is part of what makes a marker usable, so a marker
declaring a scope this store has no directory for is treated as unusable and the
walk continues — to an outer marker, then to the git remote, then to nothing.
The name `loom status` reports as pending is therefore the **remote-derived**
one, and the marker's own name is never shown; a session in such a repo with no
remote at all lands in `unresolved` instead. Onboarding the name the display
offers works — those sessions are extracted — but under the remote's name, and
the repo's declaration stays inert, which is the split the marker was declared
to prevent. So when a repo carries a `.loom-project`, onboard the name **in the
marker**, not the one `loom status` names.

## Why a scope skip is not recorded

The extraction ledger (`~/.loom/extract.state`) is permanent — it is what makes
extraction at-most-once, since a second visit costs a second round trip. A
session declined for want of `truths/<scope>/` was never spent on, and recording
it would mean creating the directory later could never rescue the very sessions
it was created for. So the sweep counts those sessions rather than marking them,
the way it already counts the sessions below `--min-turns`, and reports them in
aggregate — the pending scopes by name, the failures no directory fixes by
reason — since per-session skip lines would otherwise be re-logged every 15
minutes for a backlog a thousand sessions wide.

Records written before that change are retired on read (`purgeScopeSkips` in
`internal/extract/state.go`): an `outcomeSkipped` record whose reason begins
`no git remote`, `unknown scope `, `unsafe scope ` or `scope "` — the last being
the containment refusal, whose message quotes the name — is dropped from the
ledger in memory, and from the file at the next write. Only those — an
`extracted` or `failed` record paid for its run and is never dropped.
