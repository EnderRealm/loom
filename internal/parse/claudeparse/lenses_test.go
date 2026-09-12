package claudeparse

import (
	"encoding/json"
	"strings"
	"testing"

	"loom/internal/parse/lens"
	"loom/internal/parse/summary"
)

// TestLensResponsesAreReadWhole pins where lens verdicts are read from and
// that they are read before the result cut: a routed lens's verdict in a Bash
// tool result, reviewer verdicts in task notifications naming their dispatch
// — as user records and as a queued_command attachment — a verdict cut
// mid-block kept as malformed evidence, a verdict predating the context
// field kept as historical, a verdict a compaction summary quotes read as a
// user row naming no dispatch, a pasted note quoting a notification with no
// <tool-use-id> read as a user row too, and a compaction summary opening
// with a quoted notification that names a live dispatch read as a user row
// naming none. The tool_calls column beside it is still cut at
// resultTextLimit.
func TestLensResponsesAreReadWhole(t *testing.T) {
	s := parseFixture(t, "testdata/lens_responses.jsonl")
	if len(s.LensResponses) != 8 {
		t.Fatalf("LensResponses len: got %d, want 8", len(s.LensResponses))
	}
	want := []struct {
		line       int
		turn       int
		origin     string
		dispatchID string
		lens       string
		status     string
		reason     string
		kind       string
	}{
		{7, 0, summary.OriginToolResult, "toolu_s1", lens.Security, lens.StatusParsed, "", lens.ContextStructured},
		{8, 1, summary.OriginTaskNotification, "toolu_c1", lens.Contract, lens.StatusParsed, "", lens.ContextStructured},
		{13, 2, summary.OriginTaskNotification, "toolu_q1", lens.Quality, lens.StatusParsed, "", lens.ContextHistorical},
		{15, 3, summary.OriginTaskNotification, "toolu_c2", lens.Contract, lens.StatusMalformed, lens.ReasonUnterminated, lens.ContextHistorical},
		// Queued while the assistant was mid-turn and injected as an
		// attachment prompt, so it lands in the turn in progress.
		{18, 3, summary.OriginTaskNotification, "toolu_c3", lens.Contract, lens.StatusParsed, "", lens.ContextStructured},
		// A compaction summary is a user record that is not a notification:
		// the block it quotes is stored as such, with no dispatch to answer.
		{20, 4, summary.OriginUser, "", lens.Contract, lens.StatusParsed, "", lens.ContextStructured},
		// A user record quoting the notification marker past its own text
		// is not the envelope the harness posts: stored as user text.
		{21, 5, summary.OriginUser, "", lens.Quality, lens.StatusParsed, "", lens.ContextStructured},
		// A compaction summary that opens with a quoted notification naming
		// a dispatch this session made (toolu_c3): the compaction flag keeps
		// it user text, so the dispatch id is not read off it.
		{22, 6, summary.OriginUser, "", lens.Contract, lens.StatusParsed, "", lens.ContextStructured},
	}
	for i, w := range want {
		r := s.LensResponses[i]
		if r.SourceLine != w.line || r.TurnIdx != w.turn || r.Origin != w.origin || r.DispatchID != w.dispatchID {
			t.Errorf("LensResponses[%d] position: got line %d turn %d %s %q, want line %d turn %d %s %q",
				i, r.SourceLine, r.TurnIdx, r.Origin, r.DispatchID, w.line, w.turn, w.origin, w.dispatchID)
		}
		if r.Lens != w.lens || r.Status != w.status || r.Reason != w.reason || r.ContextKind != w.kind {
			t.Errorf("LensResponses[%d] block: got %s %s/%s %s, want %s %s/%s %s",
				i, r.Lens, r.Status, r.Reason, r.ContextKind, w.lens, w.status, w.reason, w.kind)
		}
		if r.Ordinal != 0 || r.At.IsZero() {
			t.Errorf("LensResponses[%d]: ordinal %d at %s, want ordinal 0 and a timestamp", i, r.Ordinal, r.At)
		}
	}

	routed := s.LensResponses[0]
	if len(routed.Raw) <= resultTextLimit || !json.Valid([]byte(routed.Raw)) {
		t.Errorf("routed verdict raw: %d chars valid=%v, want the whole block past the %d cut", len(routed.Raw), json.Valid([]byte(routed.Raw)), resultTextLimit)
	}
	if routed.Context == nil || routed.Context.State != "clean" || len(routed.Context.Received) != 0 {
		t.Errorf("routed verdict context = %+v, want clean with nothing received", routed.Context)
	}
	for _, tc := range s.ToolCalls {
		if tc.CallID == "toolu_s1" && (len(tc.ResultSummary) > resultTextLimit+len("…") || !strings.HasSuffix(tc.ResultSummary, "…")) {
			t.Errorf("tool result summary is %d chars, want it still cut at %d", len(tc.ResultSummary), resultTextLimit)
		}
	}

	contract := s.LensResponses[1]
	var criteria []map[string]string
	if err := json.Unmarshal(contract.Criteria, &criteria); err != nil || len(criteria) != 3 {
		t.Errorf("contract criteria = %s (%v), want 3 intact", contract.Criteria, err)
	}
	var findings []map[string]any
	if err := json.Unmarshal(contract.Findings, &findings); err != nil || len(findings) != 2 {
		t.Errorf("contract findings = %s (%v), want 2 intact", contract.Findings, err)
	}
	if contract.Verdict != "findings" || !strings.HasPrefix(contract.Summary, "AC2 fails") {
		t.Errorf("contract verdict = %q summary %q", contract.Verdict, contract.Summary)
	}

	historical := s.LensResponses[2]
	if historical.Context != nil || !lens.Contaminated(historical.Block) {
		t.Errorf("historical verdict = context %+v contaminated %v, want no context and contamination read from prose",
			historical.Context, lens.Contaminated(historical.Block))
	}

	cut := s.LensResponses[3]
	if !cut.Unterminated || cut.Summary != "All criteria met on the second pass." || len(cut.Criteria) != 0 {
		t.Errorf("cut verdict = %+v, want unterminated with lens and summary recovered and no criteria", cut)
	}
}

// TestSubagentTranscriptsAreNotReadForLenses pins that a folded subagent
// transcript contributes no lens responses: the parent holds the same verdict
// as a notification or tool result.
func TestSubagentTranscriptsAreNotReadForLenses(t *testing.T) {
	sub := `{"type":"assistant","sessionId":"a1","isSidechain":true,"promptId":"p1","timestamp":"2026-09-01T10:00:00.000Z","message":{"id":"m1","role":"assistant","model":"claude-opus-5","content":[{"type":"text","text":"` + "```json\\n{\\\"lens\\\": \\\"quality\\\", \\\"verdict\\\": \\\"satisfied\\\", \\\"summary\\\": \\\"Fine.\\\"}\\n```" + `"}]}}` + "\n"
	parent := strings.NewReader(`{"type":"user","sessionId":"s1","promptId":"p1","timestamp":"2026-09-01T10:00:00.000Z","message":{"role":"user","content":"review"}}` + "\n")
	s, err := ParseWithSubagents(parent, []SubagentInput{{
		AgentType: "reviewer", ToolUseID: "toolu_1", Open: openString(sub),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Subagents) != 1 || !strings.Contains(s.Subagents[0].ResultSummary, "quality") {
		t.Fatalf("subagents = %+v, want the transcript folded", s.Subagents)
	}
	if len(s.LensResponses) != 0 {
		t.Fatalf("LensResponses = %+v, want none from a subagent transcript", s.LensResponses)
	}
}
