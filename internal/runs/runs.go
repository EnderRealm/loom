// Package runs reads the run hierarchy out of summaries.db: every /work
// invocation (or Weft run) as one Run, with the executions attributable to it
// — subagents, routed lenses, stage attempts, commands — as a tree under its
// root execution.
//
// Two origins feed it. A run declared through execution records
// (docs/execution-records.md) is built from the runs and executions tables
// and every edge in it was written by a producer. A run nobody instrumented
// is recognized from its transcript by internal/workreport, and its children
// come only from explicit evidence in the transcripts themselves: the
// parent's own subagent rows, and Codex sessions whose session_meta names
// the parent thread. Nothing is joined by project or by time alone; an
// association the evidence cannot settle stays in Unresolved.
package runs

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"loom/internal/workreport"
)

// Origin values.
const (
	OriginRecord     = "record"
	OriginTranscript = "transcript"
)

// Kind values a synthesized node carries; recorded nodes carry the
// execution_kind their record declared.
const (
	KindRoot     = "root"
	KindSubagent = "subagent"
)

// Diagnostic codes the loader writes; the importer's are in summaries.
const (
	// DiagAmbiguousParent: a Codex subagent session whose parent session
	// holds more than one recognized run. It belongs to one of them, and
	// which one the transcripts do not say.
	DiagAmbiguousParent = "ambiguous_parent"
	// DiagUnresolvedRoot: a parentless execution the run cannot take as its
	// root — a second root, or a parentless node of another kind.
	DiagUnresolvedRoot = "unresolved_root"
	// DiagCyclicParent: an execution whose parent chain leads back to itself
	// — its own parent, or a member of a longer cycle. No child pointer is
	// added for it, so the tree stays acyclic and serializable.
	DiagCyclicParent = "cyclic_parent"
)

// SourceRef locates the record a row was last written from.
type SourceRef struct {
	Path string `json:"path"`
	Line int    `json:"line"`
}

// TranscriptRef names a session in summaries.db.
type TranscriptRef struct {
	Agent     string `json:"agent"`
	SessionID string `json:"session_id"`
}

// Node is one execution. Pointer fields are null when the record carried
// nothing: a stage occurrence of 0 would read as a real value.
type Node struct {
	ExecutionID       string         `json:"execution_id"`
	RunID             string         `json:"run_id"`
	ParentExecutionID string         `json:"parent_execution_id"`
	Kind              string         `json:"kind"`
	Transcript        *TranscriptRef `json:"transcript"`
	DispatchID        string         `json:"dispatch_id"`
	Stage             string         `json:"stage"`
	StageOccurrence   *int           `json:"stage_occurrence"`
	Lens              string         `json:"lens"`
	Round             *int           `json:"round"`
	Attempt           *int           `json:"attempt"`
	StartedAt         string         `json:"started_at"`
	EndedAt           string         `json:"ended_at"`
	Outcome           string         `json:"outcome"`
	Source            *SourceRef     `json:"source"`
	Children          []*Node        `json:"children"`
}

// Diagnostic is one thing the importer or the loader could not resolve.
// Detail is built from ids, kinds and field names only.
type Diagnostic struct {
	Code        string     `json:"code"`
	Detail      string     `json:"detail"`
	RunID       string     `json:"run_id"`
	ExecutionID string     `json:"execution_id"`
	Source      *SourceRef `json:"source"`
}

// Run is one /work invocation with its execution tree. Source is nil for a
// transcript-recognized run and for a run no record declared, which is known
// only from the executions naming it and carries an unresolved_run
// diagnostic.
type Run struct {
	RunID           string         `json:"run_id"`
	Ticket          string         `json:"ticket"`
	Runtime         string         `json:"runtime"`
	Transcript      *TranscriptRef `json:"transcript"`
	StartedAt       string         `json:"started_at"`
	EndedAt         string         `json:"ended_at"`
	Outcome         string         `json:"outcome"`
	ReportingCutoff string         `json:"reporting_cutoff"`
	Producer        string         `json:"producer"`
	Origin          string         `json:"origin"`
	Source          *SourceRef     `json:"source"`
	Root            *Node          `json:"root"`
	Unresolved      []*Node        `json:"unresolved"`
	Diagnostics     []Diagnostic   `json:"diagnostics"`
}

// ErrNotFound is returned by Load for a run id nothing declares or names.
var ErrNotFound = errors.New("run not found")

// Load returns one run by id: a declared run, a run only executions name, or
// a transcript-recognized run under its synthesized id.
func Load(db *sql.DB, runID string) (*Run, error) {
	row, err := loadRunRow(db, runID)
	if err != nil {
		return nil, err
	}
	if row != nil {
		return buildRecorded(db, *row)
	}
	nodes, err := loadExecutions(db, runID)
	if err != nil {
		return nil, err
	}
	if len(nodes) > 0 {
		run := &Run{RunID: runID, Origin: OriginRecord}
		run.Diagnostics, err = loadDiagnostics(db, runID)
		if err != nil {
			return nil, err
		}
		attach(run, nodes)
		return run, nil
	}
	historical, err := loadHistorical(db, time.Time{}, time.Time{})
	if err != nil {
		return nil, err
	}
	for i := range historical {
		if historical[i].RunID == runID {
			return &historical[i], nil
		}
	}
	return nil, fmt.Errorf("%w: %s", ErrNotFound, runID)
}

// List returns every run started in [since, until), declared and
// transcript-recognized alike. A zero bound is unbounded. A run with no start
// time cannot be placed in a bounded range and is listed only when the range
// is unbounded, the rule workreport applies.
func List(db *sql.DB, since, until time.Time) ([]Run, error) {
	recorded, err := loadRunRows(db, since, until)
	if err != nil {
		return nil, err
	}
	var out []Run
	for _, row := range recorded {
		run, err := buildRecorded(db, row)
		if err != nil {
			return nil, err
		}
		out = append(out, *run)
	}

	historical, err := loadHistorical(db, since, until)
	if err != nil {
		return nil, err
	}
	out = append(out, historical...)

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].StartedAt != out[j].StartedAt {
			return out[i].StartedAt < out[j].StartedAt
		}
		return out[i].RunID < out[j].RunID
	})
	return out, nil
}

type runRow struct {
	runID, ticket, runtime, agent, sessionID, producer string
	startedAt, endedAt, outcome, reportingCutoff       string
	sourcePath                                         string
	sourceLine                                         int
}

type scanner interface {
	Scan(dest ...any) error
}

func scanRun(sc scanner) (runRow, error) {
	var (
		row                                                  runRow
		ticket, runtime, agent, sessionID, producer          sql.NullString
		startedAt, endedAt, outcome, reportingCutoff, source sql.NullString
		line                                                 sql.NullInt64
	)
	if err := sc.Scan(&row.runID, &ticket, &runtime, &agent, &sessionID, &producer,
		&startedAt, &endedAt, &outcome, &reportingCutoff, &source, &line); err != nil {
		return row, err
	}
	row.ticket, row.runtime, row.agent, row.sessionID = ticket.String, runtime.String, agent.String, sessionID.String
	row.producer, row.startedAt, row.endedAt = producer.String, startedAt.String, endedAt.String
	row.outcome, row.reportingCutoff = outcome.String, reportingCutoff.String
	row.sourcePath, row.sourceLine = source.String, int(line.Int64)
	return row, nil
}

// loadRunRows reads every declared run in range. Fully drained before the
// per-run queries run: the summaries store allows one connection, so a
// query issued while this cursor is open would wait on itself.
func loadRunRows(db *sql.DB, since, until time.Time) ([]runRow, error) {
	rows, err := db.Query(`
		SELECT run_id, ticket, runtime, agent, session_id, producer, started_at,
		       ended_at, outcome, reporting_cutoff, source_path, source_line
		FROM runs ORDER BY started_at, run_id`)
	if err != nil {
		return nil, fmt.Errorf("query runs: %w", err)
	}
	defer rows.Close()
	var out []runRow
	for rows.Next() {
		row, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		if inRange(parseTime(row.startedAt), since, until) {
			out = append(out, row)
		}
	}
	return out, rows.Err()
}

func loadRunRow(db *sql.DB, runID string) (*runRow, error) {
	row, err := scanRun(db.QueryRow(`
		SELECT run_id, ticket, runtime, agent, session_id, producer, started_at,
		       ended_at, outcome, reporting_cutoff, source_path, source_line
		FROM runs WHERE run_id = ?`, runID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("query run: %w", err)
	}
	return &row, nil
}

func buildRecorded(db *sql.DB, row runRow) (*Run, error) {
	run := &Run{
		RunID:           row.runID,
		Ticket:          row.ticket,
		Runtime:         row.runtime,
		Transcript:      transcriptRef(row.agent, row.sessionID),
		StartedAt:       row.startedAt,
		EndedAt:         row.endedAt,
		Outcome:         row.outcome,
		ReportingCutoff: row.reportingCutoff,
		Producer:        row.producer,
		Origin:          OriginRecord,
		Source:          &SourceRef{Path: row.sourcePath, Line: row.sourceLine},
	}
	nodes, err := loadExecutions(db, row.runID)
	if err != nil {
		return nil, err
	}
	run.Diagnostics, err = loadDiagnostics(db, row.runID)
	if err != nil {
		return nil, err
	}
	attach(run, nodes)
	return run, nil
}

// loadExecutions reads a run's executions in the order children are listed:
// start time, then id, so a rebuild reproduces the tree byte for byte.
func loadExecutions(db *sql.DB, runID string) ([]*Node, error) {
	rows, err := db.Query(`
		SELECT execution_id, parent_execution_id, execution_kind, agent, session_id,
		       dispatch_id, stage, stage_occurrence, lens, round, attempt,
		       started_at, ended_at, outcome, source_path, source_line
		FROM executions WHERE run_id = ? ORDER BY started_at, execution_id`, runID)
	if err != nil {
		return nil, fmt.Errorf("query executions: %w", err)
	}
	defer rows.Close()
	var out []*Node
	for rows.Next() {
		var (
			n                                                     Node
			parent, kind, agent, sessionID, dispatch, stage, lens sql.NullString
			occurrence, round, attempt, line                      sql.NullInt64
			startedAt, endedAt, outcome, source                   sql.NullString
		)
		if err := rows.Scan(&n.ExecutionID, &parent, &kind, &agent, &sessionID, &dispatch,
			&stage, &occurrence, &lens, &round, &attempt, &startedAt, &endedAt, &outcome,
			&source, &line); err != nil {
			return nil, err
		}
		n.RunID = runID
		n.ParentExecutionID = parent.String
		n.Kind = kind.String
		n.Transcript = transcriptRef(agent.String, sessionID.String)
		n.DispatchID = dispatch.String
		n.Stage = stage.String
		n.StageOccurrence = intPtr(occurrence)
		n.Lens = lens.String
		n.Round = intPtr(round)
		n.Attempt = intPtr(attempt)
		n.StartedAt = startedAt.String
		n.EndedAt = endedAt.String
		n.Outcome = outcome.String
		n.Source = &SourceRef{Path: source.String, Line: int(line.Int64)}
		out = append(out, &n)
	}
	return out, rows.Err()
}

// attach builds the tree. The root is the first parentless node of kind root;
// every other node hangs under its parent when that parent exists. Whatever
// the root cannot reach — a node whose parent nobody declared, a second root,
// a parentless node of another kind, a cycle — is listed in Unresolved by its
// topmost node, never guessed onto the root. A node whose parent chain returns
// to it gets no child pointer and is its own top, so the tree holds no
// back-reference. Diagnostics are appended, so the importer's must already be
// loaded.
func attach(run *Run, nodes []*Node) {
	byID := make(map[string]*Node, len(nodes))
	for _, n := range nodes {
		byID[n.ExecutionID] = n
	}
	cyclic := map[string]bool{}
	for _, n := range nodes {
		seen := map[string]bool{}
		p, ok := byID[n.ParentExecutionID]
		for ok && !seen[p.ExecutionID] {
			if p == n {
				cyclic[n.ExecutionID] = true
				break
			}
			seen[p.ExecutionID] = true
			p, ok = byID[p.ParentExecutionID]
		}
	}
	for _, n := range nodes {
		if n.ParentExecutionID == "" {
			if run.Root == nil && n.Kind == KindRoot {
				run.Root = n
				continue
			}
			run.Diagnostics = append(run.Diagnostics, Diagnostic{
				Code:        DiagUnresolvedRoot,
				Detail:      fmt.Sprintf("execution_kind=%s", n.Kind),
				RunID:       run.RunID,
				ExecutionID: n.ExecutionID,
				Source:      n.Source,
			})
			continue
		}
		if cyclic[n.ExecutionID] {
			run.Diagnostics = append(run.Diagnostics, Diagnostic{
				Code:        DiagCyclicParent,
				Detail:      "parent_execution_id=" + n.ParentExecutionID,
				RunID:       run.RunID,
				ExecutionID: n.ExecutionID,
				Source:      n.Source,
			})
			continue
		}
		if p, ok := byID[n.ParentExecutionID]; ok {
			p.Children = append(p.Children, n)
		}
	}
	placed := map[string]bool{}
	if run.Root != nil {
		place(run.Root, placed)
	}
	for _, n := range nodes {
		if placed[n.ExecutionID] {
			continue
		}
		// Walk up to the top of this detached chain. A cyclic node has no
		// parent in the tree, so it is the top of whatever hangs under it.
		top := n
		for !cyclic[top.ExecutionID] {
			p, ok := byID[top.ParentExecutionID]
			if !ok || placed[p.ExecutionID] {
				break
			}
			top = p
		}
		if placed[top.ExecutionID] {
			continue
		}
		run.Unresolved = append(run.Unresolved, top)
		place(top, placed)
	}
}

func place(n *Node, placed map[string]bool) {
	if placed[n.ExecutionID] {
		return
	}
	placed[n.ExecutionID] = true
	for _, c := range n.Children {
		place(c, placed)
	}
}

func loadDiagnostics(db *sql.DB, runID string) ([]Diagnostic, error) {
	rows, err := db.Query(`
		SELECT code, detail, run_id, execution_id, source_path, source_line
		FROM execution_diagnostics WHERE run_id = ?
		ORDER BY source_path, source_line, code`, runID)
	if err != nil {
		return nil, fmt.Errorf("query diagnostics: %w", err)
	}
	defer rows.Close()
	var out []Diagnostic
	for rows.Next() {
		var (
			d                         Diagnostic
			detail, run, exec, source sql.NullString
			line                      sql.NullInt64
		)
		if err := rows.Scan(&d.Code, &detail, &run, &exec, &source, &line); err != nil {
			return nil, err
		}
		d.Detail, d.RunID, d.ExecutionID = detail.String, run.String, exec.String
		d.Source = &SourceRef{Path: source.String, Line: int(line.Int64)}
		out = append(out, d)
	}
	return out, rows.Err()
}

// loadHistorical synthesizes a run for every transcript-recognized /work
// invocation in range whose session no record has claimed. The root's id is
// transcript:<agent>:<session_id>:<turn idx>, which is also the run's.
func loadHistorical(db *sql.DB, since, until time.Time) ([]Run, error) {
	recorded, err := recordedSessions(db)
	if err != nil {
		return nil, err
	}
	invocations, err := workreport.Invocations(db)
	if err != nil {
		return nil, err
	}
	// A Codex subagent whose parent session holds more than one run cannot
	// be placed; the count decides between attaching and Unresolved.
	perSession := map[TranscriptRef]int{}
	for _, inv := range invocations {
		perSession[TranscriptRef{inv.Agent, inv.SessionID}]++
	}

	var out []Run
	for _, inv := range invocations {
		ref := TranscriptRef{inv.Agent, inv.SessionID}
		if recorded[ref] || !inRange(inv.StartedAt, since, until) {
			continue
		}
		id := fmt.Sprintf("transcript:%s:%s:%d", inv.Agent, inv.SessionID, inv.TurnIdx)
		root := &Node{
			ExecutionID: id,
			RunID:       id,
			Kind:        KindRoot,
			Transcript:  &ref,
			StartedAt:   isoOrEmpty(inv.StartedAt),
			EndedAt:     isoOrEmpty(inv.EndsAt),
		}
		run := Run{
			RunID:      id,
			Ticket:     inv.Ticket,
			Runtime:    inv.Agent,
			Transcript: &ref,
			StartedAt:  root.StartedAt,
			EndedAt:    root.EndedAt,
			Origin:     OriginTranscript,
			Root:       root,
		}
		if err := attachSubagentRows(db, &run, inv); err != nil {
			return nil, err
		}
		if err := attachCodexChildren(db, &run, inv, perSession[ref]); err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, nil
}

// recordedSessions is the set of transcripts a run record claims. A session
// with a record is not re-recognized from its transcript: the record wins.
func recordedSessions(db *sql.DB) (map[TranscriptRef]bool, error) {
	rows, err := db.Query(`SELECT agent, session_id FROM runs WHERE agent IS NOT NULL AND session_id IS NOT NULL`)
	if err != nil {
		return nil, fmt.Errorf("query recorded sessions: %w", err)
	}
	defer rows.Close()
	out := map[TranscriptRef]bool{}
	for rows.Next() {
		var ref TranscriptRef
		if err := rows.Scan(&ref.Agent, &ref.SessionID); err != nil {
			return nil, err
		}
		out[ref] = true
	}
	return out, rows.Err()
}

// attachSubagentRows adds one child per subagents row whose dispatching turn
// falls inside the run's span. The table keeps neither the dispatch's tool
// id nor its transcript's session id, so the node carries only its seq.
func attachSubagentRows(db *sql.DB, run *Run, inv workreport.Invocation) error {
	rows, err := db.Query(`
		SELECT seq FROM subagents
		WHERE agent = ? AND session_id = ? AND parent_turn_idx BETWEEN ? AND ?
		ORDER BY seq`, inv.Agent, inv.SessionID, inv.TurnIdx, inv.EndIdx)
	if err != nil {
		return fmt.Errorf("query subagents: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var seq int
		if err := rows.Scan(&seq); err != nil {
			return err
		}
		run.Root.Children = append(run.Root.Children, &Node{
			ExecutionID:       fmt.Sprintf("%s:subagent:%d", run.RunID, seq),
			RunID:             run.RunID,
			ParentExecutionID: run.Root.ExecutionID,
			Kind:              KindSubagent,
		})
	}
	return rows.Err()
}

// attachCodexChildren adds the Codex sessions whose session_meta names the
// run's session as their parent thread. That names the session, not the
// run: when the session holds several runs the child is listed unresolved
// with an ambiguous_parent diagnostic rather than placed by time.
func attachCodexChildren(db *sql.DB, run *Run, inv workreport.Invocation, runsInSession int) error {
	rows, err := db.Query(`
		SELECT agent, session_id, start_time, end_time FROM sessions
		WHERE parent_session_id = ? AND start_time IS NOT NULL
		ORDER BY start_time, session_id`, inv.SessionID)
	if err != nil {
		return fmt.Errorf("query child sessions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			ref                TranscriptRef
			startTime, endTime sql.NullString
		)
		if err := rows.Scan(&ref.Agent, &ref.SessionID, &startTime, &endTime); err != nil {
			return err
		}
		child := &Node{
			ExecutionID: fmt.Sprintf("transcript:%s:%s", ref.Agent, ref.SessionID),
			RunID:       run.RunID,
			Kind:        KindSubagent,
			Transcript:  &ref,
			StartedAt:   startTime.String,
			EndedAt:     endTime.String,
		}
		if runsInSession == 1 {
			child.ParentExecutionID = run.Root.ExecutionID
			run.Root.Children = append(run.Root.Children, child)
			continue
		}
		run.Unresolved = append(run.Unresolved, child)
		run.Diagnostics = append(run.Diagnostics, Diagnostic{
			Code:        DiagAmbiguousParent,
			Detail:      fmt.Sprintf("parent_session_id=%s runs=%d", inv.SessionID, runsInSession),
			RunID:       run.RunID,
			ExecutionID: child.ExecutionID,
		})
	}
	return rows.Err()
}

func transcriptRef(agent, sessionID string) *TranscriptRef {
	if agent == "" && sessionID == "" {
		return nil
	}
	return &TranscriptRef{Agent: agent, SessionID: sessionID}
}

func intPtr(n sql.NullInt64) *int {
	if !n.Valid {
		return nil
	}
	v := int(n.Int64)
	return &v
}

func isoOrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// inRange places a start time in [since, until); an unknown start fits only
// an unbounded range.
func inRange(t, since, until time.Time) bool {
	if since.IsZero() && until.IsZero() {
		return true
	}
	if t.IsZero() {
		return false
	}
	if !since.IsZero() && t.Before(since) {
		return false
	}
	if !until.IsZero() && !t.Before(until) {
		return false
	}
	return true
}
