package claudeparse

import (
	"encoding/json"
	"os"
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

// TestSubagentHandbacksAnswerTheirLaunch pins where a native lens's verdict
// is read from since the task notification stopped quoting it: the meta
// hand-back the harness relays, its report indented two spaces under the
// frame, on a user record or — round 3's contract, landing mid-turn — on a
// queued_command attachment. A hand-back answers the Agent call whose async
// launch named its agent, round 2's two launches batched in one assistant
// message included; one from an agent the session never launched names no
// dispatch. Neither the hand-backs nor the notifications after them open a
// turn, so the fixture's whole three-round run stays the invocation's one
// turn.
func TestSubagentHandbacksAnswerTheirLaunch(t *testing.T) {
	s := parseFixture(t, "testdata/lens_handback.jsonl")
	if len(s.Turns) != 1 || len(s.Unknown) != 0 {
		t.Fatalf("turns = %d unknown = %+v, want 1 turn and no drift: a hand-back is meta and opens none", len(s.Turns), s.Unknown)
	}
	want := []struct {
		line       int
		origin     string
		dispatchID string
		lens       string
	}{
		{8, summary.OriginToolResult, "toolu_s1", lens.Security},
		{9, summary.OriginTaskNotification, "toolu_c1", lens.Contract},
		{11, summary.OriginTaskNotification, "toolu_q1", lens.Quality},
		{20, summary.OriginToolResult, "toolu_s2", lens.Security},
		{21, summary.OriginTaskNotification, "toolu_c2", lens.Contract},
		{23, summary.OriginTaskNotification, "toolu_q2", lens.Quality},
		{32, summary.OriginToolResult, "toolu_s3", lens.Security},
		// The queued_command attachment carrying the hand-back's origin.
		{33, summary.OriginTaskNotification, "toolu_c3", lens.Contract},
		{35, summary.OriginTaskNotification, "toolu_q3", lens.Quality},
		// Agent azz was never launched here: its hand-back is kept, naming
		// no dispatch.
		{37, summary.OriginTaskNotification, "", lens.Contract},
	}
	if len(s.LensResponses) != len(want) {
		t.Fatalf("LensResponses len: got %d, want %d", len(s.LensResponses), len(want))
	}
	for i, w := range want {
		r := s.LensResponses[i]
		if r.SourceLine != w.line || r.TurnIdx != 0 || r.Origin != w.origin || r.DispatchID != w.dispatchID || r.Lens != w.lens {
			t.Errorf("LensResponses[%d] = line %d turn %d %s %q %s, want line %d turn 0 %s %q %s",
				i, r.SourceLine, r.TurnIdx, r.Origin, r.DispatchID, r.Lens, w.line, w.origin, w.dispatchID, w.lens)
		}
		if r.Status != lens.StatusParsed || r.ContextKind != lens.ContextStructured || r.At.IsZero() {
			t.Errorf("LensResponses[%d] = %s/%s %s at %s, want the indented block parsed whole with its context",
				i, r.Status, r.Reason, r.ContextKind, r.At)
		}
	}
	var criteria []map[string]string
	if err := json.Unmarshal(s.LensResponses[1].Criteria, &criteria); err != nil || len(criteria) != 1 || criteria[0]["id"] != "AC1" {
		t.Errorf("hand-back criteria = %s (%v), want AC1 intact", s.LensResponses[1].Criteria, err)
	}
}

// TestHandbackIsReadOnce pins that one hand-back delivered twice — as the
// queued_command attachment it arrived on and again as a user record — stores
// its verdict once, so it cannot answer its dispatch twice and read as a
// retry.
func TestHandbackIsReadOnce(t *testing.T) {
	raw, err := os.ReadFile("testdata/lens_handback.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	var queued struct {
		Attachment struct {
			Origin json.RawMessage `json:"origin"`
		} `json:"attachment"`
	}
	if err := json.Unmarshal([]byte(lines[32]), &queued); err != nil || len(queued.Attachment.Origin) == 0 {
		t.Fatalf("line 33 is not the queued hand-back: %v", err)
	}
	again := `{"type":"user","sessionId":"handback-fixture","isSidechain":false,"isMeta":true,"timestamp":"2026-09-22T10:30:07.500Z","origin":` +
		string(queued.Attachment.Origin) + `,"message":{"role":"user","content":"Another Claude session sent a message"}}`
	s, err := Parse(strings.NewReader(string(raw) + again + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	var contract3 int
	for _, r := range s.LensResponses {
		if r.DispatchID == "toolu_c3" {
			contract3++
		}
	}
	if len(s.LensResponses) != 10 || contract3 != 1 {
		t.Errorf("LensResponses = %d with %d answering toolu_c3, want 10 and 1: the repeated hand-back is read once", len(s.LensResponses), contract3)
	}
}

// TestOriginDriftKeepsTheTurn pins that an origin whose shape the parser does
// not model is not a hand-back and does not cost the record: the prompt
// still opens its turn and nothing is counted as drift.
func TestOriginDriftKeepsTheTurn(t *testing.T) {
	s, err := Parse(strings.NewReader(`{"type":"user","sessionId":"s1","promptId":"p1","timestamp":"2026-09-01T10:00:00.000Z","origin":"human","message":{"role":"user","content":"carry on"}}` + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Turns) != 1 || s.Turns[0].UserMessage != "carry on" || len(s.Unknown) != 0 {
		t.Errorf("turns = %+v unknown = %+v, want the prompt's turn and no drift", s.Turns, s.Unknown)
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
