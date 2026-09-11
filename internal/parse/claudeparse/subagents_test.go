package claudeparse

import (
	"errors"
	"io"
	"strings"
	"testing"

	"loom/internal/parse/summary"
)

// dispatchingSession is one turn dispatching two Task calls.
const dispatchingSession = `{"type":"user","uuid":"u1","sessionId":"sess","promptId":"p1","timestamp":"2026-09-01T10:00:00.000Z","message":{"role":"user","content":"review it"}}
{"type":"assistant","uuid":"a1","sessionId":"sess","timestamp":"2026-09-01T10:00:01.000Z","message":{"role":"assistant","model":"claude-opus-5","content":[{"type":"tool_use","id":"toolu_A","name":"Task","input":{"description":"Quality lens","subagent_type":"reviewer","prompt":"review"}},{"type":"tool_use","id":"toolu_B","name":"Task","input":{"description":"Security lens","subagent_type":"security","prompt":"audit"}}],"stop_reason":"tool_use"}}
`

// reviewerTranscript is toolu_A's sidechain: two assistant records on haiku,
// the first fast and the second standard, with usage on each.
const reviewerTranscript = `{"type":"user","uuid":"s1","sessionId":"sess","isSidechain":true,"promptId":"sp1","timestamp":"2026-09-01T10:00:02.000Z","message":{"role":"user","content":"review"}}
{"type":"assistant","uuid":"s2","sessionId":"sess","isSidechain":true,"timestamp":"2026-09-01T10:01:00.000Z","message":{"role":"assistant","model":"claude-haiku-4-5","content":[{"type":"text","text":"reading"}],"stop_reason":"end_turn","usage":{"input_tokens":100,"output_tokens":50,"cache_read_input_tokens":1000,"cache_creation_input_tokens":200,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":200},"speed":"fast"}}}
{"type":"assistant","uuid":"s3","sessionId":"sess","isSidechain":true,"timestamp":"2026-09-01T10:02:00.000Z","message":{"role":"assistant","model":"claude-haiku-4-5","content":[{"type":"text","text":"verdict"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":100,"cache_creation_input_tokens":20,"speed":"standard"}}}
`

// securityTranscript is toolu_B's sidechain: a record on opus followed by one
// on a model the rate table does not know, both carrying tokens.
const securityTranscript = `{"type":"user","uuid":"t1","sessionId":"sess","isSidechain":true,"promptId":"sp2","timestamp":"2026-09-01T10:00:03.000Z","message":{"role":"user","content":"audit"}}
{"type":"assistant","uuid":"t2","sessionId":"sess","isSidechain":true,"timestamp":"2026-09-01T10:01:00.000Z","message":{"role":"assistant","model":"claude-opus-5","content":[{"type":"text","text":"auditing"}],"stop_reason":"end_turn","usage":{"input_tokens":100,"output_tokens":50}}}
{"type":"assistant","uuid":"t3","sessionId":"sess","isSidechain":true,"timestamp":"2026-09-01T10:02:00.000Z","message":{"role":"assistant","model":"claude-unreleased-9","content":[{"type":"text","text":"verdict"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}}}
`

// explorerTranscript is toolu_C's sidechain: its first assistant record names
// one model and carries no usage, and every token-carrying record after it
// ran on another.
const explorerTranscript = `{"type":"user","uuid":"v1","sessionId":"sess","isSidechain":true,"promptId":"sp3","timestamp":"2026-09-01T10:00:04.000Z","message":{"role":"user","content":"explore"}}
{"type":"assistant","uuid":"v2","sessionId":"sess","isSidechain":true,"timestamp":"2026-09-01T10:01:00.000Z","message":{"role":"assistant","model":"claude-opus-5","content":[{"type":"text","text":"starting"}],"stop_reason":"end_turn"}}
{"type":"user","uuid":"v3","sessionId":"sess","isSidechain":true,"promptId":"sp4","timestamp":"2026-09-01T10:01:30.000Z","message":{"role":"user","content":"go on"}}
{"type":"assistant","uuid":"v4","sessionId":"sess","isSidechain":true,"timestamp":"2026-09-01T10:02:00.000Z","message":{"role":"assistant","model":"claude-haiku-4-5","content":[{"type":"text","text":"exploring"}],"stop_reason":"end_turn","usage":{"input_tokens":100,"output_tokens":50}}}
{"type":"user","uuid":"v5","sessionId":"sess","isSidechain":true,"promptId":"sp5","timestamp":"2026-09-01T10:02:30.000Z","message":{"role":"user","content":"and finish"}}
{"type":"assistant","uuid":"v6","sessionId":"sess","isSidechain":true,"timestamp":"2026-09-01T10:03:00.000Z","message":{"role":"assistant","model":"claude-haiku-4-5","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}}}
`

func openString(s string) func() (io.ReadCloser, error) {
	return func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(s)), nil }
}

// TestSubagentUsageComesFromItsOwnTranscript pins that a dispatch is priced
// from what it consumed: its own model, speed and usage split, summed over
// its records — that a dispatch whose token-carrying records disagree on
// model or speed is marked mixed rather than attributed to the first, while
// a token-less record naming another model does not set the pair the tokens
// are priced at — and that a dispatch with no transcript carries nil usage
// rather than a zero that would read as a free dispatch.
func TestSubagentUsageComesFromItsOwnTranscript(t *testing.T) {
	s, err := ParseWithSubagents(strings.NewReader(dispatchingSession), []SubagentInput{
		{AgentType: "reviewer", ToolUseID: "toolu_A", Open: openString(reviewerTranscript)},
		{AgentType: "security", ToolUseID: "toolu_B", Open: openString(securityTranscript)},
		{AgentType: "explorer", ToolUseID: "toolu_C", Open: openString(explorerTranscript)},
		{AgentType: "unshipped", ToolUseID: "toolu_D", Open: func() (io.ReadCloser, error) {
			return nil, errors.New("not shipped")
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Subagents) != 4 {
		t.Fatalf("Subagents len: got %d, want 4", len(s.Subagents))
	}
	var reviewer, security, explorer, unshipped *summary.Subagent
	for i := range s.Subagents {
		switch s.Subagents[i].AgentType {
		case "reviewer":
			reviewer = &s.Subagents[i]
		case "security":
			security = &s.Subagents[i]
		case "explorer":
			explorer = &s.Subagents[i]
		case "unshipped":
			unshipped = &s.Subagents[i]
		}
	}
	if reviewer == nil || security == nil || explorer == nil || unshipped == nil {
		t.Fatalf("Subagents: got %+v, want a reviewer, a security, an explorer and an unshipped row", s.Subagents)
	}
	u := reviewer.Usage
	if u == nil {
		t.Fatal("reviewer Usage: got nil, want the transcript's usage")
	}
	if u.Model != "claude-haiku-4-5" || u.Speed != "fast" {
		t.Errorf("reviewer Usage model/speed: got %q/%q, want claude-haiku-4-5/fast", u.Model, u.Speed)
	}
	if u.InputTokens != 110 || u.OutputTokens != 55 || u.CacheReadTokens != 1100 ||
		u.CacheCreationTokens != 220 || u.CacheCreation1hTokens != 200 {
		t.Errorf("reviewer Usage tokens: got %+v, want 110/55/1100/220 (1h 200)", *u)
	}
	// Fast then standard on one prompt: the turn is mixed, so the dispatch is.
	if !u.Mixed {
		t.Error("reviewer Usage.Mixed: got false, want true: its records ran fast then standard")
	}
	if security.Usage == nil {
		t.Fatal("security Usage: got nil, want the transcript's usage")
	}
	if security.Usage.Model != "claude-opus-5" || !security.Usage.Mixed {
		t.Errorf("security Usage: got model %q mixed %v, want claude-opus-5 and mixed: a later record named another model",
			security.Usage.Model, security.Usage.Mixed)
	}
	// The model priced is the one the tokens ran at, not the first named: a
	// token-less opening record on another model is no disagreement.
	if explorer.Usage == nil {
		t.Fatal("explorer Usage: got nil, want the transcript's usage")
	}
	if explorer.Usage.Model != "claude-haiku-4-5" || explorer.Usage.Mixed ||
		explorer.Usage.InputTokens != 110 || explorer.Usage.OutputTokens != 55 {
		t.Errorf("explorer Usage: got model %q mixed %v tokens %d/%d, want claude-haiku-4-5, not mixed, 110/55",
			explorer.Usage.Model, explorer.Usage.Mixed, explorer.Usage.InputTokens, explorer.Usage.OutputTokens)
	}
	if unshipped.Usage != nil {
		t.Errorf("unshipped Usage: got %+v, want nil for a dispatch with no transcript", *unshipped.Usage)
	}
}
