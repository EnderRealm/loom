# Writing the knowledge store

Every write to `~/.loom/knowledge` goes through one entry point, and that entry
point commits what it wrote and pushes the commit. Committing is a property of
the entry point, not of the caller: a new writer carries no git code and cannot
forget one.

The store is its own git repo, and its history is the audit trail of what the
extractor produced and what a human then decided about it. A writer that leaves
its files as working-tree state pushes the store further from that history — the
state that put ~550 uncommitted candidates in the live store.

## The entry point

`internal/knowledge/store`:

```go
warn, err := store.Apply("promote truth loom/loom-notify-state", func(tx *store.Tx) error {
    if err := tx.WriteFile(dest, body); err != nil {
        return err
    }
    return tx.Remove(src)
})
```

`Apply` runs the closure, commits every path the closure touched as one record
with `message` as its subject, and pushes that commit to the branch's upstream.

- `warn` is the zero `store.Warn` when the record landed and was published. Its
  two fields are separate states, not one: `NotCommitted` means nothing records
  the work, `NotPushed` that the commit landed and is local only — which the
  next unit of work's push heals when the failure was transient, since a push
  carries every commit before it.
  Both are short reasons for a one-line status bar; the whole failure is in
  `~/.loom/knowledge-git.log`. `Apply` sets at most one of them, but the pair is
  not exclusive: a caller that composes its own record reason — the TUI's
  promote and reject, when the gesture's closure failed after the commit
  landed — sets `NotCommitted` beside the `NotPushed` the store returned. Check
  the two independently rather than as an either/or.
- `err` is the closure's error verbatim, likewise logged in full first — or the
  store's own when the root could not be opened, in which case the closure never
  ran.
- The commit happens **even when the closure failed**: the writes that landed
  before the failure are on disk either way. Nothing touched means no commit,
  and neither does a pathspec that turns out to hold no change — a declared path
  that did not change (`$EDITOR` quit without saving) is not a failed record.

The push is the gesture's, immediately after its commit, because the store's
history is the only copy of a human's promote and reject decisions — candidates
are recoverable by re-extraction, decisions are not — so the window between a
commit and its publication is the store's real exposure. A failed push never
rolls the commit back: the local record is correct and complete, and the next
unit of work's push is this one's retry, so there is no retry machinery. That
holds for a transient failure. A push rejected as non-fast-forward — the
likeliest steady-state failure for a store shared across machines — is rejected
identically on every later gesture, and the store stays unpublished until a human
pulls or rebases; nothing here pulls on their behalf. A store with no remote, no
upstream for the branch, or a detached HEAD degrades with a stated reason rather
than an error, the way a store that is not a repo does. Every git call is bounded
on its own, and a unit of work is the sum of the several it makes — that bound is
what keeps an unattended sweep from wedging on a child blocked with nobody there
to answer it. The TUI's gestures stay responsive for a different reason: their
commit runs off the update loop as a `tea.Cmd`, so the sum is never paid on a
frame.

`Apply` writes the store at `knowledge.Root()`. `ApplyIn(root, message, fn)` is
the same against a named root, for the caller that resolves the store some other
way — `internal/extract` takes it from the extractor's persisted tunables, which
may name a store the process environment does not. The root travels with the
unit of work, so the containment check, the writes and the commit are all held
against the same store.

`ApplyDeferred(root, message, fn)` is `ApplyIn` with the commit handed back
rather than run: the writes have landed when it returns, and the record does not
exist until the returned `Commit` is called. A dropped `Commit` skips the record
silently — nothing catches it, `go vet` included — so this is for the caller that
must not block where `ApplyIn` commits, which today is the TUI's promote and
reject and nothing else. A writer that can block uses `Apply` or `ApplyIn`.

`Tx` is the whole write vocabulary:

| Op | Effect |
| --- | --- |
| `WriteFile(path, body)` | Writes a file, creating parent directories |
| `Remove(path)` | Deletes a file |
| `Rename(from, to)` | Moves a file, creating the destination's directories; records both sides |
| `Append(path, text)` | Appends to an **existing** file — never creates one, so a wrong root cannot scatter a `log.md` |
| `Touch(path)` | Declares a path mutated outside the Tx (`$EDITOR`), so it is committed; writes nothing itself |
| `Droppable(path)` | Marks a recorded path whose record lives elsewhere: an ignored one leaves the pathspec instead of sinking the commit |

Every op is performed through an open handle on the store directory
(`os.Root`), so a path that leaves the store is refused by the syscall rather
than by a check on the pathname: a directory component swapped for a symlink out
of the store between a check and the write it guards would defeat any check made
on the name alone. A path that is lexically outside the store, and any path with
a `.git` component — the repository is the store's history, not its content, and
`os.Root` knows nothing about it — are refused by name first, so the caller gets
a reason it can read.

A path is also refused when any existing component of it is a symlink. That is
what makes the `.git` rule hold rather than merely read well: an in-store
`alias -> .git`, or a file that is itself a link to `.git/config`, reaches the
repository through a name with no `.git` in it, and the open root permits those
because their targets stay inside the root. Every symlink is refused rather than
the ones aimed at `.git`, which closes it by construction instead of by
enumerating targets — and costs nothing, since git records a symlink as a
symlink, so a write through one lands outside the tree git tracks and the commit
would record something git never had. `Touch` additionally refuses a directory:
it is the only op that records without writing, and a directory in the pathspec
would stage everything dirty beneath it.

Both sides of a `Rename` go through every one of these checks.

Refusal is per op, not a pass over the whole unit of work: a closure whose second
write is refused has already made its first, and that first write is committed,
as it is for any other failure part-way through. A root that cannot be opened —
absent, or not a directory — fails the whole `Apply`: a store is a git repo
with a `SCHEMA.md` and a `log.md` that no writer here produces, so a missing one
is a misconfiguration to report rather than a tree to create.

A write to the knowledge store staying in the knowledge store is a store rule
like any other, and it has to hold against `loom knowledge write`, whose plan is
a string a non-Go writer composed.

The rest of the store's rules live behind `Apply` too, and nowhere else: the
commit is path-scoped so the routinely dirty working tree is left alone, a root
that is not a repo or is merely inside one degrades with its own reason rather
than relaying a git failure, a failed commit unstages what it staged, and the
record text is flattened and bounded before it reaches a commit subject or the
log.

## Adding a writer in Go

Call `store.Apply` — or `store.ApplyIn` when you resolve the store root
yourself. That is the whole of it: see `commitEdit` in
`internal/tui/knowledge.go` and `appendRetrospectLog` in
`internal/extract/retrospect.go`, neither of which contains any git.

`ApplyDeferred` is not a third option to weigh. It is for a writer that cannot
block where the commit runs — the TUI's gestures, whose commit would otherwise
freeze a frame — and it moves the record's timing onto that writer, which is a
way to lose the record rather than a convenience.

## Adding a writer in Python

`extractors/knowledge_store.py` is a thin client over `loom knowledge write`. It
builds the plan and hands it to the binary; it contains no git at all.

```python
from knowledge_store import apply_changes, write_change

reason = apply_changes("build-wiki index.md", [write_change(path, body)])
if reason:
    print(f"knowledge store not committed: {reason}", file=sys.stderr)
```

A failed **commit** comes back as `reason` and is not fatal — the writes landed.
A failed **write** raises `StoreWriteError` and is fatal to the run. A commit
that landed but was not **pushed** is printed to stderr by `apply_changes`
itself rather than returned: the record exists and the next write's push carries
it, so it is not the caller's decision to make.

The binary is `LOOM_BIN` if set, else `loom` on `$PATH`. `internal/extract` pins
`LOOM_BIN` to the running executable when it spawns `extract.py`, so a sweep
writes through the same build it was launched from.

Build paths from `knowledge_store.knowledge_root()` rather than a default of your
own: it resolves the store as the Go side does (`LOOM_KNOWLEDGE_ROOT`, else
`$LOOM_HOME/knowledge`, else `~/.loom/knowledge`), and a path under any other
store comes back refused.

## The extractor checkout is the production pipeline

The extraction daemon (`com.loom.extractor`, `loom extract --watch`) sweeps every
15 minutes and runs `extract.py` from the directory `LOOM_EXTRACTORS_DIR` names,
by default `~/code/loom/extractors`. The extractors are Python and are not in
the release tarball the updater installs, so there is no installed copy: that
checkout is what writes candidates into the live store. Editing
`extractors/*.py` there is editing production.

Every run that writes the store from that checkout — each sweep, each
`loom extract --backfill`, each `loom retrospect` — asks git for the checkout's
`HEAD` and whether `extractors/` holds uncommitted or untracked changes (ignored
files such as `__pycache__/` and `results/` do not count), and logs one line to
`~/.loom/extractor.log` before it extracts anything:

```
extractors dir=/Users/you/code/loom/extractors rev=<sha> dirty=false
```

That line is how a store commit is traced back to the extractor code that
produced it: the store commit's time falls inside a run, and the run names the
revision and whether the tree was dirty. When git cannot place the directory at
a commit (not a checkout, no commits yet, git unavailable) the line carries git's
error in place of the revision.

### The unattended sweep runs only committed code

Nobody watches the sweep, so it is the one run that is gated. When the directory
is dirty, or git cannot place it at a commit, the sweep logs `sweep skipped` with
the reason and stops before it reads or writes the ledger
(`~/.loom/extract.state`). Nothing is marked, so the sessions it would have
taken stay queued and are extracted by the first sweep after the change is
committed. While you edit, the daemon waits; commit to let it resume.

The check is repeated immediately before each extraction, since each one runs
`extract.py` afresh from the checkout and a sweep's extractions span minutes. If
the directory has become dirty or untraceable, or `HEAD` has moved off the
revision the sweep logged (a commit landed mid-sweep), the sweep logs
`sweep stopped` with the cause and ends there. The sessions it had not reached
stay unmarked and queued, and every extraction it did run is attributable to the
one revision line; the next sweep logs the new revision.

`loom extract --backfill` and `loom retrospect` are not gated: a human starts
them and watches their output in the foreground, and they run whatever the
checkout holds. The revision line says which.

### Failed sessions stay failed

A session the extractor fails on during a sweep or a backfill is recorded
`failed` in the ledger, and nothing retries it: the sweep and the backfill both
skip every session the ledger holds, and the only records ever dropped
automatically are scope skips, which never reached the extractor. `loom
retrospect` neither reads nor writes the ledger, so its failures burn nothing —
re-running the command is the retry.

That permanence is accepted rather than given retry machinery, on two different
grounds:

- **Sweep.** With the gate, the sweep can only fail a session on committed code.
  A committed extractor broken enough to burn sessions is a defect in reviewed
  code, handled as one — not a half-saved edit the sweep should have waited out,
  which is the case the gate removes.
- **Backfill.** A backfill run from a dirty tree can burn sessions on a
  half-saved edit, and those records are just as permanent. That is accepted
  because a human started the run and is watching it: every failure is logged
  as `FAILED` with its reason, and the revision line shows `dirty=true`.

To re-admit a burned session, delete its entry from `~/.loom/extract.state`
(the ledger is JSON keyed `<agent>/<session-id>`) while no sweep or backfill is
running — a running one rewrites the file from its own snapshot and would put the
entry back. A session summarized after the watermark is then picked up by the
next sweep; one from before it needs another `loom extract --backfill`. A
session that landed a ticket-marked commit can also be re-extracted without
touching the ledger through `loom retrospect <ticket>`, though its `failed`
record stays. There is no flag that does any of this.

## The plan contract

`loom knowledge write` reads one JSON plan on stdin:

```json
{"message": "extract abc | loom | 2 truth candidate(s)",
 "changes": [
   {"op": "write",  "path": "...", "body": "..."},
   {"op": "append", "path": "...", "text": "..."},
   {"op": "remove", "path": "..."},
   {"op": "rename", "from": "...", "to": "...", "droppable": true},
   {"op": "touch",  "path": "..."}
 ]}
```

- `droppable` applies to `rename`'s destination; it is the only op that takes it,
  and a plan that sets it on any other is refused rather than quietly losing the
  declaration.
- Unknown op, missing required field, or unparseable JSON: exit non-zero with the
  reason on stderr, nothing written. The plan is validated whole before anything
  is applied.
- A path that does not land inside the store, that names the store's own `.git`,
  or that is reached through a symlink, is refused by the store rather than by
  the plan's own validation, so every op and both sides of a rename are covered.
  That refusal is per change:
  a plan whose second change is refused has already applied and committed its
  first, and exits non-zero with the reason.
- On success: `{"warn": "<reason or empty>", "push_warn": "<reason or empty>"}`
  on stdout, exit 0. A failed commit is a warning, not a failure, since the
  writes landed; a commit that landed but was not pushed is `push_warn`, since
  the record exists and the next write publishes it.
- A failed write exits non-zero with the reason on stderr, and still prints the
  warn line for the changes that landed before it.

## Decision: a subcommand, not a documented duplicate convention

The alternative was to leave both languages writing the store directly and
document the convention — the rules of a path-scoped commit — for each writer to
re-implement. That is what the store had, and it is why the extractor and the
TUI had drifted into two implementations of the same rules, one of which
(`extract.py`'s) had to be written twice: once for the files and once for the
commit.

The subcommand was chosen because it gives one implementation. The store's rules
live in the repo that owns the store, in the language its loader and its TUI are
already written in, and there is exactly one place where "and commit" has to be
right. A convention would have needed the same care in every writer that ever
appears, and a writer that skipped it would look identical to one that did not
until someone read the store's history.

The cost accepted: the Python side now needs the loom binary present. A missing
binary fails the run — deliberately, since the alternative is a fallback that
writes uncommitted files, which is exactly the second writer this removes.
