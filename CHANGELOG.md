# Changelog

All notable changes to Loom are recorded here.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
versioning follows [SemVer](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed

- The knowledge screen's AGE column, and the ordering of the list under
  it, now measure from when a fact was first noticed — the earliest
  parseable `date:` in the artifact's `sources:` block — instead of the
  file's mtime. mtime records when the pipeline wrote the file
  (extraction for a candidate, promotion or a later edit for a validated
  artifact), which is a property of the pipeline rather than of the
  fact, so an April discovery extracted last week read as days old and
  the column said nothing about whether the claim needed re-verifying.
  Artifacts whose sources carry no parseable date fall back to mtime, so
  no row goes blank; a `<YYYY-MM-DD>` placeholder is such a value. A date
  range — `2026-03-19 to 2026-03-22`, `2026-03-09/2026-03-10`, which the
  extractor emits for a session spanning several days — is read as its
  start, since that is when the fact was first noticed. Without that the
  ten ranged artifacts in the live corpus took the mtime fallback, which
  dated them to the newest timestamp in the store and — under the
  newest-first ordering in place at the time — sorted them into the first
  ten rows of the screen. The sort key and the rendered value come from
  one accessor so they cannot drift apart.
- The knowledge screen now lists artifacts oldest-first by that same
  first-noticed basis. Newest-first dated from when AGE meant "most
  recently extracted"; now that it measures how long ago a fact was
  learned, the rows worth looking at are the stale ones, and those were
  the ones that never appeared without scrolling. Candidates still
  precede validated artifacts — that grouping is about actionability,
  not age — and are themselves ordered oldest-first.

### Fixed

- Promoting or rejecting a candidate now leaves a commit in the
  knowledge store's git repo, with a `promote truth <scope>/<id>` /
  `reject truth <scope>/<id>` subject. The gestures were a plain file
  write plus a remove, so the store the durability and auditability
  claims rest on recorded nothing: every review decision since the store
  was created lived only as working-tree state, and anything reading the
  repo's history — index regeneration, any later consumer — saw the
  corpus stand still. A promote commits the two paths it moved between,
  the written destination and the removed candidate. A reject commits the
  decision rather than the file: one entry appended to the store's
  `log.md` in the convention that file already uses — `## [YYYY-MM-DD]
  reject <id> | <scope> | <type> candidate <filename> archived` —
  together with the candidate's removal, and with the archived file
  deliberately out of the pathspec. Committing the archive tied the
  record to that tree's storage policy, and the two want opposite
  things: the record has to be durable, while the archive is a thousand
  discarded claims that do not belong in corpus history. With
  `_candidates/_rejected/` gitignored, every reject failed on an ignored
  path and left no record at all; keeping the file out means the archive
  can be tracked, gitignored, moved or pruned without touching the audit
  trail. The filename is in the entry because a re-run of the extractor
  emits siblings under one id, so id and scope alone do not say which
  candidate was rejected. An absent `log.md` is reported rather than
  created — that file is bootstrapped at store init, and a TUI pointed at
  the wrong root must not scatter one. Both commits are path-scoped
  (`git add` over those paths, then `commit --only` with the same
  pathspec) because the live store's tree is routinely dirty with
  untracked candidates and edits the gesture did not make; a whole-tree
  commit would absorb them into the record. `log.md` is the one
  exception, since the extractor appends to it without committing, so a
  reject carries any pending extraction entries along with its own. A
  candidate that was never committed leaves git nothing to record for
  its removal, so its path is dropped from the pathspec rather than
  failing the commit on a pathspec that matches nothing. Signing is
  pinned off and every git call is bounded at ten seconds, since a
  passphrase prompt or an index lock inside the fullscreen TUI is
  unrecoverable. The repo has to be the store itself, not one that
  merely encloses it — `rev-parse` walks up, and a knowledge root
  sitting inside a git-managed home directory would otherwise have its
  review decisions committed to that unrelated history. A store that is
  not a git repo of its own, a git call that fails, or a store with no
  `log.md` to record the decision in, degrades rather than skipping
  silently: the files still move — undoing them would throw away the
  human's review decision — and the reason goes both to the status line
  and to `~/.loom/knowledge-git.log`. The status bar, which these
  reasons are the first text long enough to overflow, is now clamped to
  the window width at the render site rather than left unbounded.

## [1.3.0] — 2026-08-16 — Automatic knowledge extraction

### Added

- `loom retrospect <namespaced-ticket-id>` runs the extraction pipeline
  over every summarized session whose commits carry the ticket's
  `[<id>]` subject marker, for truths and for decisions, so closing a
  ticket can push what it taught back into the store. Candidates land in
  `_candidates/<type>s/<scope>/` with their `session:` and `ticket:`
  sources filled in by `extract.py`'s existing derivation, and each run
  appends one `## [date] retrospect <ticket-id> | <scope> | N truth
  candidates, M decision candidates` entry to the store's `log.md` —
  skipped, with a logged reason, when `log.md` doesn't exist, since that
  file is bootstrapped at store init and not by the extractor. `truths/`
  and `decisions/` are never written: promotion stays human-gated. The
  marker is matched in Go rather than by a SQL `LIKE`, because a tk id
  may legally contain `_`, which `LIKE` reads as a wildcard. The
  at-most-once ledger is deliberately neither read nor written — it
  exists to stop the unattended trigger double-spending, and the sweep
  has usually already visited a just-closed ticket's sessions, so
  honoring it would no-op the command in exactly the case it exists for;
  candidate filenames carry a run timestamp, so a re-run files siblings.
  Unlike a sweep, which no-ops rather than crash-looping the daemon, a
  foreground retrospect exits non-zero on a malformed ticket id, a
  missing `extract.py`, an extraction backend absent from the absolute
  path `extract.py` invokes it by (`$PATH` is not consulted, because the
  script doesn't consult it either), or a failed extraction. A ticket
  with no commits in the summary DB is reported and exits 0, as are
  sessions skipped for the sweep's own reasons. A summary DB predating
  the commits table is an error rather than an empty answer, which would
  read exactly like a ticket that landed nothing.
- Extracted candidates now cite the tickets their source session worked
  under: one `ticket:` entry per id in the `sources:` block, alongside the
  `session:` entry that is already forced there. The ids come from git's
  own commit confirmation lines (`[main 2bbeb99] [loom/x-1a2b] Subject`)
  in the session's raw jsonl, correlated back to a `Bash` tool_use so a
  commit-shaped line quoted inside some other tool's output can't pose as
  one. Never a prose mention of an id — a transcript is thick with those,
  and citing every ticket a session merely discussed would make the field
  worthless. A session that landed commits under several tickets gets an
  entry each, first seen first, capped at 32 so a hostile transcript can't
  append unbounded lines to every candidate a run emits. The scan reads
  the raw jsonl rather than the preprocessed transcript, because
  preprocessing truncates non-error tool results to 500 chars and commit
  hooks print enough preamble to push the confirmation line past that.
  Model-emitted `ticket:` list entries are stripped — including when the
  derivation yields nothing, which is the ordinary case for a session that
  committed nothing — since an id the model chose has been validated by
  nobody. That keeps the field trustworthy without pretending to be a
  trust boundary: `knowledge.Rank` matches citations by substring over the
  whole artifact, so a model after a `--for-ticket` hit can name the id in
  its claim prose instead. What bounds that is promotion — `Rank` ranks
  only `status: validated` artifacts, so a planted citation has to get past
  a human before it can surface. Citations are allowed to dangle: nothing
  resolves them at read time, and a renamed, closed or deleted ticket does
  not invalidate the truth — a truth that dies with its ticket was never
  durable enough to promote.
- Knowledge extraction now resolves a session's scope from its repo's
  `.loom-project` marker, so `--watch` and `--backfill` file the same
  repo's candidates under the same name the marker declares rather than
  under whatever its git remote's basename happens to be. The session's
  recorded cwd is a real absolute path, so the marker is read
  opportunistically: when that path is a directory on this host, the walk
  up to its repo root runs as `resolve_project.py`'s does — nearest usable
  marker wins, an unusable one continues the walk rather than ending it,
  never above the repo root, a symlinked marker skipped rather than read,
  since the cwd is client-supplied, and a winning marker below the repo
  root warned about, since it is as likely a vendored subtree's own
  declaration as the project's. Everything else falls back to the git
  remote — a cwd that has moved or never existed here, a chain holding
  nothing usable — because the marker is an additional source of truth,
  never a new way to fail: a session that resolved before still resolves.
  A marker-derived name clears the same gate a remote-derived one does
  before it becomes a `--scope` argument and a path under the store. The
  extract log now names the derivation (`source=marker`,
  `source=git-remote`) — including in a `--backfill --dry-run`'s plan
  line, which is the only place a run that spends nothing can report it —
  names the marker file that won, logs a marker that disagrees with the
  remote outright, since one of the two is then stale and only an
  operator can say which, and says why a marker that exists was declined
  rather than leaving it indistinguishable from no marker at all. Those
  lines state a fact about a repo, so each is stated once per run rather
  than once per session, and a rejected name is echoed truncated: the
  marker is read through a 4 KiB bound and its value is arbitrary content
  reached through a client-supplied path, so neither the read nor the
  audit line it lands in is unbounded. Two deliberate divergences
  from `resolve_project.py`: this path lowercases the marker's value —
  resolved scopes are lowercase by construction here, and an exact-case
  derivation would make `--backfill --scope Loom` stop matching sessions
  it matches today — and it counts a marker naming a scope with no
  `truths/` directory as unusable, a check `resolve_project.py` has no
  knowledge store to make.
- `loom extract --backfill` — an operator-run pass over the historical
  backlog the trigger's watermark excludes, which is where most of the
  durable knowledge captured before the trigger existed still sits. One
  pass, no watch loop, and no per-sweep cap, so hundreds of sessions
  aren't paced at four per quarter hour. `--dry-run` reports the
  selection — sessions per resolved scope, and how many are excluded for
  which reason — while spending nothing: no LLM call, no candidates, no
  ledger entry, not even a watermark stamp on a host where the trigger
  has never run. `--scope` restricts the run to named scopes so one can
  be judged before committing to the rest, and `--limit` bounds it,
  stopping between sessions. Both are validated rather than trusted: a
  scope is matched case-insensitively and rejected when the store has no
  directory for it, a negative `--limit` is rejected instead of reading
  as unbounded, and passing any of the three to a sweep is an error even
  when the value looks like the default. The backfill shares
  `~/.loom/extract.state` with the trigger, so neither re-extracts what
  the other visited and an interrupted run resumes where it stopped;
  ledger writes now merge under a file lock, so a backfill running for
  hours and the agent's quarter-hour sweep can't erase each other's
  records, and the backfill re-reads the ledger before each extraction so
  a session the trigger claimed mid-run is logged and dropped rather than
  paid for twice. Being a foreground run, it appends its own output to
  `~/.loom/extractor.log` as well as printing it, so the trail of what it
  spent survives without a shell redirect. Unlike a sweep, it logs skips
  without recording them: most are `unknown scope`, and recording those
  would mean creating `knowledge/truths/<scope>/` later could never
  rescue the sessions it was created for.
- `.loom-project` — a repo-root marker naming the project a path
  belongs to, so knowledge scopes, session cwds and the tk registry
  stop drifting into three competing lists. One line of plain text,
  `#` comments allowed above it, owned by the project and read-only to
  loom and tk: neither ever writes one, and this repo now carries its
  own. `extractors/resolve_project.py` is the resolver, importable and
  runnable on its own (`./resolve_project.py [path]` prints the name on
  stdout and its reasoning on stderr). It resolves the path, walks up to
  the repo root — the first ancestor with a `.git` entry, file or
  directory, so worktrees and submodules count — and takes the nearest
  usable marker. The walk stops at the repo root, so a stray marker in
  `$HOME` can't capture an unrelated repo, and a marker that wins below
  the root is used but flagged, because a vendored subtree's
  declaration shouldn't read like the project's own. A name must be one
  safe path segment (`^[a-z0-9][a-z0-9._-]*$`, exact case — `Loom` is
  rejected rather than lowercased, since folding a human declaration
  would hide the typo); a marker that fails it, holds nothing, is a
  symlink, or isn't UTF-8 is ignored with the reason logged and the
  repo directory's basename used instead, so a repo without a marker
  still resolves. The resolved name is then checked against tk's
  registry, which never changes it: an unregistered name is a warning,
  and so is a registry that can't be located, read or decoded — never a
  failure. Resolution writes nothing, anywhere. `extract.py --scope
  auto` runs it against `--project-path` (default: cwd) and exits
  telling the caller to pass an explicit `--scope` when no name can be
  reached.
- Knowledge extraction now runs without a human command. The new
  `com.loom.extractor` LaunchAgent (`loom extract --watch`, installed by
  `loom install server` and `loom install extractor`) sweeps summarized
  sessions and runs `extractors/extract.py` over each one, so candidates
  land in `~/.loom/knowledge/_candidates/` and appear in the TUI review
  screen. Scope comes from the session's git remote basename; a session
  with no remote, one whose remote yields an unsafe scope name, one from an
  agent the extractor's preprocessor can't read (codex, for now), or one
  whose scope has no directory in the knowledge store, is skipped with the
  reason logged rather than filed under a default scope. The sweep is
  bounded by a watermark stamped when the agent first runs, so it covers
  new sessions and leaves the historical backlog to a deliberate batch run.
  Every visited session is recorded in `~/.loom/extract.state`, so a
  session is extracted at most once and re-running a sweep is a no-op.
  `extract.py` is resolved via `LOOM_EXTRACTORS_DIR` (default
  `~/code/loom/extractors`) because it isn't in the release tarball; when
  it's missing, sweeps log a no-op instead of crash-looping and
  `loom status` shows the unresolved path. The extractor's tunables
  (`LOOM_EXTRACTORS_DIR`, `LOOM_KNOWLEDGE_ROOT`, `LOOM_EXTRACT_PROVIDER`,
  `LOOM_EXTRACT_MODEL`) are persisted to `$LOOM_HOME/extract-env` at
  install, so the auto-updater's re-install — which runs from the updater
  daemon's environment — reproduces the same plist.

### Fixed

- The updater no longer treats a successful install as proof that an agent
  restarted onto it. An install's exit code says the launchd job was
  re-registered, nothing more, so a job that bootstrapped without ever
  spawning — or one launchd never respawned — kept serving the pre-update
  image while the log reported success. Observed in production: a daemon
  ran a twelve-day-old process image ten days after a newer binary landed,
  which is how a pre-v4 summarizer went on downgrading the schema marker
  (above) long after a v4 binary was on disk. Every agent is now held
  against the artifact after its install: a bounded poll for a live pid
  whose start time — from `launchctl print`, then `ps -o etime=`, which
  unlike `lstart` is not locale-formatted — is not older than the binary's
  mtime. A job still processless partway through the window gets one
  `launchctl kickstart`; bootstrap has re-registered the launch constraint
  by then, so it is a nudge rather than the activation path that cannot
  swap a differently-signed binary. Every outcome is logged by label,
  including the one where the binary can't be stat'd: with nothing to
  compare against, the log says the image was not verified rather than
  claiming a restart it never observed. The updater's own job is checked
  from inside the detached re-exec helper, the only process that outlives
  the teardown far enough to see the result; its output previously went
  nowhere and is now appended to `updater.log`. This was never a
  regression in the earlier re-bootstrap fix, which runs and has always
  covered every agent — and which equally cannot cover an out-of-band
  replacement, where something other than the updater writes the pinned
  binary and no install runs at all. `loom status` applies the same
  comparison per agent, so a stale daemon is visible without running `ps`
  by hand.

- An older loom no longer silently downgrades a newer `summaries.db`.
  `migrate` guarded the outdated direction only, so a binary opening a
  database *ahead* of its own `schemaVersion` fell through the check and
  then stamped its lower version over the marker — after which it kept
  folding sessions in the old shape, leaving the tables it did not know
  about simply unwritten with nothing to surface the gap. Observed in
  production: a store recorded schema 3 while holding the v4 `commits`
  table, and roughly seven weeks of commit rows were never extracted
  because a pre-v4 summarizer had been running against it. `Open` now
  returns `ErrSchemaTooNew` — distinct from `ErrSchemaOutdated` — before
  applying the schema or touching the marker, and names both versions.
  The remedy is updating loom, deliberately not `--rebuild`, which would
  discard a database that is not corrupt, only unreadable by this binary.

- The extraction trigger no longer writes client-supplied session identity
  into `~/.loom/extractor.log` unescaped. Agent, session id and source path
  reach the sweep from a remote shipper, so a control character in one let
  that shipper open lines of its own in the log — a forged
  `skip claude-code/x: extracted` made an unhandled session look handled in
  the one record of what the sweep declined to do. Values that aren't plain
  printable text are now quoted (ordinary UUID ids and paths still read
  unquoted), and the receiver rejects an agent, project or session id
  carrying a control character at ingest, so such an identifier no longer
  reaches the summary DB, the lines of `receiver.log` that record it, or an
  on-disk `<session_id>.jsonl` name. The project-identity fields
  (`git_remote`, `cwd`) are not covered by that check and still reach
  `receiver.log` unescaped.
- `extractors/extract.py` no longer derives a candidate's filename from an
  unsanitized model-emitted `id`. Ids that aren't a single safe path
  segment are skipped with a warning, and each write is confirmed to land
  inside the candidates directory — a transcript can no longer steer the
  extraction model into writing markdown outside the knowledge store.

## [1.2.2] — 2026-06-27 — Persisted receiver token

### Fixed

- Receiver bearer token is now persisted to `~/.loom/receiver-token`
  (0600) instead of living only in the receiver plist's
  `EnvironmentVariables`. `loom install receiver` resolves the token from
  `LOOM_RECEIVER_TOKEN` (seeding the file), then the persisted file, then
  an interactive prompt; non-interactive installs with neither error
  rather than blocking on stdin. The receiver daemon reads the persisted
  token at runtime, and the token no longer appears in the plist (so it's
  not exposed via the plist file or `launchctl print`). This fixes the
  updater's re-bootstrap of the receiver, which shells `loom install
  receiver` with no token in its env.

## [1.2.1] — 2026-06-20 — Reliable release activation

### Fixed

- Auto-updater now activates a new release binary on macOS. After
  installing the downloaded artifact it bootout+bootstraps each loaded
  loom agent (via `<bin> install <component>`) instead of `launchctl
  kickstart`. `kickstart` without `-k` is a no-op on a running daemon
  (stale in-memory code), and even `kickstart -k` can't respawn a
  differently-signed binary under launchd's managed Launch Constraint
  (`EX_CONFIG`), so the new code never ran. The updater re-bootstraps
  itself last via a detached `loom updater reexec` helper, because a job
  can't bootout its own launchd job from within. Re-bootstrap uses
  role-neutral component installs, so it never changes the machine's
  persisted role.

## [1.2.0] — 2026-06-20 — Release-driven updates

### Added

- `loom relevant --for-ticket <namespaced-id>`: read-only command that
  scans the validated knowledge corpus at `~/.loom/knowledge/` and prints
  a ranked markdown list of related truths and decisions. Ranking
  precedence is direct citations → evidence-path overlap → keyword
  overlap, with scope match breaking ties; each line carries the artifact
  id, claim title, relative path, score, and matched signal.
- Machine roles: `loom install server` (receiver + summarizer) and the
  new `loom install remote` (shipper) record the machine's role under
  `$LOOM_HOME/role`. `loom dev` and `loom status` scope health to the
  daemons that role expects, so a shipper-only machine no longer reports
  "degraded" for the receiver/summarizer it was never meant to run. The
  updater counts toward health only when its plist is installed.

### Changed

- Auto-updater now installs released GitHub Release artifacts instead of
  building `origin/main` from a source checkout. Each tick compares the
  running binary's version to the latest release, and on a newer release
  downloads the platform tarball (`loom_<ver>_<os>_<arch>.tar.gz`),
  verifies its `checksums.txt` entry, atomically installs the extracted
  binary over `~/.local/bin/loom`, and kickstarts every agent (itself
  last). This needs no git checkout and no Go toolchain on the host, and
  removes the `reset --hard` hazard entirely. `loom install updater` no
  longer requires a checkout (drops the `LOOM_SOURCE` env var). The
  released-artifact updater is the single install/update channel;
  development is a separate local `go build -o ./loom` (or `make dev`).
- `loom dev` health line shows the role and a per-role daemon count
  (e.g. `remote · 1/1 daemons`); with no role set it keeps the legacy
  format and prints a hint to run `loom install server`/`remote`.
- `loom status` prints the machine role, marks role-expected components
  that aren't installed instead of skipping them silently, and shows the
  sync-health section only when the shipper is installed.

### Removed

- Homebrew distribution channel. The `EnderRealm/homebrew-tools` tap
  publishing step is gone from `.goreleaser.yml` (no more
  `TAP_GITHUB_TOKEN`), and the brew install/upgrade instructions and
  tap-trust / cask-collision caveats are gone from the README. Loom
  deploys through a single channel: a source checkout plus the `loom
  updater` daemon on every fleet machine. Tagged GitHub releases stay
  as milestone markers and artifact stores, not an install path.

### Fixed

- Auto-updater no longer destroys local work on machines where the
  deploy checkout doubles as a dev checkout. The updater treated any
  `HEAD != origin/main` as "remote moved forward" and ran `reset
  --hard origin/main`, clobbering uncommitted changes, resetting
  unpushed commits, and overwriting checked-out feature branch refs. A
  tick now deploys only on a pure fast-forward of a clean `main`;
  otherwise it logs the reason (dirty tree / not on main / HEAD
  diverged) and skips, converging on the next clean tick.

## [1.1.1] — 2026-06-09 — Automated releases

### Added

- GoReleaser release pipeline (`.goreleaser.yml` +
  `.github/workflows/release.yml`): pushing a `vX.Y.Z` tag
  cross-compiles `loom` for darwin/linux × amd64/arm64, publishes a
  GitHub Release with binary tarballs + `checksums.txt`, and commits
  the generated formula to the `EnderRealm/homebrew-tools` tap. See
  `docs/releasing.md`.

### Changed

- Homebrew install moves to the shared `EnderRealm/homebrew-tools` tap
  (`brew install enderrealm/tools/loom`) and ships prebuilt binaries —
  no Go toolchain required at install time. The hand-maintained
  `Formula/loom.rb` (and its README) are removed; the tap formula is
  generated on each release.

## [1.1.0] — 2026-06-08 — Machine dev-state + UI

### Added

- `loom dev` — at-a-glance machine development state in three sections:
  a green/yellow/red loom health rollup (green = all daemons up and no
  sync backlog, yellow = backlog, red = a daemon down), projects with
  dirty working trees (changed-file counts), and projects with ready
  tickets. Color via lipgloss, degrades to plain text when piped.
- `loom dev` "Unreleased changelog" section — flags projects whose root
  `CHANGELOG.md` has entries under `[Unreleased]`, signalling a release
  is pending before those changes are published outside the repo.
- Candidate review screen actions: promote, reject, and edit a
  knowledge candidate inline.
- `loom ui` dashboard `(s)ort` — cycle the sort column, ordered
  left-to-right across the visible columns.

### Changed

- `loom tui` renamed to `loom ui`; the `tui` name stays as an alias so
  existing muscle memory and scripts keep working.

## [1.0.1] — 2026-04-28 — Distribution + auto-update

### Added

- `loom updater daemon` self-managing auto-updater. Polls
  `origin/main` on an interval, pulls + rebuilds + kickstarts every
  loom agent (itself last) when new commits land. Mirrors the
  Ghostwheel deployer pattern.
- Homebrew formula (`Formula/loom.rb`) and tap publish flow
  (`Formula/README.md`) so users can install the loom binary without a
  Go toolchain.
- `docs/auto-update-pattern.md` — reusable template for
  Go-binary-on-launchd apps that want the same poll-rebuild-kickstart
  loop.
- `CHANGELOG.md` (this file).

### Fixed

- `Formula/loom.rb` URL interpolation: `v#{version}` (Ruby) instead of
  the literal `v#…` that an earlier hand-edit left behind.

## [0.4.0] — 2026-04-28 — Wire-level project identity

### Added

- `wire.IngestRequest.ProjectIdentity{GitRemote, Cwd, RootSlug}` —
  authoritative project handle carried end to end. Optional;
  pre-identity clients omit it and the receiver tolerates absence.
- Receiver writes `<session>.meta.json` next to each `<session>.jsonl`
  so downstream readers (summarizer, TUI) recover identity without
  re-shipping.
- `summaries.db` schema v3: `sessions.git_remote` and
  `sessions.cwd_raw`, with an index on `git_remote`.
- TUI groups projects by `git_remote → cwd → slug`; basename
  collisions like `EnderRealm/loom` vs `elsewhere/loom` no longer
  collapse, while cross-machine clones of the same repo do.
- Receiver logs each ingest's identity tag (`identity=remote=…` /
  `identity=cwd=…` / `identity=slug-only` / `identity=none`) for live
  debugging.
- `REPO` column in the TUI dashboard showing the canonical
  `owner/repo` per project.
- `loom status` reports per-agent `interval` and `last activity`.
- Fixture tests: `transport/internal/source.TestReadClaudeCwd*` (nine
  cases pinning sparse-headered first-line scanning),
  `internal/tui.TestLoadProjectsIdentityGrouping` (synthetic
  cross-machine collapse).

### Fixed

- `readClaudeCwd` now scans up to 200 head lines for the first cwd
  field. Previously it only inspected the first record, which is
  often a sparse-headered (permission-mode, file-history-snapshot)
  entry with no cwd — sessions matching that shape shipped without
  identity and the TUI duplicated them per origin slug.
- `launchd.Install` polls for the prior service to disappear before
  bootstrapping and retries on transient EIO. Closes a race where
  bootout returned while launchd was still tearing the old job down,
  causing "Bootstrap failed: 5: Input/output error" on reinstall.
- Codex `Unknown`-record accounting: counts now reflect actual
  occurrences (was Count=0 on first sight, never refreshed).
- Both parsers stamp `UnknownRecord.FirstSeen` from the transcript's
  own timestamp instead of `time.Now()` — re-summarizing the same
  input now produces byte-identical output.
- Summarizer log lines now carry timestamps (`log.Printf` instead of
  `fmt.Printf`).
- TUI dashboard column headers no longer run together
  (`WORKTREESAGENTS` / `SESSIONSACTIVITY`).
- `loom status` deduplicates the `state =` line that
  `launchctl print` emits three times for resource and jetsam
  coalition blocks.

### Changed

- `summaries.db` primary key is `(agent, session_id)` end-to-end,
  matching the natural read-side join. Schema bumped to v2 in this
  release, then v3 with the identity columns.
- Schema bumps return `ErrSchemaOutdated` from `summaries.Open` and
  surface a `--rebuild` instruction. The summary DB is permanently
  disposable; `loom summarize --rebuild` drops and re-folds from
  `~/.loom/received/`.
- `notify.State.Maybe` takes `(enabled, cooldown)` scalars instead of
  a `*config.Config` so the notify package no longer pulls in a
  typed-config dependency.

## [0.3.0] — 2026-04-27 — Single binary + KeepAlive shipper

### Added

- One `loom` binary with `shipper`, `receiver`, `summarizer`, `tui`,
  `install`, `uninstall`, `status` subcommands. The four standalone
  binaries (`loom-shipper`, `loom-receiver`, `loom-summarize`,
  `loom-tui`) are deleted.
- `internal/launchd` package owns plist generation, `plutil -lint`,
  and `launchctl bootout/bootstrap/kickstart`. One generator covers
  all components.
- `loom shipper daemon` mode: `KeepAlive=true`,
  `ThrottleInterval=10`, in-process `time.Ticker` driven by
  `interval_minutes` from `config.json`. Replaces the
  `StartInterval`-based one-shot plist.

### Changed

- `loom install <component>` does build + plist + bootstrap + verify
  entirely in Go. No more shell-out to `install.sh`, no
  `repoRoot()` heuristic. `install.sh` reduced to a transitional
  forwarder.

### Fixed

- Shipper no longer relies on launchd's `StartInterval`, which dasd
  coalesces aggressively on modern macOS (we observed gaps of 24h+
  on a 10-minute schedule). The daemon owns its own cadence and
  launchd just respawns it on crash.

## [0.2.0] — 2026-04-28 — Drift telemetry correctness

### Added

- Composite `(agent, session_id)` primary key across every
  `summaries.db` table; the read model and the storage model now
  agree.
- `loom summarize --rebuild` flag: drops and re-folds the summary DB.
- Fixture-driven parser tests under
  `internal/parse/{claudeparse,codexparse}/testdata/`. Host-only
  smoke tests stay for breadth coverage but no longer load-bearing.

### Fixed

- Codex parser's `Unknown`-record accounting bug (copy-then-increment
  on first sight, slice never refreshed).
- `FirstSeen` reproducibility: parsers stamp from the transcript's
  own timestamp, not wall clock.
- README description matches reality (shipper + receiver + summarizer
  + TUI + unified CLI, two agents).

## [0.1.0] — Earlier 2026 — Pre-rewrite history

The pre-cleanup state. Multi-binary layout, slug-derived project
identity, `StartInterval`-based shipper, separate `cmd/loom-summarize`
/ `cmd/loom-tui` / `transport/cmd/loom-shipper` /
`transport/cmd/loom-receiver` binaries. Preserved here so anyone
spelunking through `git log` sees where the architecture started.
