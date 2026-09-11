package claudeparse

import (
	"os"
	"testing"
	"time"

	"loom/internal/parse/summary"
)

// TestUnknownRecordsAreCountedAndOrdered locks in two correctness guarantees
// that drift telemetry depends on: every Unknown entry exposes its real Count,
// and FirstSeen comes from the record's own timestamp (not wall clock) so two
// runs over the same input produce identical output.
func TestUnknownRecordsAreCountedAndOrdered(t *testing.T) {
	first := parseFixture(t, "testdata/unknown_records.jsonl")
	second := parseFixture(t, "testdata/unknown_records.jsonl")

	// Basic happy-path shape: the fixture contains one user message and one
	// assistant message, which should produce one turn with a user prompt and
	// an assistant response.
	if first.Agent != summary.AgentClaude {
		t.Errorf("Agent: got %q, want %q", first.Agent, summary.AgentClaude)
	}
	if got := len(first.Turns); got != 1 {
		t.Errorf("Turns: got %d, want 1", got)
	} else {
		turn := first.Turns[0]
		if turn.UserMessage == "" {
			t.Errorf("Turn[0].UserMessage is empty")
		}
		if turn.AssistantText == "" {
			t.Errorf("Turn[0].AssistantText is empty")
		}
	}
	wantStart := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
	if !first.StartTime.Equal(wantStart) {
		t.Errorf("StartTime: got %s, want %s", first.StartTime, wantStart)
	}

	if len(first.Unknown) != 2 {
		t.Fatalf("Unknown len: got %d, want 2 (system::unrecognized_subtype, future_top_level_kind::)", len(first.Unknown))
	}

	for _, u := range first.Unknown {
		switch u.Type + "::" + u.Subtype {
		case "system::unrecognized_subtype":
			if u.Count != 2 {
				t.Errorf("system::unrecognized_subtype Count: got %d, want 2", u.Count)
			}
			want := time.Date(2026, 4, 1, 10, 0, 2, 0, time.UTC)
			if !u.FirstSeen.Equal(want) {
				t.Errorf("system::unrecognized_subtype FirstSeen: got %s, want %s", u.FirstSeen, want)
			}
		case "future_top_level_kind::":
			if u.Count != 3 {
				t.Errorf("future_top_level_kind Count: got %d, want 3", u.Count)
			}
			want := time.Date(2026, 4, 1, 10, 0, 4, 0, time.UTC)
			if !u.FirstSeen.Equal(want) {
				t.Errorf("future_top_level_kind FirstSeen: got %s, want %s", u.FirstSeen, want)
			}
		default:
			t.Errorf("unexpected Unknown entry: %s::%s", u.Type, u.Subtype)
		}
	}

	// Reproducibility: two runs must agree on Unknown order and content.
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

// TestTurnConditionsLand pins the per-turn model, effort and CLI version: each
// comes off the turn's own assistant record, perTurnEffort overrides effort
// when set, a turn whose records carried none reports empty rather than
// inheriting a neighbour's, and an API error's "<synthetic>" placeholder does
// not stand in for the model that then answered.
func TestTurnConditionsLand(t *testing.T) {
	s := parseFixture(t, "testdata/turn_conditions.jsonl")
	if len(s.Turns) != 4 {
		t.Fatalf("Turns len: got %d, want 4", len(s.Turns))
	}
	want := []struct{ model, effort, version string }{
		{"claude-opus-5", "high", "2.1.267"},
		{"claude-sonnet-5", "low", "2.1.267"},
		{"", "", ""},
		{"claude-opus-5", "high", "2.1.267"},
	}
	for i, w := range want {
		got := s.Turns[i]
		if got.Model != w.model || got.Effort != w.effort || got.CLIVersion != w.version {
			t.Errorf("Turn[%d]: got %q/%q/%q, want %q/%q/%q", i,
				got.Model, got.Effort, got.CLIVersion, w.model, w.effort, w.version)
		}
	}
}

// TestUsageBreakdownLands pins the pricing inputs off usage: the cache write
// and its 1h-labelled share accumulate per turn — a record with no breakdown
// contributes to the write but not to the 1h share — speed is the first the
// turn's records carried, and a turn whose records then switched speed is
// marked mixed.
func TestUsageBreakdownLands(t *testing.T) {
	s := parseFixture(t, "testdata/usage_breakdown.jsonl")
	if len(s.Turns) != 2 {
		t.Fatalf("Turns len: got %d, want 2", len(s.Turns))
	}
	first := s.Turns[0]
	if first.InputTokens != 15 || first.OutputTokens != 27 || first.CacheReadTokens != 400 {
		t.Errorf("Turn[0] tokens: got %d/%d/%d, want 15/27/400", first.InputTokens, first.OutputTokens, first.CacheReadTokens)
	}
	if first.CacheCreationTokens != 550 || first.CacheCreation1hTokens != 400 {
		t.Errorf("Turn[0] cache creation: got %d (1h %d), want 550 (1h 400)", first.CacheCreationTokens, first.CacheCreation1hTokens)
	}
	if first.Speed != "fast" {
		t.Errorf("Turn[0] Speed: got %q, want fast", first.Speed)
	}
	if !first.Mixed {
		t.Error("Turn[0] Mixed: got false, want true: its records ran fast then standard")
	}
	second := s.Turns[1]
	if second.Mixed {
		t.Error("Turn[1] Mixed: got true, want false")
	}
	if second.CacheCreationTokens != 4 || second.CacheCreation1hTokens != 0 || second.Speed != "" {
		t.Errorf("Turn[1]: got cache creation %d (1h %d) speed %q, want 4 (1h 0) and no speed",
			second.CacheCreationTokens, second.CacheCreation1hTokens, second.Speed)
	}
}
