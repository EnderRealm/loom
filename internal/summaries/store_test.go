package summaries

import (
	"context"
	"database/sql"
	"errors"
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
	if v != "4" {
		t.Errorf("schema_version: got %q, want %q", v, "4")
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
