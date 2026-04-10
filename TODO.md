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

## Pre-extraction summarization pass — DONE

Two-stage pipeline (summarize → extract) is built and validated. `summarizer.md` compresses ~178K preprocessed transcripts into ~11-18K structured summaries with explicit Discoveries, Problems, Corrections, and Decisions sections. Key findings:

- Two-stage matches or beats single-pass on every metric (5/5 vs 3/5 applicable hits on apr08)
- Summarizer model matters less than expected: haiku/sonnet/opus all tie at 5/7 on familiar sessions
- On unfamiliar sessions, sonnet (0.72) and gpt-5.4 xhigh (0.75) lead; haiku is unreliable (produces chat responses instead of summaries)
- Recommended config: sonnet summarize → gpt5-low extract
- `--summarize`, `--summarize-provider`, `--summarize-model`, `--summarize-reasoning` flags all implemented

Remaining:
- [ ] Evaluate the PreCompact hook approach (free inline summary during compaction). Forge's pipeline uses this but only fires on sessions that compact — non-compacting sessions get no summary. Our post-hoc approach covers all sessions. Worth adopting as a bonus signal, not as the primary path.
- [ ] Test whether forge's catch-up prompt (8-section template with friction tags) produces better summaries than our corrections/evidence-focused prompt when both run post-hoc. Current hypothesis: our Corrections section is the key differentiator (catches T4-style errors forge misses).

## Extractor improvements

- [ ] Haiku defect-as-truth false positive — the reframe rule in the prompt doesn't stick on haiku. Candidates like `ticket-edit-acceptance-append` appear in nearly every haiku run. Either strengthen the prompt or add a post-extraction filter.
- [ ] gpt54-med anomaly — non-monotonic scoring (worse than both low and high). Investigate whether this is consistent or a fluke. If consistent, remove medium from the matrix.
- [ ] Claude preamble resilience — parser is fixed, but the prompt should also be strengthened to reduce preamble frequency. Codex never emits preambles; claude does ~50% of the time.

## Batch and scale

- [ ] Batch runner for all 94 forge sessions and 120 ticket sessions. Current matrix runs 3 sessions × 7 configs. Production should run gpt5-low on every session summary in `forge-data/sessions/`.
- [ ] Dedup across sessions — the same truth extracted from 5 different sessions should result in one truth file with 5 source entries, not 5 files. Need a merge/dedup step after batch extraction.
- [ ] Forward extraction hook — trigger extraction automatically when a new session summary lands in `forge-data/sessions/`. Could be a git hook, a watcher, or a cron job.

## Knowledge wiki — STARTED

Knowledge artifacts (truths, decisions, future runbooks) should form a navigable wiki with cross-references, an index, and an ingestion log. Pattern from Karpathy's LLM wiki: raw sources → LLM-maintained wiki pages → schema/config layer.

- [x] `knowledge/index.md` — generated catalog of all artifacts with one-line summaries, organized by type and scope, with markdown links.
- [ ] Cross-type linking — truths should link to related decisions and vice versa. Currently `related:` fields only reference siblings within the same type. Need a `## Related` section at the bottom of each file with rendered markdown links across types.
- [ ] Auto-link discovery — when a new artifact is extracted, scan existing artifacts for overlapping claims/choices and suggest cross-links. Could use the LLM judge or keyword matching.
- [ ] `knowledge/log.md` — append-only chronological record of extraction events. Format: `## [2026-04-09] session 88f1615b | forge | 5 truths, 3 decisions`. Updated by extract.py on each run.
- [ ] Regeneration — index and cross-links should be regenerated on every extraction run, not manually maintained. `build-wiki.py` or a post-extraction hook.

## Knowledge layer growth

- [ ] Runbooks — the overview identifies runbooks as a separate durable asset type. The Path A/B/C analysis pattern from the apr08 session is a clear runbook candidate. No extraction mechanism exists yet. Would follow the same `--extract-type runbook` pattern as truths and decisions.
- [x] Decisions — extraction pipeline built. 6 training + 10 eval decisions across forge, ticket, tracker. Uses `--extract-type decision`.
- [ ] Mental models — the most abstract asset type. "Sessions → raw history, Tickets → current intent, Truths → durable memory." No extraction mechanism yet — may be too abstract for automated extraction.
- [ ] Universal truths — no truth has been promoted to `universal/` yet. The review-gates truth (found in forge apr08, forge mar14, and ticket mar25 independently) is the strongest candidate. Promotion requires rewriting the claim to be project-agnostic.
- [ ] Truth staleness — `verified_at` dates exist but nothing checks them. A truth verified 6+ months ago should be flagged for re-verification. Could be a simple `find knowledge/truths -name "*.md" | xargs grep verified_at` + date comparison.
