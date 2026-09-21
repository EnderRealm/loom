package codexparse

import (
	"os"
	"testing"
	"time"

	"loom/internal/parse/drift"
	"loom/internal/parse/summary"
)

// TestUnknownRecordsAreCountedAndOrdered locks in two correctness guarantees
// the previous implementation violated: Unknown records expose accurate Count
// (the prior code appended a Count=0 copy on first sight and never refreshed
// the slice), and FirstSeen comes from the record's own timestamp so two runs
// over the same input produce identical output.
func TestUnknownRecordsAreCountedAndOrdered(t *testing.T) {
	first := parseFixture(t, "testdata/unknown_records.jsonl")
	second := parseFixture(t, "testdata/unknown_records.jsonl")

	// Basic happy-path shape: session_meta and turn_context land in the
	// summary header; the timeline anchors at the earliest record's timestamp.
	if first.Agent != summary.AgentCodex {
		t.Errorf("Agent: got %q, want %q", first.Agent, summary.AgentCodex)
	}
	if first.SessionID != "sess-abc" {
		t.Errorf("SessionID: got %q, want %q", first.SessionID, "sess-abc")
	}
	if first.Cwd != "/tmp/p" {
		t.Errorf("Cwd: got %q, want %q", first.Cwd, "/tmp/p")
	}
	wantStart := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
	if !first.StartTime.Equal(wantStart) {
		t.Errorf("StartTime: got %s, want %s", first.StartTime, wantStart)
	}

	if len(first.Unknown) != 3 {
		t.Fatalf("Unknown len: got %d, want 3", len(first.Unknown))
	}

	for _, u := range first.Unknown {
		key := u.Type + "::" + u.Subtype
		switch key {
		case "response_item::future_response_kind":
			if u.Count != 2 {
				t.Errorf("%s Count: got %d, want 2", key, u.Count)
			}
			want := time.Date(2026, 4, 1, 10, 0, 2, 0, time.UTC)
			if !u.FirstSeen.Equal(want) {
				t.Errorf("%s FirstSeen: got %s, want %s", key, u.FirstSeen, want)
			}
		case "event_msg::future_event_kind":
			if u.Count != 2 {
				t.Errorf("%s Count: got %d, want 2", key, u.Count)
			}
			want := time.Date(2026, 4, 1, 10, 0, 4, 0, time.UTC)
			if !u.FirstSeen.Equal(want) {
				t.Errorf("%s FirstSeen: got %s, want %s", key, u.FirstSeen, want)
			}
		case "future_top_level_kind::":
			if u.Count != 1 {
				t.Errorf("%s Count: got %d, want 1", key, u.Count)
			}
			want := time.Date(2026, 4, 1, 10, 0, 6, 0, time.UTC)
			if !u.FirstSeen.Equal(want) {
				t.Errorf("%s FirstSeen: got %s, want %s", key, u.FirstSeen, want)
			}
		default:
			t.Errorf("unexpected Unknown entry: %s", key)
		}
	}

	if len(first.Unknown) != len(second.Unknown) {
		t.Fatalf("Unknown len drift across runs: %d vs %d", len(first.Unknown), len(second.Unknown))
	}
	for i := range first.Unknown {
		if first.Unknown[i] != second.Unknown[i] {
			t.Errorf("Unknown[%d] drift: %#v vs %#v", i, first.Unknown[i], second.Unknown[i])
		}
	}
}

func parseFixture(t *testing.T, path string) *summary.SessionSummary {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	s, err := Parse(f)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return s
}

// TestSessionMetaSourceShapes covers the two forms codex-cli writes for
// session_meta.source — a plain string for a top-level session, an object
// describing the spawn for a subagent one — and asserts that a payload we
// cannot model degrades to an Unknown record instead of discarding the
// session, which is how every subagent transcript was being lost.
func TestSessionMetaSourceShapes(t *testing.T) {
	sub := parseFixture(t, "testdata/subagent_source.jsonl")
	if sub.SessionID != "sess-sub" {
		t.Errorf("SessionID: got %q, want %q", sub.SessionID, "sess-sub")
	}
	if sub.Cwd != "/tmp/sub" {
		t.Errorf("Cwd: got %q, want %q", sub.Cwd, "/tmp/sub")
	}
	if sub.CLIVersion != "0.153.4" {
		t.Errorf("CLIVersion: got %q, want %q", sub.CLIVersion, "0.153.4")
	}
	if sub.ParentSessionID != "sess-parent" || sub.SpawnDepth != 1 {
		t.Errorf("spawn: got %q depth %d, want sess-parent depth 1",
			sub.ParentSessionID, sub.SpawnDepth)
	}

	// The drifted line (a numeric call_id) sits mid-file: the lines after it
	// still land, and the record itself is counted rather than silent.
	if len(sub.Turns) != 1 {
		t.Fatalf("Turns len: got %d, want 1", len(sub.Turns))
	}
	if sub.Turns[0].UserMessage != "check the proposal" {
		t.Errorf("UserMessage: got %q, want %q", sub.Turns[0].UserMessage,
			"check the proposal")
	}
	if sub.Turns[0].AssistantText != "looks fine" {
		t.Errorf("AssistantText: got %q, want %q", sub.Turns[0].AssistantText,
			"looks fine")
	}
	if len(sub.Unknown) != 1 {
		t.Fatalf("Unknown len: got %d, want 1", len(sub.Unknown))
	}
	u := sub.Unknown[0]
	wantSub := drift.UnmodeledPayloadMarker + ":call_id"
	if u.Type != "response_item" || u.Subtype != wantSub {
		t.Errorf("Unknown entry: got %s::%s, want response_item::%s",
			u.Type, u.Subtype, wantSub)
	}
	if u.Count != 1 {
		t.Errorf("Unknown Count: got %d, want 1", u.Count)
	}
	wantSeen := time.Date(2026, 4, 2, 9, 0, 3, 0, time.UTC)
	if !u.FirstSeen.Equal(wantSeen) {
		t.Errorf("Unknown FirstSeen: got %s, want %s", u.FirstSeen, wantSeen)
	}

	top := parseFixture(t, "testdata/string_source.jsonl")
	if top.SessionID != "sess-top" {
		t.Errorf("SessionID: got %q, want %q", top.SessionID, "sess-top")
	}
	if top.Cwd != "/tmp/top" {
		t.Errorf("Cwd: got %q, want %q", top.Cwd, "/tmp/top")
	}
	if top.CLIVersion != "0.153.4" {
		t.Errorf("CLIVersion: got %q, want %q", top.CLIVersion, "0.153.4")
	}
	if top.ParentSessionID != "" || top.SpawnDepth != 0 {
		t.Errorf("spawn: got %q depth %d, want none for the string source",
			top.ParentSessionID, top.SpawnDepth)
	}
	if len(top.Unknown) != 0 {
		t.Errorf("Unknown len: got %d, want 0", len(top.Unknown))
	}
}

// TestSessionMetaPayloadDriftDegrades pins the regression that motivated the
// degrade path: a session_meta we cannot unmarshal used to abort the whole
// file. The header fields it carried are lost — nothing is applied once the
// payload fails — but the session still parses, later records still land, and
// Cwd is recovered from turn_context. It also pins what replaced the log line
// the abort used to produce: the drifted field's name in the subtype, two
// records drifting on that same field collapsing to one counted row, and the
// bare marker for a payload whose type error names no field at all.
func TestSessionMetaPayloadDriftDegrades(t *testing.T) {
	s := parseFixture(t, "testdata/meta_payload_drift.jsonl")

	if s.SessionID != "" {
		t.Errorf("SessionID: got %q, want %q", s.SessionID, "")
	}
	if s.CLIVersion != "" {
		t.Errorf("CLIVersion: got %q, want %q", s.CLIVersion, "")
	}
	if s.Cwd != "/tmp/drift" {
		t.Errorf("Cwd: got %q, want %q", s.Cwd, "/tmp/drift")
	}

	if len(s.Turns) != 1 {
		t.Fatalf("Turns len: got %d, want 1", len(s.Turns))
	}
	if s.Turns[0].UserMessage != "read the notes" {
		t.Errorf("UserMessage: got %q, want %q", s.Turns[0].UserMessage,
			"read the notes")
	}
	if s.Turns[0].AssistantText != "notes read" {
		t.Errorf("AssistantText: got %q, want %q", s.Turns[0].AssistantText,
			"notes read")
	}

	if len(s.Unknown) != 2 {
		t.Fatalf("Unknown len: got %d, want 2", len(s.Unknown))
	}

	// Fieldless fallback, sorted first: the payload is valid JSON but not an
	// object, so the type error carries no field path.
	bare := s.Unknown[0]
	if bare.Type != "session_meta" || bare.Subtype != drift.UnmodeledPayloadMarker {
		t.Errorf("Unknown entry: got %s::%s, want session_meta::%s",
			bare.Type, bare.Subtype, drift.UnmodeledPayloadMarker)
	}
	if bare.Count != 1 {
		t.Errorf("Unknown Count: got %d, want 1", bare.Count)
	}

	// Two records drift on cwd; they collapse to one row carrying the field
	// name and the count, with FirstSeen from the earlier of them.
	u := s.Unknown[1]
	wantSub := drift.UnmodeledPayloadMarker + ":cwd"
	if u.Type != "session_meta" || u.Subtype != wantSub {
		t.Errorf("Unknown entry: got %s::%s, want session_meta::%s",
			u.Type, u.Subtype, wantSub)
	}
	if u.Count != 2 {
		t.Errorf("Unknown Count: got %d, want 2", u.Count)
	}
	wantSeen := time.Date(2026, 4, 2, 9, 0, 0, 0, time.UTC)
	if !u.FirstSeen.Equal(wantSeen) {
		t.Errorf("Unknown FirstSeen: got %s, want %s", u.FirstSeen, wantSeen)
	}
}

// TestTurnConditionsLand pins the per-turn model and effort off each
// turn_context, and the CLI version every turn inherits from session_meta —
// a rollout records it once, so it is the value in force for all of them.
func TestTurnConditionsLand(t *testing.T) {
	s := parseFixture(t, "testdata/turn_conditions.jsonl")
	if len(s.Turns) != 2 {
		t.Fatalf("Turns len: got %d, want 2", len(s.Turns))
	}
	want := []struct{ model, effort, version string }{
		{"gpt-5.4", "xhigh", "0.160.0"},
		{"gpt-5.4-mini", "low", "0.160.0"},
	}
	for i, w := range want {
		got := s.Turns[i]
		if got.Model != w.model || got.Effort != w.effort || got.CLIVersion != w.version {
			t.Errorf("Turn[%d]: got %q/%q/%q, want %q/%q/%q", i,
				got.Model, got.Effort, got.CLIVersion, w.model, w.effort, w.version)
		}
	}
}

// TestTokenUsageReconcilesCumulativeSamples pins turn usage as the sum of
// attributable deltas of the session's cumulative counter. Turn 1 carries the
// samples of a retained one-turn reviewer rollout whose final counter reads
// 185,213 input / 910 output; assigning each sample's last_token_usage in
// place of the previous one landed 52,317 / 108. A repeated counter (the
// rate-limit refresh after the last request) adds nothing, a second turn
// attributes only its own samples, and a counter that drops below the
// baseline attributes just that sample's own usage, with the next delta taken
// against the reset counter.
func TestTokenUsageReconcilesCumulativeSamples(t *testing.T) {
	s := parseFixture(t, "testdata/token_usage.jsonl")
	if len(s.Turns) != 2 {
		t.Fatalf("Turns len: got %d, want 2", len(s.Turns))
	}
	if len(s.TokenCounts) != 8 {
		t.Fatalf("TokenCounts len: got %d, want 8", len(s.TokenCounts))
	}

	want := []struct{ input, output, cached int64 }{
		{185213, 910, 132352},
		// 10000 + 3000 (reset, own usage) + 4000 (against the reset counter).
		{17000, 210, 11500},
	}
	for i, w := range want {
		got := s.Turns[i]
		if got.InputTokens != w.input || got.OutputTokens != w.output ||
			got.CacheReadTokens != w.cached {
			t.Errorf("Turn[%d] tokens: got %d/%d/%d, want %d/%d/%d", i,
				got.InputTokens, got.OutputTokens, got.CacheReadTokens,
				w.input, w.output, w.cached)
		}
		if got.CacheReadTokens > got.InputTokens {
			t.Errorf("Turn[%d]: cache read %d exceeds input %d", i,
				got.CacheReadTokens, got.InputTokens)
		}
	}

	var sumIn, sumOut, sumCached int64
	for _, tu := range s.Turns {
		sumIn += tu.InputTokens
		sumOut += tu.OutputTokens
		sumCached += tu.CacheReadTokens
	}
	if s.InputTokens != sumIn || s.OutputTokens != sumOut ||
		s.CacheReadTokens != sumCached {
		t.Errorf("session tokens: got %d/%d/%d, want sum over turns %d/%d/%d",
			s.InputTokens, s.OutputTokens, s.CacheReadTokens,
			sumIn, sumOut, sumCached)
	}

	// Each sample's own attestation keeps the subsets; the repeated sample is
	// still recorded as an observation even though it attributes nothing.
	for i, tc := range s.TokenCounts {
		if tc.Cached > tc.Input {
			t.Errorf("TokenCounts[%d]: cached %d exceeds input %d", i,
				tc.Cached, tc.Input)
		}
		if tc.Reasoning > tc.Output {
			t.Errorf("TokenCounts[%d]: reasoning %d exceeds output %d", i,
				tc.Reasoning, tc.Output)
		}
	}
	if s.TokenCounts[3].TurnIdx != 0 || s.TokenCounts[4].TurnIdx != 0 ||
		s.TokenCounts[5].TurnIdx != 1 {
		t.Errorf("TokenCounts turn attribution: got %d/%d/%d, want 0/0/1",
			s.TokenCounts[3].TurnIdx, s.TokenCounts[4].TurnIdx,
			s.TokenCounts[5].TurnIdx)
	}
	if s.TokenCounts[4].LimitUsedPercent != 13.0 {
		t.Errorf("repeated sample LimitUsedPercent: got %v, want 13",
			s.TokenCounts[4].LimitUsedPercent)
	}
}

// TestAttributeTokenUsageDeltas pins the attribution rule sample by sample:
// no baseline and a reset attribute the sample's own usage, a continuous
// counter attributes its advance, a repeated counter attributes zero, and
// every attributed delta keeps cached within input and reasoning within
// output when the counter stream does.
func TestAttributeTokenUsageDeltas(t *testing.T) {
	st := newState(&summary.SessionSummary{})
	steps := []struct {
		name        string
		total, last tokenUsage
		want        tokenUsage
	}{
		{"first", tokenUsage{35036, 0, 228, 0, 35264},
			tokenUsage{35036, 0, 228, 0, 35264},
			tokenUsage{InputTokens: 35036, OutputTokens: 228}},
		{"continuous", tokenUsage{80799, 34816, 637, 78, 81436},
			tokenUsage{45763, 34816, 409, 78, 46172},
			tokenUsage{InputTokens: 45763, CachedInputTokens: 34816,
				OutputTokens: 409, ReasoningOutputTokens: 78}},
		{"repeated", tokenUsage{80799, 34816, 637, 78, 81436},
			tokenUsage{45763, 34816, 409, 78, 46172},
			tokenUsage{}},
		{"reset", tokenUsage{3000, 1000, 50, 10, 3050},
			tokenUsage{3000, 1000, 50, 10, 3050},
			tokenUsage{InputTokens: 3000, CachedInputTokens: 1000,
				OutputTokens: 50, ReasoningOutputTokens: 10}},
		{"after reset", tokenUsage{7000, 3500, 110, 25, 7110},
			tokenUsage{4000, 2500, 60, 15, 4060},
			tokenUsage{InputTokens: 4000, CachedInputTokens: 2500,
				OutputTokens: 60, ReasoningOutputTokens: 15}},
	}
	for _, step := range steps {
		got := st.attributeTokenUsage(step.total, step.last)
		if got != step.want {
			t.Errorf("%s: got %+v, want %+v", step.name, got, step.want)
		}
		if got.CachedInputTokens > got.InputTokens {
			t.Errorf("%s: cached %d exceeds input %d", step.name,
				got.CachedInputTokens, got.InputTokens)
		}
		if got.ReasoningOutputTokens > got.OutputTokens {
			t.Errorf("%s: reasoning %d exceeds output %d", step.name,
				got.ReasoningOutputTokens, got.OutputTokens)
		}
	}
}
