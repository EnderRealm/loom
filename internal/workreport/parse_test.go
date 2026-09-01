package workreport

import "testing"

func TestInvocationRecognizesBothRuntimeForms(t *testing.T) {
	cases := []struct {
		name    string
		message string
		ticket  string
		want    bool
	}{
		{
			name:    "claude with args",
			message: "<command-message>work</command-message>\n<command-name>/work</command-name>\n<command-args>loom/foo-1234</command-args>",
			ticket:  "loom/foo-1234",
			want:    true,
		},
		{
			name:    "claude without args",
			message: "<command-message>work</command-message>\n<command-name>/work</command-name>",
			want:    true,
		},
		{
			name:    "claude behind system reminders",
			message: "<system-reminder>be careful</system-reminder>\n<system-reminder>really</system-reminder>\n<command-message>work</command-message>\n<command-name>/work</command-name>\n<command-args>loom/foo-1234</command-args>",
			ticket:  "loom/foo-1234",
			want:    true,
		},
		{
			name:    "codex typed, older rollouts",
			message: "#work ticket/watch-serve-loops-4466",
			ticket:  "ticket/watch-serve-loops-4466",
			want:    true,
		},
		{
			name:    "codex bare",
			message: "$work",
			want:    true,
		},
		{
			name:    "codex typed behind a system reminder",
			message: "<system-reminder>be careful</system-reminder>\n#work ticket/watch-serve-loops-4466",
			ticket:  "ticket/watch-serve-loops-4466",
			want:    true,
		},
		// The current form: the harness expands the whole skill into the user
		// message and the typed invocation trails it.
		{
			name:    "codex skill block with the typed ticket",
			message: skillInvocation("ticket/repo-flag-accept-bb51"),
			ticket:  "ticket/repo-flag-accept-bb51",
			want:    true,
		},
		{
			name:    "codex skill block, no ticket typed",
			message: skillBlock("work") + "\n",
			want:    true,
		},
		{
			name:    "another skill's block",
			message: skillBlock("next"),
		},
		// A lens session is handed the work skill body mid-turn; only a message
		// that opens with the block is the invocation.
		{
			name:    "skill block quoted mid-prompt",
			message: "# Reviewer\n\nThe orchestrator's skill reads:\n\n" + skillBlock("work"),
		},
		// A summarizer session quotes the whole skill body, tags included; a
		// substring test would count every one of those as a run.
		{
			name:    "tags quoted mid-prompt",
			message: "# Session Summarizer\n\nThe skill begins <command-message>work</command-message>\n<command-name>/work</command-name> and continues.",
		},
		{
			name:    "another skill",
			message: "<command-message>next</command-message>\n<command-name>/next</command-name>",
		},
		{
			name:    "codex prefix of another word",
			message: "#workaround for the flake",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ticket, ok := invocation(tc.message)
			if ok != tc.want {
				t.Fatalf("invocation ok = %v, want %v", ok, tc.want)
			}
			if ticket != tc.ticket {
				t.Fatalf("ticket = %q, want %q", ticket, tc.ticket)
			}
		})
	}
}

func TestDispatchLinesCountRounds(t *testing.T) {
	var d dispatchLines
	d.scan("Plan is clear.\ndispatching (loom/foo-1234 round 1): contract, quality, security")
	d.scan("Round 2 diff is ready.\n\ndispatching (loom/foo-1234 round 2, final): contract, quality, security")
	d.scan("dispatching (round 3): contract, quality, security")
	// The same line at the start of a sentence is capitalized.
	d.scan("Dispatching (loom/foo-1234 Round 4, final): contract, quality, security")
	if d.count != 4 {
		t.Fatalf("count = %d, want 4", d.count)
	}
	if d.maxRound != 4 {
		t.Fatalf("maxRound = %d, want 4 (the review iterations)", d.maxRound)
	}
	if d.unparseable {
		t.Fatal("unparseable = true, want false: every line named a round")
	}
}

func TestDispatchLineWithoutARoundIsUnparseable(t *testing.T) {
	var d dispatchLines
	d.scan("dispatching (loom/foo-1234 round N): contract, quality, security")
	if !d.unparseable {
		t.Fatal("unparseable = false, want true: an uncountable round must fail closed")
	}
}

func TestParseVerdictsReadsBlocksAndToleratesTruncation(t *testing.T) {
	text := "Here is the contract lens.\n" +
		fenced(`{"lens": "contract", "verdict": "findings", "summary": "One criterion untested.",
			"criteria": [{"id": "AC1", "status": "pass"}, {"id": "AC2", "status": "unverified"}]}`) +
		"\nand the security lens, cut off mid-flight:\n" +
		"```json\n{\n  \"lens\": \"security\",\n  \"verdict\": \"findings\",\n  \"summary\": \"The payload carried a forbidden input.\",\n  \"criteria\": ["

	got := parseVerdicts(text)
	if len(got) != 2 {
		t.Fatalf("parsed %d verdicts, want 2", len(got))
	}
	if !got[0].parsed || got[0].lens != lensContract || len(got[0].criteria) != 2 {
		t.Fatalf("contract verdict = %+v, want a fully parsed block with 2 criteria", got[0])
	}
	if got[1].parsed {
		t.Fatal("truncated block reported as fully parsed")
	}
	if got[1].lens != "security" || got[1].summary != "The payload carried a forbidden input." {
		t.Fatalf("truncated verdict = %+v, want lens and summary recovered", got[1])
	}
	if !contaminationRe.MatchString(got[1].summary) {
		t.Fatal("contamination not detected in a recovered summary")
	}
}

func TestParseVerdictsRejectsBlocksThatAreNotVerdicts(t *testing.T) {
	// Every one of these carries a "lens" field, and a transcript can hold any
	// of them: a quoted verdict template, a lens nobody dispatches, a block with
	// no verdict at all, and a truncated block naming no known lens.
	text := fenced(`{"lens": "<contract|quality|security>", "verdict": "<satisfied|findings>"}`) +
		fenced(`{"lens": "performance", "verdict": "satisfied", "summary": "Fast enough."}`) +
		fenced(`{"lens": "contract", "summary": "No verdict field here."}`) +
		"```json\n{\n  \"lens\": \"whatever\",\n  \"summary\": \"cut off\","
	if got := parseVerdicts(text); len(got) != 0 {
		t.Fatalf("parsed %d verdicts, want 0: %+v", len(got), got)
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
		if got := reportsContamination(s); got != want {
			t.Fatalf("reportsContamination(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestContaminationIgnoresNonVerdictProse(t *testing.T) {
	// The merge text a run writes says this routinely; only verdict summaries
	// are matched, so it never reaches the counter.
	if len(parseVerdicts("All three lenses returned, no contamination reports.")) != 0 {
		t.Fatal("prose read as a verdict block")
	}
}

func TestRuntimeOf(t *testing.T) {
	cases := map[string]Runtime{
		"claude-code": RuntimeClaude,
		"codex-cli":   RuntimeCodex,
		"cursor-cli":  RuntimeCursor,
		"":            RuntimeUnknown,
		"gemini-cli":  RuntimeUnknown,
	}
	for agent, want := range cases {
		if got := runtimeOf(agent); got != want {
			t.Fatalf("runtimeOf(%q) = %q, want %q", agent, got, want)
		}
	}
}

func TestSubjectNamesTicket(t *testing.T) {
	if !subjectNamesTicket("[loom/foo-1234] Do the thing", "loom/foo-1234") {
		t.Fatal("namespaced marker did not match")
	}
	// /work is often invoked with the bare id where the commit carries the
	// namespaced one.
	if !subjectNamesTicket("[loom/foo-1234] Do the thing", "foo-1234") {
		t.Fatal("bare id did not match its namespaced marker")
	}
	if subjectNamesTicket("[loom/bar-5678] Do the thing", "loom/foo-1234") {
		t.Fatal("unrelated ticket matched")
	}
	if subjectNamesTicket("Do the thing", "loom/foo-1234") {
		t.Fatal("unmarked subject matched")
	}
	if subjectNamesTicket("[loom/foo-1234] Do the thing", "") {
		t.Fatal("empty ticket matched")
	}
}
