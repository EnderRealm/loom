// Package store persists summary.SessionSummary into a SQLite database for
// cross-session pattern analysis (bottleneck detection, error rates, learnings).
package store

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"time"

	_ "modernc.org/sqlite"

	"loom/internal/parse/summary"
)

// Store is a thin wrapper over a SQLite database.
type Store struct {
	db *sql.DB
}

// Open opens (and migrates) the SQLite database at path. The file is created
// if it doesn't exist. Concurrent writers from the same process share one
// *Store; cross-process writers should still avoid stepping on each other.
func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)" +
		"&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// DB exposes the underlying *sql.DB so tools can run ad-hoc queries without
// going through this package. Read-only callers only — schema is owned here.
func (s *Store) DB() *sql.DB { return s.db }

func migrate(db *sql.DB) error {
	if _, err := db.Exec(schemaSQL); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	_, err := db.Exec(
		`INSERT OR REPLACE INTO schema_meta(key, value) VALUES (?, ?)`,
		"schema_version", strconv.Itoa(schemaVersion),
	)
	return err
}

// SessionAlreadyCurrent reports whether the DB already has a summary for
// this session_id whose source_size and source_mtime match. Used to skip
// re-summarizing unchanged files.
func (s *Store) SessionAlreadyCurrent(sessionID string, size int64,
	mtime time.Time) (bool, error) {
	row := s.db.QueryRow(
		`SELECT source_size, source_mtime FROM sessions WHERE session_id = ?`,
		sessionID,
	)
	var existingSize sql.NullInt64
	var existingMtime sql.NullString
	if err := row.Scan(&existingSize, &existingMtime); err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	if !existingSize.Valid || existingSize.Int64 != size {
		return false, nil
	}
	if !existingMtime.Valid {
		return false, nil
	}
	if existingMtime.String != mtime.UTC().Format(time.RFC3339Nano) {
		return false, nil
	}
	return true, nil
}

// WriteSummary upserts one SessionSummary into the DB inside a transaction.
// All child rows for the session are deleted first so the write is
// idempotent — re-summarizing a session won't duplicate rows.
func (s *Store) WriteSummary(ctx context.Context, sum *summary.SessionSummary,
	source SourceInfo) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, table := range []string{
		"turns", "tool_calls", "errors", "compactions",
		"token_counts", "files_touched", "subagents", "unknown_records",
	} {
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM "+table+" WHERE session_id = ?",
			sum.SessionID); err != nil {
			return fmt.Errorf("clear %s: %w", table, err)
		}
	}

	durationMs := int64(0)
	if !sum.StartTime.IsZero() && !sum.EndTime.IsZero() {
		durationMs = sum.EndTime.Sub(sum.StartTime).Milliseconds()
	}
	errCount := len(sum.Errors)

	_, err = tx.ExecContext(ctx, `
		INSERT INTO sessions (
		    session_id, agent, project, cwd, git_branch, cli_version,
		    model_provider, model, personality, custom_title, agent_name,
		    pr_url, start_time, end_time, duration_ms, turn_count,
		    tool_call_count, error_count, compacted, input_tokens,
		    output_tokens, cache_read_tokens, source_path, source_size,
		    source_mtime, summarized_at
		) VALUES (
		    ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
		    ?, ?, ?, ?, ?,
		    ?, ?, ?, ?,
		    ?, ?, ?, ?,
		    ?, ?
		)
		ON CONFLICT(session_id) DO UPDATE SET
		    agent             = excluded.agent,
		    project           = excluded.project,
		    cwd               = excluded.cwd,
		    git_branch        = excluded.git_branch,
		    cli_version       = excluded.cli_version,
		    model_provider    = excluded.model_provider,
		    model             = excluded.model,
		    personality       = excluded.personality,
		    custom_title      = excluded.custom_title,
		    agent_name        = excluded.agent_name,
		    pr_url            = excluded.pr_url,
		    start_time        = excluded.start_time,
		    end_time          = excluded.end_time,
		    duration_ms       = excluded.duration_ms,
		    turn_count        = excluded.turn_count,
		    tool_call_count   = excluded.tool_call_count,
		    error_count       = excluded.error_count,
		    compacted         = excluded.compacted,
		    input_tokens      = excluded.input_tokens,
		    output_tokens     = excluded.output_tokens,
		    cache_read_tokens = excluded.cache_read_tokens,
		    source_path       = excluded.source_path,
		    source_size       = excluded.source_size,
		    source_mtime      = excluded.source_mtime,
		    summarized_at     = excluded.summarized_at
	`,
		sum.SessionID, string(sum.Agent), source.Project, sum.Cwd,
		sum.GitBranch, sum.CLIVersion,
		sum.ModelProvider, sum.Model, sum.Personality,
		sum.CustomTitle, sum.AgentName,
		sum.PRURL, isoOrNull(sum.StartTime), isoOrNull(sum.EndTime),
		durationMs, len(sum.Turns),
		len(sum.ToolCalls), errCount, boolToInt(sum.Compacted),
		sum.InputTokens,
		sum.OutputTokens, sum.CacheReadTokens,
		source.Path, source.Size,
		isoOrNull(source.Mtime), time.Now().UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("insert sessions: %w", err)
	}

	if err := writeTurns(ctx, tx, sum); err != nil {
		return err
	}
	if err := writeToolCalls(ctx, tx, sum); err != nil {
		return err
	}
	if err := writeErrors(ctx, tx, sum); err != nil {
		return err
	}
	if err := writeCompactions(ctx, tx, sum); err != nil {
		return err
	}
	if err := writeTokenCounts(ctx, tx, sum); err != nil {
		return err
	}
	if err := writeFilesTouched(ctx, tx, sum); err != nil {
		return err
	}
	if err := writeSubagents(ctx, tx, sum); err != nil {
		return err
	}
	if err := writeUnknown(ctx, tx, sum); err != nil {
		return err
	}

	return tx.Commit()
}

// SourceInfo is the per-file provenance the writer needs to mark a session
// as up-to-date.
type SourceInfo struct {
	Project string
	Path    string
	Size    int64
	Mtime   time.Time
}

func writeTurns(ctx context.Context, tx *sql.Tx,
	sum *summary.SessionSummary) error {
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO turns (
		    session_id, idx, turn_id, user_message, assistant_text,
		    reasoning_chars, stop_reason, completion_status,
		    input_tokens, output_tokens, cache_read_tokens,
		    started_at, ended_at, wall_clock_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, t := range sum.Turns {
		ms := int64(0)
		if !t.StartedAt.IsZero() && !t.EndedAt.IsZero() {
			ms = t.EndedAt.Sub(t.StartedAt).Milliseconds()
		}
		if _, err := stmt.ExecContext(ctx,
			sum.SessionID, t.Idx, t.TurnID, t.UserMessage,
			t.AssistantText, t.ReasoningChars, t.StopReason,
			string(t.CompletionStatus), t.InputTokens, t.OutputTokens,
			t.CacheReadTokens, isoOrNull(t.StartedAt),
			isoOrNull(t.EndedAt), ms,
		); err != nil {
			return fmt.Errorf("insert turn %d: %w", t.Idx, err)
		}
	}
	return nil
}

func writeToolCalls(ctx context.Context, tx *sql.Tx,
	sum *summary.SessionSummary) error {
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO tool_calls (
		    session_id, turn_idx, seq, call_id, tool_kind, tool_name,
		    key_arg, started_at, duration_ms, exit_code, is_error,
		    result_summary
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for i, tc := range sum.ToolCalls {
		var exit any
		if tc.ExitCode != nil {
			exit = *tc.ExitCode
		}
		if _, err := stmt.ExecContext(ctx,
			sum.SessionID, tc.TurnIdx, i, tc.CallID, string(tc.Kind),
			tc.ToolName, tc.KeyArg, isoOrNull(tc.StartedAt),
			tc.DurationMs, exit, boolToInt(tc.IsError),
			tc.ResultSummary,
		); err != nil {
			return fmt.Errorf("insert tool_call %d: %w", i, err)
		}
	}
	return nil
}

func writeErrors(ctx context.Context, tx *sql.Tx,
	sum *summary.SessionSummary) error {
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO errors (session_id, seq, turn_idx, source, message, ts)
		VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for i, e := range sum.Errors {
		if _, err := stmt.ExecContext(ctx,
			sum.SessionID, i, e.TurnIdx, e.Source, e.Message,
			isoOrNull(e.Time),
		); err != nil {
			return err
		}
	}
	return nil
}

func writeCompactions(ctx context.Context, tx *sql.Tx,
	sum *summary.SessionSummary) error {
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO compactions (session_id, seq, ts, anchor,
		    tokens_before, tokens_after)
		VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for i, c := range sum.Compactions {
		if _, err := stmt.ExecContext(ctx,
			sum.SessionID, i, isoOrNull(c.Time), c.Anchor,
			c.TokensBefore, c.TokensAfter,
		); err != nil {
			return err
		}
	}
	return nil
}

func writeTokenCounts(ctx context.Context, tx *sql.Tx,
	sum *summary.SessionSummary) error {
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO token_counts (
		    session_id, seq, turn_idx, ts, input, output, cached,
		    reasoning, limit_id, limit_used_pct
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for i, tc := range sum.TokenCounts {
		if _, err := stmt.ExecContext(ctx,
			sum.SessionID, i, tc.TurnIdx, isoOrNull(tc.Time),
			tc.Input, tc.Output, tc.Cached, tc.Reasoning,
			tc.LimitID, tc.LimitUsedPercent,
		); err != nil {
			return err
		}
	}
	return nil
}

func writeFilesTouched(ctx context.Context, tx *sql.Tx,
	sum *summary.SessionSummary) error {
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO files_touched (session_id, path, op, count)
		VALUES (?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, f := range sum.FilesTouched {
		if _, err := stmt.ExecContext(ctx,
			sum.SessionID, f.Path, f.Op, f.Count,
		); err != nil {
			return err
		}
	}
	return nil
}

func writeSubagents(ctx context.Context, tx *sql.Tx,
	sum *summary.SessionSummary) error {
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO subagents (session_id, seq, parent_turn_idx,
		    agent_type, prompt, result_summary, duration_ms, error_count)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for i, sa := range sum.Subagents {
		if _, err := stmt.ExecContext(ctx,
			sum.SessionID, i, sa.ParentTurnIdx, sa.AgentType,
			sa.Prompt, sa.ResultSummary, sa.DurationMs, sa.ErrorCount,
		); err != nil {
			return err
		}
	}
	return nil
}

func writeUnknown(ctx context.Context, tx *sql.Tx,
	sum *summary.SessionSummary) error {
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO unknown_records (session_id, agent, type, subtype,
		    count, first_seen)
		VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, u := range sum.Unknown {
		if _, err := stmt.ExecContext(ctx,
			sum.SessionID, string(u.Agent), u.Type, u.Subtype,
			u.Count, isoOrNull(u.FirstSeen),
		); err != nil {
			return err
		}
	}
	return nil
}

func isoOrNull(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
