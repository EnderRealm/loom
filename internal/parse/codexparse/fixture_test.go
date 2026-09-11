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
	wantSub := UnmodeledPayloadMarker + ":call_id"
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
	if bare.Type != "session_meta" || bare.Subtype != UnmodeledPayloadMarker {
		t.Errorf("Unknown entry: got %s::%s, want session_meta::%s",
			bare.Type, bare.Subtype, UnmodeledPayloadMarker)
	}
	if bare.Count != 1 {
		t.Errorf("Unknown Count: got %d, want 1", bare.Count)
	}

	// Two records drift on cwd; they collapse to one row carrying the field
	// name and the count, with FirstSeen from the earlier of them.
	u := s.Unknown[1]
	wantSub := UnmodeledPayloadMarker + ":cwd"
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
