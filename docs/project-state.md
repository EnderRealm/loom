# Project state

Synthesis reads one transcript at a time, and a transcript says nothing about
the project around it: whether the repo is still worked on, how much is open,
when anything last shipped. Reconstructing that per transcript is expensive and
inconsistent, and some of it cannot be reconstructed at all — a store of
validated truths about a repo dormant since June reads as current from every
session that produced them. `project_state` is that context, computed once per
project and kept standing.

## Where it lives

A table in `summaries.db`, one row per project, rebuilt whole by the summarizer.

| | `summaries.db` | knowledge store | new artifact |
| --- | --- | --- | --- |
| Regenerable | yes — derived by construction | no — holds reviewed claims | yes, with its own writer |
| Git-tracked | no | yes | depends |
| Cost of a rebuild | one transaction | one commit and push per rebuild | a new sync path |

The state is derived from data loom already holds — the sessions and commits
tables and tk — so losing the table costs a rebuild and nothing else. That
rules out the knowledge store: it is git-tracked and written through a single
commit-and-push entry point (`docs/knowledge-store-writes.md`), so a table that
changes on every rebuild would be a stream of commits recording numbers that
are stale by the next one, beside artifacts that are meant to be durable. A new
file artifact would be a third location to regenerate and keep consistent,
without the transactional replace the table gets for free.

The table is not git-tracked and is not schema-versioned: it is created in
place when a summarizer opens the database and filled by the next sweep, so it
needs no `--rebuild` (see the comment on `schemaSQL` in
`internal/summaries/schema.go`).

### Reading it from /work

`/work` reads it through the CLI, never through sqlite:

```
loom project-state --project forge --json
```

That prints one object; without `--project` it prints an array of every
project, and without `--json` one line each. It is readable wherever the
summarizer's `summaries.db` is — the host whose `~/.loom/role` is `server`. A
`remote` host only ships sessions and has no `summaries.db` of its own, and
there the command errors rather than answering empty. An empty table, a project
with no row and a database that predates the table are errors for the same
reason: each would otherwise read as a fact about the projects.

## The project set

Every knowledge scope: each directory under `truths/` in the store the
extractor is configured with (`LOOM_KNOWLEDGE_ROOT`), skipping names that are
not scope names — `_`-prefixed entries and files such as `_schema.md`
(`extract.Scopes`). A scope is matched to tk by namespace name, so a scope whose
tk namespace is spelled differently counts no tickets.

Attribution is `synthesis-input`'s:

- A **ticket** belongs to its id's namespace.
- A **session** belongs to the namespace its checkout resolves to through tk
  (`ticket.CentralStoreForRepo`: configured path, git remote, directory name).
  Claude subagent sessions are folded into their parent and not counted apart.
- A **commit** belongs to the namespace its `[<id>]` marker names, else to its
  session's. The marker wins because it survives a commit made from another
  checkout or from a worktree that no longer resolves. A hash read under two
  sessions counts once.

## Fields

| Field | Meaning |
| --- | --- |
| `project` | The scope. |
| `computed_at` | When the rebuild ran; the window ends here. |
| `window_seconds` | The liveness window's length. |
| `last_commit_at`, `last_session_at`, `last_ticket_closed_at` | The latest of each, ever; `null` for none. A session counts at its end. |
| `commits_in_window`, `sessions_in_window` | Distinct commits landed, and sessions active, in the window. |
| `open_tickets` | Tickets in any status but `done` or `closed`, backlog included. |
| `tickets_closed_in_window` | Tickets whose closed date falls in the window. A ticket closed before tk stored closed dates has none, and never counts. |
| `dormant` | No commit, no session and no ticket closed in the window. |

## Rebuilds

The window is `loom summarize --state-window` (default `30d`; whole days or a Go
duration), stored on every row so a reader knows what the counts cover. The
reader does not re-window: counts over a different window come from a rebuild
with that flag.

`loom summarize` rebuilds after its sweep; under `--watch`, after the first
sweep and then at most every ten minutes, since a rebuild reads every session
and resolves every checkout through tk — about two seconds on a host with five
thousand sessions — against a sweep that ticks every few seconds.

A rebuild that fails — tk missing or exiting non-zero, a knowledge store with
no `truths/` — logs `project state: … — previous rows kept` and leaves the last
good rows, whose `computed_at` shows their age. It never fails the sweep. With
`-v` the rebuild logs its totals and one line per project:

```
project state forge: commits=0 sessions=0 open=300 closed=0 dormant=true
```

The summarizer's launchd plist carries the full `PATH` the other agents do, so
the rebuild finds tk; an existing install gains it on the next
`loom install summarizer`, which the updater runs on every release.
