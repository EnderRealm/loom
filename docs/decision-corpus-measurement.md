# Decision corpus measurement

`extractors/decision-extractor.md` rule 7 told the model that a typical session
yields 2-5 decisions and that finding fewer than 2 in a populated
`### Decisions` section meant it was filtering too aggressively. The truth
extractor carried the same floor until `loom/raise-truth-extraction-0c7e`
removed it: at the instructed rate it had produced 1,201 truth candidates from
222 sessions, none stating a reusable pattern, with 849 of 965 lexical clusters
singletons. This records the same measurement over decisions, which decides
whether rule 7 gets the same ceiling.

## The active corpus

`~/.loom/knowledge/_candidates/decisions/` held, on 2026-10-09:

| Measure | Value |
| --- | --- |
| Active candidates | 6, all scope `loom` |
| Sessions | 2 (`1edef100`: 4, `5c37b3b4`: 2) — 3.0 per session |
| Rejected candidates | 0 |
| Promoted decisions | 6 (`forge` 4, `ticket` 2), predating the store's externalization |
| Lexical clusters | 5, of which 4 singletons |
| Stating a rule past one file | 6 of 6 |

The same store holds 1,392 active truth candidates. The gap is structural, not
a difference in yield: the production sweep pins `--extract-type truth`
(`const extractType = extractTypeTruth`, `internal/extract/extract.go`), so
decisions come only from `loom extract retrospect` (`retrospectTypes` in
`internal/extract/retrospect.go` runs both types) and manual `extract.py` runs.
`log.md` records three decision runs in total (2026-08-14 twice, 2026-09-15).

The one non-singleton cluster is the two re-gate candidates from `1edef100`,
which contradict each other: "re-gate after validation suggestions" against
"skip re-gate for low-risk suggestions".

Six candidates can neither show nor rule out inflation. The corpus was never
produced at volume, so the measurement is taken on a sample extracted for the
purpose.

## Sample

Each session was extracted under the current prompt, before any change to rule
7, five sessions in parallel, one command per session:

```
python3 extractors/extract.py --extract-type decision --input <path> \
  --input-format <raw|summary> [--summarize] --scope <scope> \
  --provider claude --model sonnet --judge keyword --no-emit-candidates \
  --json-out <dir>/<id>.json --raw-out <dir>/<id>.raw.txt
```

- **15 production sessions.** `<path>` is
  `~/.loom/received/claude-code/<project-dir>/<session>.jsonl`, run with
  `--input-format raw --summarize` — the sweep's own pipeline. The selection is
  the most recent claude-code sessions the sweep marked `extracted` with at
  least one truth candidate, stratified by scope: `loom` 4, `forge` 3, `ticket`
  3, `warp` 2, `weft` 1, `ghostwheel` 1, `village2` 1. `--scope` is the
  session's scope from the sweep state.
- **5 eval sessions.** `<path>` is `extractors/eval-data/<file>.md`, run with
  `--input-format summary`. `--scope` is the file name's project segment
  (`forge`, `ticket`, `tracker`).

`--no-emit-candidates` leaves the knowledge store untouched. Counts are
`candidates_valid` from the `--json-out` result. Candidate bodies — the
`## Principle` counts, the hand classification and the appendix titles — come
from the `--raw-out` file, split on the `===END-OF-DECISION===` sentinel. The
run directories were scratch and are not kept.

### Per-session counts

| Session | Scope | Candidates |
| --- | --- | --- |
| `80622fdc` | weft | 5 |
| `ca290f97` | warp | 4 |
| `3974ae5a` | warp | 3 |
| `1cf91833` | loom | 7 |
| `3fe3a43e` | loom | 5 |
| `ae074817` | loom | 7 |
| `03939580` | loom | 5 |
| `46fb0b0c` | ticket | 3 |
| `616f5dfe` | ticket | 3 |
| `b24de800` | ticket | 2 |
| `0d9df8c4` | ghostwheel | 6 |
| `85f615d6` | village2 | 3 |
| `16d42d97` | forge | 0 (`NO_DECISIONS`) |
| `2c982c40` | forge | 8 |
| `2dbe1edb` | forge | 4 |
| `0916c546` | eval | 8 |
| `2e9a386c` | eval | 4 |
| `5bc59345` | eval | 3 |
| `aaa967e8` | eval | 5 |
| `c7dbbcca` | eval | 7 |

## Metrics

| Metric | Value |
| --- | --- |
| Total | 92 candidates from 20 sessions |
| Per session | 4.6 overall; 4.3 production (65 / 15), 5.4 eval (27 / 5) |
| Singleton share | 92 of 92 clusters (100%) at Jaccard ≥ 0.3; 88 of 90 (98%) at ≥ 0.2 |
| Stating a rule past one file | 91 of 92 |

**Per session.** 4.6 sits inside the instructed 2-5 range; 19 of 20 sessions
produced at least 2. For comparison, the sweep's truth extraction over the same
15 production sessions produced 40 candidates (2.7 per session), and the truth
prompt's pre-change baseline on the 5 eval sessions was 21 (4.2 per session).

**Singletons.** Clusters are single-linkage over title content tokens. The
truth figure (849 of 965) came from 1,201 candidates across 222 sessions; a
20-session sample spread over seven projects has less room to repeat itself,
so the 100% likely overstates what the full corpus would show and is not a
like-for-like figure.

**Reach past one file.** This metric separated truths (0 of 1,201) but does
not separate decisions. The decision template's `## Principle` section asks
for a transferable rule, and the model writes one for every candidate that has
the section — 91 of 92, the exception being #26. A generic maxim attached to a
one-function choice passes the metric. The discriminating question for a
decision is whether the *choice* binds work beyond the session, which takes a
hand read.

## Hand classification

Every candidate was read against that question and placed in one of three
classes. Numbers index the 92 sampled candidates ordered by session id,
production sessions before eval; [Appendix: baseline candidates](#appendix-baseline-candidates)
lists each with its session and class.

| Class | Count | Candidates |
| --- | --- | --- |
| Durable design decision | 35 | #1, 5, 6, 8, 12, 13, 14, 17, 19, 20, 21, 22, 23, 24, 26, 31, 35, 39, 40, 42, 45, 51, 52, 66, 68, 76, 78, 79, 81, 82, 85, 86, 87, 89, 90 |
| Process or workflow move in the run | 32 | #2, 3, 4, 7, 9, 10, 11, 18, 25, 27, 28, 29, 30, 32, 33, 37, 38, 44, 49, 50, 58, 59, 61, 62, 63, 65, 67, 73, 77, 88, 91, 92 |
| Local implementation choice | 25 | #15, 16, 34, 36, 41, 43, 46, 47, 48, 53, 54, 55, 56, 57, 60, 64, 69, 70, 71, 72, 74, 75, 80, 83, 84 |

**Durable design decision** — architecture, data model, interface or project
policy a later engineer working in the project needs. Examples: "Store
per-project state as a rebuildable table in summaries.db" (#12), "Store
tickets and sprints as markdown files only, with no SQLite layer" (#19),
"Keep Status in the struct for parsing and auto-migrate legacy tickets on
Parse" (#79), "Store DNF position as NULL with dnf=True, apply synthetic
positions only at scoring" (#82).

**Process or workflow move** — a step the run took under rules it was already
following: banking or declining a review suggestion, filing or bundling
tickets, picking the next ticket, stopping to surface a failure, commit and
amend handling, priority triage. Examples: "Bank a style-only review
suggestion in the handoff instead of reopening review" (#65), "Pick a ready
ticket over a higher-priority backlog ticket as the next work item" (#58),
"Bundle machine-name and timing frontmatter changes into one ticket" (#28).
Many restate the operating rule as the extracted principle: #30 quotes "NEVER
silently work around a bug in our own tools" verbatim; #33, #38, #49 and #65
restate the diff-freeze rule; #61 and #62 restate the follow-up filing rule.
The choice was made before the session started.

**Local implementation choice** — one function, one test parameter, one
constant, with a generic maxim as the principle. Examples: "Trim only trailing
whitespace on the quiet-commit result line" (#56), "Raise PipeDrainTests count
to 100 rather than shrink the new stress test" (#47), "Poll the ticket preview
query every 5s instead of event-based invalidation" (#70).

57 of 92 (62%) are not durable decisions. Durable yield is 35 over 20 sessions,
1.75 per session. 15 of the 20 sessions held at least one durable decision; the
five that held none are `16d42d97`, `2dbe1edb`, `ae074817`, `b24de800` and
`ca290f97`.

## Verdict

The sample shows the inflation the truth corpus showed. Yield tracks the
instructed floor rather than the input — 4.6 per session against a 2-5
instruction, where the durable yield is 1.75 — the excess is process moves and
local choices that bind nothing past the session, and the lexical corpus is
entirely singletons. Rule 7 therefore takes the truth extractor's shape: a
soft ceiling of two, matching the measured durable yield, and `NO_DECISIONS`
stated as a correct output. It departs from the truth wording in one place.
The truth corpus supported calling zero the correct output for most sessions;
here 15 of 20 sessions held a durable decision, so the rule says many sessions
have none, not most.

## After the change

The same 20 sessions were re-extracted under the current rule 7 with the same
command and pipeline. Durable counts use the hand classification; a survivor
counts against the baseline candidate it restates.

| Session | Scope | Before | After | Durable before | Durable after |
| --- | --- | --- | --- | --- | --- |
| `80622fdc` | weft | 5 | 2 | 1 | 1 |
| `ca290f97` | warp | 4 | 1 | 0 | 0 |
| `3974ae5a` | warp | 3 | 1 | 1 | 1 |
| `1cf91833` | loom | 7 | 2 | 4 | 2 |
| `3fe3a43e` | loom | 5 | 2 | 1 | 0 |
| `ae074817` | loom | 7 | 2 | 0 | 0 |
| `03939580` | loom | 5 | 1 | 2 | 1 |
| `46fb0b0c` | ticket | 3 | 1 | 2 | 1 |
| `616f5dfe` | ticket | 3 | 1 | 1 | 1 |
| `b24de800` | ticket | 2 | 0 (`NO_DECISIONS`) | 0 | 0 |
| `0d9df8c4` | ghostwheel | 6 | 1 | 2 | 1 |
| `85f615d6` | village2 | 3 | 1 | 2 | 0 |
| `16d42d97` | forge | 0 (`NO_DECISIONS`) | 0 (`NO_DECISIONS`) | 0 | 0 |
| `2c982c40` | forge | 8 | 2 | 7 | 2 |
| `2dbe1edb` | forge | 4 | 0 (`NO_DECISIONS`) | 0 | 0 |
| `0916c546` | eval | 8 | 2 | 2 | 2 |
| `2e9a386c` | eval | 4 | 1 | 1 | 1 |
| `5bc59345` | eval | 3 | 1 | 2 | 1 |
| `aaa967e8` | eval | 5 | 2 | 3 | 2 |
| `c7dbbcca` | eval | 7 | 2 | 4 | 2 |

| Metric | Before | After |
| --- | --- | --- |
| Total | 92 | 25 |
| Per session | 4.6 | 1.25 |
| Production | 65 (4.3 per session) | 17 (1.13 per session) |
| Eval | 27 (5.4 per session) | 8 (1.6 per session) |
| `NO_DECISIONS` sessions | 1 | 3 |
| Sessions over two | 18 | 0 |
| Sessions with a durable decision | 15 | 13 |
| Singleton share at Jaccard ≥ 0.3 | 92 of 92 | 25 of 25 |
| Carrying a `## Principle` | 91 of 92 | 25 of 25 |

### Survivors by class

All 25 survivors were read against the same three classes.

| Class | Before | After |
| --- | --- | --- |
| Durable design decision | 35 of 92 (38%) | 18 of 25 (72%) |
| Process or workflow move in the run | 32 | 3 |
| Local implementation choice | 25 | 4 |

The 18 durable survivors restate 19 baseline durable candidates: #1, 8, 13, 14,
19, 20, 23, 31, 39, 42, 45, 66, 68, 76, 79, 81, 82, 86 and 89. One survivor,
"Merge tk into forge as a TypeScript layer over markdown files, no SQLite",
merges #19 and #20. Every durable survivor has a baseline counterpart. Others
include "Throttle project_state rebuild to 10m under --watch; one-shot always
rebuilds" (#14), "Rebuild the dashboard client side-by-side in client-v2
instead of porting in place" (#23) and "Keep legacy Status readable and
auto-migrated on Parse, but never write it back" (#79).

The seven non-durable survivors:

| Survivor | Class | Baseline |
| --- | --- | --- |
| Halt adapter work until the shared observer contract it conforms to is built | Process | #50 |
| Hand-label escape fixtures without blame-based attribution or the tool's output | Process | none; same family as #59 |
| Ship despite a ticket_verify fail caused by tk's 120s bound, not a defect | Process | #62 |
| Score loom escapes precision on the blame-stage class, not the final class | Local | #36 |
| Lower the AC4 precision floor to 60% rather than add rules tuned to the fixture | Local | #34 |
| Wrap test setenv/unsetenv in the spawn lock instead of giving spawns explicit environments | Local | #46 |
| Record a quiet-commit echo only if the command itself wrote that subject | Local | #55 |

Each matches the class its baseline counterpart carries in the appendix.

### Cost of the ceiling

19 of the baseline's 35 durable candidates resurface. The other 16 split by
whether the ceiling removed them:

- **9 ceiling-bound**, from the four sessions with more than two durable
  candidates, each of which now emits exactly two durable survivors.
  `2c982c40` had seven (#19-24, #26); #19 and #20 resurface as one survivor
  and #23 as the other, and #21, #22, #24 and #26 — sprint location,
  activity-based navigation, accessibility and Observable Plot — do not.
  `1cf91833` loses #12 and #17, `c7dbbcca` loses #87 and #90, and `aaa967e8`
  loses #85.
- **7 below the ceiling**, from sessions that emitted fewer than two or filled
  a slot with a non-durable choice. `0d9df8c4` (#6), `03939580` (#5),
  `5bc59345` (#78) and `46fb0b0c` (#40) each emitted one. `3fe3a43e` filled
  both slots with local choices (#34, #36) and lost #35. `85f615d6` emitted
  one process move (#50) and lost #51 and #52.

The two sessions that held a durable decision before and hold none now are
`3fe3a43e` and `85f615d6`, both below-ceiling losses.

The ceiling-bound loss is the trade the truth prompt made: a session with more
than two durable decisions surfaces at most two of them. The below-ceiling loss
is not the ceiling's doing — the session had room and the model did not use it
on a durable decision.

### Against the earlier wording

An earlier rule 7 called zero "the correct output for most sessions". Run
against it, the same 20 sessions produced 27 candidates, 20 of them durable;
the current wording produced 25, 18 durable. One sample per wording cannot
distinguish a difference that small, and the softer wording did not raise
yield here. Below-ceiling loss is non-zero under both — 5 then, 7 now — and remains
the open question: some durable decisions are dropped by the model's own
selectivity rather than by the ceiling, and this sample does not show whether
that rate is stable or which wording moves it.

### Caveat

Each figure is one extraction per session per wording. Run-to-run variance was
not re-sampled; the truth change observed counts stable within ±2 across four
runs while the specific survivors varied.

## Appendix: baseline candidates

The 92 candidates of the pre-change sample, by title. Class is the hand
classification above: durable design decision, process or workflow move, or
local implementation choice.

| # | Session | Class | Title |
| --- | --- | --- | --- |
| 1 | `03939580` | Durable | Keep superseded truths as deprecated with a Superseded section naming the successor |
| 2 | `03939580` | Process | Promote the keepalive-daemon candidate and reject the four contradicting candidates |
| 3 | `03939580` | Process | Deliver knowledge-store changes from a clean worktree, not the dirty checkout |
| 4 | `03939580` | Process | Record candidate rejection reasons as a paragraph in log.md under the reject entries |
| 5 | `03939580` | Durable | Link a superseding truth to the deprecated one via related, not contradicts |
| 6 | `0d9df8c4` | Durable | Build cross-system log tracing into the admin console, not a standalone CLI |
| 7 | `0d9df8c4` | Process | Fold loss-path logging into the bus failure semantics ticket as an acceptance criterion |
| 8 | `0d9df8c4` | Durable | Defer JSON logging; rely on grep-able text conventions plus correlation work |
| 9 | `0d9df8c4` | Process | Make docs-only reconciliation edits from the MacBook without a runtime |
| 10 | `0d9df8c4` | Process | Flag strategic doc contradictions as open decisions instead of resolving them in docs |
| 11 | `0d9df8c4` | Process | File all review findings as P0 tickets to force focus |
| 12 | `1cf91833` | Durable | Store per-project state as a rebuildable table in summaries.db |
| 13 | `1cf91833` | Durable | Add project_state via CREATE TABLE IF NOT EXISTS, not a schema version bump |
| 14 | `1cf91833` | Durable | Throttle project_state rebuilds to every 10m under --watch |
| 15 | `1cf91833` | Local | Set PATH in the summarizer launchd plist so it can find tk |
| 16 | `1cf91833` | Local | Treat Options.StateWindow == 0 as "skip project_state rebuild" |
| 17 | `1cf91833` | Durable | Fix the liveness window at write time and expose no --window on the reader |
| 18 | `1cf91833` | Process | Stop at a server-side push rejection instead of retrying or working around it |
| 19 | `2c982c40` | Durable | Store tickets and sprints as markdown files only, with no SQLite layer |
| 20 | `2c982c40` | Durable | Port tk to TypeScript inside forge and retire the Go codebase |
| 21 | `2c982c40` | Durable | Store sprints in FORGE_DATA_DIR/sprints instead of repo-local .sprints |
| 22 | `2c982c40` | Durable | Organize the client around activities with project as a global filter |
| 23 | `2c982c40` | Durable | Build the redesign in a new src/client-v2 alongside the existing client |
| 24 | `2c982c40` | Durable | Treat accessibility as part of the keyboard-first design, not a non-requirement |
| 25 | `2c982c40` | Process | Ship Foundry v1 as an operational board and defer the visual pipeline to v2 |
| 26 | `2c982c40` | Durable | Use Observable Plot for charts instead of hand-rolled SVG |
| 27 | `2dbe1edb` | Process | Record machine via hostname -s and wall-clock duration in integer seconds |
| 28 | `2dbe1edb` | Process | Bundle machine-name and timing frontmatter changes into one ticket |
| 29 | `2dbe1edb` | Process | Capture the feature as a P2 ticket now and defer open specifics to spec |
| 30 | `2dbe1edb` | Process | Halt and surface a tool bug instead of silently working around it |
| 31 | `3974ae5a` | Durable | Score the Gin cell by the pinned target alone; keep the probe as preflight only |
| 32 | `3974ae5a` | Process | Commit warp-filed work-eval changes in the work-eval repo, not warp |
| 33 | `3974ae5a` | Process | Decline a quality-lens suggestion that exceeds ticket scope; bank it in the note |
| 34 | `3fe3a43e` | Local | Lower the SZZ precision floor to 60% instead of tuning heuristics against the fixture |
| 35 | `3fe3a43e` | Durable | Define escape rate as distinct introducing commits over commits, with credit as a separate column |
| 36 | `3fe3a43e` | Local | Score SZZ precision on the blame-stage class, not the post-join final class |
| 37 | `3fe3a43e` | Process | Verify new-CLI acceptance criteria with `go run` from the checkout, not the installed binary |
| 38 | `3fe3a43e` | Process | Apply review suggestions only in round 1; bank later ones rather than filing |
| 39 | `46fb0b0c` | Durable | Replace one-line capture input with a soft-wrapping box capped at 5 rows, no char limit |
| 40 | `46fb0b0c` | Durable | Keep pasted newlines collapsing to spaces in the capture prompt |
| 41 | `46fb0b0c` | Local | Derive capture row count from the prompt's rendered height, not a hard-coded 1 |
| 42 | `616f5dfe` | Durable | Derive empty-acceptance from one shared predicate on the stored body |
| 43 | `616f5dfe` | Local | Fix the test, not BareCriteria, when prose acceptance yields no bare criteria |
| 44 | `616f5dfe` | Process | Cut v8.7.0 (minor) when Unreleased holds Added and Changed entries |
| 45 | `80622fdc` | Durable | Serialize child spawns with a lock and set CLOEXEC on pipes, via one helper |
| 46 | `80622fdc` | Local | Take the spawn lock around test-side setenv/unsetenv instead of patching individual spawns |
| 47 | `80622fdc` | Local | Raise PipeDrainTests count to 100 rather than shrink the new stress test |
| 48 | `80622fdc` | Local | Use 16 dedicated Threads, not concurrentPerform, for the spawn stress test |
| 49 | `80622fdc` | Process | Treat a frozen diff as barring review suggestions, not must_fix items tied to an AC |
| 50 | `85f615d6` | Process | Stop and leave the ticket open when the contract it adapts to is not built |
| 51 | `85f615d6` | Durable | Reject a village2-local observation lifecycle to be conformed to shuttle later |
| 52 | `85f615d6` | Durable | Leave the Python-to-Go validation boundary to the shuttle contract's definition |
| 53 | `ae074817` | Local | Let `[WIP] thing` yield ticket id `WIP` rather than adding a `/` rule to markerTicketID |
| 54 | `ae074817` | Local | Do not recover commit subjects for bare-hash quiet commits in extractCommits |
| 55 | `ae074817` | Local | Record a quiet-path commit only if its subject appears in the command text |
| 56 | `ae074817` | Local | Trim only trailing whitespace on the quiet-commit result line |
| 57 | `ae074817` | Local | Hand-scan YAML in config.ProjectRepoPath instead of adding a yaml dependency |
| 58 | `ae074817` | Process | Pick a ready ticket over a higher-priority backlog ticket as the next work item |
| 59 | `ae074817` | Process | Leave tickets out of the hand-label fixture when the origin cannot be settled honestly |
| 60 | `b24de800` | Local | Reuse the shared runServer helper in MCP tests instead of a per-file shutdown join |
| 61 | `b24de800` | Process | Do not file follow-up tickets for criteria unverifiable only by source reading |
| 62 | `ca290f97` | Process | Ship despite AC3 verify failing at tk's 120s bound when the suite passes untimed |
| 63 | `ca290f97` | Process | Amend the commit to drop a stray artifact dir instead of pushing a follow-up commit |
| 64 | `ca290f97` | Local | Print the retention-skip line only for an absolute evidence root |
| 65 | `ca290f97` | Process | Bank a style-only review suggestion in the handoff instead of reopening review |
| 66 | `0916c546` | Durable | Clamp effort "max" to "high" in orchestrator dispatch, not in agent definitions |
| 67 | `0916c546` | Process | Disable the Board view tab and defer the KanbanBoard rewrite to its own ticket |
| 68 | `0916c546` | Durable | Keep the deprecated status field on the Ticket type as optional instead of removing it |
| 69 | `0916c546` | Local | Fetch closed tickets via MCP stage "done" filter instead of CLI tk closed |
| 70 | `0916c546` | Local | Poll the ticket preview query every 5s instead of event-based invalidation |
| 71 | `0916c546` | Local | Map idle runs with successful dispatches to "completed" on reload |
| 72 | `0916c546` | Local | Derive stage transition labels from advancing events, not the raw targetStage |
| 73 | `0916c546` | Process | File a P2 bug for generator vs gate verdict labels instead of fixing inline |
| 74 | `2e9a386c` | Local | Keep empty-listing encouraging messages in an external messages.txt |
| 75 | `2e9a386c` | Local | Use one encouraging empty-state message across all listing commands |
| 76 | `2e9a386c` | Durable | Test MCP handlers in-process via NewInMemoryTransports, not by swapping the installed binary |
| 77 | `2e9a386c` | Process | File separate bug tickets for gate/edit mismatch, note duplication, and body-field drops |
| 78 | `5bc59345` | Durable | Remove legacy status field entirely instead of deriving it from stage |
| 79 | `5bc59345` | Durable | Keep Status in the struct for parsing and auto-migrate legacy tickets on Parse |
| 80 | `5bc59345` | Local | Leave parsed legacy status value in memory instead of zeroing it |
| 81 | `aaa967e8` | Durable | Collect race data with httpx instead of adopting the Fast-F1 library |
| 82 | `aaa967e8` | Durable | Store DNF position as NULL with dnf=True, apply synthetic positions only at scoring |
| 83 | `aaa967e8` | Local | Treat F1 API positionNumber 666 as DNF regardless of completionStatusCode |
| 84 | `aaa967e8` | Local | Give all DNFs the same synthetic trajectory position, max_classified + 1 |
| 85 | `aaa967e8` | Durable | Verify against the real DB with integration tests instead of mocked pytest suites |
| 86 | `c7dbbcca` | Durable | Use Dolt as a query layer over forge-data, not as the ticket store |
| 87 | `c7dbbcca` | Durable | Use z.preprocess helpers instead of z.coerce for MCP numeric/boolean params |
| 88 | `c7dbbcca` | Process | Fix the MCP coercion bug before continuing the backlog cleanup |
| 89 | `c7dbbcca` | Durable | Run the tk commit journal sync loop inside each process lifetime, no daemon |
| 90 | `c7dbbcca` | Durable | Inject the logger into the commit journal via callback to protect MCP stdio |
| 91 | `c7dbbcca` | Process | Rate the tk commit journal as high risk because it sits on every ticket write |
| 92 | `c7dbbcca` | Process | Demote inflated P0 tickets so P0 means urgent during the V2 push |
