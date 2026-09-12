package summaries

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"loom/internal/parse/summary"
)

// TestOpenFreshDB exercises the happy path: a brand-new DB applies the
// current schema and writes the version marker.
func TestOpenFreshDB(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "summaries.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	var v string
	if err := st.DB().QueryRow(`SELECT value FROM schema_meta WHERE key = 'schema_version'`).Scan(&v); err != nil {
		t.Fatalf("read version: %v", err)
	}
	if v != "8" {
		t.Errorf("schema_version: got %q, want %q", v, "8")
	}
}

// TestOpenOutdatedDB exercises the upgrade path: an existing v1 DB is
// rejected with ErrSchemaOutdated so the caller can surface --rebuild.
func TestOpenOutdatedDB(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "summaries.db")

	// First open with the current code, then manually rewrite the version
	// to simulate an older DB without porting the entire v1 schema.
	st, err := Open(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	if _, err := st.DB().Exec(`UPDATE schema_meta SET value = '2' WHERE key = 'schema_version'`); err != nil {
		t.Fatalf("downgrade: %v", err)
	}
	st.Close()

	_, err = Open(path)
	if !errors.Is(err, ErrSchemaOutdated) {
		t.Fatalf("Open: got %v, want %v", err, ErrSchemaOutdated)
	}
}

// TestOpenTooNewDB exercises the downgrade guard: a DB one version ahead of
// this binary is rejected without touching the marker or applying schemaSQL.
// The fixture holds only schema_meta so the "schemaSQL was not applied"
// assertion is real — going through Open would have created every table first.
func TestOpenTooNewDB(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "summaries.db")
	future := strconv.Itoa(schemaVersion + 1)

	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		t.Fatalf("create schema_meta: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO schema_meta(key, value) VALUES ('schema_version', ?)`, future); err != nil {
		t.Fatalf("stamp version: %v", err)
	}
	db.Close()

	_, err = Open(path)
	if !errors.Is(err, ErrSchemaTooNew) {
		t.Fatalf("Open: got %v, want %v", err, ErrSchemaTooNew)
	}
	if errors.Is(err, ErrSchemaOutdated) {
		t.Errorf("Open: error also matches ErrSchemaOutdated, must be distinguishable")
	}
	msg := err.Error()
	if !strings.Contains(msg, future) || !strings.Contains(msg, strconv.Itoa(schemaVersion)) {
		t.Errorf("error %q: want both %q and %q named", msg, future, strconv.Itoa(schemaVersion))
	}

	db, err = sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db.Close()

	var v string
	if err := db.QueryRow(`SELECT value FROM schema_meta WHERE key = 'schema_version'`).Scan(&v); err != nil {
		t.Fatalf("read version: %v", err)
	}
	if v != future {
		t.Errorf("schema_version: got %q, want %q", v, future)
	}

	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'sessions'`).Scan(&n); err != nil {
		t.Fatalf("check sessions table: %v", err)
	}
	if n != 0 {
		t.Errorf("sessions table exists: schemaSQL was applied to a too-new DB")
	}
}

// TestWriteSummaryRoundTrip verifies the (agent, session_id) composite key:
// two sessions sharing the same session_id but different agents coexist
// without overwriting each other.
func TestWriteSummaryRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "summaries.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	now := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
	src := SourceInfo{Project: "p", Path: "/tmp/p/x.jsonl", Size: 100, Mtime: now}

	for _, a := range []summary.Agent{summary.AgentClaude, summary.AgentCodex} {
		s := &summary.SessionSummary{
			SessionID: "shared-id",
			Agent:     a,
			StartTime: now,
			EndTime:   now.Add(time.Minute),
			Turns:     []summary.Turn{{Idx: 0, UserMessage: "hi", AssistantText: string(a)}},
		}
		if err := st.WriteSummary(ctx, s, src); err != nil {
			t.Fatalf("WriteSummary %s: %v", a, err)
		}
	}

	rows, err := st.DB().Query(`SELECT agent FROM sessions WHERE session_id = 'shared-id' ORDER BY agent`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var agents []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			t.Fatalf("scan: %v", err)
		}
		agents = append(agents, a)
	}
	if len(agents) != 2 || agents[0] != "claude-code" || agents[1] != "codex-cli" {
		t.Errorf("agents: got %v, want [claude-code codex-cli]", agents)
	}

	// SessionAlreadyCurrent must distinguish by agent — re-summarizing one
	// agent should not falsely report the other as already current.
	cur, err := st.SessionAlreadyCurrent(string(summary.AgentClaude), "shared-id", src.Size, src.Mtime)
	if err != nil {
		t.Fatalf("SessionAlreadyCurrent claude: %v", err)
	}
	if !cur {
		t.Errorf("claude SessionAlreadyCurrent: got false, want true")
	}
	cur, err = st.SessionAlreadyCurrent(string(summary.AgentClaude), "shared-id", 999, src.Mtime)
	if err != nil {
		t.Fatalf("SessionAlreadyCurrent claude (size mismatch): %v", err)
	}
	if cur {
		t.Errorf("claude SessionAlreadyCurrent (size mismatch): got true, want false")
	}
}

// TestWriteTurnsStoresConditionsOrNull pins the per-turn model/effort/
// cli_version columns: a turn that carries them lands with the values, and a
// turn that carries none lands NULL rather than "" so "not recorded" stays
// distinguishable from "recorded as none".
func TestWriteTurnsStoresConditionsOrNull(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "summaries.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	now := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
	s := &summary.SessionSummary{
		SessionID: "conditions",
		Agent:     summary.AgentClaude,
		StartTime: now,
		EndTime:   now.Add(time.Minute),
		Turns: []summary.Turn{
			{Idx: 0, UserMessage: "hi", Model: "claude-opus-5", Effort: "high", CLIVersion: "2.1.267"},
			{Idx: 1, UserMessage: "again"},
		},
	}
	if err := st.WriteSummary(context.Background(), s, SourceInfo{Project: "p"}); err != nil {
		t.Fatalf("WriteSummary: %v", err)
	}

	read := func(idx int) (model, effort, version sql.NullString) {
		t.Helper()
		err := st.DB().QueryRow(
			`SELECT model, effort, cli_version FROM turns WHERE session_id = 'conditions' AND idx = ?`, idx,
		).Scan(&model, &effort, &version)
		if err != nil {
			t.Fatalf("read turn %d: %v", idx, err)
		}
		return model, effort, version
	}

	model, effort, version := read(0)
	if model.String != "claude-opus-5" || effort.String != "high" || version.String != "2.1.267" {
		t.Errorf("turn 0: got %q/%q/%q, want claude-opus-5/high/2.1.267", model.String, effort.String, version.String)
	}
	model, effort, version = read(1)
	if model.Valid || effort.Valid || version.Valid {
		t.Errorf("turn 1: got %v/%v/%v, want all NULL", model, effort, version)
	}
}

// TestWriteTurnsStoresCacheCreationAndSpeed pins the pricing columns on turns:
// the cache write, its 1h share and the mixed flag round-trip, and speed
// lands NULL when the transcript carried none.
func TestWriteTurnsStoresCacheCreationAndSpeed(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "summaries.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	now := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
	s := &summary.SessionSummary{
		SessionID: "pricing",
		Agent:     summary.AgentClaude,
		StartTime: now,
		EndTime:   now.Add(time.Minute),
		Turns: []summary.Turn{
			{Idx: 0, UserMessage: "hi", CacheCreationTokens: 5000, CacheCreation1hTokens: 3000, Speed: "fast", Mixed: true},
			{Idx: 1, UserMessage: "again"},
		},
	}
	if err := st.WriteSummary(context.Background(), s, SourceInfo{Project: "p"}); err != nil {
		t.Fatalf("WriteSummary: %v", err)
	}

	read := func(idx int) (creation, creation1h, mixed int64, speed sql.NullString) {
		t.Helper()
		err := st.DB().QueryRow(
			`SELECT cache_creation_tokens, cache_creation_1h_tokens, usage_mixed, speed FROM turns WHERE session_id = 'pricing' AND idx = ?`, idx,
		).Scan(&creation, &creation1h, &mixed, &speed)
		if err != nil {
			t.Fatalf("read turn %d: %v", idx, err)
		}
		return creation, creation1h, mixed, speed
	}

	creation, creation1h, mixed, speed := read(0)
	if creation != 5000 || creation1h != 3000 || mixed != 1 || speed.String != "fast" {
		t.Errorf("turn 0: got %d/%d/%d/%q, want 5000/3000/1/fast", creation, creation1h, mixed, speed.String)
	}
	creation, creation1h, mixed, speed = read(1)
	if creation != 0 || creation1h != 0 || mixed != 0 || speed.Valid {
		t.Errorf("turn 1: got %d/%d/%d/%v, want 0/0/0/NULL", creation, creation1h, mixed, speed)
	}
}

// TestWriteSubagentsStoresUsageOrNull pins the per-dispatch usage columns: a
// dispatch whose transcript was folded lands with its model and counts, and
// one with no transcript lands NULL on every usage column rather than zero,
// which would read as a free dispatch.
func TestWriteSubagentsStoresUsageOrNull(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "summaries.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	now := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
	s := &summary.SessionSummary{
		SessionID: "subagent-usage",
		Agent:     summary.AgentClaude,
		StartTime: now,
		EndTime:   now.Add(time.Minute),
		Subagents: []summary.Subagent{
			{ParentTurnIdx: 0, AgentType: "reviewer", Usage: &summary.SubagentUsage{
				Model: "claude-haiku-4-5", InputTokens: 10, OutputTokens: 20, CacheReadTokens: 30,
				CacheCreationTokens: 40, CacheCreation1hTokens: 15, Mixed: true,
			}},
			{ParentTurnIdx: 0, AgentType: "security"},
		},
	}
	if err := st.WriteSummary(context.Background(), s, SourceInfo{Project: "p"}); err != nil {
		t.Fatalf("WriteSummary: %v", err)
	}

	type usage struct {
		model, speed                                             sql.NullString
		input, output, cacheRead, cacheCreation, cacheCreation1h sql.NullInt64
		mixed                                                    sql.NullInt64
	}
	read := func(seq int) usage {
		t.Helper()
		var u usage
		err := st.DB().QueryRow(`
			SELECT model, speed, input_tokens, output_tokens, cache_read_tokens,
			       cache_creation_tokens, cache_creation_1h_tokens, usage_mixed
			FROM subagents WHERE session_id = 'subagent-usage' AND seq = ?`, seq,
		).Scan(&u.model, &u.speed, &u.input, &u.output, &u.cacheRead, &u.cacheCreation, &u.cacheCreation1h, &u.mixed)
		if err != nil {
			t.Fatalf("read subagent %d: %v", seq, err)
		}
		return u
	}

	u := read(0)
	if u.model.String != "claude-haiku-4-5" || u.speed.Valid ||
		u.input.Int64 != 10 || u.output.Int64 != 20 || u.cacheRead.Int64 != 30 ||
		u.cacheCreation.Int64 != 40 || u.cacheCreation1h.Int64 != 15 || u.mixed.Int64 != 1 {
		t.Errorf("subagent 0: got %+v, want claude-haiku-4-5/NULL/10/20/30/40/15/1", u)
	}
	u = read(1)
	if u.model.Valid || u.speed.Valid || u.input.Valid || u.output.Valid ||
		u.cacheRead.Valid || u.cacheCreation.Valid || u.cacheCreation1h.Valid || u.mixed.Valid {
		t.Errorf("subagent 1: got %+v, want every usage column NULL", u)
	}
}

// TestWriteSessionStoresSpawnOrNull pins parent_session_id and spawn_depth:
// a Codex subagent session lands with both, and a session nothing spawned
// lands NULL in both so a depth of 0 never reads as a recorded placement.
func TestWriteSessionStoresSpawnOrNull(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "summaries.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	now := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
	for _, s := range []*summary.SessionSummary{
		{SessionID: "spawned", Agent: summary.AgentCodex, StartTime: now, ParentSessionID: "sess-parent", SpawnDepth: 1},
		{SessionID: "top", Agent: summary.AgentCodex, StartTime: now},
		{SessionID: "claude", Agent: summary.AgentClaude, StartTime: now},
	} {
		if err := st.WriteSummary(context.Background(), s, SourceInfo{Project: "p"}); err != nil {
			t.Fatalf("WriteSummary %s: %v", s.SessionID, err)
		}
	}

	read := func(sessionID string) (sql.NullString, sql.NullInt64) {
		t.Helper()
		var parent sql.NullString
		var depth sql.NullInt64
		if err := st.DB().QueryRow(`SELECT parent_session_id, spawn_depth FROM sessions WHERE session_id = ?`,
			sessionID).Scan(&parent, &depth); err != nil {
			t.Fatalf("read %s: %v", sessionID, err)
		}
		return parent, depth
	}
	parent, depth := read("spawned")
	if parent.String != "sess-parent" || !depth.Valid || depth.Int64 != 1 {
		t.Errorf("spawned: parent = %v depth = %v, want sess-parent 1", parent, depth)
	}
	for _, id := range []string{"top", "claude"} {
		parent, depth := read(id)
		if parent.Valid || depth.Valid {
			t.Errorf("%s: parent = %v depth = %v, want both NULL", id, parent, depth)
		}
	}
}

// TestOpenEscapesURIDelimiters pins the DSN: a path carrying '#' or '?' is
// opened where it says, not cut at the fragment or query delimiter.
func TestOpenEscapesURIDelimiters(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "loom #1? x")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "summaries.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	st.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database not at %s: %v", path, err)
	}
}
