package claudeparse

import (
	"io"
	"sort"
	"time"

	"loom/internal/parse/summary"
)

// SubagentInput is one subagent transcript plus the dispatch metadata
// recorded beside it. Open is called once, when the transcript is folded, so
// a parent that dispatched dozens holds one file open at a time.
type SubagentInput struct {
	AgentType string
	ToolUseID string
	Open      func() (io.ReadCloser, error)
}

// SubagentParseFailureMarker is bumped into the parent's Unknown when a
// dispatched subagent transcript fails to parse. The row is still written,
// with no duration — this marker is what separates a transcript whose shape
// drifted from one with no measurable span.
const SubagentParseFailureMarker = "__subagent_parse_failed__"

// foldSubagents writes one Subagent row per dispatch: one for each shipped
// transcript, plus one for every Task call in the parent no transcript
// claimed. Duration is the span of the transcript's own records — for a
// background dispatch the parent's tool_result lands on acknowledgement, so
// its timestamp measures the handoff rather than the work — and is nil for a
// dispatch with no transcript, which keeps "dispatched but not measured"
// distinct from a measured zero.
func foldSubagents(st *state, subs []SubagentInput) {
	taskTurn := map[string]int{}
	for _, tc := range st.s.ToolCalls {
		if tc.Kind == summary.KindTask && tc.CallID != "" {
			taskTurn[tc.CallID] = tc.TurnIdx
		}
	}

	type folded struct {
		start time.Time
		sa    summary.Subagent
	}
	out := make([]folded, 0, len(subs))
	claimed := map[string]bool{}
	for _, in := range subs {
		claimed[in.ToolUseID] = true
		// -1 is the parser's "unattributed turn": no sidecar, or a
		// tool_use id this transcript never dispatched.
		f := folded{sa: summary.Subagent{
			ParentTurnIdx: -1,
			AgentType:     in.AgentType,
			ToolUseID:     in.ToolUseID,
		}}
		if idx, ok := taskTurn[in.ToolUseID]; ok {
			f.sa.ParentTurnIdx = idx
		}
		// A transcript we can't read or can't parse leaves the row with a
		// nil duration — unmeasured, which is the honest answer.
		if sub := parseSubagent(st, in); sub != nil {
			f.start = sub.s.StartTime
			if sub.stamps >= 2 {
				ms := sub.s.EndTime.Sub(sub.s.StartTime).Milliseconds()
				f.sa.DurationMs = &ms
			}
			f.sa.Prompt = truncate(firstUserMessage(sub.s), resultTextLimit)
			f.sa.ResultSummary = truncate(sub.lastAssistantText, resultTextLimit)
			f.sa.ErrorCount = len(sub.s.Errors)
		}
		out = append(out, f)
	}

	// seq is written from slice position, so the order has to be stable
	// across rebuilds. Directory order already is; start time makes the row
	// sequence follow dispatch order where it's known.
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].start.Equal(out[j].start) {
			return out[i].start.Before(out[j].start)
		}
		return out[i].sa.ToolUseID < out[j].sa.ToolUseID
	})
	for _, f := range out {
		st.s.Subagents = append(st.s.Subagents, f.sa)
	}

	// Dispatches whose transcript never reached us — not shipped, or
	// recorded inline in this stream where the isSidechain guards drop
	// them. They carry no start time, so the comparator above has nothing
	// to place them by; they follow the transcript-backed rows in the
	// parent's tool-call order, which a rebuild reproduces.
	for _, tc := range st.s.ToolCalls {
		if tc.Kind != summary.KindTask || tc.CallID == "" || claimed[tc.CallID] {
			continue
		}
		st.s.Subagents = append(st.s.Subagents, summary.Subagent{
			ParentTurnIdx: tc.TurnIdx,
			AgentType:     st.taskAgentType[tc.CallID],
			ToolUseID:     tc.CallID,
		})
	}
}

// parseSubagent folds one transcript, returning nil when it can't be read or
// parsed. A parse error here is not malformed JSON — feed absorbs that into
// Unknown — it is a decode failure on a known record type, so it is drift and
// is bumped onto the parent rather than dropped.
func parseSubagent(st *state, in SubagentInput) *state {
	rc, err := in.Open()
	if err != nil {
		// Opening is the caller's IO, reported there; nothing to say about
		// the transcript's shape.
		return nil
	}
	defer rc.Close()

	sub, err := parseStream(rc, true)
	if err != nil {
		st.bumpUnknown(SubagentParseFailureMarker, in.AgentType, time.Time{})
		return nil
	}
	sub.finalize()
	return sub
}

func firstUserMessage(s *summary.SessionSummary) string {
	for _, t := range s.Turns {
		if t.UserMessage != "" {
			return t.UserMessage
		}
	}
	return ""
}
