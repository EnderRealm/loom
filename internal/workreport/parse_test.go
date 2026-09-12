package workreport

import (
	"strings"
	"testing"
)

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

func TestCommitmentsReadRoundAndLenses(t *testing.T) {
	text := "Plan is clear.\ndispatching (loom/foo-1234 round 1): contract, quality, security\n" +
		"dispatching (loom/foo-1234 round N): contract\n" +
		"Round 2 diff is ready.\n\nDispatching (loom/foo-1234 Round 2, final): Contract, Security"
	got := commitments(text)
	if len(got) != 2 {
		t.Fatalf("commitments = %+v, want 2 (the unparseable round is skipped)", got)
	}
	if got[0].round != 1 || strings.Join(got[0].lenses, ",") != "contract,quality,security" {
		t.Fatalf("commitments[0] = %+v, want round 1 naming all three", got[0])
	}
	if got[1].round != 2 || strings.Join(got[1].lenses, ",") != "contract,security" {
		t.Fatalf("commitments[1] = %+v, want round 2 naming contract and security", got[1])
	}
	if got[0].offset >= got[1].offset || got[0].offset != strings.Index(text, "dispatching") {
		t.Fatalf("offsets = %d/%d, want the lines' positions in the text", got[0].offset, got[1].offset)
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
