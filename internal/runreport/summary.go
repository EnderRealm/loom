package runreport

import (
	"database/sql"
	"fmt"
	"time"

	"loom/internal/pricing"
	"loom/internal/runs"
	"loom/internal/summaries"
)

// Summary is one run as a list row: the figures a reader compares runs by,
// read off a Report and nothing else, so a row and `run-report` cannot
// disagree. A nil pointer is a value the report could not measure; a zero
// time is one it did not have.
type Summary struct {
	RunID   string
	Ticket  string
	Runtime string
	// StartedAt is the record's start; Outcome the record's word or
	// running/unknown, never a reading of the telemetry.
	StartedAt time.Time
	Outcome   string
	// TelemetryState is complete or partial; Pending the executions still
	// open; LastObservedAt the latest timestamp anything in the run carried.
	TelemetryState string
	Pending        int
	LastObservedAt time.Time
	WallMs         *int64
	// ExecutionTimeMs, ToolTimeMs, TotalTokens, ToolCalls and Failures are the
	// total scope's; Children the descendant executions counted.
	// TimedExecutions and UntimedExecutions are the execution-time coverage:
	// ExecutionTimeMs is unmeasured, not zero, when nothing was timed.
	// Metered is whether any transcript was metered for the scope: without
	// one, ToolTimeMs, TotalTokens and ToolCalls are unknown, not zero.
	ExecutionTimeMs   int64
	TimedExecutions   int
	UntimedExecutions int
	ToolTimeMs        int64
	Metered           bool
	TotalTokens       int64
	ToolCalls         int
	Failures          int
	Children          int
	CostUSD           *float64
	// LegacyActiveMs is the parent span's, cost-report's active_ms.
	LegacyActiveMs int64
}

// SummaryOf reduces a report to its row.
func SummaryOf(rep *Report) Summary {
	total := rep.Metrics.Total
	return Summary{
		RunID:             rep.Run.RunID,
		Ticket:            rep.Run.Ticket,
		Runtime:           rep.Run.Runtime,
		StartedAt:         parseTime(rep.Run.StartedAt),
		Outcome:           rep.Run.Outcome,
		TelemetryState:    rep.Telemetry.State,
		Pending:           len(rep.Telemetry.ExecutionsPending),
		LastObservedAt:    parseTime(rep.Run.LastObservedAt),
		WallMs:            rep.Time.WallMs,
		ExecutionTimeMs:   rep.Time.ExecutionTimeMs,
		TimedExecutions:   total.ExecutionTimeCoverage.Timed,
		UntimedExecutions: total.ExecutionTimeCoverage.Untimed,
		ToolTimeMs:        rep.Time.ToolTimeMs,
		Metered:           len(total.TokensByRuntime) > 0,
		TotalTokens:       total.TotalTokens,
		ToolCalls:         total.ToolCalls,
		Failures:          total.Failures.Tool + total.Failures.API + total.Failures.Process + total.Failures.Other,
		Children:          rep.Metrics.Descendants.Executions,
		CostUSD:           total.CostUSD,
		LegacyActiveMs:    rep.Time.LegacyActiveMs,
	}
}

// ListSummaries opens dbPath the way Load does and summarizes every run
// started in [since, until), in runs.List order.
func ListSummaries(dbPath string, since, until time.Time) ([]Summary, error) {
	db, table, err := open(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return Summaries(db, table, since, until)
}

// Summaries builds each run in range and reduces it: the list is the report,
// row by row, with no accounting of its own.
func Summaries(db *sql.DB, table *pricing.Table, since, until time.Time) ([]Summary, error) {
	list, err := runs.List(db, since, until)
	if err != nil {
		return nil, err
	}
	out := make([]Summary, 0, len(list))
	for i := range list {
		rep, err := Build(db, &list[i], table)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", list[i].RunID, err)
		}
		out = append(out, SummaryOf(rep))
	}
	return out, nil
}

// Detail is one run as a reader inspects it: the report, the complete body
// of every lens response the run's attempts carry, keyed by LensKey, and
// when the summarizer last completed a sweep of the database (zero when it
// never recorded one, or when SweepErr says the marker could not be read:
// a defect in the freshness signal does not blank the report). The extras
// stay off the Report so `run-report` prints what it always has.
type Detail struct {
	Report        *Report
	LensResponses map[string]string
	SweptAt       time.Time
	SweepErr      error
}

// LoadDetail opens dbPath the way Load does and reads the run's detail.
func LoadDetail(dbPath, runID string) (*Detail, error) {
	db, table, err := open(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	run, err := runs.Load(db, runID)
	if err != nil {
		return nil, err
	}
	rep, err := Build(db, run, table)
	if err != nil {
		return nil, err
	}
	responses, err := LensResponses(db, run)
	if err != nil {
		return nil, err
	}
	sweptAt, _, sweepErr := summaries.LastSweep(db)
	return &Detail{Report: rep, LensResponses: responses, SweptAt: sweptAt, SweepErr: sweepErr}, nil
}

// LensKey names one attempt the way Report.Lenses groups it.
func LensKey(lens string, round, attempt int) string {
	return fmt.Sprintf("%s/%d/%d", lens, round, attempt)
}

// LensResponses reads the whole fenced body of each response the run's lens
// attempts paired with, keyed by LensKey. An attempt with no response, or
// whose response row is gone, has no entry.
func LensResponses(db *sql.DB, run *runs.Run) (map[string]string, error) {
	out := map[string]string{}
	for _, a := range run.Lenses {
		if a.ResponseID == "" {
			continue
		}
		var raw sql.NullString
		err := db.QueryRow(`SELECT raw FROM lens_responses WHERE response_id = ?`, a.ResponseID).Scan(&raw)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("query lens response: %w", err)
		}
		out[LensKey(a.Lens, a.Round, a.Attempt)] = raw.String
	}
	return out, nil
}
