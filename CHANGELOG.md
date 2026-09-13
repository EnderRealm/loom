# Changelog

All notable changes to Loom are recorded here.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
versioning follows [SemVer](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed

- A Codex `/work` run's report no longer omits its parent session. The
  Codex render's run record names no `session_id` (no session id is
  reachable from a shell on that runtime), so the report metered only the
  routed lens and called its telemetry `complete`. `internal/runs` now joins
  such a record to the session whose recognized `/work` invocation names the
  run's ticket, in the run's runtime, and spans the run's start, claiming it
  so the same work is not also listed as a transcript-origin run; the run
  and the report carry `transcript_basis` (`declared`, `invocation` or
  `transcript`), and the `loom ui` run header shows it. More than one
  candidate is an `ambiguous_join` diagnostic and no join. A root with no
  transcript is now a named telemetry gap, and the report reads `partial`.

## [1.8.0] — 2026-09-12 — Run visibility

### Added

- Thirty-second visibility into an active run. `config.json` takes
  `interval_seconds` and `summarizer_interval_seconds`: a positive seconds
  value overrides the minute cadence, an absent one keeps it, and zero or
  negative is rejected. `loom install summarizer` bakes the configured
  interval into the plist as `--interval` (reinstall to change it), and the
  summarizer stamps the database with the end of each completed sweep. The
  `loom ui` run detail reloads itself every 5 seconds with one load out at a
  time — a tick or `r` during a load is coalesced into it — keeping the
  cursors and scroll across reloads. Its header carries a `Freshness` line
  (last successful load, `stale` with the error when a reload fails, while
  the last good report stays up) and a `Pipeline` line (the summarizer's
  last sweep and the local shipper's last sync, each `stale` past twice its
  configured cadence and never under 30 seconds, or that neither is
  recorded).
  Tokens remain what the transcripts recorded: a pending execution reads
  `pending` with tokens unavailable. `internal/pipeline` carries complete
  parent, child, Weft and routed-lens records from producer files through
  the shipper, a receiver, the summarizer and the run view in one process,
  and checks late and post-outage records land exactly once.

- `loom ui` runs screen (`w` from the dashboard) and `loom ui --run <id>`:
  the last 30 days of runs as one sortable row each — ticket, date, outcome,
  telemetry completeness with the pending count, last observed, wall,
  execution and tool time, tokens, tool calls, failures, children and cost —
  with `s` cycling the sort across the metric columns, and a run detail
  showing the report's header, time, per-scope totals, failure classes, the
  execution hierarchy with a node cursor (`j`/`k`), stages with attempts and
  retries, and lens attempts (`n`/`p`) whose whole stored response opens on
  enter. Rows are `runreport.SummaryOf` over the same report `run-report`
  prints, so the two cannot disagree; what the report could not measure
  renders as unavailable, never as zero. Both loads run off the update loop,
  so the list takes keys while a query is out.
- `loom run-report --run <id>` and `internal/runreport`: one JSON document
  per run measuring everything attributable to it. `cost-report` meters the
  parent span and the subagent rows it can see; this reads the execution tree
  `internal/runs` assembles and meters every execution's transcript once —
  nested children, routed lenses, Weft stage retries — into parent-only,
  descendant and total scopes, with per-execution, per-stage, per-lens and
  per-attempt breakdowns and the tree, unresolved list and diagnostics
  rendered in the output. Tokens stay per runtime under a `cache_semantics`
  label so a Codex cache read, recorded inside its input, is never counted
  twice; failures are classed by source (`tool`, `api`, `process`, `other`)
  apart from `stop_hook` signals; wall clock, summed execution time, tool time
  and `cost-report`'s `active_ms` are reported as four named measures, the
  last with its overlap semantics beside it. Outcome comes from the run record
  alone (`completed`, `failed`, `stopped`, `running`, `unknown`) and telemetry
  completeness is reported separately, with `gaps` naming each session not
  folded, execution still pending or dispatch with no usage rather than
  reporting zero. A model with no rate leaves cost null and every other
  metric standing. `workreport` exports the pricing helpers and
  `HumanInteraction`, and `runs` exports `SpanningInvocation`, so the report
  shares those rules rather than copying them.

### Fixed

- The shipper released its pass lock only when the process exited, so with
  `flock` per open description the daemon's next tick was refused as another
  shipper until the leaked descriptor was garbage collected. The lock is now
  held for the pass and released at its end.

## [1.7.0] — 2026-09-12 — Session metrics baseline

### Added

- Attribution stamps: a producer that runs an agent in a throwaway
  working directory can record the checkout that run is about in
  `~/.loom/attribution.jsonl`, and the Codex adapter resolves session
  identity through it. warp's `codex-lens.sh` starts each `/work` review
  lens in an empty `mktemp -d` so the reviewed repo cannot instruct its
  own reviewer; loom keys project identity on the reported cwd, so every
  dispatch arrived as its own single-session project — 254 of them, and
  272 of the ~310 project rows on this host were throwaway directories.
  The cost was not only a cluttered dashboard: `resolveScope` derives a
  session's knowledge scope from its cwd and git remote, and a throwaway
  root has neither, so every Codex review verdict was silently skipped by
  extraction. The stamp is consulted only for a cwd under a temp root,
  and every failure mode — no registry, a malformed line, no matching
  record — leaves the session with the identity it reports today. The
  storage slug still names the directory the agent ran in; identity is
  the seam that moves. Producer contract in
  `docs/attribution-stamps.md`.
- `scripts/repair-attribution.py` re-applies attribution stamps to the
  Codex identity sidecars already on disk. The capture pass writes identity
  only alongside new transcript bytes, so a session captured before stamps
  existed keeps the throwaway cwd in its `.meta.json` and nothing revisits
  it. The script walks `transport/staging/codex-cli` and
  `received/codex-cli`, applies `attribution.go`'s rule as written —
  ephemeral cwd, newest stamp at or before the session's `session_meta`
  timestamp, newest stamp when that is unknown, nothing when every stamp
  postdates the session — and rewrites each matched sidecar with the
  stamped checkout and its resolved remote, in the same field order and
  omitempty shape as `wire.ProjectIdentity`. Dry run by default; `--apply`
  writes, atomically and 0600, and exits non-zero if any write failed. It
  touches nothing else: not transcripts, cursors, offsets or the summary
  DB.
- `loom cost-report` reports what each `/work` run in `summaries.db` cost,
  as JSON over the same `--since`/`--until`/`--db` flags as `work-report`:
  wall clock from the invocation to the run's own commit, active time (turn
  wall clock plus tool duration inside the span), turns, tokens, tool calls
  by kind, subagents dispatched and their summed duration. Session duration
  cannot stand in for ticket cost — a third of the sessions that commit a
  ticket commit two or more — so cost is an intra-session span, and
  `work-report` already recognizes those spans from transcript content, so
  the anchor needs no new instrumentation and covers history nobody
  instrumented. Null means unmeasurable throughout: an abandoned run never
  runs its span to the session end. Whether a run committed and when are
  answered separately on purpose — a ticket-named commit anywhere proves
  the run committed, but only a commit inside the run's own window can time
  it, or `/work` re-invoked on one ticket would charge the first run with
  the second's span.
- Each cost-report run carries the conditions it was measured under: every
  distinct model, reasoning effort and CLI version its turns recorded, the
  errors inside its span, and how many times the human interacted. A cost
  trend is unactionable without them — output tokens that grew between two
  windows say nothing if the model or the effort changed, or the human
  simply intervened more. The transcripts already carry the first three per
  record (Claude on every assistant record, Codex on every `turn_context`)
  but the summary DB kept them per session, so `turns` gains `model`,
  `effort` and `cli_version` (schema 6, NULL where the transcript carried
  none). Claude's API-error placeholder stamps `<synthetic>` as its model
  and is skipped rather than reported as a model that ran. A human
  interaction is a rule rather than a field: a turn counts when its stored
  user message, after any leading system-reminder blocks, is non-empty and
  is neither a harness envelope — slash-command tags, task notifications,
  local command output, Codex's expanded skill body or preamble — nor a
  `/work` invocation in any form. Tool results and Claude's meta skill body
  never open a turn, so they never reach the rule. One function over the
  shared `user_message` column, so Claude and Codex turns are judged
  identically.
- Each cost-report run is priced. Transcripts carry tokens and never cost,
  so `cost_usd` and `subagent_cost_usd` are computed from
  `internal/pricing/rates.json`, keyed by model and effective date and
  looked up at the run's invocation time so an old window is never repriced
  at today's rate. Cache reads and writes price at their own rates; a write
  is split by TTL from the `usage.cache_creation` breakdown, and a turn at
  `speed: fast` prices at the fast rates with the cache multipliers stacked
  on them. Anything that cannot be priced nulls the figure and names the
  cause in `pricing_warnings` — an unknown model or date, a turn with no
  model, a turn or dispatch whose records disagree on model or speed, fast
  mode without a fast rate, a dispatch with no transcript — rather than
  falling back to a default or to zero; a span with nothing to price costs
  0. Subagent cost is exact, not estimated: each dispatch's own transcript
  carries a full usage split and model, so the parser sums them into a
  per-dispatch usage record the store persists. That and the per-turn
  cache-creation and speed columns are schema 7, and `cost-report` refuses
  an older DB until `loom summarize --rebuild`. The table's five standard
  rates are required: they decode as pointers and a missing, misspelled or
  null one is rejected naming the entry and the field, where a plain
  float64 decode had turned it into 0 and priced that bucket free with no
  warning. An explicit 0 still parses.
- Execution records, the v1 contract in `docs/execution-records.md`: a
  producer appends a `run` record per `/work` or Weft invocation and an
  `execution` record per unit of work to `$LOOM_HOME/executions.jsonl`, and
  they ship through the existing capture path under the agent
  `loom-executions`. One `/work` run is more than one transcript — the
  orchestrating session dispatches Claude subagents, those dispatch Codex
  children, the security lens is routed into a throwaway directory — and
  nothing in the transcripts says which run any of them belongs to. Schema
  8 adds `runs`, `executions`, `execution_diagnostics` and
  `execution_imports`, plus `sessions.parent_session_id` and `spawn_depth`
  read from Codex `session_meta`. The importer merges by id, keeps an
  execution's run immutable, and writes a diagnostic for every record it
  rejects or cannot associate, carrying ids only. `internal/runs` reads the
  hierarchy back, synthesizing transcript-recognized runs for history with
  children drawn only from explicit evidence. The `summaries.db` path is
  now escaped in the `file:` DSN, since a `#` in a temp dir truncated it
  and opened the wrong database during tests.
- Review-lens verdicts are kept whole. A `/work` run fans out to three
  lenses and each answers with one fenced `json` block, which reached the
  summary tables only through the 800-character tool-result cut that lands
  inside most verdicts' criteria lists. Both parsers now read every fenced
  lens block off the full record before the cut, and schema 9 adds
  `lens_responses` (raw block, decoded fields, structured context, criteria
  and findings JSON, source path and line) plus normalized `lens_criteria`
  and `lens_findings`; `response_id` is a hash of the transcript position,
  so a re-fold rewrites the same rows rather than duplicating them.
  `internal/parse/lens` is the one extractor: a block is parsed only when
  it is a JSON object naming a known lens and verdict whose context,
  criteria and findings fit the schema, and anything else is kept as
  malformed evidence with a reason. `workreport.Lenses` reads a run back as
  attempts keyed by lens, round and attempt — missing, dispatched, failed,
  responded or parsed, with superseded and late flags. A response places
  only through provenance: a dispatch id, a router call that ran alone, or
  an exclusive `cat` of the router's own redirect path. Quoted blocks in
  user text, compaction summaries, file reads and compound shell commands
  are stored but never answer an attempt. The compliance report's
  contamination and unverified-criteria metrics now come from those rows,
  and `runs.Run` carries the attempts for its transcript. See
  `docs/lens-responses.md`.

### Changed

- `summaries.db` moves from schema 4, which 1.6.0 wrote, to schema 9:
  a populated `subagents` table (5), per-turn `model`/`effort`/`cli_version`
  (6), per-turn cache-creation tokens and speed plus per-dispatch usage
  (7), the execution-record tables and `sessions.parent_session_id`/
  `spawn_depth` (8), and the lens-response tables (9). Each step leaves an
  existing database with columns absent or tables empty, and the watch-mode
  summarizer skips sessions whose file is unchanged, so `Open` returns
  `ErrSchemaOutdated` and **`loom summarize --rebuild` is required after
  upgrading**. The summary DB is disposable; the rebuild re-folds it from
  `~/.loom/received/`.
- The truth extractor asks for reusable claims instead of volume. The
  prompt instructed the model to over-extract — a 3–6 floor, "if you find
  fewer than 2 you are being too conservative", "the human reviewer
  filters" — and production ran at the instructed rate: 5.4 candidates per
  session, 1,201 active candidates from 222 sessions, none stating a
  reusable pattern, 849 of 965 lexical clusters singletons, and the
  reviewer that was supposed to filter had promoted 16 artifacts in total.
  The floor is replaced with a soft ceiling of two and zero stated as the
  normal output, tied to the existing `NO_TRUTHS` path. The Reusable
  criterion gains a reader test — a claim whose only reader is someone
  already editing that file fails it — plus a file-local-facts exclusion,
  and the reframe rule gains a class-elevation step, so a mechanism is
  either raised to the rule it instances or dropped, guarded against
  widening a class the input never evidenced. The two `INPUT_GUIDANCE`
  constants carried the same pressure and are substituted into both
  extractors, since `build_prompt` selects them by input format rather than
  extraction type; they are reduced to type-neutral navigation hints so the
  decision prompt is not left carrying a damper against its own yield
  floor. Measured over the five sessions in `extractors/eval-data/`: 21
  candidates before, 6 after, and all six survivors state their reach
  explicitly rather than describing one file.

### Fixed

- Subagent durations are measured from the dispatch's own transcript. The
  `subagents` table has been in the schema since v2 and held 0 rows across
  2,185 sessions: the parser tracked sidechain messages and never wrote the
  record, and the only fallback, `tool_calls.duration_ms` for
  `tool_kind='task'`, stopped measuring the work when the harness moved to
  background agents — the call returns on dispatch, so the recorded value
  was the acknowledgement. Each shipped subagent transcript is now folded
  into its parent's summary and the duration taken from that transcript's
  own first-to-last record span; the join to the dispatch is the sidecar's
  `tool_use_id` against the parent's Task call, which also gives
  `parent_turn_idx`, and `agent_type` comes from the sidecar rather than
  the description, which `tool_calls.key_arg` already holds. Rows are one
  per dispatch, not per transcript: a Task call no transcript claimed still
  gets a row with a null duration, so "not measured" stays distinguishable
  from "returned instantly" — the distinction the whole measurement turns
  on. A transcript that fails to parse is bumped onto the parent's Unknown
  under a named marker rather than silently producing another NULL.
  Currency tracks the newer of the parent's mtime and its subagent
  transcripts', since a background dispatch outlives the parent's last
  record by construction and keying on the parent alone left a truncated
  span until the next rebuild. Measured over the live corpus rebuilt into
  an isolated `LOOM_HOME`: September's share of subagent durations under 5
  seconds falls from 865 of 906 (95%, the acknowledgement) to 4 of 918
  (0.4%, the work); mean reviewer 223s, mean coder 2579s. Schema 5.

- Extracted candidates are filed under the scope they declare. The model
  already names the project a truth is *about* in the candidate's `scope:`,
  and it is routinely not the project the session ran in — a loom session
  that debugs tk discovers a tk truth — but nothing read that value:
  `emit_candidates` wrote unconditionally to `base_dir / args.scope`,
  leaving ~200 candidates in a directory their own frontmatter disagreed
  with, which `truths/_schema.md` requires to match. `route_candidate_scope`
  files each candidate by its declaration, gated on `truths/<declared>/`
  existing — the same gate `scopeInStore` applies on the Go side, because
  the store's write path creates parent directories and an ungated route
  would onboard scopes nobody opted into. A declaration that is unusable,
  unonboarded, or that the filesystem cannot answer for keeps the candidate
  under `--scope` carrying `scope_mismatch:`, the one reviewer channel that
  needs no Go change since the TUI renders a candidate's body verbatim
  while `Artifact.Scope` comes from the directory. The declaration is model
  output steered by a transcript loom did not author, so it clears
  `NAME_PATTERN` and `SCOPE_NAME_LIMIT` before it is joined to a path — an
  unbounded name makes the lookup raise `ENAMETOOLONG` rather than report
  absence, which pathlib does not swallow — and every echo of it is
  redacted before it is truncated. A model-emitted `scope_mismatch:` is
  stripped so the key in a stored file is always loom's own verdict. The
  run's `log.md` entry and commit subject now name every scope a run filed
  under, and both extractor templates stop asking for "the session's
  project field", the wording that made the value ambiguous now that it
  routes. See `docs/knowledge-scopes.md`.

- Claude Code subagent transcripts never shipped. The Claude adapter
  listed only the `.jsonl` files sitting directly in a project
  directory and skipped every subdirectory, so the
  `<session>/subagents/` subtree — 1972 transcripts under 143 parent
  sessions on this host, the record of what each dispatched agent
  actually did — was enumerated by nothing and captured by nothing.
  They now enumerate at any depth beneath `subagents/`: 1794 sit
  directly in it and 178 nest one level deeper under
  `workflows/<wf_id>/`, and for 3 of the parent sessions every
  transcript is nested, so a one-level walk would still have shipped
  them nothing at all. A workflow directory's `journal.jsonl` (13 of
  them) is that workflow's bookkeeping rather than a dispatch, carries
  no sidecar, and is excluded. A subagent is identified under its
  parent — cursor key `<parent>.<agent-id>`, and a nested transcript's
  `<agent-id>` is its path below `subagents/` joined with `.`
  (`workflows.wf_5daf2eee-720.agent-a0e0`) — so every id stays a single
  path component the receiver's identifier guard accepts, and two
  dispatches under one parent can never share a staging file or an
  offset. `IngestRequest` grows an optional `subagent` object (parent
  session, agent type, description, tool use id, spawn depth); staging
  and `received/` both nest the transcript under `<parent>/subagents/`
  and persist that metadata in a `.subagent.json` sidecar beside it,
  refreshed each tick — rewritten only when it changed — so a sidecar
  Claude Code writes after the transcript's bytes were captured still
  ships, as long as some of those bytes are still unshipped. A request
  without the field lands exactly where it does today.

- Codex subagent sessions were being discarded whole. codex-cli 0.153.4
  writes `session_meta.payload.source` as an object describing the spawn
  (parent thread, depth, agent path) rather than the string loom typed
  it as, so the first line failed to unmarshal and every sweep dropped
  24 sessions — the subagent transcripts loom has no other record of,
  re-attempted and re-failed on each pass. The field is now
  `json.RawMessage`: nothing reads it yet, and keeping the producer's
  bytes leaves the spawn structure available without modelling it here.
  The deeper defect was that a payload-level decode failure aborted the
  whole file where an envelope-level one had always degraded, so a
  handler's unmarshal error is now counted as an Unknown record and
  parsing continues. Those records carry the drifted field's name —
  `session_meta::__unmodeled_payload__:source` — since the discarded
  error text was the only thing that made this class of drift visible,
  and the name is bounded by the same allow-list the TUI applies to
  untrusted transcript fields. `unknown_records` separates it from
  `__malformed__`: being behind the producer is actionable, a corrupt
  line is not. On this host the sweep goes from 24 errored to 0.

- The extractor's `claude` provider spawned an agent with every tool the
  host offers. Extraction is text in, text out — the transcript arrives
  on stdin and candidates come back on stdout — so `call_claude` now
  passes `--tools ""` to drop the built-in set and
  `--strict-mcp-config --mcp-config '{"mcpServers":{}}'` to drop the MCP
  servers, and the CLI's own init event reports `tools: []` and
  `mcp_servers: []`. Neither flag implies the other: `--tools ""` leaves
  the MCP servers loaded, and those are the wider exposure, since a
  stdio server runs in its client's process tree at the client's
  privilege and this client is an unattended LaunchAgent. Measured on
  one host before the change: 30 built-in tools and 45 MCP tools across
  12 servers. The `codex` provider already ran `--sandbox read-only`;
  the asymmetry was between the two branches of the same function, and
  the trigger pins `claude` as its default provider, so the unsandboxed
  branch was the one every unattended sweep took. Transcripts are
  untrusted input by construction (`docs/transcript-trust-and-redaction.md`),
  which is what made handing them to a tool-enabled agent worth closing.

## [1.6.0] — 2026-09-07 — Knowledge scope onboarding

### Added

- `loom knowledge scope add <name>...` creates `truths/<name>/` under the
  store the **extractor** resolves — its persisted `LOOM_KNOWLEDGE_ROOT`,
  which may name a store the invoking shell does not — and commits and
  pushes it through the store's single write entry point. Extraction is
  gated on that directory and there is deliberately no default scope, so
  a project nobody onboarded accumulates nothing rather than filing its
  truths under another project's name. The directory carries a
  `.gitkeep`, since git tracks files and not directories and an empty
  scope would otherwise exist only on the machine that made it. Names
  clear the same gate a derived scope clears, validated whole before
  anything is written, and a name that already has a directory is
  reported rather than refused. See `docs/knowledge-scopes.md`.
- `loom status` grows a `=== knowledge scopes ===` section: every scope
  this host's sessions resolve to, which of them the store has a
  directory for, how many sessions each un-onboarded one is costing, and
  the command that onboards it. It counts the whole summary DB rather
  than one sweep's window, since the backlog a scope would rescue is the
  number that decides whether onboarding it is worth anything — and
  without it, the sessions the sweep declines are visible only in
  `extractor.log`, which makes non-use of the knowledge layer read as a
  decline rather than as an onboarding step nobody took.

### Changed

- A session the sweep cannot resolve a scope for is no longer recorded
  in `~/.loom/extract.state`. The ledger is permanent — it is what makes
  extraction at-most-once — and such a session was never spent on, so
  recording it meant creating `truths/<scope>/` later could never rescue
  the very sessions the directory was created for. They are counted per
  sweep instead, the way the sessions below `--min-turns` already were:
  the pending scopes by name with the command that fixes them, and the
  failures no directory fixes — no git remote, an unsafe name — by
  reason, with no part of the offending name in the labels. The
  per-session skip lines went with the record: unrecorded means
  re-decided every tick, and a backlog a thousand sessions wide would
  otherwise restate them in `extractor.log` every 15 minutes.
- Scope-skip records written before that change are retired from the
  ledger on read, in memory and from the file at its next write, so
  onboarding rescues the sessions they would otherwise claim forever.
  Only those: an `extracted` or `failed` record paid for its run and is
  never dropped.

## [1.5.0] — 2026-09-02 — Committed knowledge writes

### Added

- A stated trust and redaction policy for transcript-derived artifacts,
  in `docs/transcript-trust-and-redaction.md`, with the redaction half
  implemented at the extraction path's choke points.
  `extractors/redact.py` holds an ordered table of credential shapes
  (PEM blocks, provider keys, github/slack/AWS/Google tokens, JWTs,
  bearer credentials, URL passwords, and secret-named assignments); each
  match becomes `[REDACTED:<kind>]`. It is applied in
  `preprocess.preprocess()` (every raw-jsonl consumer), in `extract.py`'s
  summary-input branch (which bypasses the pre-processor), on the
  few-shot reference examples read back out of the knowledge store, and
  on each candidate body before it is written — and on each tool result
  before `--max-result-chars` truncates it, since a key cut mid-token can
  fall below its pattern's minimum length. A marker is the same size
  whether it replaced one character or forty thousand, so each stage also
  prints a per-kind span and character count, and the sweep re-logs those
  lines from the child's stderr on a successful run — they were
  previously discarded with the rest of the child's output — so an
  over-broad pattern shows up in `extractor.log` rather than only in what
  a candidate is missing. The extractor prompts now substitute the
  transcript inside `<session-input>` markers and the few-shot examples
  inside `<reference-example>` markers, both neutralized in the
  substituted text by `fence_input()` so neither can close its span
  early, and state the stance the doc records: agent- and tool-authored
  text is data to summarize or extract from, never instructions.
- Knowledge extraction skips stub sessions: `loom extract --min-turns N`
  (tunable through `LOOM_EXTRACT_MIN_TURNS`, default 3, `0` disables)
  excludes sessions the summarizer folded fewer than N turns for, on
  both the sweep and the backfill. The exclusion is not recorded in
  `extract.state`, so changing the threshold re-admits the sessions it
  passed over, and the backfill dry run reports the excluded count as
  its own bucket so a threshold can be chosen from data.
- `loom work-report` reports per-`/work`-run compliance as JSON over
  `summaries.db` for a `--since`/`--until` range: fan-out dispatch,
  review iterations, contamination reports, unverified criteria, and
  ticket-edit span. Runs are recognized from transcript content across
  both Claude verdict-delivery shapes and both Codex invocation forms,
  and the runtimes are classified separately so an inlined Codex pass
  does not read as a skipped one. Anything the parser cannot resolve
  fails closed to unknown, never to compliant, and evidence a transcript
  can trivially write for itself is discounted: a verdict block must
  carry a known lens and verdict, inlined evidence needs distinct
  contract and quality verdicts, and a router call counts only when its
  recorded output holds a lens verdict.

### Changed

- The knowledge gestures no longer run their git on the bubbletea update
  loop. A promote or a reject is several git invocations in sequence,
  each bounded on its own by the store's timeout, and running them where
  bubbletea dispatches keys froze the frame for their sum — on a slow
  remote, seconds of a TUI that answered nothing. The move is what takes
  the candidate out of the list, so it stays where it was: it has landed
  when the gesture returns, the list reloads against it immediately, and
  the commit's outcome arrives afterwards on the status line, composed
  onto the line the gesture already set rather than replacing it. The
  seam is `store.ApplyDeferred`, which performs the write and hands back
  the commit to run later; what the commit does — the pathspec, the
  droppable paths, the push, `knowledge-git.log` — is still the store's,
  so no caller gained git code. The store now also serializes its own
  commits in-process, since two of them overlapping in one repo contend
  on `index.lock`, which git answers by failing rather than waiting.
  Quitting waits on a commit still in flight, reporting the wait on the
  status line: bubbletea abandons a Cmd's goroutine at exit and cmd/loom
  returns as soon as Run does, so leaving mid-commit abandons the
  sequence partway — the git child already running is orphaned rather
  than killed, but the push, the unstaging recovery and the outcome
  report never happen, stranding the gesture's already-moved file with a
  record that is at best partial and reported nowhere. The wait is
  bounded only by git, so a second press leaves anyway.
- The knowledge store's entry point now pushes the commit it just made,
  so a promote or a reject leaves the store's branch in sync with its
  upstream. Nothing ever pushed the store before: it was published only
  when a human happened to run `/work` in it, and since the gestures
  started committing, the unpushed window grew by a commit per gesture
  with no bound. Candidates are recoverable — `loom extract --backfill`
  produces them again — but a promote or reject decision is human
  judgment recorded nowhere else, so that window held the only
  irreplaceable part of the store. The push fires on the gesture rather
  than on a timer, and carries no retry machinery: a push is cumulative,
  so the next gesture's push is the failed one's retry — for a
  transient failure. A push a diverged remote rejects is rejected the
  same way on every later gesture, and the store stays unpublished until
  a human pulls; nothing pulls or rebases on their behalf. A failed push
  never rolls the commit back — the local record is correct and complete
  — and it is reported apart from a failed commit, `not pushed:` rather
  than `not committed:`, since the two are a recoverable state and a
  missing record. A store with no remote, no upstream, or a detached
  HEAD says so and carries on, and `loom knowledge write` reports the
  same outcome as a second `push_warn` field alongside `warn`. The whole
  failure, as ever, is in `knowledge-git.log`.
- Every write to the knowledge store now goes through one entry point —
  `internal/knowledge/store`'s `Apply` — which commits every path the
  write touched as one record. Committing was a property of each caller
  before: it had been bolted onto the two gestures anyone noticed
  (promote, reject) and open-coded per call site in two languages, so
  the TUI's `e` edit committed nothing and every future writer started
  in the same state, with the store's rules — path-scoped commits, an
  untouched dirty tree, the non-repo and enclosing-repo reasons, record
  sanitization — duplicated between Go and Python and free to drift.
  They now live in one Go package and nowhere else. The `e` edit leaves
  a commit as a consequence of going through it rather than as a third
  special case, and adding a writer needs no commit code at all:
  `extractors/build-wiki.py` commits its generated `index.md` in one
  line that mentions no git. A `retrospect` run's `log.md` entry goes
  through the same entry point and is now committed too, where before it
  was appended by hand and left for whatever writer next committed that
  file to absorb.
- The entry point confines a write to the store. Every op is performed
  through an open handle on the store directory, so a path that leaves
  the store is refused by the syscall rather than by a check on the
  pathname — a directory component swapped for a symlink out of the
  store cannot be slipped in between a check and the write it guards.
  The store's own `.git` is refused too, by name and case-insensitively:
  a plan that rewrote `.git/config` or dropped a hook would corrupt the
  repository or leave code to run under the next `git` command a human
  types in the store. So is any path reached through a symlink, which is
  what makes that rule hold — an in-store `alias -> .git` reaches the
  repository through a name with no `.git` in it, and a symlink whose
  target stays inside the store is one the open handle would otherwise
  follow. Every symlink rather than the ones aimed at `.git`: git
  records a symlink as a symlink, so a write through one lands outside
  the tree git tracks and the commit would record something git never
  had. Refusal is per op, not a validation pass over the
  whole plan, so a plan whose second change is out of store has already
  applied and committed its first — the writes that landed are still
  recorded, as they are for any other failure part-way through. The
  rules matter most for `loom knowledge write`, whose plan arrives as
  text from another language and which runs with the user's own reach:
  an absolute path elsewhere, a traversal, or a relative
  `LOOM_KNOWLEDGE_ROOT` resolved against an unexpected working directory
  would otherwise scatter files across the filesystem and report nothing
  worse than a commit warning. A root that cannot be opened — absent, or
  not a directory — now fails the write instead of being created.
- A unit of work whose paths turn out to hold no change now leaves no
  commit instead of reporting a failed one. Opening `e`, quitting
  `$EDITOR` without saving and finding "not committed: git commit: On
  branch main" on the status bar — plus a failure record in
  `knowledge-git.log` — was the shape this takes when a declared path is
  a file nobody edited.
- The Python extractor writes the store through the new `loom knowledge
  write` subcommand, which applies one JSON plan read from stdin. A
  subcommand rather than a documented convention each language
  re-implements: one implementation, living in the repo that owns the
  store. `extractors/knowledge_git.py` is replaced by
  `extractors/knowledge_store.py`, a client that builds the plan and
  contains no git. The cost is that the extractor now needs the loom
  binary — `LOOM_BIN`, which `internal/extract` pins to the running
  executable, else `loom` on `$PATH` — and a run that cannot find one
  fails rather than falling back to writing uncommitted files, which is
  the second writer this removes. A failed commit stays a warning, since
  the writes landed. See `docs/knowledge-store-writes.md`.

### Fixed

- A reject now commits the archived candidate alongside the `log.md`
  entry and the candidate's removal, so the store's only corpus of what
  the extractor got wrong lives in history instead of as untracked
  working-tree state. 1.4.0 kept the archive out of the pathspec to hold
  the record independent of that tree's storage policy; the independence
  is structural now rather than positional — the archive is passed as a
  path whose record lives elsewhere, so git ignoring it drops it from
  the pathspec and the `log.md` entry still lands, and a store with
  `_candidates/_rejected/` gitignored gets its record either way. The
  drop reaches only paths declared that way: a promote's destination is
  itself the record, so an ignored one still fails loudly instead of
  yielding a commit of the candidate's removal alone. Each dropped path
  is named in `~/.loom/knowledge-git.log`, since the commit that lands
  otherwise reads like any other.

- An extraction run that emits candidates now leaves one commit in the
  knowledge store's git repo, subject `extract <session> | <scope> | <n>
  <type> candidate(s)` — the same unit `log.md` already records, minus
  its date scaffolding. The extractor is the store's highest-volume
  writer and never touched git: it wrote candidate files, appended to
  `log.md`, and stopped there, which is where the 550 uncommitted
  candidates in the live store came from. Since it runs unattended on
  the LaunchAgent, every sweep pushed the store further from its own
  history. The commit covers the candidate files and the `log.md` append
  together, one per run rather than one per file, so `git log` reads
  back as a list of extraction events. A run that emits no candidates
  leaves no commit; the zero-count `log.md` entry it may still have
  appended rides along with the next run's, the same file-granular
  absorption a reject already accepts. The commit is path-scoped to what
  the run wrote, on the same rule the promote and reject gestures follow,
  because the live tree is routinely dirty with candidates awaiting
  review and edits the run did not make. A store that is not a git repo
  of its own, one that merely sits inside another repo, or a git call
  that fails, degrades rather than skipping silently: the reason is
  printed on a foreground run and always appended to
  `~/.loom/knowledge-git.log`, which is the record to rely on — a
  scheduled sweep keeps the run's output only when the extraction itself
  failed — and the extraction still succeeds, since a failed commit must
  not cost an unattended sweep its candidates. Git calls are bounded at thirty
  seconds, and signing and the store's hooks are both pinned off, since
  an index lock, a passphrase prompt or a hook that blocks has nobody to
  answer it on the LaunchAgent and would wedge every sweep that follows.

## [1.4.0] — 2026-08-22 — Auditable knowledge review

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
