package summarize

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"loom/internal/parse/claudeparse"
	"loom/internal/summaries"
)

// Parent session: two turns, the second dispatching two background Task
// calls. Both tool_results land within seconds of dispatch — the
// acknowledgement, not the work.
const parentSession = `{"type":"user","uuid":"u1","sessionId":"sess-parent","promptId":"p1","timestamp":"2026-09-01T10:00:00.000Z","message":{"role":"user","content":"open the ticket"}}
{"type":"assistant","uuid":"a1","sessionId":"sess-parent","timestamp":"2026-09-01T10:00:01.000Z","message":{"role":"assistant","model":"claude-opus","content":[{"type":"text","text":"on it"}],"stop_reason":"end_turn"}}
{"type":"user","uuid":"u2","sessionId":"sess-parent","promptId":"p2","timestamp":"2026-09-01T10:01:00.000Z","message":{"role":"user","content":"run the review lenses"}}
{"type":"assistant","uuid":"a2","sessionId":"sess-parent","timestamp":"2026-09-01T10:01:01.000Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_A","name":"Task","input":{"description":"Quality lens round 2","subagent_type":"reviewer","prompt":"review the diff"}},{"type":"tool_use","id":"toolu_B","name":"Task","input":{"description":"Security lens round 2","subagent_type":"security","prompt":"audit the diff"}}],"stop_reason":"tool_use"}}
{"type":"user","uuid":"u3","sessionId":"sess-parent","timestamp":"2026-09-01T10:01:04.000Z","toolUseResult":{"status":"dispatched"},"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_A","content":"agent started"}]}}
{"type":"user","uuid":"u4","sessionId":"sess-parent","timestamp":"2026-09-01T10:01:04.000Z","toolUseResult":{"status":"dispatched"},"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_B","content":"agent started"}]}}
`

// Sidechain of toolu_A: four minutes of work behind a three-second ack. Its
// last message carries two text blocks, so the result summary has to join
// them the way the turn's assistant text does.
const reviewerSidechain = `{"type":"user","uuid":"s1","sessionId":"sess-parent","isSidechain":true,"promptId":"sp1","timestamp":"2026-09-01T10:01:01.500Z","message":{"role":"user","content":"review the diff"}}
{"type":"assistant","uuid":"s2","sessionId":"sess-parent","isSidechain":true,"timestamp":"2026-09-01T10:03:00.000Z","message":{"role":"assistant","content":[{"type":"text","text":"reading files"}],"stop_reason":"end_turn"}}
{"type":"assistant","uuid":"s3","sessionId":"sess-parent","isSidechain":true,"timestamp":"2026-09-01T10:05:01.500Z","message":{"role":"assistant","content":[{"type":"text","text":"verdict: ship it"},{"type":"text","text":"no blockers"}],"stop_reason":"end_turn"}}
`

// Sidechain of toolu_B: a single record, so no span can be measured.
const securitySidechain = `{"type":"user","uuid":"s4","sessionId":"sess-parent","isSidechain":true,"promptId":"sp2","timestamp":"2026-09-01T10:01:02.000Z","message":{"role":"user","content":"audit the diff"}}
`

// Sidecar-less transcript: nothing ties it to a dispatch.
const orphanSidechain = `{"type":"user","uuid":"s5","sessionId":"sess-parent","isSidechain":true,"promptId":"sp3","timestamp":"2026-09-01T10:02:00.000Z","message":{"role":"user","content":"explore the tree"}}
{"type":"assistant","uuid":"s6","sessionId":"sess-parent","isSidechain":true,"timestamp":"2026-09-01T10:02:30.000Z","message":{"role":"assistant","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn"}}
`

// Second parent: one turn dispatching the same agent type twice, the shape a
// /work round produces when it re-runs a lens. Two dispatches, two rows.
const repeatSession = `{"type":"user","uuid":"w1","sessionId":"sess-repeat","promptId":"r1","timestamp":"2026-09-01T10:19:00.000Z","message":{"role":"user","content":"review it twice"}}
{"type":"assistant","uuid":"w2","sessionId":"sess-repeat","timestamp":"2026-09-01T10:19:30.000Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_D","name":"Task","input":{"description":"Quality lens round 2","subagent_type":"reviewer","prompt":"review again"}},{"type":"tool_use","id":"toolu_E","name":"Task","input":{"description":"Quality lens round 1","subagent_type":"reviewer","prompt":"review once"}}],"stop_reason":"tool_use"}}
`

// Round two, dispatched second but first in directory order.
const repeatTwoSidechain = `{"type":"user","uuid":"x1","sessionId":"sess-repeat","isSidechain":true,"promptId":"xp1","timestamp":"2026-09-01T10:22:00.000Z","message":{"role":"user","content":"review round two"}}
{"type":"assistant","uuid":"x2","sessionId":"sess-repeat","isSidechain":true,"timestamp":"2026-09-01T10:24:00.000Z","message":{"role":"assistant","content":[{"type":"text","text":"round two done"}],"stop_reason":"end_turn"}}
`

const repeatOneSidechain = `{"type":"user","uuid":"y1","sessionId":"sess-repeat","isSidechain":true,"promptId":"yp1","timestamp":"2026-09-01T10:20:00.000Z","message":{"role":"user","content":"review round one"}}
{"type":"assistant","uuid":"y2","sessionId":"sess-repeat","isSidechain":true,"timestamp":"2026-09-01T10:21:00.000Z","message":{"role":"assistant","content":[{"type":"text","text":"round one done"}],"stop_reason":"end_turn"}}
`

// Third parent: its dispatch's transcript drifted. isSidechain arrives as a
// string, which decodes cleanly at the probe and fails on the typed record —
// real shape drift, not a malformed line.
const driftSession = `{"type":"user","uuid":"z1","sessionId":"sess-drift","promptId":"d1","timestamp":"2026-09-01T12:00:00.000Z","message":{"role":"user","content":"explore"}}
{"type":"assistant","uuid":"z2","sessionId":"sess-drift","timestamp":"2026-09-01T12:00:01.000Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_F","name":"Task","input":{"description":"Explore the tree","subagent_type":"explorer","prompt":"explore"}}],"stop_reason":"tool_use"}}
`

const driftSidechain = `{"type":"user","uuid":"z3","sessionId":"sess-drift","isSidechain":true,"promptId":"dp1","timestamp":"2026-09-01T12:00:02.000Z","message":{"role":"user","content":"explore"}}
{"type":"assistant","uuid":"z4","sessionId":"sess-drift","isSidechain":"yes","timestamp":"2026-09-01T12:04:00.000Z","message":{"role":"assistant","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn"}}
`

// Fourth parent: it dispatches, and no transcript for that dispatch ever
// reached the tree — the shipper declines some, and an inline sidechain never
// becomes a file at all. The row still has to exist, unmeasured.
const unshippedSession = `{"type":"user","uuid":"n1","sessionId":"sess-unshipped","promptId":"n1","timestamp":"2026-09-01T13:00:00.000Z","message":{"role":"user","content":"explore"}}
{"type":"assistant","uuid":"n2","sessionId":"sess-unshipped","timestamp":"2026-09-01T13:00:01.000Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_G","name":"Task","input":{"description":"Explore the tree","subagent_type":"explorer","prompt":"explore"}}],"stop_reason":"tool_use"}}
{"type":"user","uuid":"n3","sessionId":"sess-unshipped","timestamp":"2026-09-01T13:00:04.000Z","toolUseResult":{"status":"dispatched"},"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_G","content":"agent started"}]}}
`

const plainSession = `{"type":"user","uuid":"v1","sessionId":"sess-plain","promptId":"q1","timestamp":"2026-09-01T11:00:00.000Z","message":{"role":"user","content":"read the file"}}
{"type":"assistant","uuid":"v2","sessionId":"sess-plain","timestamp":"2026-09-01T11:00:01.000Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_C","name":"Read","input":{"file_path":"/tmp/x"}}],"stop_reason":"tool_use"}}
{"type":"user","uuid":"v3","sessionId":"sess-plain","timestamp":"2026-09-01T11:00:02.000Z","toolUseResult":{"status":"ok"},"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_C","content":"contents"}]}}
`

type subagentRow struct {
	parentTurnIdx sql.NullInt64
	agentType     string
	prompt        string
	resultSummary string
	durationMs    sql.NullInt64
	errorCount    int
}

// buildReceivedTree lays out the parent sessions and their subagent
// transcripts plus one plain session, mirroring the receiver's layout.
func buildReceivedTree(t *testing.T) string {
	t.Helper()
	received := t.TempDir()
	project := filepath.Join(received, "claude-code", "loom")
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(project, "sess-parent.jsonl"), parentSession)
	write(filepath.Join(project, "sess-repeat.jsonl"), repeatSession)
	write(filepath.Join(project, "sess-drift.jsonl"), driftSession)
	write(filepath.Join(project, "sess-unshipped.jsonl"), unshippedSession)
	write(filepath.Join(project, "sess-plain.jsonl"), plainSession)

	subs := filepath.Join(project, "sess-parent", "subagents")
	write(filepath.Join(subs, "agent-aaa.jsonl"), reviewerSidechain)
	write(filepath.Join(subs, "agent-aaa.subagent.json"),
		`{"parent_session_id":"sess-parent","agent_type":"reviewer","description":"Quality lens round 2","tool_use_id":"toolu_A","spawn_depth":1}`)
	write(filepath.Join(subs, "agent-bbb.jsonl"), securitySidechain)
	write(filepath.Join(subs, "agent-bbb.subagent.json"),
		`{"parent_session_id":"sess-parent","agent_type":"security","description":"Security lens round 2","tool_use_id":"toolu_B","spawn_depth":1}`)
	write(filepath.Join(subs, "agent-ccc.jsonl"), orphanSidechain)

	repeat := filepath.Join(project, "sess-repeat", "subagents")
	write(filepath.Join(repeat, "agent-ddd.jsonl"), repeatTwoSidechain)
	write(filepath.Join(repeat, "agent-ddd.subagent.json"),
		`{"parent_session_id":"sess-repeat","agent_type":"reviewer","description":"Quality lens round 2","tool_use_id":"toolu_D","spawn_depth":1}`)
	write(filepath.Join(repeat, "agent-eee.jsonl"), repeatOneSidechain)
	write(filepath.Join(repeat, "agent-eee.subagent.json"),
		`{"parent_session_id":"sess-repeat","agent_type":"reviewer","description":"Quality lens round 1","tool_use_id":"toolu_E","spawn_depth":1}`)

	drift := filepath.Join(project, "sess-drift", "subagents")
	write(filepath.Join(drift, "agent-fff.jsonl"), driftSidechain)
	write(filepath.Join(drift, "agent-fff.subagent.json"),
		`{"parent_session_id":"sess-drift","agent_type":"explorer","description":"Explore the tree","tool_use_id":"toolu_F","spawn_depth":1}`)
	return received
}

func summarizeTree(t *testing.T, received string) *summaries.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "summaries.db")
	if err := Run(Options{ReceivedDir: received, DBPath: dbPath, Rebuild: true}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	st, err := summaries.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// readSubagents returns one session's rows in seq order. A slice, not a map
// keyed by agent_type: one parent routinely dispatches the same agent type
// more than once, and a map would collapse those into one entry.
func readSubagents(t *testing.T, st *summaries.Store, sessionID string) []subagentRow {
	t.Helper()
	rows, err := st.DB().Query(`
		SELECT agent_type, parent_turn_idx, prompt, result_summary,
		       duration_ms, error_count
		FROM subagents WHERE session_id = ? ORDER BY seq`, sessionID)
	if err != nil {
		t.Fatalf("query subagents: %v", err)
	}
	defer rows.Close()
	var out []subagentRow
	for rows.Next() {
		var r subagentRow
		if err := rows.Scan(&r.agentType, &r.parentTurnIdx, &r.prompt,
			&r.resultSummary, &r.durationMs, &r.errorCount); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

// subagentByType finds the single row for an agent type, failing when the
// type is absent or dispatched more than once.
func subagentByType(t *testing.T, rows []subagentRow, agentType string) subagentRow {
	t.Helper()
	var found []subagentRow
	for _, r := range rows {
		if r.agentType == agentType {
			found = append(found, r)
		}
	}
	if len(found) != 1 {
		t.Fatalf("rows with agent_type %q: got %d, want 1 (%v)", agentType, len(found), rows)
	}
	return found[0]
}

// TestSubagentDurationComesFromSidechainNotAcknowledgement is the core of the
// bug: a background dispatch's tool_result returns in three seconds while the
// agent works for four minutes. The subagents row must report the work.
func TestSubagentDurationComesFromSidechainNotAcknowledgement(t *testing.T) {
	st := summarizeTree(t, buildReceivedTree(t))
	reviewer := subagentByType(t, readSubagents(t, st, "sess-parent"), "reviewer")

	if !reviewer.durationMs.Valid {
		t.Fatal("reviewer duration_ms is NULL, want the sidechain span")
	}

	var ackMs int64
	if err := st.DB().QueryRow(`
		SELECT duration_ms FROM tool_calls
		WHERE session_id = 'sess-parent' AND call_id = 'toolu_A'`).Scan(&ackMs); err != nil {
		t.Fatalf("query tool_call: %v", err)
	}
	if ackMs != 3000 {
		t.Fatalf("tool_calls.duration_ms = %d, want 3000 (the acknowledgement)", ackMs)
	}
	if reviewer.durationMs.Int64 != 240000 {
		t.Errorf("subagents.duration_ms = %d, want 240000 (the sidechain span)",
			reviewer.durationMs.Int64)
	}
	if reviewer.durationMs.Int64 == ackMs {
		t.Errorf("subagents.duration_ms tracks the acknowledgement (%d ms), not the work", ackMs)
	}
}

// TestUnresolvableSidechainWritesNull keeps "not measured" distinguishable
// from "returned instantly".
func TestUnresolvableSidechainWritesNull(t *testing.T) {
	st := summarizeTree(t, buildReceivedTree(t))
	security := subagentByType(t, readSubagents(t, st, "sess-parent"), "security")

	if security.durationMs.Valid {
		t.Errorf("duration_ms = %d, want NULL for a single-record sidechain",
			security.durationMs.Int64)
	}

	// A NULL read back as 0 would be indistinguishable from an instant
	// return, so assert against the column itself.
	var nulls int
	if err := st.DB().QueryRow(`
		SELECT COUNT(*) FROM subagents
		WHERE session_id = 'sess-parent' AND duration_ms IS NULL`).Scan(&nulls); err != nil {
		t.Fatalf("count nulls: %v", err)
	}
	if nulls != 1 {
		t.Errorf("rows with duration_ms IS NULL = %d, want 1", nulls)
	}
	var zeros int
	if err := st.DB().QueryRow(`
		SELECT COUNT(*) FROM subagents
		WHERE session_id = 'sess-parent' AND duration_ms = 0`).Scan(&zeros); err != nil {
		t.Fatalf("count zeros: %v", err)
	}
	if zeros != 0 {
		t.Errorf("rows with duration_ms = 0 = %d, want 0", zeros)
	}
}

// TestAgentTypeComesFromSidecarNotDescription pins the fourth criterion: the
// dispatch description already lives in tool_calls.key_arg.
func TestAgentTypeComesFromSidecarNotDescription(t *testing.T) {
	st := summarizeTree(t, buildReceivedTree(t))
	rows := readSubagents(t, st, "sess-parent")

	for _, r := range rows {
		if r.agentType == "Quality lens round 2" {
			t.Error("agent_type carries the dispatch description, want the sidecar's agent_type")
		}
	}
	reviewer := subagentByType(t, rows, "reviewer")
	if reviewer.prompt != "review the diff" {
		t.Errorf("prompt = %q, want the sidechain's first user message", reviewer.prompt)
	}
	// Both text blocks of the last assistant message, joined as the turn's
	// assistant text joins them — not just the trailing block.
	if reviewer.resultSummary != "verdict: ship it\nno blockers" {
		t.Errorf("result_summary = %q, want the sidechain's last assistant message",
			reviewer.resultSummary)
	}

	var keyArg string
	if err := st.DB().QueryRow(`
		SELECT key_arg FROM tool_calls
		WHERE session_id = 'sess-parent' AND call_id = 'toolu_A'`).Scan(&keyArg); err != nil {
		t.Fatalf("query tool_call: %v", err)
	}
	if keyArg != "Quality lens round 2" {
		t.Errorf("tool_calls.key_arg = %q, want the dispatch description", keyArg)
	}
}

// TestParentTurnIdxResolvesThroughToolUseID covers both the join and the
// unattributed case.
func TestParentTurnIdxResolvesThroughToolUseID(t *testing.T) {
	st := summarizeTree(t, buildReceivedTree(t))
	rows := readSubagents(t, st, "sess-parent")

	if got := len(rows); got != 3 {
		t.Fatalf("subagent rows: got %d, want 3 (%v)", got, rows)
	}
	// The Task calls live in the second turn.
	if got := subagentByType(t, rows, "reviewer").parentTurnIdx; !got.Valid || got.Int64 != 1 {
		t.Errorf("reviewer parent_turn_idx = %v, want 1", got)
	}
	if got := subagentByType(t, rows, "security").parentTurnIdx; !got.Valid || got.Int64 != 1 {
		t.Errorf("security parent_turn_idx = %v, want 1", got)
	}
	// The sidecar-less transcript names no dispatch.
	orphan := subagentByType(t, rows, "")
	if !orphan.parentTurnIdx.Valid || orphan.parentTurnIdx.Int64 != -1 {
		t.Errorf("orphan parent_turn_idx = %v, want -1", orphan.parentTurnIdx)
	}
	if !orphan.durationMs.Valid || orphan.durationMs.Int64 != 30000 {
		t.Errorf("orphan duration_ms = %v, want 30000", orphan.durationMs)
	}
}

// TestRepeatedAgentTypeKeepsBothRows pins the corpus's normal shape: one
// parent dispatching the same lens twice must yield two rows, ordered by when
// each transcript started rather than by directory order.
func TestRepeatedAgentTypeKeepsBothRows(t *testing.T) {
	st := summarizeTree(t, buildReceivedTree(t))
	rows := readSubagents(t, st, "sess-repeat")

	if len(rows) != 2 {
		t.Fatalf("subagent rows: got %d, want 2 (%v)", len(rows), rows)
	}
	for i, want := range []struct {
		prompt     string
		durationMs int64
	}{
		{"review round one", 60000},
		{"review round two", 120000},
	} {
		if rows[i].agentType != "reviewer" {
			t.Errorf("row %d agent_type = %q, want reviewer", i, rows[i].agentType)
		}
		if rows[i].prompt != want.prompt {
			t.Errorf("row %d prompt = %q, want %q", i, rows[i].prompt, want.prompt)
		}
		if !rows[i].durationMs.Valid || rows[i].durationMs.Int64 != want.durationMs {
			t.Errorf("row %d duration_ms = %v, want %d", i, rows[i].durationMs, want.durationMs)
		}
		if !rows[i].parentTurnIdx.Valid || rows[i].parentTurnIdx.Int64 != 0 {
			t.Errorf("row %d parent_turn_idx = %v, want 0", i, rows[i].parentTurnIdx)
		}
	}
}

// TestGrowingSubagentTranscriptRefoldsTheParent covers the currency check: a
// background dispatch outlives the parent's last record, so a parent file that
// never changes again still has to be re-folded while its transcript grows.
func TestGrowingSubagentTranscriptRefoldsTheParent(t *testing.T) {
	received := buildReceivedTree(t)
	opts := Options{ReceivedDir: received, DBPath: filepath.Join(t.TempDir(), "summaries.db")}
	if err := Run(opts); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Four more minutes of work land after the parent went quiet. Only the
	// transcript moves; the parent's size and mtime are untouched.
	transcript := filepath.Join(received, "claude-code", "loom", "sess-parent",
		"subagents", "agent-aaa.jsonl")
	grown := reviewerSidechain + `{"type":"assistant","uuid":"s7","sessionId":"sess-parent","isSidechain":true,"timestamp":"2026-09-01T10:09:01.500Z","message":{"role":"assistant","content":[{"type":"text","text":"one more pass"}],"stop_reason":"end_turn"}}` + "\n"
	if err := os.WriteFile(transcript, []byte(grown), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Run(opts); err != nil {
		t.Fatalf("Run: %v", err)
	}

	st, err := summaries.Open(opts.DBPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	reviewer := subagentByType(t, readSubagents(t, st, "sess-parent"), "reviewer")
	if !reviewer.durationMs.Valid || reviewer.durationMs.Int64 != 480000 {
		t.Errorf("duration_ms = %v, want 480000 (the grown span); the session was skipped as current",
			reviewer.durationMs)
	}
}

// TestSubagentParseFailureSurfacesAsDrift keeps a NULL duration caused by a
// transcript whose shape drifted distinguishable from one caused by a
// transcript with no measurable span.
func TestSubagentParseFailureSurfacesAsDrift(t *testing.T) {
	st := summarizeTree(t, buildReceivedTree(t))
	rows := readSubagents(t, st, "sess-drift")

	if len(rows) != 1 {
		t.Fatalf("subagent rows: got %d, want 1 (%v)", len(rows), rows)
	}
	if rows[0].durationMs.Valid {
		t.Errorf("duration_ms = %d, want NULL for a transcript that failed to parse",
			rows[0].durationMs.Int64)
	}

	var count int
	if err := st.DB().QueryRow(`
		SELECT COUNT(*) FROM unknown_records
		WHERE session_id = 'sess-drift' AND type = ? AND subtype = 'explorer'`,
		claudeparse.SubagentParseFailureMarker).Scan(&count); err != nil {
		t.Fatalf("query unknown_records: %v", err)
	}
	if count != 1 {
		t.Errorf("unknown_records rows naming the failed subagent parse: got %d, want 1", count)
	}

	// The session that merely lacks a measurable span must not look like drift.
	if err := st.DB().QueryRow(`
		SELECT COUNT(*) FROM unknown_records
		WHERE session_id = 'sess-parent' AND type = ?`,
		claudeparse.SubagentParseFailureMarker).Scan(&count); err != nil {
		t.Fatalf("query unknown_records: %v", err)
	}
	if count != 0 {
		t.Errorf("sess-parent unknown_records naming a subagent parse failure: got %d, want 0", count)
	}
}

// TestDispatchWithoutTranscriptStillWritesARow pins the first criterion
// against the second: the row source is the dispatch, and a dispatch whose
// transcript never shipped is unmeasured rather than absent.
func TestDispatchWithoutTranscriptStillWritesARow(t *testing.T) {
	st := summarizeTree(t, buildReceivedTree(t))
	rows := readSubagents(t, st, "sess-unshipped")

	if len(rows) != 1 {
		t.Fatalf("subagent rows: got %d, want 1 (%v)", len(rows), rows)
	}
	if rows[0].agentType != "explorer" {
		t.Errorf("agent_type = %q, want explorer (the dispatch's subagent_type)", rows[0].agentType)
	}
	if !rows[0].parentTurnIdx.Valid || rows[0].parentTurnIdx.Int64 != 0 {
		t.Errorf("parent_turn_idx = %v, want 0", rows[0].parentTurnIdx)
	}

	// The acknowledgement is measurable and is not this row's duration; a
	// NULL read back as 0 would be indistinguishable from an instant
	// return, so assert against the column itself.
	var nulls int
	if err := st.DB().QueryRow(`
		SELECT COUNT(*) FROM subagents
		WHERE session_id = 'sess-unshipped' AND duration_ms IS NULL`).Scan(&nulls); err != nil {
		t.Fatalf("count nulls: %v", err)
	}
	if nulls != 1 {
		t.Errorf("rows with duration_ms IS NULL = %d, want 1", nulls)
	}
}

// TestSessionWithoutSubagentsDirIsUnchanged guards the common case: no
// subagents directory means no subagent rows and an otherwise normal summary.
func TestSessionWithoutSubagentsDirIsUnchanged(t *testing.T) {
	st := summarizeTree(t, buildReceivedTree(t))

	if got := len(readSubagents(t, st, "sess-plain")); got != 0 {
		t.Errorf("subagent rows for a session with no subagents dir: got %d, want 0", got)
	}
	var turns, tools int
	if err := st.DB().QueryRow(`
		SELECT (SELECT COUNT(*) FROM turns WHERE session_id = 'sess-plain'),
		       (SELECT COUNT(*) FROM tool_calls WHERE session_id = 'sess-plain')`).
		Scan(&turns, &tools); err != nil {
		t.Fatalf("query counts: %v", err)
	}
	if turns != 1 || tools != 1 {
		t.Errorf("turns=%d tools=%d, want 1 and 1", turns, tools)
	}
}
