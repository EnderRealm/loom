package tui

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"

	"loom/internal/config"
)

// SessionMetrics is the per-session view of summaries.db rows that the TUI
// surfaces on the dashboard and detail page.
type SessionMetrics struct {
	Agent           string
	SessionID       string
	Project         string // legacy slug
	Cwd             string // parsed from JSONL
	CwdRaw          string // sidecar-captured raw cwd
	GitRemote       string // sidecar-captured git remote
	Model           string
	TurnCount       int
	ToolCallCount   int
	ErrorCount      int
	Compacted       bool
	InputTokens     int64
	OutputTokens    int64
	CacheReadTokens int64
	DurationMs      int64
}

// SummaryData is the bundle of metrics the TUI needs per refresh. Indexed by
// (agent, sessionID) for fast joins; project-level rollups are pre-aggregated
// by Slug for the dashboard's ACTIVITY column.
type SummaryData struct {
	BySession map[string]*SessionMetrics
	ByProject map[string]*ProjectMetrics
	// ToolStats holds per-project tool aggregates keyed by lowercased project
	// basename. Each entry is sorted by call count desc.
	ToolStats map[string][]ToolStat
	// CompactionsByProject counts compactions across the project's sessions.
	CompactionsByProject map[string]int
	// Available is false when summaries.db doesn't exist yet — every other
	// field is nil/empty in that case and the TUI should degrade silently.
	Available bool
}

type ProjectMetrics struct {
	TurnCount     int
	ToolCallCount int
	ErrorCount    int
	SessionCount  int
}

type ToolStat struct {
	Kind     string
	Calls    int
	Errors   int
	AvgMs    int64
}

// LoadSummaryData opens the SQLite summary DB read-only, materializes the
// per-session and per-project rollups the TUI needs, and returns. If the DB
// doesn't exist yet (e.g. summarizer not installed), Available is false.
func LoadSummaryData() (*SummaryData, error) {
	dbPath := filepath.Join(config.Home(), "summaries.db")
	if _, err := os.Stat(dbPath); err != nil {
		return &SummaryData{Available: false}, nil
	}

	// mode=ro keeps us out of the writer's way; the launchd summarizer
	// holds the only write connection.
	dsn := fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(2000)", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open summaries.db: %w", err)
	}
	defer db.Close()

	d := &SummaryData{
		BySession:            map[string]*SessionMetrics{},
		ByProject:            map[string]*ProjectMetrics{},
		ToolStats:            map[string][]ToolStat{},
		CompactionsByProject: map[string]int{},
		Available:            true,
	}

	if err := loadSessions(db, d); err != nil {
		return nil, err
	}
	if err := loadToolStats(db, d); err != nil {
		return nil, err
	}
	if err := loadCompactions(db, d); err != nil {
		return nil, err
	}

	return d, nil
}

// sessionKey is the same shape we use elsewhere in the TUI for per-session
// lookup. Centralized here so dashboard/detail can share it.
func sessionKey(agent, sessionID string) string {
	return agent + "\x00" + sessionID
}

func loadSessions(db *sql.DB, d *SummaryData) error {
	rows, err := db.Query(`
		SELECT session_id, agent, project, cwd, cwd_raw, git_remote, model,
		       turn_count, tool_call_count, error_count, compacted,
		       input_tokens, output_tokens, cache_read_tokens, duration_ms
		FROM sessions
	`)
	if err != nil {
		return fmt.Errorf("query sessions: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			m         SessionMetrics
			project   sql.NullString
			cwd       sql.NullString
			cwdRaw    sql.NullString
			gitRemote sql.NullString
			model     sql.NullString
			compact   sql.NullInt64
		)
		if err := rows.Scan(
			&m.SessionID, &m.Agent, &project, &cwd, &cwdRaw, &gitRemote, &model,
			&m.TurnCount, &m.ToolCallCount, &m.ErrorCount, &compact,
			&m.InputTokens, &m.OutputTokens, &m.CacheReadTokens,
			&m.DurationMs,
		); err != nil {
			return err
		}
		if project.Valid {
			m.Project = project.String
		}
		if cwd.Valid {
			m.Cwd = cwd.String
		}
		if cwdRaw.Valid {
			m.CwdRaw = cwdRaw.String
		}
		if gitRemote.Valid {
			m.GitRemote = gitRemote.String
		}
		if model.Valid {
			m.Model = model.String
		}
		m.Compacted = compact.Valid && compact.Int64 != 0

		d.BySession[sessionKey(m.Agent, m.SessionID)] = &m

		// Project-level rollup keyed by the slug. We don't have access to
		// the dashboard's basename-canonicalization here; that join is done
		// later in data.go where the slug → projectKey mapping lives.
		if project.Valid {
			pm, ok := d.ByProject[project.String]
			if !ok {
				pm = &ProjectMetrics{}
				d.ByProject[project.String] = pm
			}
			pm.SessionCount++
			pm.TurnCount += m.TurnCount
			pm.ToolCallCount += m.ToolCallCount
			pm.ErrorCount += m.ErrorCount
		}
	}
	return rows.Err()
}

func loadToolStats(db *sql.DB, d *SummaryData) error {
	// One row per (project, tool_kind) with counts, errors, avg duration.
	// Joining on sessions gives us the per-project bucketing we need without
	// a second walk through tool_calls in Go.
	rows, err := db.Query(`
		SELECT s.project,
		       COALESCE(tc.tool_kind, 'other') AS kind,
		       COUNT(*) AS calls,
		       SUM(tc.is_error) AS errors,
		       CAST(ROUND(AVG(tc.duration_ms)) AS INTEGER) AS avg_ms
		FROM tool_calls tc
		JOIN sessions s ON s.agent = tc.agent AND s.session_id = tc.session_id
		WHERE s.project IS NOT NULL
		GROUP BY s.project, kind
		ORDER BY s.project, calls DESC
	`)
	if err != nil {
		return fmt.Errorf("query tool_stats: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			project sql.NullString
			ts      ToolStat
			errs    sql.NullInt64
			avg     sql.NullInt64
		)
		if err := rows.Scan(&project, &ts.Kind, &ts.Calls, &errs, &avg); err != nil {
			return err
		}
		if !project.Valid {
			continue
		}
		if errs.Valid {
			ts.Errors = int(errs.Int64)
		}
		if avg.Valid {
			ts.AvgMs = avg.Int64
		}
		d.ToolStats[project.String] = append(d.ToolStats[project.String], ts)
	}
	return rows.Err()
}

func loadCompactions(db *sql.DB, d *SummaryData) error {
	rows, err := db.Query(`
		SELECT s.project, COUNT(*) AS n
		FROM compactions c
		JOIN sessions s ON s.agent = c.agent AND s.session_id = c.session_id
		WHERE s.project IS NOT NULL
		GROUP BY s.project
	`)
	if err != nil {
		return fmt.Errorf("query compactions: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var project sql.NullString
		var n int
		if err := rows.Scan(&project, &n); err != nil {
			return err
		}
		if project.Valid {
			d.CompactionsByProject[project.String] = n
		}
	}
	return rows.Err()
}
