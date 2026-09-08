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

## Cross-scope extraction

A session's scope is the project it *ran in*; a truth's scope is the project it
is *about*, and the two diverge routinely — a loom session that debugs a tk
failure discovers a truth about tk. Both extractor templates ask the model to
name the subject project in the candidate's `scope:`, and it does; what the
extractor did with that value was nothing. Every candidate was filed under
`--scope`, so one declaring `scope: ticket` landed in
`_candidates/truths/loom/` — a file whose frontmatter disagrees with its own
directory, which `truths/_schema.md` requires to match. Nothing downstream
notices: `internal/knowledge` derives an artifact's scope from the directory
name and never reads the key.

`route_candidate_scope` in `extractors/extract.py` files each candidate by its
declaration instead, gated on `truths/<declared>/` existing — deliberately the
same gate as [The gate](#the-gate) above, not a looser one:

- **Nothing declared, or the declaration matches `--scope`.** Filed under
  `--scope`, nothing reported.
- **Declared, a usable name, and the store has the directory.** Filed under the
  declared scope. The file's `scope:` and its parent directory agree again,
  which is the whole point.
- **Declared, a usable name, no directory.** Filed under `--scope` carrying
  `scope_mismatch: <declared>` in its frontmatter. The store's write path
  creates parent directories, so routing here would onboard a scope nobody
  opted into — the same reason the sweep skips a session rather than defaulting
  it, and the same reason onboarding is an explicit command.
- **Declared, but not a usable scope name.** Filed under `--scope` with
  `scope_mismatch:` as well. The declaration is model output steered by a
  transcript loom did not author, so it clears `NAME_PATTERN` before it can
  become a path segment, and `SCOPE_NAME_LIMIT` — 255 characters, the component
  limit on APFS and ext4 — before it is joined to a path at all: a longer name
  makes the directory lookup raise `ENAMETOOLONG` rather than report absence.
  That bound belongs to that call site in `extract.py`; the shared name pattern
  stays unbounded on both sides. A lookup that errors regardless leaves the
  candidate under `--scope`, flagged the same way. Every echo of a
  declaration — both notes and the frontmatter value — goes through
  `echo_scope`, which redacts before it truncates to 60 chars, marks a
  truncation with `...` so a cut name is not read as a shorter one, and reduces
  to `[A-Za-z0-9._-]`; a declaration carrying something credential-shaped is
  echoed as `redacted` whole, since the shape is the evidence and the bytes are
  not.

`scope_mismatch:` is the reviewer's only channel that needs no Go change: the
TUI renders a candidate's body verbatim, while `Artifact.Scope` comes from the
directory. The key in a stored file is always this process's verdict — a
model-emitted one is stripped from the frontmatter first, or a candidate that
routed cleanly would carry a flag loom never raised. A human then either
onboards the declared scope and moves the file, or corrects the declaration. The
trade-off is that an un-onboarded subject scope still costs a manual move — but
a candidate misfiled loudly is recoverable where one misfiled silently, as ~200
in the store were, is not.

The run's log.md entry and commit subject name every scope a run filed under
(`extract 92118425 | loom | 6 truth candidate(s) (1 → ticket)`), so a re-scope is
in the store's history rather than only in a directory listing.

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
