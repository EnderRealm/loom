# Loom — remaining work

## Verification automation

The `How to verify` section in each truth file contains runnable shell commands, but nothing executes them programmatically.

- [ ] `extractors/verify.py` — takes a truth file path, extracts code blocks from `## How to verify`, resolves the project root from the scope directory name, runs the commands, dumps output for human review. Not auto-pass/fail — just surfaces results.
- [ ] Standardize "Expected:" prose in truth files into a machine-checkable form (regex, exit code, line count). Defer until verify.py reveals which patterns are common.
- [ ] Add `--verify` flag to `extract.py` that runs verification on each candidate before emitting it. Candidates that fail verification get demoted or flagged.

## Promotion workflow

Promotion from candidate to validated truth is fully manual.

- [ ] `extractors/promote.py` — takes a candidate truth (from raw output or JSON), runs its verification commands, and on human approval writes it to `knowledge/truths/<scope>/`. Should refuse to write without at least one passing verification command.
- [ ] Consensus detection script — scan matrix results for extras that appear across 2+ model configs. Currently done ad-hoc in the aggregator extras view; should be a first-class output with the full candidate text (claim + verify) ready for review.
- [ ] `_candidates/` directory per scope for truths that pass extraction but haven't been human-reviewed. Currently candidates only exist in JSON result files.

## Pre-extraction summarization pass

Truth extraction from forge's session summaries outperforms extraction from loom's raw preprocessed transcripts, despite the raw path having strictly more information. The summary acts as a free compression pass: implicit claims become explicit, signal is pre-organized into labeled sections (Discoveries, Decisions, Problems), and the truth extractor works on 500-1000 words instead of 10k-50k. Two focused LLM passes (summarize → extract) beat one broad pass (understand conversation + extract truths simultaneously).

- [ ] Design a lightweight summarization prompt for loom — not forge's full 8-section template, just enough to compress a preprocessed transcript into explicit claims. Focus on: what was discovered, what was decided, what went wrong and why. Skip the archival metadata (ticket IDs, outcome rating, metrics) — loom's transport already captures those quantitatively.
- [ ] Wire it into the extraction pipeline: `preprocess.py` → summarize (one LLM call) → `extract.py` on the summary. Compare against direct raw extraction on the same sessions to validate the hypothesis holds for loom's preprocessor output specifically, not just forge's cruder transcript.
- [ ] Evaluate whether the PreCompact hook approach (free inline summary during compaction) is worth adopting in loom. Cost is zero extra tokens, but it couples loom to Claude Code's hook system and only fires on sessions that compact.

## Extractor improvements

- [ ] Haiku defect-as-truth false positive — the reframe rule in the prompt doesn't stick on haiku. Candidates like `ticket-edit-acceptance-append` appear in nearly every haiku run. Either strengthen the prompt or add a post-extraction filter.
- [ ] gpt54-med anomaly — non-monotonic scoring (worse than both low and high). Investigate whether this is consistent or a fluke. If consistent, remove medium from the matrix.
- [ ] Claude preamble resilience — parser is fixed, but the prompt should also be strengthened to reduce preamble frequency. Codex never emits preambles; claude does ~50% of the time.

## Batch and scale

- [ ] Batch runner for all 94 forge sessions and 120 ticket sessions. Current matrix runs 3 sessions × 7 configs. Production should run gpt5-low on every session summary in `forge-data/sessions/`.
- [ ] Dedup across sessions — the same truth extracted from 5 different sessions should result in one truth file with 5 source entries, not 5 files. Need a merge/dedup step after batch extraction.
- [ ] Forward extraction hook — trigger extraction automatically when a new session summary lands in `forge-data/sessions/`. Could be a git hook, a watcher, or a cron job.

## Knowledge layer growth

- [ ] Runbooks — the overview identifies runbooks as a separate durable asset type. The Path A/B/C analysis pattern from the apr08 session is a clear runbook candidate. No extraction mechanism exists for runbooks yet.
- [ ] Decisions — another durable asset type from the overview. Decisions with `(human)` tags in session summaries are extractable but need a different prompt/schema.
- [ ] Universal truths — no truth has been promoted to `universal/` yet. The review-gates truth (found in forge apr08, forge mar14, and ticket mar25 independently) is the strongest candidate. Promotion requires rewriting the claim to be project-agnostic.
- [ ] Truth staleness — `verified_at` dates exist but nothing checks them. A truth verified 6+ months ago should be flagged for re-verification. Could be a simple `find knowledge/truths -name "*.md" | xargs grep verified_at` + date comparison.
