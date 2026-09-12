package lens

import (
	"encoding/json"
	"testing"
)

func fenced(body string) string {
	return "```json\n" + body + "\n```"
}

func TestExtractReadsBlocksWholeAndKeepsTruncatedOnes(t *testing.T) {
	text := "Here is the contract lens.\n" +
		fenced(`{"lens": "contract", "verdict": "findings", "summary": "One criterion untested.",
			"context": {"state": "clean", "received": []},
			"criteria": [{"id": "AC1", "status": "pass"}, {"id": "AC2", "status": "unverified"}],
			"findings": [{"file": "a.go", "line": 3, "severity": "must_fix"}]}`) +
		"\nand the security lens, cut off mid-flight:\n" +
		"```json\n{\n  \"lens\": \"security\",\n  \"verdict\": \"findings\",\n  \"summary\": \"The payload carried a forbidden input.\",\n  \"criteria\": ["

	got := Extract(text)
	if len(got) != 2 {
		t.Fatalf("extracted %d blocks, want 2", len(got))
	}
	whole := got[0]
	if whole.Status != StatusParsed || whole.Lens != Contract || whole.Verdict != "findings" || whole.Ordinal != 0 {
		t.Fatalf("whole block = %+v, want a parsed contract verdict", whole)
	}
	if whole.Context == nil || whole.Context.State != "clean" || whole.ContextKind != ContextStructured {
		t.Fatalf("context = %+v kind %q, want structured clean", whole.Context, whole.ContextKind)
	}
	var criteria []map[string]string
	if err := json.Unmarshal(whole.Criteria, &criteria); err != nil || len(criteria) != 2 {
		t.Fatalf("criteria = %s (%v), want the two-item array intact", whole.Criteria, err)
	}
	var findings []map[string]any
	if err := json.Unmarshal(whole.Findings, &findings); err != nil || len(findings) != 1 {
		t.Fatalf("findings = %s (%v), want the one-item array intact", whole.Findings, err)
	}
	if whole.Offset != len("Here is the contract lens.\n") {
		t.Fatalf("offset = %d, want the fence's position", whole.Offset)
	}
	if !json.Valid([]byte(whole.Raw)) {
		t.Fatalf("raw = %q, want the whole fenced body", whole.Raw)
	}

	cut := got[1]
	if cut.Status != StatusMalformed || cut.Reason != ReasonUnterminated || !cut.Unterminated || cut.Ordinal != 1 {
		t.Fatalf("cut block = %+v, want malformed/unterminated", cut)
	}
	if cut.Lens != Security || cut.Summary != "The payload carried a forbidden input." {
		t.Fatalf("cut block = %+v, want lens and summary recovered", cut)
	}
	if cut.ContextKind != ContextHistorical || cut.Context != nil {
		t.Fatalf("cut block context = %+v kind %q, want historical", cut.Context, cut.ContextKind)
	}
	if !Contaminated(cut) {
		t.Fatal("contamination not read off a recovered summary")
	}
	if Contaminated(whole) {
		t.Fatal("a structured clean context read as contaminated")
	}
}

func TestExtractKeepsBlocksThatAreNotVerdictsAsMalformed(t *testing.T) {
	// Every one of these carries a "lens" field, and a transcript can hold any
	// of them: a quoted verdict template, a lens nobody dispatches, a block with
	// no verdict at all, an array, and a truncated block naming no known lens.
	text := fenced(`{"lens": "<contract|quality|security>", "verdict": "<satisfied|findings>"}`) +
		fenced(`{"lens": "performance", "verdict": "satisfied", "summary": "Fast enough."}`) +
		fenced(`{"lens": "contract", "summary": "No verdict field here."}`) +
		fenced(`["lens"]`) +
		"```json\n{\n  \"lens\": \"whatever\",\n  \"summary\": \"cut off\","
	got := Extract(text)
	want := []string{ReasonUnknownLens, ReasonUnknownLens, ReasonUnknownVerdict, ReasonNotObject, ReasonUnterminated}
	if len(got) != len(want) {
		t.Fatalf("extracted %d blocks, want %d: %+v", len(got), len(want), got)
	}
	for i, b := range got {
		if b.Status != StatusMalformed || b.Reason != want[i] {
			t.Fatalf("block %d = %s/%s, want malformed/%s", i, b.Status, b.Reason, want[i])
		}
	}
	if got[2].Lens != Contract || got[2].Summary != "No verdict field here." {
		t.Fatalf("block 2 = %+v, want its lens and summary kept", got[2])
	}
}

func TestStructuredContextWinsOverProse(t *testing.T) {
	// A structured state decides; the summary's prose is only read when the
	// verdict predates the context field.
	shared := Extract(fenced(`{"lens": "quality", "verdict": "satisfied",
		"summary": "The context was shared.", "context": {"state": "shared", "received": ["coder transcript"]}}`))[0]
	if Contaminated(shared) || shared.Context.State != "shared" || len(shared.Context.Received) != 1 {
		t.Fatalf("shared block = %+v, want structured shared and not contaminated", shared)
	}
	dirty := Extract(fenced(`{"lens": "quality", "verdict": "satisfied",
		"summary": "No contamination.", "context": {"state": "contaminated", "received": ["earlier lens output"]}}`))[0]
	if !Contaminated(dirty) {
		t.Fatal("structured contaminated state not read")
	}
	prose := Extract(fenced(`{"lens": "quality", "verdict": "satisfied", "summary": "The context was shared."}`))[0]
	if prose.ContextKind != ContextHistorical || !Contaminated(prose) {
		t.Fatalf("prose block = %+v, want historical and contaminated by prose", prose)
	}
}

func TestReportsContamination(t *testing.T) {
	cases := map[string]bool{
		"The lens saw the coder's transcript, so its context was contaminated.": true,
		// Word order varies; both readings are the same report.
		"The lens was handed the coder's transcript, so its context was shared.": true,
		"A shared context put the coder's reasoning in front of the lens.":       true,
		"The payload carried a forbidden input.":                                 true,
		// The negation belongs to the clause before it, not to the report.
		"No must_fix findings, but the context was shared.": true,
		// Negated phrasings carry the same stems and are not reports.
		"No contamination: the lens saw the diff and the ticket only.": false,
		"The review context was not contaminated.":                     false,
		"No shared context, no forbidden inputs.":                      false,
		"All acceptance criteria are met.":                             false,
	}
	for s, want := range cases {
		if got := ReportsContamination(s); got != want {
			t.Fatalf("ReportsContamination(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestProseWithoutAFenceIsNotABlock(t *testing.T) {
	// The merge text a run writes says this routinely; only fenced blocks are
	// read, so it never reaches a counter.
	if len(Extract("All three lenses returned, no contamination reports.")) != 0 {
		t.Fatal("prose read as a verdict block")
	}
}

func TestMalformedFieldsAreNotParsed(t *testing.T) {
	// Each is a JSON object naming a known lens and verdict whose context,
	// criteria or findings do not fit the schema; none may parse, since a
	// parsed status is what the attempt model counts as a successful review.
	head := `"lens": "contract", "verdict": "findings", "summary": "Kept.", `
	cases := []struct {
		body   string
		reason string
	}{
		{head + `"findings": "invalid"`, ReasonInvalidFindings},
		{head + `"findings": [{"file": "a.go", "severity": "blocker"}]`, ReasonInvalidFindings},
		{head + `"findings": [{"file": "a.go", "line": "3", "severity": "must_fix"}]`, ReasonInvalidFindings},
		{head + `"criteria": {"AC1": "pass"}`, ReasonInvalidCriteria},
		{head + `"criteria": [{"id": 1, "status": "pass"}]`, ReasonInvalidCriteria},
		{head + `"criteria": [{"id": "AC1", "status": "maybe"}]`, ReasonInvalidCriteria},
		{head + `"context": "clean"`, ReasonInvalidContext},
		{head + `"context": {"state": "dirty", "received": []}`, ReasonInvalidContext},
		{head + `"context": {"state": "clean", "received": "none"}`, ReasonInvalidContext},
		{head + `"context": {"received": []}`, ReasonInvalidContext},
		// A missing or null field is not an empty one: the schema requires
		// the received array, and null decodes into a slice without error.
		{head + `"context": {"state": "clean"}`, ReasonInvalidContext},
		{head + `"context": {"state": "clean", "received": null}`, ReasonInvalidContext},
		{head + `"context": null`, ReasonInvalidContext},
		{head + `"criteria": null`, ReasonInvalidCriteria},
		{head + `"findings": null`, ReasonInvalidFindings},
	}
	for _, tc := range cases {
		got := Extract(fenced("{" + tc.body + "}"))
		if len(got) != 1 {
			t.Fatalf("%s: extracted %d blocks, want 1", tc.body, len(got))
		}
		b := got[0]
		if b.Status != StatusMalformed || b.Reason != tc.reason {
			t.Errorf("%s: status %s/%s, want malformed/%s", tc.body, b.Status, b.Reason, tc.reason)
		}
		if b.Lens != Contract || b.Verdict != "findings" || b.Summary != "Kept." || b.Raw != "{"+tc.body+"}" {
			t.Errorf("%s: block = %+v, want its lens, verdict, summary and raw kept", tc.body, b)
		}
		if b.Context != nil || b.ContextKind != ContextHistorical {
			t.Errorf("%s: context = %+v kind %q, want none", tc.body, b.Context, b.ContextKind)
		}
	}
	// The same fields in their schema shapes parse; a block with no context
	// field at all predates it and parses as historical.
	whole := Extract(fenced(`{` + head + `"context": {"state": "clean", "received": []},
		"criteria": [{"id": "AC1", "text": "t", "status": "pass", "evidence": "e"}],
		"findings": [{"file": "a.go", "line": null, "severity": "defer", "category": "scope", "criterion": null, "description": "d", "fix": "f"}]}`))[0]
	if whole.Status != StatusParsed || whole.ContextKind != ContextStructured {
		t.Errorf("whole block = %s/%s %s, want parsed and structured", whole.Status, whole.Reason, whole.ContextKind)
	}
	old := Extract(fenced(`{` + head + `"criteria": [], "findings": []}`))[0]
	if old.Status != StatusParsed || old.ContextKind != ContextHistorical || old.Context != nil {
		t.Errorf("historical block = %s/%s %s, want parsed and historical", old.Status, old.Reason, old.ContextKind)
	}
}
