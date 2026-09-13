package codexparse

import (
	"encoding/json"
	"strings"
	"testing"

	"loom/internal/parse/lens"
	"loom/internal/parse/summary"
)

// TestLensResponsesAreReadWhole pins the two Codex routes a lens verdict
// takes: the router's output on a shell call, read whole before the 800-char
// result cut and once even though the same output arrives as an
// exec_command_end event and again as a function_call_output item; and the
// inlined passes in the assistant's own message item, which the agent_message
// event repeats and is not read for.
func TestLensResponsesAreReadWhole(t *testing.T) {
	s := parseFixture(t, "testdata/lens_responses.jsonl")
	if len(s.LensResponses) != 3 {
		t.Fatalf("LensResponses len: got %d, want 3: %+v", len(s.LensResponses), s.LensResponses)
	}
	routed := s.LensResponses[0]
	if routed.Origin != summary.OriginToolResult || routed.DispatchID != "call_sec" || routed.SourceLine != 5 || routed.TurnIdx != 0 {
		t.Errorf("routed = origin %s dispatch %q line %d turn %d, want the exec_command_end on line 5 for call_sec",
			routed.Origin, routed.DispatchID, routed.SourceLine, routed.TurnIdx)
	}
	if routed.Lens != lens.Security || routed.Status != lens.StatusParsed || len(routed.Raw) <= 800 || !json.Valid([]byte(routed.Raw)) {
		t.Errorf("routed = %s %s raw %d chars, want a parsed security verdict stored whole", routed.Lens, routed.Status, len(routed.Raw))
	}
	for _, tc := range s.ToolCalls {
		if tc.CallID == "call_sec" && len(tc.ResultSummary) > 801 {
			t.Errorf("tool result summary is %d chars, want it still cut", len(tc.ResultSummary))
		}
	}

	for i, want := range []string{lens.Contract, lens.Quality} {
		r := s.LensResponses[1+i]
		if r.Origin != summary.OriginAssistant || r.SourceLine != 7 || r.Ordinal != i || r.DispatchID != "" {
			t.Errorf("inlined[%d] = origin %s line %d ordinal %d dispatch %q, want the assistant item on line 7", i, r.Origin, r.SourceLine, r.Ordinal, r.DispatchID)
		}
		if r.Lens != want || r.Status != lens.StatusParsed || r.Context == nil || r.Context.State != "shared" {
			t.Errorf("inlined[%d] = %s %s context %+v, want a parsed %s verdict with a shared context", i, r.Lens, r.Status, r.Context, want)
		}
		if lens.Contaminated(r.Block) {
			t.Errorf("inlined[%d]: a shared context read as contaminated", i)
		}
	}
	if len(s.LensResponses[1].Context.Received) != 1 || len(s.LensResponses[2].Context.Received) != 2 {
		t.Errorf("received lists = %v / %v, want 1 and 2 items", s.LensResponses[1].Context.Received, s.LensResponses[2].Context.Received)
	}
}

// TestLastAgentMessageIsReadForLenses pins the third carrier of a turn's
// assistant text: a turn with no message item and no agent_message event
// takes its text from task_complete.last_agent_message, and an inlined pass
// there lands as an assistant-origin row like the other two.
func TestLastAgentMessageIsReadForLenses(t *testing.T) {
	stream := `{"timestamp": "2026-09-03T09:00:01.000Z", "type": "turn_context", "payload": {"turn_id": "t1", "cwd": "/tmp/c", "model": "gpt-5.4"}}
{"timestamp": "2026-09-03T09:00:02.000Z", "type": "response_item", "payload": {"type": "message", "role": "user", "content": [{"type": "input_text", "text": "#work loom/last-1111"}]}}
{"timestamp": "2026-09-03T09:00:03.000Z", "type": "event_msg", "payload": {"type": "task_complete", "turn_id": "t1", "last_agent_message": "dispatching (loom/last-1111 round 1): quality\n` + "```json\\n{\\\"lens\\\": \\\"quality\\\", \\\"verdict\\\": \\\"satisfied\\\", \\\"summary\\\": \\\"Fine.\\\", \\\"context\\\": {\\\"state\\\": \\\"clean\\\", \\\"received\\\": []}}\\n```" + `"}}
`
	s, err := Parse(strings.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Turns) != 1 || !strings.Contains(s.Turns[0].AssistantText, "dispatching") {
		t.Fatalf("turns = %+v, want one carrying the last agent message", s.Turns)
	}
	if len(s.LensResponses) != 1 {
		t.Fatalf("LensResponses = %+v, want the one inlined pass", s.LensResponses)
	}
	r := s.LensResponses[0]
	if r.Origin != summary.OriginAssistant || r.DispatchID != "" || r.TurnIdx != 0 || r.SourceLine != 3 {
		t.Errorf("row = origin %s dispatch %q turn %d line %d, want an assistant row on turn 0 from line 3", r.Origin, r.DispatchID, r.TurnIdx, r.SourceLine)
	}
	if r.Lens != lens.Quality || r.Status != lens.StatusParsed {
		t.Errorf("row = %s %s, want a parsed quality verdict", r.Lens, r.Status)
	}
}

// TestFileWritesAreReadForLenses pins the fourth carrier of an inlined pass:
// a verdict the agent wrote to a file through apply_patch lives in an
// item_completed FileChange item's `add` content and in no message. Each
// added file's blocks land as file_write rows numbered across the record in
// path order; an `update` carries a diff and a `delete` the removed file's
// content, and neither is read. The event is modeled, so no item type of it
// is unknown.
func TestFileWritesAreReadForLenses(t *testing.T) {
	s := parseFixture(t, "testdata/lens_filewrite.jsonl")
	var written []summary.LensResponse
	for _, r := range s.LensResponses {
		if r.Origin == summary.OriginFileWrite {
			written = append(written, r)
		}
	}
	if len(written) != 4 || len(s.LensResponses) != 6 {
		t.Fatalf("LensResponses = %+v, want the two routed verdicts and the four added files' verdicts, nothing from the update or delete", s.LensResponses)
	}
	want := []struct {
		line, ordinal int
		lens, verdict string
	}{
		{7, 0, lens.Contract, "findings"}, {7, 1, lens.Quality, "findings"},
		{10, 0, lens.Contract, "satisfied"}, {10, 1, lens.Quality, "satisfied"},
	}
	for i, r := range written {
		if r.DispatchID != "" || r.TurnIdx != 0 || r.SourceLine != want[i].line || r.Ordinal != want[i].ordinal || r.At.IsZero() {
			t.Errorf("written[%d] = dispatch %q turn %d line %d ordinal %d at %v, want a row on turn 0 from line %d at ordinal %d", i, r.DispatchID, r.TurnIdx, r.SourceLine, r.Ordinal, r.At, want[i].line, want[i].ordinal)
		}
		if r.Lens != want[i].lens || r.Verdict != want[i].verdict || r.Status != lens.StatusParsed || r.Context == nil || r.Context.State != "shared" {
			t.Errorf("written[%d] = %s %s %s context %+v, want a parsed %s %s verdict in path order", i, r.Lens, r.Verdict, r.Status, r.Context, want[i].lens, want[i].verdict)
		}
	}
	if len(s.Unknown) != 0 {
		t.Errorf("unknown = %+v, want none: item_completed is modeled, its other item types included", s.Unknown)
	}
}
