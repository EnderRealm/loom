# Candidate destinations

The truth extractor's output has two destinations, and every artifact names one
in a `destination:` frontmatter key:

- **`truth`** — a durable, reusable, evidence-backed claim, reviewed for
  promotion into `truths/<scope>/` and consumed by `/work` through
  `loom relevant`.
- **`ticket`** — a defect or improvement the session left unresolved, reviewed
  for filing into tk against the project whose code has to change, loom
  included.

Before this there was one destination, so a finding that was really a bug had
nowhere to go but `truths/`. `truth-extractor.md` compensated with a reframe
rule — "extract the mechanism, not the defect" — and the store filled with bug
reports dressed as truths that were neither good truths nor filed bugs. The rule
is gone: a defect is emitted as a ticket candidate, and the mechanism behind it
is emitted as a truth only when it passes the four truth tests on its own.

## Review queue, not auto-file

A ticket candidate is never filed by the extractor. It lands in
`_candidates/tickets/<scope>/` and waits for a human in the TUI's candidate
review screen, exactly as a truth candidate waits for promotion.

Decided 2026-10-07. Auto-filing was rejected because:

- **Promotion never happens automatically.** The store's own contract
  (`SCHEMA.md`, Lifecycle) puts a human between extraction and anything durable,
  and a filed ticket is durable work in someone's backlog.
- **The blast radius is the shared store.** The candidate directory is local
  and cheap to wrong. The tk central store is a git repo that replicates to
  every machine, so an LLM classification filed there is everyone's problem.
- **Most candidates are wrong.** The candidate corpus held 2,454 files when this
  was decided, most of them rejected or still pending. Auto-filing at that
  precision would flood the backlog.

Because nothing auto-files, there is no unattended write to tk to bound.

## Extraction

`extract.py` reads `destination:` off every truth-extractor artifact
(`split_destinations`):

- **`truth`** with a `## Claim` and `## How to verify` → `_candidates/truths/<scope>/`.
- **`ticket`** with a `## Problem` and `ticket_type: bug | feature` →
  `_candidates/tickets/<scope>/`.
- **Anything else** — no destination, an unknown one, or one whose shape the
  artifact lacks — is dropped with a stderr warning. A missing destination is
  never defaulted to `truth`: defaulting is the reframe the key exists to end.

Ticket candidates get everything truth candidates get: the authoritative
session-id override, the `ticket:` source citations derived from the session's
commits, scope routing by the declared `scope:` (gated on `truths/<scope>/`,
see [knowledge-scopes.md](./knowledge-scopes.md)), and output-side redaction.
Each destination is written as its own store record, so `log.md` reads
`extract <session> | <scope> | N truth candidate(s)` and, separately,
`… | M ticket candidate(s)`. `--json-out` reports the ticket count as
`ticket_candidates`, which a retrospect subtracts from its truth count and
appends to its own entry as `, K ticket candidates` when there are any.
`--benchmark` and the coverage score consider truth-destined candidates only.

The decision extractor is unchanged: its artifacts have one destination, the
type itself.

## Review: filing a ticket candidate

`p` on a ticket candidate files it rather than promoting it, in three steps.

1. **Duplicate check.** The project's standing rule is to search for a
   duplicate before filing a bug and add context to it instead. The candidate's
   title is ranked against every ticket in its scope's tk namespace — done and
   closed included, so a defect already fixed or already refused is seen — with
   the same `ticket.Search` ranking `tk search` prints, read through the library
   because `tk search` has no structured output. The top five are shown. A
   check that cannot run opens nothing; a tk read that skipped files says so in
   the chooser rather than presenting a partial check as a complete one.
2. **Choose.** Row 0 files a new ticket; each match row adds a note to that
   ticket instead. `esc` cancels and leaves the candidate in place.
3. **File, then archive.**
   - New: `tk --project=<scope> create -t <ticket_type> -d <text> -- <title>`,
     which lands in `backlog`.
   - Note: `tk --project=<namespace> add-note -- <id> <text>`.

   The text is the candidate's body, its evidence paths, and a closing list of
   the source sessions — the filed ticket's only link back to the evidence. tk
   runs as argv, never through a shell. The candidate then moves to
   `_candidates/_filed/tickets/<scope>/` and `log.md` gains
   `## [date] file|note <id> | <scope> | ticket candidate <filename> → <ticket-id>`,
   committed together the way a reject is.

tk is written before the store, and the two cannot be one unit. A tk refusal
leaves the candidate untouched. A tk write whose archive then fails leaves the
candidate listed with the ticket already filed; the status line names the
ticket, and the next attempt's duplicate check offers it, so it becomes a note
rather than a second ticket.

`x` rejects a ticket candidate into `_candidates/_rejected/tickets/<scope>/`,
the same gesture as for truths.

## Layout

```
_candidates/
  truths/<scope>/
  decisions/<scope>/
  tickets/<scope>/          # ticket candidates awaiting review
  _rejected/
    tickets/<scope>/        # beside truths/ and decisions/
  _filed/
    tickets/<scope>/        # candidates filed into tk, or noted onto a ticket
```

There is no `tickets/` validated tree: a ticket's validated home is tk.
`SCHEMA.md` in the store is its own repo and does not yet describe this layout.

## Verified on the corpus

The defect "`ticket_create`'s `repo` param resolves via `.tickets/` walk-up,
breaking central-store projects" sat in the store as five truth candidates. Their
five cited sessions (`1bdf4151`, `ace9a39d`, `cb3fe3ba`, `dc303a88`, `a220bca2`)
are not five observations: each is a captured extractor run whose prompt is
byte-identical across all five, carrying one summary of one ticket-repo session,
`91d979db-8c94-4f38-999b-90b028c5b543`. Five candidates, one input.

Re-running the new extractor four times over that summary (`claude:sonnet`,
isolated store) produced one ticket candidate per run and no walk-up truth.
Since the five sources share the one input, those runs reproduce what each of
them would now yield. The four are fixtures in
`internal/tui/testdata/walkup-tickets/`, and
`TestWalkupCandidatesResolveToOneTicket` files them through the review screen
against a throwaway tk store: the first files a bug, the duplicate check ranks
that bug first for each of the other three, and they become three notes on it.
One ticket, citing the session in its body and in every note.

The five existing truth candidates are rejected (`x`) once that ticket is
filed. They are the defect under the retired reframe rule, the ticket now
carries it, and the re-extraction emits no walk-up truth to stand in their
place, so the corpus ends with one ticket and no walk-up truth.
