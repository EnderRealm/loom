package runreport

import (
	"database/sql"
	"fmt"
	"time"

	"loom/internal/parse/summary"
	"loom/internal/pricing"
	"loom/internal/runs"
	"loom/internal/workreport"
)

type turnRow struct {
	idx             int
	userMessage     string
	model           string
	effort          string
	cliVersion      string
	speed           string
	wallClockMs     int64
	inputTokens     int64
	outputTokens    int64
	cacheReadTokens int64
	cacheCreation   int64
	cacheCreation1h int64
	usageMixed      bool
	startedAt       time.Time
	endedAt         time.Time
}

type callRow struct {
	turnIdx    int
	toolKind   string
	durationMs int64
	isError    bool
	startedAt  time.Time
}

type errorRow struct {
	turnIdx int
	source  string
	ts      time.Time
}

// sessionData is the metered rows of one session. Rows with no turn to hang
// off are dropped at load, as cost.go does: scanning their NULL to 0 would
// charge them to whichever span holds turn 0.
type sessionData struct {
	agent  string
	turns  []turnRow
	calls  []callRow
	errors []errorRow
}

// subagentRow is a historical dispatch's own row in the parent's subagents
// table. The usage columns are NULL together when no transcript was folded;
// inputTokens.Valid is the marker.
type subagentRow struct {
	agentType       string
	durationMs      sql.NullInt64
	model           sql.NullString
	speed           sql.NullString
	inputTokens     sql.NullInt64
	outputTokens    sql.NullInt64
	cacheReadTokens sql.NullInt64
	cacheCreation   sql.NullInt64
	cacheCreation1h sql.NullInt64
	usageMixed      sql.NullBool
}

// loadSession reads one session's metered rows, or nil when the sessions
// table does not hold it: an absent session is a gap, not an empty one.
func loadSession(db *sql.DB, agent, sessionID string) (*sessionData, error) {
	var one int
	err := db.QueryRow(`SELECT 1 FROM sessions WHERE agent = ? AND session_id = ?`, agent, sessionID).Scan(&one)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query session: %w", err)
	}
	data := &sessionData{agent: agent}

	turns, err := db.Query(`
		SELECT idx, user_message, model, effort, cli_version, speed, wall_clock_ms,
		       input_tokens, output_tokens, cache_read_tokens,
		       cache_creation_tokens, cache_creation_1h_tokens, usage_mixed,
		       started_at, ended_at
		FROM turns WHERE agent = ? AND session_id = ? ORDER BY idx`, agent, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query turns: %w", err)
	}
	defer turns.Close()
	for turns.Next() {
		var (
			t                                      turnRow
			message, model, effort, version, speed sql.NullString
			wall, input, output, cache             sql.NullInt64
			creation, creation1h                   sql.NullInt64
			mixed                                  sql.NullBool
			startedAt, endedAt                     sql.NullString
		)
		if err := turns.Scan(&t.idx, &message, &model, &effort, &version, &speed, &wall,
			&input, &output, &cache, &creation, &creation1h, &mixed, &startedAt, &endedAt); err != nil {
			return nil, err
		}
		t.userMessage, t.model, t.effort, t.cliVersion, t.speed = message.String, model.String, effort.String, version.String, speed.String
		t.wallClockMs, t.inputTokens, t.outputTokens, t.cacheReadTokens = wall.Int64, input.Int64, output.Int64, cache.Int64
		t.cacheCreation, t.cacheCreation1h, t.usageMixed = creation.Int64, creation1h.Int64, mixed.Bool
		t.startedAt, t.endedAt = parseTime(startedAt.String), parseTime(endedAt.String)
		data.turns = append(data.turns, t)
	}
	if err := turns.Err(); err != nil {
		return nil, err
	}

	calls, err := db.Query(`
		SELECT turn_idx, tool_kind, duration_ms, is_error, started_at
		FROM tool_calls WHERE agent = ? AND session_id = ? ORDER BY seq`, agent, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query tool calls: %w", err)
	}
	defer calls.Close()
	for calls.Next() {
		var (
			c         callRow
			turnIdx   sql.NullInt64
			kind      sql.NullString
			duration  sql.NullInt64
			isError   sql.NullBool
			startedAt sql.NullString
		)
		if err := calls.Scan(&turnIdx, &kind, &duration, &isError, &startedAt); err != nil {
			return nil, err
		}
		if !turnIdx.Valid {
			continue
		}
		c.turnIdx, c.toolKind, c.durationMs, c.isError = int(turnIdx.Int64), kind.String, duration.Int64, isError.Bool
		c.startedAt = parseTime(startedAt.String)
		data.calls = append(data.calls, c)
	}
	if err := calls.Err(); err != nil {
		return nil, err
	}

	errs, err := db.Query(`
		SELECT turn_idx, source, ts FROM errors WHERE agent = ? AND session_id = ? ORDER BY seq`, agent, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query errors: %w", err)
	}
	defer errs.Close()
	for errs.Next() {
		var (
			e          errorRow
			turnIdx    sql.NullInt64
			source, ts sql.NullString
		)
		if err := errs.Scan(&turnIdx, &source, &ts); err != nil {
			return nil, err
		}
		if !turnIdx.Valid {
			continue
		}
		e.turnIdx, e.source, e.ts = int(turnIdx.Int64), source.String, parseTime(ts.String)
		data.errors = append(data.errors, e)
	}
	return data, errs.Err()
}

// loadSubagentRow reads one dispatch's row off its parent's transcript, or
// nil when the row is gone.
func loadSubagentRow(db *sql.DB, ref runs.TranscriptRef, seq int) (*subagentRow, error) {
	var (
		s         subagentRow
		agentType sql.NullString
	)
	err := db.QueryRow(`
		SELECT agent_type, duration_ms, model, speed, input_tokens, output_tokens,
		       cache_read_tokens, cache_creation_tokens, cache_creation_1h_tokens, usage_mixed
		FROM subagents WHERE agent = ? AND session_id = ? AND seq = ?`, ref.Agent, ref.SessionID, seq).
		Scan(&agentType, &s.durationMs, &s.model, &s.speed, &s.inputTokens, &s.outputTokens,
			&s.cacheReadTokens, &s.cacheCreation, &s.cacheCreation1h, &s.usageMixed)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query subagent: %w", err)
	}
	s.agentType = agentType.String
	return &s, nil
}

// meter accumulates one scope's Metrics over the units added to it.
type meter struct {
	table        *pricing.Table
	m            Metrics
	p            *workreport.Pricer
	priced       bool
	cost         float64
	sessions     map[runs.TranscriptRef]bool
	lastObserved time.Time
}

func (b *builder) newMeter() *meter {
	m := &meter{
		table: b.table,
		m: Metrics{
			ToolCallsByKind:         map[string]int{},
			TokensByRuntime:         map[string]*Tokens{},
			Failures:                Failures{OtherBySource: map[string]int{}},
			Models:                  []string{},
			Efforts:                 []string{},
			CLIVersions:             []string{},
			LegacyActiveMsSemantics: LegacyActiveMsSemantics,
		},
		p:        workreport.NewPricer(b.table, b.at),
		priced:   !b.at.IsZero(),
		sessions: map[runs.TranscriptRef]bool{},
	}
	// No start time means no rate can be said to be in force, at any model,
	// so nothing is priced; one warning rather than one per unit.
	if !m.priced {
		m.p.Warn("run start time unknown")
	}
	return m
}

// add meters one execution: its own span for execution time, then — when
// this scope counts its transcript — its transcript rows or its dispatch
// row, whichever it has. An execution whose transcript another execution
// already counted still ran, so its count and duration are its own.
func (m *meter) add(u *unit, transcript bool) {
	m.m.Executions++
	if ms := durationOf(u); ms != nil {
		m.m.ExecutionTimeMs += *ms
		m.m.ExecutionTimeCoverage.Timed++
	} else {
		m.m.ExecutionTimeCoverage.Untimed++
	}
	m.observe(parseTime(u.node.StartedAt), parseTime(u.node.EndedAt))
	if !transcript {
		return
	}
	switch {
	case u.subagent != nil:
		m.addSubagent(u)
	case u.node.Transcript != nil && u.data == nil:
		// The transcript is named but not folded: its cost is unknown, not zero.
		m.p.Warn(u.node.ExecutionID + ": session not in summaries.db")
		m.priced = false
	case u.data != nil:
		ref := *u.node.Transcript
		if !m.sessions[ref] {
			m.sessions[ref] = true
			m.m.Transcripts++
		}
		m.addSpan(u)
	}
}

func (m *meter) addSpan(u *unit) {
	data := u.data
	inSpan := func(idx int) bool { return idx >= u.startIdx && idx <= u.endIdx }
	for _, t := range data.turns {
		if !inSpan(t.idx) {
			continue
		}
		m.m.Turns++
		m.m.ConditionsCoverage.Turns++
		if t.model != "" {
			m.m.ConditionsCoverage.TurnsWithModel++
		}
		if t.effort != "" {
			m.m.ConditionsCoverage.TurnsWithEffort++
		}
		if t.cliVersion != "" {
			m.m.ConditionsCoverage.TurnsWithCLIVersion++
		}
		m.m.Models = appendDistinct(m.m.Models, t.model)
		m.m.Efforts = appendDistinct(m.m.Efforts, t.effort)
		m.m.CLIVersions = appendDistinct(m.m.CLIVersions, t.cliVersion)
		// The rule tells a human from a harness envelope by text shape; a
		// descendant's user messages are the dispatching agent's prompts and
		// carry no envelope, so only the root's span can hold a human turn.
		if u.node.Kind == runs.KindRoot && workreport.HumanInteraction(t.userMessage) {
			m.m.HumanInteractions++
		}
		// A turn's wall clock is a subtraction of transcript timestamps, so a
		// negative span is representable and is not a duration.
		if t.wallClockMs >= 0 {
			m.m.LegacyActiveMs += t.wallClockMs
		}
		m.observe(t.startedAt, t.endedAt)
		m.addTokens(data.agent, fmt.Sprintf("%s turn %d", u.node.ExecutionID, t.idx), t.model, t.speed, t.usageMixed,
			t.inputTokens, t.outputTokens, t.cacheReadTokens, t.cacheCreation, t.cacheCreation1h)
	}
	for _, c := range data.calls {
		if !inSpan(c.turnIdx) {
			continue
		}
		m.m.ToolCalls++
		kind := c.toolKind
		if kind == "" {
			kind = unknownToolKind
		}
		m.m.ToolCallsByKind[kind]++
		if c.isError {
			m.m.ToolCallsErrored++
		}
		if c.durationMs >= 0 {
			m.m.ToolTimeMs += c.durationMs
			m.m.LegacyActiveMs += c.durationMs
		}
		m.observe(c.startedAt)
	}
	for _, e := range data.errors {
		if !inSpan(e.turnIdx) {
			continue
		}
		m.observe(e.ts)
		switch e.source {
		case sourceAPIError:
			m.m.Failures.API++
		case sourceToolError, sourceExecError, sourcePatchError:
			m.m.Failures.Tool++
		case sourceTurnAborted:
			m.m.Failures.Process++
		case sourceStopHook:
			m.m.HookSignals++
		default:
			m.m.Failures.Other++
			m.m.Failures.OtherBySource[e.source]++
		}
	}
}

// addSubagent meters a historical dispatch from its row: its usage under the
// parent runtime's semantics, or nothing when no transcript was folded.
func (m *meter) addSubagent(u *unit) {
	s := u.subagent
	if !s.inputTokens.Valid {
		m.p.Warn(u.node.ExecutionID + ": no transcript")
		m.priced = false
		return
	}
	m.addTokens(u.agent, u.node.ExecutionID, s.model.String, s.speed.String, s.usageMixed.Bool,
		s.inputTokens.Int64, s.outputTokens.Int64, s.cacheReadTokens.Int64, s.cacheCreation.Int64, s.cacheCreation1h.Int64)
}

// addTokens adds one unit's usage to its runtime's bucket and prices it. The
// priced input is the billable one: for a runtime whose input already holds
// the cache read, the read is taken back out before the two are priced at
// their own rates, so a rate for such a model would not count it twice.
func (m *meter) addTokens(agent, subject, model, speed string, mixed bool, input, output, cacheRead, cacheWrite, cacheWrite1h int64) {
	tok := m.m.TokensByRuntime[agent]
	if tok == nil {
		tok = &Tokens{CacheSemantics: cacheSemantics(agent)}
		m.m.TokensByRuntime[agent] = tok
	}
	tok.Input += input
	tok.Output += output
	tok.CacheRead += cacheRead
	tok.CacheWrite += cacheWrite
	tok.CacheWrite1h += cacheWrite1h

	billableInput := input
	if tok.CacheSemantics == CacheReadInsideInput {
		billableInput = input - cacheRead
		if billableInput < 0 {
			m.p.Warn(subject + ": cache read exceeds input")
			m.priced = false
			return
		}
	}
	u, ok := workreport.UsageOf(billableInput, output, cacheRead, cacheWrite, cacheWrite1h)
	if !ok {
		m.p.Warn(subject + workreport.BreakdownExceedsTotal)
		m.priced = false
		return
	}
	// A unit with no tokens costs nothing whatever its model, so it cannot
	// make the scope unpriceable.
	if !workreport.HasTokens(u) {
		return
	}
	if mixed {
		m.p.Warn(subject + workreport.RecordsDisagree)
		m.priced = false
		return
	}
	usd, ok := m.p.Price(subject, model, speed, u)
	m.priced = m.priced && ok
	m.cost += usd
}

func (m *meter) observe(times ...time.Time) {
	m.lastObserved = latest(m.lastObserved, times...)
}

// metrics finishes the scope: per-runtime totals under each runtime's
// semantics, the price when everything priced, and the table it came from.
func (m *meter) metrics() Metrics {
	out := m.m
	out.TotalTokens = 0
	for _, tok := range out.TokensByRuntime {
		tok.Total = tok.Input + tok.Output
		if tok.CacheSemantics != CacheReadInsideInput {
			tok.Total += tok.CacheRead + tok.CacheWrite
		}
		out.TotalTokens += tok.Total
	}
	if m.priced {
		out.CostUSD = workreport.RoundUSD(m.cost)
	}
	out.PricingWarnings = emptyList(m.p.Warnings())
	out.Pricing = Pricing{
		Currency:  m.table.Currency,
		Source:    m.table.Source,
		Checked:   m.table.Checked.Format(time.DateOnly),
		Available: out.CostUSD != nil,
	}
	return out
}

func cacheSemantics(agent string) string {
	switch agent {
	case string(summary.AgentClaude):
		return CacheSeparate
	case string(summary.AgentCodex):
		return CacheReadInsideInput
	default:
		return CacheUnknown
	}
}

func appendDistinct(list []string, v string) []string {
	if v == "" {
		return list
	}
	for _, have := range list {
		if have == v {
			return list
		}
	}
	return append(list, v)
}
