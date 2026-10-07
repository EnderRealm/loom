package summaries

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"
)

// ProjectState is one knowledge scope's standing state as of ComputedAt: when
// it last saw a commit, a session and a ticket close, how much of each landed
// in the window ending at ComputedAt, and whether that adds up to dormant —
// no commit, no session and no ticket closed in the window. A Last* field is
// nil when the project has never had one.
type ProjectState struct {
	Project               string     `json:"project"`
	ComputedAt            time.Time  `json:"computed_at"`
	WindowSeconds         int64      `json:"window_seconds"`
	LastCommitAt          *time.Time `json:"last_commit_at"`
	LastSessionAt         *time.Time `json:"last_session_at"`
	LastTicketClosedAt    *time.Time `json:"last_ticket_closed_at"`
	CommitsInWindow       int        `json:"commits_in_window"`
	SessionsInWindow      int        `json:"sessions_in_window"`
	OpenTickets           int        `json:"open_tickets"`
	TicketsClosedInWindow int        `json:"tickets_closed_in_window"`
	Dormant               bool       `json:"dormant"`
}

// ReplaceProjectState swaps the whole table for rows in one transaction, so a
// scope that left the knowledge store leaves the table, and a reader never
// sees half of one rebuild beside half of the last.
func (s *Store) ReplaceProjectState(ctx context.Context, rows []ProjectState) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM project_state`); err != nil {
		return fmt.Errorf("clear project_state: %w", err)
	}
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO project_state (
		    project, computed_at, window_seconds, last_commit_at, last_session_at,
		    last_ticket_closed_at, commits_in_window, sessions_in_window,
		    open_tickets, tickets_closed_in_window, dormant
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, r := range rows {
		if _, err := stmt.ExecContext(ctx,
			r.Project, r.ComputedAt.UTC().Format(time.RFC3339Nano), r.WindowSeconds,
			timeOrNull(r.LastCommitAt), timeOrNull(r.LastSessionAt), timeOrNull(r.LastTicketClosedAt),
			r.CommitsInWindow, r.SessionsInWindow, r.OpenTickets, r.TicketsClosedInWindow,
			boolToInt(r.Dormant),
		); err != nil {
			return fmt.Errorf("insert project_state %s: %w", r.Project, err)
		}
	}
	return tx.Commit()
}

// LoadProjectState reads every project_state row at dbPath, ordered by
// project. A missing database, and one no build carrying the table has
// opened, are errors rather than an empty answer, which would read as loom
// tracking no project.
func LoadProjectState(dbPath string) ([]ProjectState, error) {
	if _, err := os.Stat(dbPath); err != nil {
		return nil, fmt.Errorf("summaries.db: %w — run `loom summarize`", err)
	}
	// mode=ro keeps us out of the summarizer's way; it holds the only writer.
	dsn := fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(2000)", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open summaries.db: %w", err)
	}
	defer db.Close()

	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'project_state'`).Scan(&n); err != nil {
		return nil, fmt.Errorf("read summaries.db: %w", err)
	}
	if n == 0 {
		return nil, fmt.Errorf("%s has no project_state table — run `loom summarize` with a build that writes it", dbPath)
	}

	rows, err := db.Query(`
		SELECT project, computed_at, window_seconds, last_commit_at, last_session_at,
		       last_ticket_closed_at, commits_in_window, sessions_in_window,
		       open_tickets, tickets_closed_in_window, dormant
		FROM project_state
		ORDER BY project`)
	if err != nil {
		return nil, fmt.Errorf("query project_state: %w", err)
	}
	defer rows.Close()
	var out []ProjectState
	for rows.Next() {
		var (
			r                                   ProjectState
			computedAt                          string
			lastCommit, lastSession, lastClosed sql.NullString
		)
		if err := rows.Scan(&r.Project, &computedAt, &r.WindowSeconds, &lastCommit, &lastSession,
			&lastClosed, &r.CommitsInWindow, &r.SessionsInWindow, &r.OpenTickets,
			&r.TicketsClosedInWindow, &r.Dormant); err != nil {
			return nil, err
		}
		if r.ComputedAt, err = time.Parse(time.RFC3339Nano, computedAt); err != nil {
			return nil, fmt.Errorf("project_state %s: computed_at: %w", r.Project, err)
		}
		if r.LastCommitAt, err = parseNullTime(lastCommit); err != nil {
			return nil, fmt.Errorf("project_state %s: last_commit_at: %w", r.Project, err)
		}
		if r.LastSessionAt, err = parseNullTime(lastSession); err != nil {
			return nil, fmt.Errorf("project_state %s: last_session_at: %w", r.Project, err)
		}
		if r.LastTicketClosedAt, err = parseNullTime(lastClosed); err != nil {
			return nil, fmt.Errorf("project_state %s: last_ticket_closed_at: %w", r.Project, err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func timeOrNull(t *time.Time) any {
	if t == nil {
		return nil
	}
	return isoOrNull(*t)
}

func parseNullTime(v sql.NullString) (*time.Time, error) {
	if !v.Valid {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339Nano, v.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}
