// Package runreport measures everything attributable to one run: the tree
// internal/runs reads back, each execution's transcript metered the way
// internal/workreport meters a cost span, and the sum of it all. It is the
// read surface for `loom run-report --run <id>`.
//
// Three rules keep the totals defensible. Every transcript is counted once,
// however many executions name it — the root's parent-only span is its /work
// invocation's turn range, and a stage node sharing the root's session is
// listed but not counted again. Outcome comes from the run record alone and
// telemetry completeness is reported beside it, never inferred from it. And
// whatever cannot be measured is named as a gap rather than reported as zero:
// a session not in the database, an execution still pending, a dispatch with
// no usage, a model with no rate.
package runreport

import (
	"database/sql"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"loom/internal/pricing"
	"loom/internal/runs"
	"loom/internal/workreport"

	_ "modernc.org/sqlite"
)

// schemaVersion is the summaries.db schema this report reads: the execution
// tables arrived in 8 and the lens attempts it joins against in 9.
const schemaVersion = 9

// Outcome values the report adds to the record's completed|failed|stopped.
const (
	// OutcomeRunning: a record declared the run's start and nothing has
	// declared its end.
	OutcomeRunning = "running"
	// OutcomeUnknown: no record says — a transcript-recognized run, a run
	// known only from the executions naming it, or a record that ended
	// without saying how.
	OutcomeUnknown = "unknown"
)

// Telemetry states.
const (
	StateComplete = "complete"
	StatePartial  = "partial"
)

// Span basis values: what the root's parent-only span is bounded by.
const (
	SpanInvocation = "invocation"
	SpanSession    = "session"
)

// Placement values of an execution in the report.
const (
	PlacementTree       = "tree"
	PlacementUnresolved = "unresolved"
)

// Wall-clock basis values.
const (
	WallEnded        = "started_at→ended_at"
	WallLastObserved = "started_at→last_observed_at"
)

// Cache semantics labels. Claude records input tokens exclusive of the cache
// buckets; Codex records cached_input_tokens as a subset of input_tokens. A
// total that summed both the same way would count a Codex cache read twice.
const (
	CacheSeparate        = "cache_separate"
	CacheReadInsideInput = "cache_read_included_in_input"
	CacheUnknown         = "unknown"
)

// LegacyActiveMsSemantics is the fixed label beside every legacy_active_ms.
const LegacyActiveMsSemantics = "turn wall clock plus tool durations; terms overlap, upper bound on attention, not exclusive activity"

// Error sources the parsers emit (internal/parse/claudeparse, codexparse)
// and the failure class each one is. stop_hook is a hook telling the agent
// to keep going, not a failure, and is counted apart.
const (
	sourceAPIError    = "api_error"
	sourceToolError   = "tool_error"
	sourceExecError   = "exec_error"
	sourcePatchError  = "patch_error"
	sourceTurnAborted = "turn_aborted"
	sourceStopHook    = "stop_hook"
)

// unknownToolKind keys tool calls whose recorded kind is empty, as cost.go
// does, so the per-kind breakdown never emits an empty JSON key.
const unknownToolKind = "unknown"

// Report is the whole document. Every list is [] when empty; a pointer is
// null only where "not recorded" has to differ from zero.
type Report struct {
	Run         RunInfo            `json:"run"`
	Telemetry   Telemetry          `json:"telemetry"`
	Time        Time               `json:"time"`
	Metrics     Scopes             `json:"metrics"`
	Executions  []ExecutionMetrics `json:"executions"`
	Stages      []Stage            `json:"stages"`
	Lenses      []LensGroup        `json:"lenses"`
	Attempts    []Attempt          `json:"attempts"`
	Tree        *runs.Node         `json:"tree"`
	Unresolved  []*runs.Node       `json:"unresolved"`
	Diagnostics []runs.Diagnostic  `json:"diagnostics"`
}

// RunInfo is the run's identity as the record (or the transcript) declared
// it. Outcome is the record's word or running/unknown — see the Outcome
// constants — and never a reading of the telemetry. LastObservedAt is the
// latest timestamp anything attributable to the run carried.
type RunInfo struct {
	RunID           string              `json:"run_id"`
	Ticket          string              `json:"ticket"`
	Runtime         string              `json:"runtime"`
	Origin          string              `json:"origin"`
	Producer        string              `json:"producer"`
	Transcript      *runs.TranscriptRef `json:"transcript"`
	StartedAt       string              `json:"started_at"`
	EndedAt         string              `json:"ended_at"`
	ReportingCutoff string              `json:"reporting_cutoff"`
	Outcome         string              `json:"outcome"`
	LastObservedAt  string              `json:"last_observed_at"`
	Source          *runs.SourceRef     `json:"source"`
}

// Telemetry is how complete the evidence is, apart from how the run ended.
// State is complete when every execution has ended and every one naming a
// transcript has that session in the database; Gaps names each reason it is
// not. RootSpan says whether the root's parent-only span is its /work
// invocation's turn range or, with no invocation recognized, the whole
// session; empty when the root names no transcript.
type Telemetry struct {
	State                       string   `json:"state"`
	Gaps                        []string `json:"gaps"`
	RootSpan                    string   `json:"root_span"`
	ExecutionsTotal             int      `json:"executions_total"`
	ExecutionsWithTranscript    int      `json:"executions_with_transcript"`
	ExecutionsWithoutTranscript []string `json:"executions_without_transcript"`
	ExecutionsPending           []string `json:"executions_pending"`
	Unresolved                  int      `json:"unresolved"`
	Diagnostics                 int      `json:"diagnostics"`
	TranscriptsCounted          int      `json:"transcripts_counted"`
}

// Time is the run's elapsed time under each definition. WallMs is one span,
// start to end (or to the last observation, for a run still going);
// ExecutionTimeMs is the sum of every counted execution's own span, so two
// children running in parallel add where the wall does not. ToolTimeMs sums
// tool durations over the total scope, root and counted descendants alike.
// LegacyActiveMs is the parent span's alone, so it matches cost-report's
// active_ms for the same run, and carries its semantics beside it; the
// per-scope figures, including the descendants' and the total, live under
// Metrics.
type Time struct {
	WallMs                  *int64 `json:"wall_ms"`
	WallBasis               string `json:"wall_basis"`
	ExecutionTimeMs         int64  `json:"execution_time_ms"`
	ToolTimeMs              int64  `json:"tool_time_ms"`
	LegacyActiveMs          int64  `json:"legacy_active_ms"`
	LegacyActiveMsSemantics string `json:"legacy_active_ms_semantics"`
}

// Scopes separates what the root did itself from what ran under it. Total is
// recomputed over the counted set, not summed from the other two.
type Scopes struct {
	Parent      Metrics `json:"parent"`
	Descendants Metrics `json:"descendants"`
	Total       Metrics `json:"total"`
}

// Metrics is one scope's measure: a root's span, a set of executions, or one
// execution.
type Metrics struct {
	// Executions is the executions contributing; Transcripts the distinct
	// sessions metered for them.
	Executions      int            `json:"executions"`
	Transcripts     int            `json:"transcripts"`
	Turns           int            `json:"turns"`
	ToolCalls       int            `json:"tool_calls"`
	ToolCallsByKind map[string]int `json:"tool_calls_by_kind"`
	// ToolCallsErrored is the tool_calls rows flagged is_error. On Claude the
	// same event is also an error row of source tool_error, counted under
	// Failures.Tool; the two are different tables' views of it, not a sum.
	ToolCallsErrored int   `json:"tool_calls_errored"`
	ToolTimeMs       int64 `json:"tool_time_ms"`
	// TokensByRuntime keeps each runtime's buckets under its own cache
	// semantics; TotalTokens sums each runtime's non-double-counted total.
	TokensByRuntime map[string]*Tokens `json:"tokens_by_runtime"`
	TotalTokens     int64              `json:"total_tokens"`
	Failures        Failures           `json:"failures"`
	// HookSignals is the stop_hook rows: a hook told the agent to continue.
	// Soft, and not a failure.
	HookSignals int `json:"hook_signals"`
	// HumanInteractions is the root's by construction: a descendant's user
	// messages are the dispatching agent's prompts, so descendants report 0
	// and total equals parent.
	HumanInteractions int `json:"human_interactions"`
	// Models, Efforts and CLIVersions are the distinct values the metered
	// turns carried, in first-seen order. ConditionsCoverage says how many
	// turns carried each, so an empty list reads as "not recorded" only when
	// its coverage is 0 of n.
	Models             []string           `json:"models"`
	Efforts            []string           `json:"efforts"`
	CLIVersions        []string           `json:"cli_versions"`
	ConditionsCoverage ConditionsCoverage `json:"conditions_coverage"`
	// ExecutionTimeMs sums each execution's own started_at→ended_at (or a
	// historical dispatch's duration_ms); ExecutionTimeCoverage counts the
	// executions that carried both bounds and those that did not.
	ExecutionTimeMs         int64    `json:"execution_time_ms"`
	ExecutionTimeCoverage   Coverage `json:"execution_time_coverage"`
	LegacyActiveMs          int64    `json:"legacy_active_ms"`
	LegacyActiveMsSemantics string   `json:"legacy_active_ms_semantics"`
	// CostUSD is null when anything in the scope could not be priced, and
	// PricingWarnings then names every cause. Nothing is priced at a default.
	CostUSD         *float64 `json:"cost_usd"`
	PricingWarnings []string `json:"pricing_warnings"`
	Pricing         Pricing  `json:"pricing"`
}

// Tokens is one runtime's usage. Total is input+output+cache_read+cache_write
// under cache_separate and input+output under cache_read_included_in_input.
type Tokens struct {
	Input          int64  `json:"input"`
	Output         int64  `json:"output"`
	CacheRead      int64  `json:"cache_read"`
	CacheWrite     int64  `json:"cache_write"`
	CacheWrite1h   int64  `json:"cache_write_1h"`
	CacheSemantics string `json:"cache_semantics"`
	Total          int64  `json:"total"`
}

// Failures is the error rows by class. Tool is tool_error, exec_error and
// patch_error; API is api_error; Process is turn_aborted; Other is every
// source the parsers do not emit today, listed by source.
type Failures struct {
	Tool          int            `json:"tool"`
	API           int            `json:"api"`
	Process       int            `json:"process"`
	Other         int            `json:"other"`
	OtherBySource map[string]int `json:"other_by_source"`
}

// ConditionsCoverage is how many metered turns recorded each condition.
type ConditionsCoverage struct {
	TurnsWithModel      int `json:"turns_with_model"`
	TurnsWithEffort     int `json:"turns_with_effort"`
	TurnsWithCLIVersion int `json:"turns_with_cli_version"`
	Turns               int `json:"turns"`
}

// Coverage counts what carried a measure and what did not.
type Coverage struct {
	Timed   int `json:"timed"`
	Untimed int `json:"untimed"`
}

// Pricing names the rate table the scope was priced from and whether the
// price is available. A missing rate leaves every other metric standing.
type Pricing struct {
	Currency  string `json:"currency"`
	Source    string `json:"source"`
	Checked   string `json:"checked"`
	Available bool   `json:"available"`
}

// ExecutionMetrics is one execution, in the tree or unresolved, with its own
// transcript's measure. Counted is false when its transcript was already
// counted by another execution (CountedBy names it) or when its attribution
// to the run is what is unresolved (CountedBy empty, and a telemetry gap
// names it); Metrics is its own span either way. A node whose transcript is
// another counted execution's has no invocation range bounding it, so it
// reports that whole session as its own metrics.
type ExecutionMetrics struct {
	ExecutionID       string              `json:"execution_id"`
	ParentExecutionID string              `json:"parent_execution_id"`
	Kind              string              `json:"kind"`
	Stage             string              `json:"stage"`
	StageOccurrence   *int                `json:"stage_occurrence"`
	Lens              string              `json:"lens"`
	Round             *int                `json:"round"`
	Attempt           *int                `json:"attempt"`
	Transcript        *runs.TranscriptRef `json:"transcript"`
	AgentType         string              `json:"agent_type"`
	StartedAt         string              `json:"started_at"`
	EndedAt           string              `json:"ended_at"`
	Outcome           string              `json:"outcome"`
	Placement         string              `json:"placement"`
	Counted           bool                `json:"counted"`
	CountedBy         string              `json:"counted_by"`
	DurationMs        *int64              `json:"duration_ms"`
	Metrics           Metrics             `json:"metrics"`
}

// Stage is one (stage, occurrence) with every attempt at it. Retries is the
// attempts past the first.
type Stage struct {
	Stage      string         `json:"stage"`
	Occurrence int            `json:"occurrence"`
	Attempts   []StageAttempt `json:"attempts"`
	Retries    int            `json:"retries"`
}

// StageAttempt is one stage execution.
type StageAttempt struct {
	Attempt     int     `json:"attempt"`
	ExecutionID string  `json:"execution_id"`
	Outcome     string  `json:"outcome"`
	DurationMs  *int64  `json:"duration_ms"`
	Metrics     Metrics `json:"metrics"`
}

// LensGroup is one (lens, round) with every attempt at it, joining the
// execution records of kind lens with the run's LensAttempt model.
type LensGroup struct {
	Lens     string        `json:"lens"`
	Round    int           `json:"round"`
	Attempts []LensAttempt `json:"attempts"`
	Retries  int           `json:"retries"`
}

// LensAttempt is one attempt from either side of the join: ExecutionID is
// empty and Metrics null with no execution record; Recorded is false with no
// LensAttempt in the transcript, and the status fields are then empty.
type LensAttempt struct {
	Attempt      int      `json:"attempt"`
	ExecutionID  string   `json:"execution_id"`
	Outcome      string   `json:"outcome"`
	DurationMs   *int64   `json:"duration_ms"`
	Metrics      *Metrics `json:"metrics"`
	Recorded     bool     `json:"recorded"`
	Status       string   `json:"status"`
	Verdict      string   `json:"verdict"`
	ContextState string   `json:"context_state"`
	Contaminated bool     `json:"contaminated"`
	Superseded   bool     `json:"superseded"`
	Late         bool     `json:"late"`
	Malformed    string   `json:"malformed"`
}

// Attempt is the flat retry view: every stage and lens attempt. Key is
// <stage>/<occurrence> or <lens>/<round>.
type Attempt struct {
	Kind        string `json:"kind"`
	Key         string `json:"key"`
	Attempt     int    `json:"attempt"`
	ExecutionID string `json:"execution_id"`
	Outcome     string `json:"outcome"`
	Status      string `json:"status"`
}

// Load opens dbPath read-only and reports the run.
func Load(dbPath, runID string) (*Report, error) {
	db, table, err := open(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	run, err := runs.Load(db, runID)
	if err != nil {
		return nil, err
	}
	return Build(db, run, table)
}

// open is the one way this package reads a database: read-only, at a schema
// that holds the execution and lens tables, priced from the default table.
func open(dbPath string) (*sql.DB, *pricing.Table, error) {
	if _, err := os.Stat(dbPath); err != nil {
		return nil, nil, fmt.Errorf("summaries.db not found at %s — run `loom summarize`", dbPath)
	}
	// mode=ro keeps us out of the summarizer's way; it holds the only writer.
	dsn := fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(2000)", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("open summaries.db: %w", err)
	}
	if v := workreport.SchemaVersionOf(db); v < schemaVersion {
		db.Close()
		return nil, nil, fmt.Errorf("summaries.db is at schema %d and predates the execution and lens tables (want %d) — run `loom summarize --rebuild`", v, schemaVersion)
	}
	table, err := pricing.Default()
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	return db, table, nil
}

// unit is one execution with whatever evidence there is to meter it: its
// transcript's rows over a turn range, or a historical dispatch's subagents
// row, or nothing.
type unit struct {
	node      *runs.Node
	placement string
	// attributed is false for an unresolved execution nothing declared
	// under this run: listed and metered, added to no scope.
	attributed bool
	// counted is false when another execution (countedBy) already counts
	// this one's transcript.
	counted   bool
	countedBy string
	data      *sessionData
	startIdx  int
	endIdx    int
	// subagent is a historical dispatch's row off the parent transcript,
	// whose agent is the runtime its usage is recorded under.
	subagent *subagentRow
	agent    string
}

// Build measures a loaded run against table. The run's tree is normalized in
// place (nil Children, Unresolved and Diagnostics become empty slices) and the
// Report aliases the run's nodes rather than copying them.
func Build(db *sql.DB, run *runs.Run, table *pricing.Table) (*Report, error) {
	b := &builder{
		db:       db,
		run:      run,
		table:    table,
		at:       parseTime(run.StartedAt),
		sessions: map[runs.TranscriptRef]*sessionData{},
	}
	if err := b.collect(); err != nil {
		return nil, err
	}
	return b.report(), nil
}

type builder struct {
	db       *sql.DB
	run      *runs.Run
	table    *pricing.Table
	at       time.Time
	sessions map[runs.TranscriptRef]*sessionData
	units    []*unit
	rootSpan string
	gaps     []string
}

// collect walks the tree depth-first, root first, then each unresolved
// chain, deciding for every execution what it contributes.
func (b *builder) collect() error {
	if b.run.Root != nil {
		if err := b.walk(b.run.Root, PlacementTree); err != nil {
			return err
		}
	}
	for _, n := range b.run.Unresolved {
		if err := b.walk(n, PlacementUnresolved); err != nil {
			return err
		}
	}
	return nil
}

func (b *builder) walk(n *runs.Node, placement string) error {
	u, err := b.unitOf(n, placement)
	if err != nil {
		return err
	}
	b.units = append(b.units, u)
	for _, c := range n.Children {
		if err := b.walk(c, placement); err != nil {
			return err
		}
	}
	return nil
}

// unitOf decides one execution's evidence. The root's span is its /work
// invocation's turn range when one spans the run's start, else the whole
// session; every other transcript is its whole session, counted once — the
// first execution naming it in walk order counts it. A synthesized
// unresolved node is one whose attribution to this run the evidence could
// not settle, so it is listed and metered but not counted.
func (b *builder) unitOf(n *runs.Node, placement string) (*unit, error) {
	u := &unit{node: n, placement: placement, attributed: true, counted: true, startIdx: math.MinInt, endIdx: math.MaxInt}
	if placement == PlacementUnresolved && n.Source == nil {
		u.attributed, u.counted = false, false
		b.gap(fmt.Sprintf("execution %s is unresolved; not counted", n.ExecutionID))
	}
	if seq, ok := b.historicalSubagent(n); ok {
		row, err := loadSubagentRow(b.db, *b.run.Root.Transcript, seq)
		if err != nil {
			return nil, err
		}
		u.subagent, u.agent = row, b.run.Root.Transcript.Agent
		if row == nil || !row.inputTokens.Valid {
			b.gap(fmt.Sprintf("execution %s has no transcript usage", n.ExecutionID))
		}
		return u, nil
	}
	if n.Transcript == nil {
		return u, nil
	}
	ref := *n.Transcript
	if n == b.run.Root {
		invocations, err := workreport.Invocations(b.db)
		if err != nil {
			return nil, err
		}
		b.rootSpan = SpanSession
		if inv, ok := runs.SpanningInvocation(invocations, ref.Agent, ref.SessionID, b.at); ok {
			u.startIdx, u.endIdx = inv.TurnIdx, inv.EndIdx
			b.rootSpan = SpanInvocation
		}
	} else if by := b.counter(ref); by != "" {
		u.counted, u.countedBy = false, by
	}
	data, err := b.session(ref)
	if err != nil {
		return nil, err
	}
	if data == nil {
		if u.counted {
			b.gap(fmt.Sprintf("session %s/%s not in summaries.db", ref.Agent, ref.SessionID))
		}
		return u, nil
	}
	u.data = data
	return u, nil
}

// historicalSubagent reads the seq out of a transcript-recognized run's
// `<run>:subagent:<seq>` child.
func (b *builder) historicalSubagent(n *runs.Node) (int, bool) {
	prefix := b.run.RunID + ":subagent:"
	if b.run.Origin != runs.OriginTranscript || n.Transcript != nil || !strings.HasPrefix(n.ExecutionID, prefix) {
		return 0, false
	}
	seq, err := strconv.Atoi(strings.TrimPrefix(n.ExecutionID, prefix))
	if err != nil || b.run.Root == nil || b.run.Root.Transcript == nil {
		return 0, false
	}
	return seq, true
}

// counter is the execution that already counts ref, or "".
func (b *builder) counter(ref runs.TranscriptRef) string {
	for _, u := range b.units {
		if u.counted && u.node.Transcript != nil && *u.node.Transcript == ref {
			return u.node.ExecutionID
		}
	}
	return ""
}

func (b *builder) session(ref runs.TranscriptRef) (*sessionData, error) {
	if data, ok := b.sessions[ref]; ok {
		return data, nil
	}
	data, err := loadSession(b.db, ref.Agent, ref.SessionID)
	if err != nil {
		return nil, err
	}
	b.sessions[ref] = data
	return data, nil
}

func (b *builder) gap(msg string) {
	for _, have := range b.gaps {
		if have == msg {
			return
		}
	}
	b.gaps = append(b.gaps, msg)
}

func (b *builder) report() *Report {
	run := b.run
	parent, descendants, total := b.newMeter(), b.newMeter(), b.newMeter()
	last := parseTime(run.EndedAt)
	var pending, noTranscript []string
	withTranscript := 0

	rep := &Report{
		Executions:  []ExecutionMetrics{},
		Stages:      []Stage{},
		Lenses:      []LensGroup{},
		Attempts:    []Attempt{},
		Unresolved:  []*runs.Node{},
		Diagnostics: []runs.Diagnostic{},
	}
	for _, u := range b.units {
		n := u.node
		if n.Transcript != nil {
			withTranscript++
		} else {
			noTranscript = append(noTranscript, n.ExecutionID)
		}
		// A recorded execution is pending until a record closes it with an
		// outcome. A historical dispatch's row is never pending: its NULL
		// duration means the dispatch was not measured, not that it is
		// still running.
		switch {
		case u.subagent != nil:
			if !u.subagent.durationMs.Valid {
				b.gap(fmt.Sprintf("execution %s duration unmeasured", n.ExecutionID))
			}
		case n.EndedAt == "" || (n.Source != nil && n.Outcome == ""):
			pending = append(pending, n.ExecutionID)
			b.gap(fmt.Sprintf("execution %s still pending", n.ExecutionID))
		}

		own := b.newMeter()
		own.add(u, true)
		if u.attributed {
			total.add(u, u.counted)
			if n == run.Root {
				parent.add(u, u.counted)
			} else {
				descendants.add(u, u.counted)
			}
		}

		em := ExecutionMetrics{
			ExecutionID:       n.ExecutionID,
			ParentExecutionID: n.ParentExecutionID,
			Kind:              n.Kind,
			Stage:             n.Stage,
			StageOccurrence:   n.StageOccurrence,
			Lens:              n.Lens,
			Round:             n.Round,
			Attempt:           n.Attempt,
			Transcript:        n.Transcript,
			StartedAt:         n.StartedAt,
			EndedAt:           n.EndedAt,
			Outcome:           n.Outcome,
			Placement:         u.placement,
			Counted:           u.counted,
			CountedBy:         u.countedBy,
			DurationMs:        durationOf(u),
			Metrics:           own.metrics(),
		}
		if u.subagent != nil {
			em.AgentType = u.subagent.agentType
		}
		// One entry per unit, in walk order: stages() and lenses() rely on
		// rep.Executions[i] corresponding to b.units[i].
		rep.Executions = append(rep.Executions, em)
	}
	// The own meter of an uncounted execution walks its whole session, which
	// holds rows belonging to other runs; only the total scope observes what
	// is attributable to this run.
	last = latest(last, total.lastObserved)

	rep.Metrics = Scopes{Parent: parent.metrics(), Descendants: descendants.metrics(), Total: total.metrics()}
	rep.Run = RunInfo{
		RunID:           run.RunID,
		Ticket:          run.Ticket,
		Runtime:         run.Runtime,
		Origin:          run.Origin,
		Producer:        run.Producer,
		Transcript:      run.Transcript,
		StartedAt:       run.StartedAt,
		EndedAt:         run.EndedAt,
		ReportingCutoff: run.ReportingCutoff,
		Outcome:         outcomeOf(run),
		LastObservedAt:  isoOrEmpty(last),
		Source:          run.Source,
	}
	rep.Time = b.time(last, rep.Metrics)
	rep.Telemetry = Telemetry{
		State:                       StateComplete,
		Gaps:                        emptyList(b.gaps),
		RootSpan:                    b.rootSpan,
		ExecutionsTotal:             len(b.units),
		ExecutionsWithTranscript:    withTranscript,
		ExecutionsWithoutTranscript: emptyList(noTranscript),
		ExecutionsPending:           emptyList(pending),
		Unresolved:                  len(run.Unresolved),
		Diagnostics:                 len(run.Diagnostics),
		TranscriptsCounted:          rep.Metrics.Total.Transcripts,
	}
	if len(b.gaps) > 0 {
		rep.Telemetry.State = StatePartial
	}
	b.stages(rep)
	b.lenses(rep)

	rep.Tree = run.Root
	if run.Root != nil {
		normalize(run.Root)
	}
	for _, n := range run.Unresolved {
		normalize(n)
		rep.Unresolved = append(rep.Unresolved, n)
	}
	rep.Diagnostics = append(rep.Diagnostics, run.Diagnostics...)
	return rep
}

// time derives the wall clock: the record's own span when it has ended, else
// start to the last observation for a run still going.
func (b *builder) time(last time.Time, scopes Scopes) Time {
	t := Time{
		ExecutionTimeMs:         scopes.Total.ExecutionTimeMs,
		ToolTimeMs:              scopes.Total.ToolTimeMs,
		LegacyActiveMs:          scopes.Parent.LegacyActiveMs,
		LegacyActiveMsSemantics: LegacyActiveMsSemantics,
	}
	start := parseTime(b.run.StartedAt)
	if start.IsZero() {
		return t
	}
	end, basis := parseTime(b.run.EndedAt), WallEnded
	if end.IsZero() {
		end, basis = last, WallLastObserved
	}
	// A span assembled from two clocks can run backwards; that is not a
	// duration, and reporting it would drag a mean down.
	if ms := end.Sub(start).Milliseconds(); !end.IsZero() && ms >= 0 {
		t.WallMs, t.WallBasis = &ms, basis
	}
	return t
}

// stages groups the stage executions by (stage, occurrence) in walk order.
func (b *builder) stages(rep *Report) {
	index := map[string]int{}
	for i, u := range b.units {
		n := u.node
		if n.Kind != "stage" {
			continue
		}
		occurrence := intOf(n.StageOccurrence)
		key := fmt.Sprintf("%s/%d", n.Stage, occurrence)
		at, ok := index[key]
		if !ok {
			at = len(rep.Stages)
			index[key] = at
			rep.Stages = append(rep.Stages, Stage{Stage: n.Stage, Occurrence: occurrence, Attempts: []StageAttempt{}})
		}
		em := rep.Executions[i]
		rep.Stages[at].Attempts = append(rep.Stages[at].Attempts, StageAttempt{
			Attempt:     intOf(n.Attempt),
			ExecutionID: n.ExecutionID,
			Outcome:     n.Outcome,
			DurationMs:  em.DurationMs,
			Metrics:     em.Metrics,
		})
		rep.Attempts = append(rep.Attempts, Attempt{
			Kind: "stage", Key: key, Attempt: intOf(n.Attempt), ExecutionID: n.ExecutionID, Outcome: n.Outcome,
		})
	}
	for i := range rep.Stages {
		rep.Stages[i].Retries = len(rep.Stages[i].Attempts) - 1
	}
}

// lenses joins the lens executions with the run's LensAttempt records on
// (lens, round, attempt): an attempt on either side alone is still listed.
// Groups follow the LensAttempt order (round, lens, attempt), then
// executions no record matched in walk order.
func (b *builder) lenses(rep *Report) {
	index := map[string]int{}
	group := func(name string, round int) *LensGroup {
		key := fmt.Sprintf("%s/%d", name, round)
		at, ok := index[key]
		if !ok {
			at = len(rep.Lenses)
			index[key] = at
			rep.Lenses = append(rep.Lenses, LensGroup{Lens: name, Round: round, Attempts: []LensAttempt{}})
		}
		return &rep.Lenses[at]
	}
	find := func(name string, round, attempt int) *LensAttempt {
		g := group(name, round)
		for i := range g.Attempts {
			if g.Attempts[i].Attempt == attempt {
				return &g.Attempts[i]
			}
		}
		g.Attempts = append(g.Attempts, LensAttempt{Attempt: attempt})
		return &g.Attempts[len(g.Attempts)-1]
	}
	for _, a := range b.run.Lenses {
		la := find(a.Lens, a.Round, a.Attempt)
		la.Recorded = true
		la.Status, la.Verdict, la.ContextState = a.Status, a.Verdict, a.ContextState
		la.Contaminated, la.Superseded, la.Late, la.Malformed = a.Contaminated, a.Superseded, a.Late, a.Malformed
	}
	for i, u := range b.units {
		n := u.node
		if n.Kind != "lens" {
			continue
		}
		la := find(n.Lens, intOf(n.Round), intOf(n.Attempt))
		em := rep.Executions[i]
		m := em.Metrics
		la.ExecutionID, la.Outcome, la.DurationMs, la.Metrics = n.ExecutionID, n.Outcome, em.DurationMs, &m
	}
	for i := range rep.Lenses {
		g := &rep.Lenses[i]
		g.Retries = len(g.Attempts) - 1
		for _, a := range g.Attempts {
			rep.Attempts = append(rep.Attempts, Attempt{
				Kind: "lens", Key: fmt.Sprintf("%s/%d", g.Lens, g.Round), Attempt: a.Attempt,
				ExecutionID: a.ExecutionID, Outcome: a.Outcome, Status: a.Status,
			})
		}
	}
}

// outcomeOf reads the outcome off the record and nothing else.
func outcomeOf(run *runs.Run) string {
	if run.Outcome != "" {
		return run.Outcome
	}
	if run.Source != nil && run.EndedAt == "" {
		return OutcomeRunning
	}
	return OutcomeUnknown
}

// durationOf is the execution's own span: the record's bounds, or a
// historical dispatch's measured duration. Null when neither is known.
func durationOf(u *unit) *int64 {
	if u.subagent != nil {
		if u.subagent.durationMs.Valid {
			ms := u.subagent.durationMs.Int64
			return &ms
		}
		return nil
	}
	start, end := parseTime(u.node.StartedAt), parseTime(u.node.EndedAt)
	if start.IsZero() || end.IsZero() {
		return nil
	}
	ms := end.Sub(start).Milliseconds()
	if ms < 0 {
		return nil
	}
	return &ms
}

// normalize makes every empty child list [] so the tree renders the way the
// rest of the report does.
func normalize(n *runs.Node) {
	if n.Children == nil {
		n.Children = []*runs.Node{}
	}
	for _, c := range n.Children {
		normalize(c)
	}
}

func emptyList(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}

func intOf(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func latest(t time.Time, others ...time.Time) time.Time {
	for _, o := range others {
		if o.After(t) {
			t = o
		}
	}
	return t
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func isoOrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}
