# Measured finding measurement

`loom/surface-measured-own-528f` (commit `33f43de`) taught the truth extractor
to emit `kind: measured` truths: process findings the session derived by
measuring its own artifacts, whose numbers contradicted what was believed
beforehand, and which hold past the ticket that produced them.
`split_destinations` holds the class to its shape — Hypothesis, Measurement
and Applies beyond sections, a runnable command and a standalone number — and
drops one that falls short (`docs/candidate-destinations.md`). Whether the
detector is useful depends on its rate: one that fires on every session is as
useless as one that never fires, and only a measured rate says which. This
records that rate over a set of past sessions, and how many of the surfaced
candidates a human keeps.

## Sample

Each session was extracted under the truth prompt at `33f43de`, five runs at a
time in parallel, one command per run:

```
python3 extractors/extract.py --extract-type truth --input <path> \
  --input-format <raw|summary> [--summarize] --scope <scope> \
  --provider claude --model sonnet --judge keyword --no-emit-candidates \
  --json-out <dir>/<id>.json --raw-out <dir>/<id>.raw.txt
```

- **Base rate: the 20 sessions of `docs/decision-corpus-measurement.md`.**
  15 production sessions, `<path>`
  `~/.loom/received/claude-code/<project-dir>/<session>.jsonl` with
  `--input-format raw --summarize`, and 5 eval sessions, `<path>`
  `extractors/eval-data/<file>.md` with `--input-format summary`. Selection and
  scopes are that document's: production scopes from the sweep state, eval
  scopes from the file name's project segment.
- **Positive control: warp session `93701235-319a-482a-aa53-5b96aa7dbb6a`,**
  raw with `--summarize`, scope `warp`, run 3 times. It is a session in which
  the run measured its own diffs and overturned the belief that ticket scope
  drove their size. Its hits are reported as recall, separately from the base
  rate.

`--no-emit-candidates` leaves the knowledge store untouched. Counts come from
the `--json-out` result: ticket candidates are `ticket_candidates`, and truth
candidates are `candidates_valid` less `ticket_candidates`, since a truth run's
`candidates_valid` counts every routable candidate, tickets included. A
measured candidate is a truth whose record carries `kind: measured`. A kind
drop is a stderr `warn: dropping candidate` line naming a kind, which
`split_destinations` prints for a measured truth missing its shape, an unknown
kind, or a kind off a truth. The count takes all three on purpose, so a
misspelled `measured` dropped as an unknown kind is not missed. 14 runs exited
1 on the keyword judge's `FAIL` verdict against the scope's existing truths;
the other 9 had no scoring references and skipped scoring. No run crashed. The
run directories were scratch and are not kept.

### Per-session counts

| Session | Set | Scope | Truth candidates | Ticket candidates | Measured | Kind drops |
| --- | --- | --- | --- | --- | --- | --- |
| `80622fdc` | production | weft | 2 | 0 | 0 | 0 |
| `ca290f97` | production | warp | 0 | 1 | 0 | 0 |
| `3974ae5a` | production | warp | 0 | 1 | 0 | 0 |
| `1cf91833` | production | loom | 2 | 0 | 0 | 0 |
| `3fe3a43e` | production | loom | 1 | 1 | 0 | 0 |
| `ae074817` | production | loom | 1 | 2 | 0 | 0 |
| `03939580` | production | loom | 2 | 0 | 0 | 0 |
| `46fb0b0c` | production | ticket | 0 (`NO_TRUTHS`) | 0 | 0 | 0 |
| `616f5dfe` | production | ticket | 0 (`NO_TRUTHS`) | 0 | 0 | 0 |
| `b24de800` | production | ticket | 1 | 0 | 0 | 0 |
| `0d9df8c4` | production | ghostwheel | 0 | 2 | 0 | 0 |
| `85f615d6` | production | village2 | 0 (`NO_TRUTHS`) | 0 | 0 | 0 |
| `16d42d97` | production | forge | 0 (`NO_TRUTHS`) | 0 | 0 | 0 |
| `2c982c40` | production | forge | 1 | 0 | 0 | 0 |
| `2dbe1edb` | production | forge | 1 | 1 | 0 | 0 |
| `0916c546` | eval | forge | 1 | 0 | 0 | 0 |
| `2e9a386c` | eval | ticket | 0 (`NO_TRUTHS`) | 0 | 0 | 0 |
| `5bc59345` | eval | ticket | 1 | 0 | 0 | 0 |
| `aaa967e8` | eval | tracker | 1 | 0 | 0 | 0 |
| `c7dbbcca` | eval | forge | 2 | 0 | 0 | 0 |
| `93701235` run 1 | control | warp | 3 | 0 | 1 | 0 |
| `93701235` run 2 | control | warp | 3 | 1 | 1 | 0 |
| `93701235` run 3 | control | warp | 3 | 0 | 1 | 0 |

## Metrics

| Metric | Value |
| --- | --- |
| Base-rate measured candidates | 0 from 20 sessions; 0 of 15 production, 0 of 5 eval |
| Base-rate kind drops | 0 |
| Base-rate truth candidates | 16 (0.8 per session); 11 production (0.73 per session), 5 eval (1.0 per session) |
| Base-rate ticket candidates | 8, all production |
| Sessions with no truth candidate | 8 of 20: 5 `NO_TRUTHS`, 3 with ticket candidates only |
| Control recall | 3 of 3 runs surfaced one measured candidate |
| Control kind drops | 0 |

**Base rate.** The detector fired on none of the 20 sessions, and no run
produced a kind the shape check then dropped: the model declared `kind:`
nowhere in the base-rate sample, so the zero is the prompt's, not the
filter's.

**Recall.** Each control run surfaced exactly one measured candidate, under a
different id each run (`warp-review-diff-size-generated-copies`,
`warp-review-diff-size-driven-by-generated-copies-and-comments`,
`warp-work-diffs-dominated-by-generated-copies-and-comments`). All three state
the same finding and cite the same figures: the final diff added 2,430 lines,
47% of them generated copies, with the 956 lines of golden copies
`cmp`-identical to `scripts/work-gate.sh`, which is 53% comment lines and grew
from 237 to 478 lines under review. The hypothesis each overturns is that the
ticket's scope made the diffs large.

## Human judgement

A human judged every surfaced measured candidate on 2026-11-15, keep or
discard.

| Set | Surfaced | Kept | Discarded |
| --- | --- | --- | --- |
| Base rate | 0 | 0 | 0 |
| Control | 3 | 3 | 0 |

All three control candidates were kept. With nothing surfaced, the base-rate
sample offered nothing to judge.

## Verdict

The detector does not fire on every session: over 20 sessions its rate is 0,
with no candidate surfaced and none dropped. It does fire on a session known
to hold a measured finding, 3 of 3 runs, and a human kept every candidate it
surfaced there. The sample shows a detector that is selective, and whose
surfaced candidates were all kept. It does not show that the zero base rate is
correct rather than missed findings; see the limits below.

## Limits

- **No hand read of the base-rate sessions.** Nobody read the 20 sessions for
  measured findings, so this cannot say whether any held one the detector
  missed. The zero is a rate, not a recall figure. The intermediate summaries
  carry the question open: the summarizer recorded a `Verdict: overturned`
  self-measurement in three production sessions (`80622fdc` 3, `3fe3a43e` 1,
  `ca290f97` 1), none of which the extractor surfaced. Whether any of those
  holds past its ticket, which the class requires, was not judged.
- **Eval inputs predate the summarizer change.** The five eval files are
  pre-written summaries with no `### Measurements` section, so they test the
  extractor on summaries that lack the unit `33f43de` added for this class.
- **Small recall sample.** Recall rests on one session run three times. It
  shows the detector reproduces one finding; it does not show recall across
  sessions or kinds of measurement.
- **One extraction per base-rate session.** Run-to-run variance was not
  sampled outside the control.
