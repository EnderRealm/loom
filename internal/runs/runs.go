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
//
// A recorded run that names no transcript — the Codex render cannot reach
// its own session id from a shell — is joined to the /work invocation naming
// its ticket in a session of its runtime whose span holds the run's start,
// and the Run's TranscriptBasis says so.
package runs

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"loom/internal/workreport"
)

// Origin values.
const (
	OriginRecord     = "record"
	OriginTranscript = "transcript"
)

// Kind values used by tree and report readers; recorded nodes carry the
// execution_kind their record declared.
const (
	KindRoot     = "root"
	KindSubagent = "subagent"
	KindLens     = "lens"
	KindCommand  = "command"
)

// TranscriptBasis values: how a run came by its Transcript. Empty when the
// run has none.
const (
	// BasisDeclared: the run record named it.
	BasisDeclared = "declared"
	// BasisInvocation: the record named none, and loom inferred it from a
	// /work invocation naming the run's ticket in a session of the run's
	// runtime whose span holds the run's start.
	BasisInvocation = "invocation"
	// BasisTranscript: a transcript-recognized run, whose transcript is its
	// identity.
	BasisTranscript = "transcript"
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
	// DiagAmbiguousJoin: a recorded run naming no transcript for which more
	// than one /work invocation of its runtime names its ticket and spans its
	// start. It belongs to one of them, and nothing says which; no join.
	DiagAmbiguousJoin = "ambiguous_join"
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
	DispatchIDs       []string       `json:"dispatch_ids,omitempty"`
	Stage             string         `json:"stage"`
	StageOccurrence   *int           `json:"stage_occurrence"`
	Lens              string         `json:"lens"`
	Round             *int           `json:"round"`
	Attempt           *int           `json:"attempt"`
	StartedAt         string         `json:"started_at"`
	EndedAt           string         `json:"ended_at"`
	// DurationMs is an observed dispatch duration when transcript bounds
	// are unavailable; it never supplies synthetic timestamps.
	DurationMs *int64     `json:"duration_ms,omitempty"`
	Outcome    string     `json:"outcome"`
	Source     *SourceRef `json:"source"`
	Children   []*Node    `json:"children"`
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
	RunID                string         `json:"run_id"`
	Ticket               string         `json:"ticket"`
	Runtime              string         `json:"runtime"`
	Transcript           *TranscriptRef `json:"transcript"`
	TranscriptBasis      string         `json:"transcript_basis"`
	InvocationUnresolved bool           `json:"invocation_unresolved,omitempty"`
	// Transcript recognition establishes turn identity even without timestamps.
	Invocation      *workreport.Invocation `json:"-"`
	StartedAt       string                 `json:"started_at"`
	EndedAt         string                 `json:"ended_at"`
	Outcome         string                 `json:"outcome"`
	ReportingCutoff string                 `json:"reporting_cutoff"`
	Producer        string                 `json:"producer"`
	Origin          string                 `json:"origin"`
	Source          *SourceRef             `json:"source"`
	Root            *Node                  `json:"root"`
	Unresolved      []*Node                `json:"unresolved"`
	Diagnostics     []Diagnostic           `json:"diagnostics"`
	// Lenses is the run's review attempts (workreport.LensAttempt), read from
	// its transcript's lens responses. Null for a run with no transcript, and
	// for a recorded run whose session holds no /work invocation spanning
	// its start.
	Lenses []workreport.LensAttempt `json:"lenses"`
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
		invocations, err := workreport.Invocations(db)
		if err != nil {
			return nil, err
		}
		return buildRecorded(db, *row, invocations)
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
	invocations, err := workreport.Invocations(db)
	if err != nil {
		return nil, err
	}
	historical, err := loadHistorical(db, invocations, time.Time{}, time.Time{})
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
	invocations, err := workreport.Invocations(db)
	if err != nil {
		return nil, err
	}
	var out []Run
	for _, row := range recorded {
		run, err := buildRecorded(db, row, invocations)
		if err != nil {
			return nil, err
		}
		out = append(out, *run)
	}

	historical, err := loadHistorical(db, invocations, since, until)
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

// buildRecorded assembles a declared run. Its lens attempts come from the
// /work invocation recognized in its transcript: the only one in the session,
// or with several, the one whose span holds the run's start. A record naming
// no transcript takes the one invocation inferJoin finds, and its root — a
// record that named none either — takes the same reference.
func buildRecorded(db *sql.DB, row runRow, invocations []workreport.Invocation) (*Run, error) {
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
	if run.Transcript != nil {
		run.TranscriptBasis = BasisDeclared
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
	if run.Transcript == nil {
		run.Transcript = declaredRootTranscript(nodes)
		if run.Transcript != nil {
			run.TranscriptBasis = BasisDeclared
		}
	}
	var inv workreport.Invocation
	var ok bool
	if run.Transcript != nil {
		inv, ok = SpanningInvocation(invocations, run.Transcript.Agent, run.Transcript.SessionID, parseTime(row.startedAt))
	} else {
		var n int
		inv, n = inferJoin(invocations, row)
		ok = n == 1
		switch {
		case ok:
			ref := TranscriptRef{Agent: inv.Agent, SessionID: inv.SessionID}
			run.Transcript = &ref
			run.TranscriptBasis = BasisInvocation
			if run.Root != nil && run.Root.Transcript == nil {
				r := ref
				run.Root.Transcript = &r
			}
		case n > 1:
			d := Diagnostic{
				Code:   DiagAmbiguousJoin,
				Detail: fmt.Sprintf("runtime=%s ticket=%s invocations=%d", row.runtime, row.ticket, n),
				RunID:  row.runID,
			}
			if run.Root != nil {
				d.ExecutionID = run.Root.ExecutionID
			}
			run.Diagnostics = append(run.Diagnostics, d)
		}
	}
	if ok {
		run.Lenses, err = workreport.Lenses(db, inv, lensExecutions(nodes))
		if err != nil {
			return nil, err
		}
	}
	if run.Root != nil && run.Root.Transcript != nil {
		ref := *run.Root.Transcript
		if _, matched := SpanningInvocation(invocations, ref.Agent, ref.SessionID, parseTime(row.startedAt)); !matched {
			count := 0
			for _, candidate := range invocations {
				if candidate.Agent == ref.Agent && candidate.SessionID == ref.SessionID {
					count++
				}
			}
			if count > 0 {
				run.InvocationUnresolved = true
				run.Diagnostics = append(run.Diagnostics, Diagnostic{
					Code: DiagAmbiguousJoin, RunID: run.RunID, ExecutionID: run.Root.ExecutionID,
					Detail: fmt.Sprintf("invocation attribution unresolved: session=%s/%s invocations=%d", ref.Agent, ref.SessionID, count),
				})
			}
		}
	}
	return run, nil
}

// lensExecutions is what the attempt model joins: the run's lens records in
// the order they were loaded.
func lensExecutions(nodes []*Node) []workreport.LensExecution {
	var out []workreport.LensExecution
	for _, n := range nodes {
		if n.Kind != KindLens {
			continue
		}
		out = append(out, workreport.LensExecution{
			ExecutionID: n.ExecutionID,
			Lens:        n.Lens,
			Round:       intOf(n.Round),
			Attempt:     intOf(n.Attempt),
			DispatchID:  n.DispatchID,
			Agent:       refAgent(n.Transcript),
			SessionID:   refSession(n.Transcript),
			StartedAt:   parseTime(n.StartedAt),
		})
	}
	return out
}

func refAgent(ref *TranscriptRef) string {
	if ref == nil {
		return ""
	}
	return ref.Agent
}

func refSession(ref *TranscriptRef) string {
	if ref == nil {
		return ""
	}
	return ref.SessionID
}

// Match attach's root choice before inferring identity from ticket and time.
func declaredRootTranscript(nodes []*Node) *TranscriptRef {
	for _, n := range nodes {
		if n.ParentExecutionID == "" && n.Kind == KindRoot {
			return n.Transcript
		}
	}
	return nil
}

// inferJoin finds the /work invocation a record naming no transcript belongs
// to: same runtime, same ticket, both named, and a span holding the run's
// start. It returns the last match and how many there were; only a count of
// one is a join. A row with no start cannot be placed in any span.
func inferJoin(invocations []workreport.Invocation, row runRow) (workreport.Invocation, int) {
	startedAt := parseTime(row.startedAt)
	if row.agent != "" || row.sessionID != "" || row.runtime == "" || row.ticket == "" || startedAt.IsZero() {
		return workreport.Invocation{}, 0
	}
	var match workreport.Invocation
	n := 0
	for _, inv := range invocations {
		if inv.Agent != row.runtime || inv.Ticket != row.ticket || !invocationContainsTime(inv, startedAt) {
			continue
		}
		match = inv
		n++
	}
	return match, n
}

// SpanningInvocation picks the invocation a recorded run's lens attempts are
// read from: the only one in the session, or with several, the one whose span
// holds startedAt. None matching is not a diagnostic: the run's transcript
// simply holds no recognized /work invocation to read them under. Exported
// for internal/runreport, whose parent-only span is the same invocation.
func SpanningInvocation(invocations []workreport.Invocation, agent, sessionID string, startedAt time.Time) (workreport.Invocation, bool) {
	var inSession []workreport.Invocation
	for _, inv := range invocations {
		if inv.Agent == agent && inv.SessionID == sessionID {
			inSession = append(inSession, inv)
		}
	}
	if len(inSession) == 1 {
		return inSession[0], true
	}
	var match workreport.Invocation
	n := 0
	for _, inv := range inSession {
		if invocationContainsTime(inv, startedAt) {
			match = inv
			n++
		}
	}
	return match, n == 1
}

func invocationContainsTime(inv workreport.Invocation, at time.Time) bool {
	if at.IsZero() || inv.StartedAt.IsZero() || at.Before(inv.StartedAt) {
		return false
	}
	if inv.EndsAt.IsZero() {
		// A missing next-invocation timestamp is an unknown boundary;
		// only the final invocation can have an open-ended time span.
		return inv.EndIdx == math.MaxInt
	}
	return at.Before(inv.EndsAt)
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
func loadHistorical(db *sql.DB, invocations []workreport.Invocation, since, until time.Time) ([]Run, error) {
	recorded, err := recordedSessions(db, invocations)
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
		if recorded[invocationKey(inv)] || !inRange(inv.StartedAt, since, until) {
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
			RunID:           id,
			Ticket:          inv.Ticket,
			Runtime:         inv.Agent,
			Transcript:      &ref,
			TranscriptBasis: BasisTranscript,
			Invocation:      &inv,
			StartedAt:       root.StartedAt,
			EndedAt:         root.EndedAt,
			Origin:          OriginTranscript,
			Root:            root,
		}
		if err := attachSubagentRows(db, &run, inv); err != nil {
			return nil, err
		}
		if err := attachCodexChildren(db, &run, inv, perSession[ref]); err != nil {
			return nil, err
		}
		run.Lenses, err = workreport.Lenses(db, inv, nil)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, nil
}

// recordedSessions is the set of invocations claimed by run or root identity,
// else by an unambiguous ticket/time join. Only that invocation is suppressed.
func recordedSessions(db *sql.DB, invocations []workreport.Invocation) (map[string]bool, error) {
	rows, err := loadRunRows(db, time.Time{}, time.Time{})
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, row := range rows {
		ref := transcriptRef(row.agent, row.sessionID)
		if ref == nil {
			nodes, err := loadExecutions(db, row.runID)
			if err != nil {
				return nil, err
			}
			ref = declaredRootTranscript(nodes)
		}
		if ref != nil {
			if inv, ok := SpanningInvocation(invocations, ref.Agent, ref.SessionID, parseTime(row.startedAt)); ok {
				out[invocationKey(inv)] = true
			}
			continue
		}
		if inv, n := inferJoin(invocations, row); n == 1 {
			out[invocationKey(inv)] = true
		}
	}
	return out, nil
}

func invocationKey(inv workreport.Invocation) string {
	return fmt.Sprintf("%s\x00%s\x00%d", inv.Agent, inv.SessionID, inv.TurnIdx)
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

// attachCodexChildren adds sessions whose metadata names a parent thread.
// Cursor also supplies dispatch identities, so each edge can be resolved
// against its immediate parent and descendants followed transitively.
func attachCodexChildren(db *sql.DB, run *Run, inv workreport.Invocation, runsInSession int) error {
	dispatchColumn := "NULL"
	if workreport.SchemaVersionOf(db) >= 11 {
		dispatchColumn = "parent_tool_call_id"
	}
	queue := []*Node{run.Root}
	seen := map[TranscriptRef]bool{*run.Root.Transcript: true}
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		if err := attachChildSessions(db, run, inv, runsInSession, dispatchColumn, parent, seen, &queue); err != nil {
			return err
		}
	}
	return nil
}

func attachChildSessions(db *sql.DB, run *Run, inv workreport.Invocation, runsInSession int, dispatchColumn string, parent *Node, seen map[TranscriptRef]bool, queue *[]*Node) error {
	type childRow struct {
		ref                  TranscriptRef
		start, end, dispatch sql.NullString
		resolved             bool
		turnIdx              int
		durationMs           *int64
		dispatchIDs          []string
	}
	var children []childRow
	if parent.Transcript.Agent == "cursor-cli" && dispatchColumn != "NULL" {
		cursorChildren, err := workreport.CursorChildren(db, parent.Transcript.SessionID)
		if err != nil {
			return fmt.Errorf("query Cursor children: %w", err)
		}
		for _, c := range cursorChildren {
			children = append(children, childRow{
				ref:   TranscriptRef{Agent: "cursor-cli", SessionID: c.SessionID},
				start: sql.NullString{String: c.StartedAt}, end: sql.NullString{String: c.EndedAt},
				dispatch: sql.NullString{String: c.DispatchID}, resolved: c.Resolved, turnIdx: c.TurnIdx, durationMs: c.DurationMs,
				dispatchIDs: c.DispatchIDs,
			})
		}
	} else {
		rows, err := db.Query(`
		SELECT agent, session_id, start_time, end_time, `+dispatchColumn+` FROM sessions
		WHERE parent_session_id = ? AND (start_time IS NOT NULL OR agent = 'cursor-cli')
		AND (agent <> 'cursor-cli' OR ? = 'cursor-cli')
		ORDER BY start_time, session_id`, parent.Transcript.SessionID, parent.Transcript.Agent)
		if err != nil {
			return fmt.Errorf("query child sessions: %w", err)
		}
		for rows.Next() {
			var c childRow
			if err := rows.Scan(&c.ref.Agent, &c.ref.SessionID, &c.start, &c.end, &c.dispatch); err != nil {
				rows.Close()
				return err
			}
			children = append(children, c)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	for _, c := range children {
		ref := c.ref
		if seen[ref] {
			run.Diagnostics = append(run.Diagnostics, Diagnostic{
				Code: DiagCyclicParent, Detail: "parent_session_id=" + parent.Transcript.SessionID,
				RunID: run.RunID, ExecutionID: fmt.Sprintf("transcript:%s:%s", ref.Agent, ref.SessionID),
			})
			continue
		}
		child := &Node{
			ExecutionID: fmt.Sprintf("transcript:%s:%s", ref.Agent, ref.SessionID),
			RunID:       run.RunID,
			Kind:        KindSubagent,
			Transcript:  &ref,
			StartedAt:   c.start.String,
			EndedAt:     c.end.String,
			DispatchID:  c.dispatch.String,
			DispatchIDs: c.dispatchIDs,
			DurationMs:  c.durationMs,
		}
		placed := runsInSession == 1
		if ref.Agent == "cursor-cli" {
			placed = c.resolved
			if placed && parent == run.Root && (c.turnIdx < inv.TurnIdx || c.turnIdx > inv.EndIdx) {
				continue
			}
		}
		seen[ref] = true
		if ref.Agent == "cursor-cli" {
			*queue = append(*queue, child)
		}
		if placed {
			child.ParentExecutionID = parent.ExecutionID
			parent.Children = append(parent.Children, child)
			continue
		}
		run.Unresolved = append(run.Unresolved, child)
		run.Diagnostics = append(run.Diagnostics, Diagnostic{
			Code:        DiagAmbiguousParent,
			Detail:      fmt.Sprintf("parent_session_id=%s dispatch_id=%s runs=%d", parent.Transcript.SessionID, c.dispatch.String, runsInSession),
			RunID:       run.RunID,
			ExecutionID: child.ExecutionID,
		})
	}
	return nil
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

func intOf(p *int) int {
	if p == nil {
		return 0
	}
	return *p
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
