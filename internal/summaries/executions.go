package summaries

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// ExecutionsAgent is the agent the shipper files execution records under, so
// they land at received/loom-executions/<host>/executions.jsonl. Declared
// here as well as in the transport, the way claude-code and codex-cli are:
// the summarizer cannot import transport/internal.
const ExecutionsAgent = "loom-executions"

// executionRecordVersion is the producer contract this importer reads
// (docs/execution-records.md). A record at any other version is skipped with
// an unsupported_version diagnostic rather than read on the assumption that
// the fields still mean the same thing.
const executionRecordVersion = 1

// Record kinds.
const (
	recordKindRun       = "run"
	recordKindExecution = "execution"
)

// maxRecordBytes caps one line. A record is a few hundred bytes; the cap only
// has to keep a runaway line from being read as a record, and a line over it
// is skipped with a diagnostic so the file stays importable.
const maxRecordBytes = 1024 * 1024

// Diagnostic codes written to execution_diagnostics.
const (
	DiagUnsupportedVersion = "unsupported_version"
	DiagUnsupportedKind    = "unsupported_kind"
	DiagMissingID          = "missing_id"
	DiagInvalidValue       = "invalid_value"
	DiagUnresolvedRun      = "unresolved_run"
	DiagUnresolvedParent   = "unresolved_parent"
	DiagMalformedJSON      = "malformed_json"
	DiagIdentityConflict   = "identity_conflict"
)

var (
	runtimes       = map[string]bool{"claude-code": true, "codex-cli": true, "weft": true}
	outcomes       = map[string]bool{"completed": true, "failed": true, "stopped": true}
	executionKinds = map[string]bool{"root": true, "subagent": true, "lens": true, "stage": true, "command": true}
)

// executionRecord is the union of both record kinds on the wire. Integers are
// pointers so an absent field stores NULL rather than a zero that would read
// as "round 0".
type executionRecord struct {
	V    int    `json:"v"`
	Kind string `json:"kind"`

	RunID           string `json:"run_id"`
	Ticket          string `json:"ticket"`
	Runtime         string `json:"runtime"`
	Agent           string `json:"agent"`
	SessionID       string `json:"session_id"`
	Producer        string `json:"producer"`
	StartedAt       string `json:"started_at"`
	EndedAt         string `json:"ended_at"`
	Outcome         string `json:"outcome"`
	ReportingCutoff string `json:"reporting_cutoff"`
	RecordedAt      string `json:"recorded_at"`

	ExecutionID       string `json:"execution_id"`
	ParentExecutionID string `json:"parent_execution_id"`
	ExecutionKind     string `json:"execution_kind"`
	DispatchID        string `json:"dispatch_id"`
	Stage             string `json:"stage"`
	StageOccurrence   *int   `json:"stage_occurrence"`
	Lens              string `json:"lens"`
	Round             *int   `json:"round"`
	Attempt           *int   `json:"attempt"`
}

// ImportCounts is what one ImportExecutions pass wrote, for the sweep's log
// line. Runs and Executions count records upserted, not rows: a start and a
// terminal record for the same id count twice. Diagnostics counts every row
// written for the source, unresolved associations included.
type ImportCounts struct {
	Runs        int
	Executions  int
	Diagnostics int
}

// ExecutionImportCurrent reports whether sourcePath was already imported at
// this size and mtime, the same skip rule SessionAlreadyCurrent applies.
func (s *Store) ExecutionImportCurrent(sourcePath string, size int64, mtime time.Time) (bool, error) {
	var existingSize sql.NullInt64
	var existingMtime sql.NullString
	err := s.db.QueryRow(`SELECT size, mtime FROM execution_imports WHERE source_path = ?`,
		sourcePath).Scan(&existingSize, &existingMtime)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return existingSize.Valid && existingSize.Int64 == size &&
		existingMtime.Valid && existingMtime.String == mtime.UTC().Format(time.RFC3339Nano), nil
}

// ImportExecutions folds one execution-record file into runs, executions and
// execution_diagnostics in a single transaction. Records merge by id: a field
// the row already holds is kept unless the record carries a non-empty value
// for it, so a start record followed by a terminal one yields one row with
// both, and replaying the file changes nothing. Diagnostics are recomputed
// from scratch for this source, and the unresolved-association ones for every
// source, since a record here can resolve an execution recorded elsewhere.
// Nothing from a record body reaches a diagnostic or the returned error: only
// ids, kinds, field names, the path and the 1-based line number.
func (s *Store) ImportExecutions(ctx context.Context, sourcePath string, r io.Reader,
	size int64, mtime time.Time) (ImportCounts, error) {
	var counts ImportCounts
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return counts, err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM execution_diagnostics WHERE source_path = ?`, sourcePath); err != nil {
		return counts, fmt.Errorf("clear diagnostics: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	imp := &importer{ctx: ctx, tx: tx, sourcePath: sourcePath, now: now, counts: &counts}
	br := bufio.NewReaderSize(r, 64*1024)
	for line := 1; ; line++ {
		text, tooLong, err := readLine(br, maxRecordBytes)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return counts, err
		}
		if tooLong {
			if err := imp.diagnostic(line, DiagMalformedJSON, "", "", fmt.Sprintf("bytes>%d", maxRecordBytes)); err != nil {
				return counts, fmt.Errorf("%s:%d: %w", sourcePath, line, err)
			}
			continue
		}
		text = bytes.TrimSpace(text)
		if len(text) == 0 {
			continue
		}
		if err := imp.record(line, text); err != nil {
			return counts, fmt.Errorf("%s:%d: %w", sourcePath, line, err)
		}
	}

	if err := imp.unresolved(); err != nil {
		return counts, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO execution_imports (source_path, size, mtime, imported_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(source_path) DO UPDATE SET
		    size = excluded.size, mtime = excluded.mtime, imported_at = excluded.imported_at`,
		sourcePath, size, mtime.UTC().Format(time.RFC3339Nano), now); err != nil {
		return counts, fmt.Errorf("mark import: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return counts, err
	}
	return counts, nil
}

// readLine returns the next line without its terminator. A line longer than
// max is consumed to its end and reported tooLong with no text, so the caller
// can skip it and carry on at the next line.
func readLine(br *bufio.Reader, max int) (text []byte, tooLong bool, err error) {
	for {
		chunk, more, err := br.ReadLine()
		if err != nil {
			return nil, false, err
		}
		if !tooLong {
			if len(text)+len(chunk) > max {
				tooLong, text = true, nil
			} else {
				text = append(text, chunk...)
			}
		}
		if !more {
			return text, tooLong, nil
		}
	}
}

type importer struct {
	ctx        context.Context
	tx         *sql.Tx
	sourcePath string
	now        string
	counts     *ImportCounts
}

// record validates one line and upserts it, or writes the diagnostic that
// explains why it was skipped. The returned error is a database failure, never
// a bad record.
func (imp *importer) record(line int, text []byte) error {
	var rec executionRecord
	if err := json.Unmarshal(text, &rec); err != nil {
		// A field of the wrong JSON type names itself; a line that is not a
		// record at all does not.
		var te *json.UnmarshalTypeError
		if errors.As(err, &te) && te.Field != "" {
			return imp.diagnostic(line, DiagInvalidValue, rec.RunID, rec.ExecutionID, "field="+te.Field)
		}
		return imp.diagnostic(line, DiagMalformedJSON, "", "", "")
	}
	if rec.V != executionRecordVersion {
		return imp.diagnostic(line, DiagUnsupportedVersion, rec.RunID, rec.ExecutionID,
			fmt.Sprintf("v=%d want=%d", rec.V, executionRecordVersion))
	}
	switch rec.Kind {
	case recordKindRun:
		return imp.run(line, rec)
	case recordKindExecution:
		return imp.execution(line, rec)
	default:
		return imp.diagnostic(line, DiagUnsupportedKind, rec.RunID, rec.ExecutionID, "kind="+rec.Kind)
	}
}

func (imp *importer) run(line int, rec executionRecord) error {
	if rec.RunID == "" {
		return imp.diagnostic(line, DiagMissingID, "", "", "kind=run field=run_id")
	}
	invalid := func(field string) error {
		return imp.diagnostic(line, DiagInvalidValue, rec.RunID, "", "kind=run field="+field)
	}
	if rec.Runtime != "" && !runtimes[rec.Runtime] {
		return invalid("runtime")
	}
	if rec.Outcome != "" && !outcomes[rec.Outcome] {
		return invalid("outcome")
	}
	if !normalizeTime(&rec.StartedAt) {
		return invalid("started_at")
	}
	if !normalizeTime(&rec.EndedAt) {
		return invalid("ended_at")
	}
	if !normalizeTime(&rec.ReportingCutoff) {
		return invalid("reporting_cutoff")
	}
	if !normalizeTime(&rec.RecordedAt) {
		return invalid("recorded_at")
	}
	_, err := imp.tx.ExecContext(imp.ctx, `
		INSERT INTO runs (
		    run_id, ticket, runtime, agent, session_id, producer, started_at,
		    ended_at, outcome, reporting_cutoff, recorded_at,
		    source_path, source_line, first_seen, last_seen
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(run_id) DO UPDATE SET
		    ticket           = COALESCE(excluded.ticket, runs.ticket),
		    runtime          = COALESCE(excluded.runtime, runs.runtime),
		    agent            = COALESCE(excluded.agent, runs.agent),
		    session_id       = COALESCE(excluded.session_id, runs.session_id),
		    producer         = COALESCE(excluded.producer, runs.producer),
		    started_at       = COALESCE(excluded.started_at, runs.started_at),
		    ended_at         = COALESCE(excluded.ended_at, runs.ended_at),
		    outcome          = COALESCE(excluded.outcome, runs.outcome),
		    reporting_cutoff = COALESCE(excluded.reporting_cutoff, runs.reporting_cutoff),
		    recorded_at      = COALESCE(excluded.recorded_at, runs.recorded_at),
		    source_path      = excluded.source_path,
		    source_line      = excluded.source_line,
		    last_seen        = excluded.last_seen`,
		rec.RunID, strOrNull(rec.Ticket), strOrNull(rec.Runtime), strOrNull(rec.Agent),
		strOrNull(rec.SessionID), strOrNull(rec.Producer), strOrNull(rec.StartedAt),
		strOrNull(rec.EndedAt), strOrNull(rec.Outcome), strOrNull(rec.ReportingCutoff),
		strOrNull(rec.RecordedAt), imp.sourcePath, line, imp.now, imp.now)
	if err != nil {
		return fmt.Errorf("upsert run: %w", err)
	}
	imp.counts.Runs++
	return nil
}

func (imp *importer) execution(line int, rec executionRecord) error {
	if rec.ExecutionID == "" {
		return imp.diagnostic(line, DiagMissingID, rec.RunID, "", "kind=execution field=execution_id")
	}
	if rec.RunID == "" {
		return imp.diagnostic(line, DiagMissingID, "", rec.ExecutionID, "kind=execution field=run_id")
	}
	invalid := func(field string) error {
		return imp.diagnostic(line, DiagInvalidValue, rec.RunID, rec.ExecutionID, "kind=execution field="+field)
	}
	if rec.ParentExecutionID == rec.ExecutionID {
		return invalid("parent_execution_id")
	}
	if rec.ExecutionKind != "" && !executionKinds[rec.ExecutionKind] {
		return invalid("execution_kind")
	}
	if rec.Outcome != "" && !outcomes[rec.Outcome] {
		return invalid("outcome")
	}
	if !normalizeTime(&rec.StartedAt) {
		return invalid("started_at")
	}
	if !normalizeTime(&rec.EndedAt) {
		return invalid("ended_at")
	}
	if !normalizeTime(&rec.RecordedAt) {
		return invalid("recorded_at")
	}
	// An execution_id belongs to the run that first declared it. A record
	// naming it under another run is rejected whole rather than merged, or
	// the execution would move runs while keeping the first run's fields.
	var existingRunID string
	err := imp.tx.QueryRowContext(imp.ctx,
		`SELECT run_id FROM executions WHERE execution_id = ?`, rec.ExecutionID).Scan(&existingRunID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("lookup execution: %w", err)
	}
	if err == nil && existingRunID != rec.RunID {
		return imp.diagnostic(line, DiagIdentityConflict, rec.RunID, rec.ExecutionID, "existing_run_id="+existingRunID)
	}
	_, err = imp.tx.ExecContext(imp.ctx, `
		INSERT INTO executions (
		    execution_id, run_id, parent_execution_id, execution_kind, agent,
		    session_id, dispatch_id, stage, stage_occurrence, lens, round,
		    attempt, started_at, ended_at, outcome, producer, recorded_at,
		    source_path, source_line, first_seen, last_seen
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(execution_id) DO UPDATE SET
		    run_id              = executions.run_id,
		    parent_execution_id = COALESCE(excluded.parent_execution_id, executions.parent_execution_id),
		    execution_kind      = COALESCE(excluded.execution_kind, executions.execution_kind),
		    agent               = COALESCE(excluded.agent, executions.agent),
		    session_id          = COALESCE(excluded.session_id, executions.session_id),
		    dispatch_id         = COALESCE(excluded.dispatch_id, executions.dispatch_id),
		    stage               = COALESCE(excluded.stage, executions.stage),
		    stage_occurrence    = COALESCE(excluded.stage_occurrence, executions.stage_occurrence),
		    lens                = COALESCE(excluded.lens, executions.lens),
		    round               = COALESCE(excluded.round, executions.round),
		    attempt             = COALESCE(excluded.attempt, executions.attempt),
		    started_at          = COALESCE(excluded.started_at, executions.started_at),
		    ended_at            = COALESCE(excluded.ended_at, executions.ended_at),
		    outcome             = COALESCE(excluded.outcome, executions.outcome),
		    producer            = COALESCE(excluded.producer, executions.producer),
		    recorded_at         = COALESCE(excluded.recorded_at, executions.recorded_at),
		    source_path         = excluded.source_path,
		    source_line         = excluded.source_line,
		    last_seen           = excluded.last_seen`,
		rec.ExecutionID, rec.RunID, strOrNull(rec.ParentExecutionID), strOrNull(rec.ExecutionKind),
		strOrNull(rec.Agent), strOrNull(rec.SessionID), strOrNull(rec.DispatchID), strOrNull(rec.Stage),
		intOrNull(rec.StageOccurrence), strOrNull(rec.Lens), intOrNull(rec.Round), intOrNull(rec.Attempt),
		strOrNull(rec.StartedAt), strOrNull(rec.EndedAt), strOrNull(rec.Outcome), strOrNull(rec.Producer),
		strOrNull(rec.RecordedAt), imp.sourcePath, line, imp.now, imp.now)
	if err != nil {
		return fmt.Errorf("upsert execution: %w", err)
	}
	imp.counts.Executions++
	return nil
}

// diagnostic records why a line was skipped. INSERT OR REPLACE because a
// re-import of the same file lands on the same (path, line, code) key.
func (imp *importer) diagnostic(line int, code, runID, executionID, detail string) error {
	if _, err := imp.tx.ExecContext(imp.ctx, `
		INSERT OR REPLACE INTO execution_diagnostics
		    (source_path, source_line, code, run_id, execution_id, detail)
		VALUES (?, ?, ?, ?, ?, ?)`,
		imp.sourcePath, line, code, strOrNull(runID), strOrNull(executionID), strOrNull(detail)); err != nil {
		return fmt.Errorf("write diagnostic: %w", err)
	}
	imp.counts.Diagnostics++
	return nil
}

// unresolved rewrites the association diagnostics for every source: an
// execution whose run nobody declared, and one whose parent nobody declared
// in the same run — the loader reads one run's executions, so a parent in
// another run is out of its reach. Recomputed globally because the record
// that resolves an association may sit in a different file from the one
// that made it.
func (imp *importer) unresolved() error {
	if _, err := imp.tx.ExecContext(imp.ctx,
		`DELETE FROM execution_diagnostics WHERE code IN (?, ?)`,
		DiagUnresolvedRun, DiagUnresolvedParent); err != nil {
		return fmt.Errorf("clear unresolved: %w", err)
	}
	if _, err := imp.tx.ExecContext(imp.ctx, `
		INSERT OR REPLACE INTO execution_diagnostics
		    (source_path, source_line, code, run_id, execution_id, detail)
		SELECT e.source_path, e.source_line, ?, e.run_id, e.execution_id,
		       'run_id=' || e.run_id || ' execution_id=' || e.execution_id
		FROM executions e LEFT JOIN runs r ON r.run_id = e.run_id
		WHERE r.run_id IS NULL`, DiagUnresolvedRun); err != nil {
		return fmt.Errorf("write unresolved runs: %w", err)
	}
	if _, err := imp.tx.ExecContext(imp.ctx, `
		INSERT OR REPLACE INTO execution_diagnostics
		    (source_path, source_line, code, run_id, execution_id, detail)
		SELECT e.source_path, e.source_line, ?, e.run_id, e.execution_id,
		       'parent_execution_id=' || e.parent_execution_id || ' execution_id=' || e.execution_id
		FROM executions e LEFT JOIN executions p
		    ON p.execution_id = e.parent_execution_id AND p.run_id = e.run_id
		WHERE e.parent_execution_id IS NOT NULL AND p.execution_id IS NULL`,
		DiagUnresolvedParent); err != nil {
		return fmt.Errorf("write unresolved parents: %w", err)
	}
	var n int
	if err := imp.tx.QueryRowContext(imp.ctx, `
		SELECT COUNT(*) FROM execution_diagnostics
		WHERE source_path = ? AND code IN (?, ?)`,
		imp.sourcePath, DiagUnresolvedRun, DiagUnresolvedParent).Scan(&n); err != nil {
		return fmt.Errorf("count unresolved: %w", err)
	}
	imp.counts.Diagnostics += n
	return nil
}

// normalizeTime rewrites an RFC 3339 timestamp into the UTC RFC3339Nano form
// every other timestamp column holds (isoOrNull), so string order is time
// order to the second; RFC3339Nano drops trailing zeros, so a whole-second
// value sorts after a fractional one in the same second. An empty value
// passes untouched; anything unparseable reports false.
func normalizeTime(s *string) bool {
	if *s == "" {
		return true
	}
	t, err := time.Parse(time.RFC3339Nano, *s)
	if err != nil {
		return false
	}
	*s = t.UTC().Format(time.RFC3339Nano)
	return true
}

func intOrNull(n *int) any {
	if n == nil {
		return nil
	}
	return *n
}
