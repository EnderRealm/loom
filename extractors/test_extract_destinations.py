#!/usr/bin/env python3
"""Tests for routing truth-extractor candidates by their declared destination.

Run with: python3 -m unittest

Candidates are built through parse_truth from the text a model emits, so the
split is asserted over the same dicts main() hands it.
"""
import io
import unittest
from contextlib import redirect_stderr

from extract import decision_candidates, parse_output, parse_truth, split_destinations

TRUTH = """---
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
id: ticket-create-repo-param
title: ticket_create repo parameter fails for central-store projects
scope: ticket
type: ticket
destination: ticket
ticket_type: bug
---

## Problem

It walks up looking for .tickets/.
"""

# Credential-shaped, and fake: matches redact.py's `github-token` row.
FAKE_TOKEN = "ghp_" + "0123456789abcdefghijklmnopqrstuvwxyz"


def split(*texts):
    err = io.StringIO()
    with redirect_stderr(err):
        out = split_destinations([parse_truth(t) for t in texts])
    return out, err.getvalue()


class SplitDestinationsTest(unittest.TestCase):
    def test_each_candidate_goes_to_the_destination_it_names(self):
        out, err = split(TRUTH, TICKET)

        self.assertEqual([c["id"] for c in out["truth"]], ["loom-example-truth"])
        self.assertEqual([c["id"] for c in out["ticket"]], ["ticket-create-repo-param"])
        self.assertEqual(err, "")

    def test_a_missing_destination_is_dropped_not_defaulted(self):
        out, err = split(TRUTH.replace("destination: truth\n", ""))

        self.assertEqual(out, {"truth": [], "ticket": []})
        self.assertIn("dropping candidate 'loom-example-truth'", err)

    def test_an_unknown_destination_is_dropped(self):
        out, err = split(TRUTH.replace("destination: truth", "destination: decision"))

        self.assertEqual(out, {"truth": [], "ticket": []})
        self.assertIn("'decision' is not one of truth, ticket", err)

    def test_a_destination_without_its_shape_is_dropped(self):
        # A truth declared as a ticket has no Problem; a ticket declared as a
        # truth has no Claim or How to verify. Neither reaches a reviewer as
        # the thing it is not.
        as_ticket = TRUTH.replace("destination: truth", "destination: ticket")
        as_truth = TICKET.replace("destination: ticket", "destination: truth")
        out, err = split(as_ticket, as_truth)

        self.assertEqual(out, {"truth": [], "ticket": []})
        self.assertEqual(err.count("dropping candidate"), 2)

    def test_a_ticket_needs_a_tk_type(self):
        for bad in ("ticket_type: epic\n", ""):
            with self.subTest(ticket_type=bad):
                out, err = split(TICKET.replace("ticket_type: bug\n", bad))
                self.assertEqual(out["ticket"], [])
                self.assertIn("ticket_type of bug or feature", err)

    def test_a_credential_declared_as_a_destination_reaches_no_output(self):
        out, err = split(TRUTH.replace("destination: truth", f"destination: {FAKE_TOKEN}"))

        self.assertEqual(out, {"truth": [], "ticket": []})
        self.assertNotIn(FAKE_TOKEN, err)

    def test_model_output_with_both_destinations_parses_into_both(self):
        output = TRUTH + "\n===END-OF-TRUTH===\n" + TICKET + "\n===END-OF-TRUTH===\n"
        candidates = parse_output(output)

        self.assertTrue(all(c["valid"] for c in candidates))
        out = split_destinations(candidates)
        self.assertEqual((len(out["truth"]), len(out["ticket"])), (1, 1))

    def test_a_problem_section_alone_is_not_valid_without_the_ticket_destination(self):
        # The Problem section counts as content only on a ticket-destined
        # artifact, so a decision run's validity is what it was.
        self.assertFalse(parse_truth(TICKET.replace("destination: ticket\n", ""))["valid"])
        self.assertTrue(parse_truth(TICKET)["valid"])


DECISION = """---
id: loom-example-decision
title: An example decision
scope: loom
type: decision
---

## Choice

Do the thing.

## Rationale

Because.
"""


class DecisionCandidatesTest(unittest.TestCase):
    def test_a_ticket_in_a_decision_run_is_dropped(self):
        # parse_truth accepts the ticket on any run; a decision run has no
        # ticket route, so it must not land under _candidates/decisions/.
        err = io.StringIO()
        with redirect_stderr(err):
            out = decision_candidates([parse_truth(DECISION), parse_truth(TICKET)])

        self.assertEqual([c["id"] for c in out], ["loom-example-decision"])
        self.assertIn("dropping candidate 'ticket-create-repo-param'", err.getvalue())


if __name__ == "__main__":
    unittest.main()
