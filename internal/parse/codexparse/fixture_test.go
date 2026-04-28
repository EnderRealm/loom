package codexparse

import (
	"os"
	"testing"
	"time"

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
