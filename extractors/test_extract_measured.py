#!/usr/bin/env python3
"""Tests for the `kind: measured` truth class.

Run with: python3 -m unittest

A measured truth is filed only when its sections prove the class's three
signals — a Hypothesis, a re-runnable Measurement with its numbers, and where
it Applies beyond — so the checks are asserted over parse_truth's dicts, the
same ones main() hands split_destinations.
"""
import io
import tempfile
import unittest
from contextlib import redirect_stderr
from pathlib import Path

from extract import (candidate_record, decision_candidates, emit_candidates, parse_output,
                     parse_truth, split_destinations)

MEASURED = """---
id: loom-review-rounds-scale-cost
title: Review payload cost scales per round, not per changed line
scope: loom
type: truth
destination: truth
kind: measured
---

## Claim

Each review round resends the whole diff, so total review cost tracks rounds times diff size.

## Why it matters

Shrinking a fix does not shrink the next round's payload.

## Hypothesis

The cost was believed to track the lines each round changed.

## Measurement

```
git log --format=%h -- extractors/ | wc -l
```

Then `git diff --numstat main~3 main` per round. Four rounds sent 1,200 lines each while the fixes changed 40.

## Applies beyond

Every ticket reviewed in more than one round.

## How to verify

Run from the loom repo root: `git diff --numstat main~3 main`.
"""

ORDINARY = """---
id: loom-example-truth
title: An example truth
scope: loom
type: truth
destination: truth
---

## Claim

Something is true.

## How to verify

Run the thing.
"""

TICKET = """---
id: loom-example-ticket
title: An example ticket
scope: loom
type: ticket
destination: ticket
ticket_type: bug
kind: measured
---

## Problem

It breaks.
"""

DECISION = """---
id: loom-example-decision
title: An example decision
scope: loom
type: decision
kind: measured
---

## Choice

Do the thing.

## Rationale

Because.
"""

# Credential-shaped, and fake: matches redact.py's `github-token` row.
FAKE_TOKEN = "ghp_" + "0123456789abcdefghijklmnopqrstuvwxyz"


def split(*texts):
    err = io.StringIO()
    with redirect_stderr(err):
        out = split_destinations([parse_truth(t) for t in texts])
    return out, err.getvalue()


def without_section(text, heading, next_heading):
    start = text.index(f"## {heading}\n")
    return text[:start] + text[text.index(f"## {next_heading}\n"):]


def with_measurement(text, measurement):
    start = text.index("## Measurement\n")
    end = text.index("## Applies beyond\n")
    return text[:start] + f"## Measurement\n\n{measurement}\n\n" + text[end:]


class MeasuredTruthTest(unittest.TestCase):
    def test_a_measured_truth_with_all_signals_is_filed_as_a_truth(self):
        out, err = split(MEASURED)

        self.assertEqual([c["id"] for c in out["truth"]], ["loom-review-rounds-scale-cost"])
        self.assertEqual(out["truth"][0]["kind"], "measured")
        self.assertEqual(err, "")

    def test_each_missing_signals_section_is_dropped(self):
        cases = (("Hypothesis", "Measurement"), ("Measurement", "Applies beyond"),
                 ("Applies beyond", "How to verify"))
        for heading, next_heading in cases:
            with self.subTest(missing=heading):
                out, err = split(without_section(MEASURED, heading, next_heading))
                self.assertEqual(out, {"truth": [], "ticket": []})
                self.assertIn(f"measured truth without {heading}", err)

    def test_an_empty_signals_section_counts_as_missing(self):
        text = MEASURED.replace("Every ticket reviewed in more than one round.\n", "")
        out, err = split(text)

        self.assertEqual(out["truth"], [])
        self.assertIn("without Applies beyond", err)

    def test_a_measurement_without_a_command_is_dropped(self):
        for measurement in ("Four rounds sent 1,200 lines each.",
                            "`extract.py` showed four rounds of 1,200 lines.",
                            "```\n```\n\nFour rounds sent 1,200 lines each."):
            with self.subTest(measurement=measurement):
                out, err = split(with_measurement(MEASURED, measurement))
                self.assertEqual(out["truth"], [])
                self.assertIn("Measurement has no command", err)

    def test_a_measurement_with_only_an_inline_command_is_filed(self):
        out, _ = split(with_measurement(
            MEASURED, "`git diff --numstat main~3 main` showed 1,200 lines a round."))

        self.assertEqual(len(out["truth"]), 1)

    def test_a_measurement_without_a_number_is_dropped(self):
        # The digits in the commands are not results, and neither is a list
        # marker: a number has to be stated outside the code.
        for measurement in (("```\ngit diff --numstat main~3 main\n```\n\n"
                             "Then `head -20 out.txt`. The rounds were large."),
                            ("1. Ran `git diff --numstat main~3 main`\n"
                             "2) Compared the goldens with `cmp a b`.")):
            with self.subTest(measurement=measurement):
                out, err = split(with_measurement(MEASURED, measurement))
                self.assertEqual(out["truth"], [])
                self.assertIn("Measurement states no number", err)

    def test_a_measurement_whose_only_digits_are_a_sha_or_version_is_dropped(self):
        for prose in ("The rounds were large at a1fd6f5.", "The rounds were large in v2."):
            with self.subTest(prose=prose):
                measurement = f"```\ngit diff --numstat main~3 main\n```\n\n{prose}"
                out, err = split(with_measurement(MEASURED, measurement))
                self.assertEqual(out["truth"], [])
                self.assertIn("Measurement states no number", err)

    def test_a_kind_on_a_ticket_is_dropped_not_filed_unchecked(self):
        out, err = split(TICKET)

        self.assertEqual(out, {"truth": [], "ticket": []})
        self.assertIn("kind is a truth attribute", err)

    def test_a_kind_in_a_decision_run_is_dropped(self):
        err = io.StringIO()
        with redirect_stderr(err):
            out = decision_candidates([parse_truth(DECISION),
                                       parse_truth(DECISION.replace("kind: measured\n", ""))])

        self.assertEqual(len(out), 1)
        self.assertEqual(out[0]["kind"], "")
        self.assertIn("kind 'measured' in a decision run", err.getvalue())

    def test_an_unknown_kind_is_dropped_not_read_as_ordinary(self):
        for kind in ("observed", FAKE_TOKEN):
            with self.subTest(kind=kind[:8]):
                out, err = split(ORDINARY.replace("destination: truth\n",
                                                  f"destination: truth\nkind: {kind}\n"))
                self.assertEqual(out["truth"], [])
                self.assertIn("is not one of measured", err)
                self.assertNotIn(FAKE_TOKEN, err)

    def test_ordinary_truths_are_unaffected_by_the_signals(self):
        out, err = split(ORDINARY, ORDINARY.replace("destination: truth\n",
                                                    "destination: truth\nkind:\n"))

        self.assertEqual(len(out["truth"]), 2)
        self.assertEqual(out["truth"][0]["kind"], "")
        self.assertEqual(err, "")

    def test_the_measurement_survives_into_the_candidate_file_and_json_record(self):
        candidates = parse_output(MEASURED + "\n===END-OF-TRUTH===\n")
        with tempfile.TemporaryDirectory() as tmp:
            changes, _ = emit_candidates(candidates, Path(tmp), "loom", "claude", "sonnet",
                                         None, "5c37b3b4-fb29-4e2e-8468-f63119e0d930", [],
                                         Path(tmp) / "truths")

        body = changes[0]["body"]
        self.assertIn("\nkind: measured\n", body.split("\n---\n", 1)[0])
        for heading in ("## Hypothesis", "## Measurement", "## Applies beyond"):
            self.assertIn(heading, body)
        self.assertIn("git log --format=%h -- extractors/ | wc -l", body)

        record = candidate_record(candidates[0])
        self.assertEqual(record["kind"], "measured")
        self.assertIn("1,200 lines", record["measurement"])
        self.assertNotIn("kind", candidate_record(parse_truth(ORDINARY)))


if __name__ == "__main__":
    unittest.main()
