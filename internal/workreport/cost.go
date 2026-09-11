package workreport

import (
	"database/sql"
	"fmt"
	"math"
	"os"
	"sort"
	"time"
)

// unknownToolKind keys tool calls whose recorded kind is empty, so the per-kind
// breakdown never emits an empty JSON key.
const unknownToolKind = "unknown"

// CostRun is what one /work run cost.
type CostRun struct {
	Ticket    string  `json:"ticket"`
	SessionID string  `json:"session_id"`
	Agent     string  `json:"agent"`
	Runtime   Runtime `json:"runtime"`
	InvokedAt string  `json:"invoked_at"`
	Committed bool    `json:"committed"`
	// WallClockMs is invocation to commit — the time actually felt, idle
	// included. Null when the run did not commit, so an abandoned run cannot
	// run its span to the session end and inflate the trend.
	WallClockMs *int64 `json:"wall_clock_ms"`
	// ActiveMs is the run's turn wall clock plus its tool durations. It is
	// immune to the human being asleep or in a meeting, which wall_clock_ms is
	// not; the gap between the two separates "the agent got slower" from "I was
	// slower to respond". A zero means nothing in the span was timed, not that
	// the run was instant.
	ActiveMs           int64          `json:"active_ms"`
	Turns              int            `json:"turns"`
	InputTokens        int64          `json:"input_tokens"`
	OutputTokens       int64          `json:"output_tokens"`
	CacheReadTokens    int64          `json:"cache_read_tokens"`
	ToolCalls          int            `json:"tool_calls"`
	ToolCallsByKind    map[string]int `json:"tool_calls_by_kind"`
	Subagents          int            `json:"subagents"`
	SubagentDurationMs *int64         `json:"subagent_duration_ms"`
}

// CostReport is the whole document. Like Report it carries no generation
// timestamp: two reports are meant to be diffed as a before and an after.
type CostReport struct {
	Since string    `json:"since"`
	Until string    `json:"until"`
	Runs  []CostRun `json:"runs"`
}

// LoadCost reads dbPath and reports what every /work run invoked in
// [since, until) cost. A zero since or until is unbounded.
func LoadCost(dbPath string, since, until time.Time) (*CostReport, error) {
	// A missing database is an error rather than an empty report: "no runs" is
	// itself the number being measured, and a zero that means "nothing to read"
	// reads exactly like a zero that means "nobody ran /work".
	if _, err := os.Stat(dbPath); err != nil {
		return nil, fmt.Errorf("summaries.db not found at %s — run `loom summarize`", dbPath)
	}

	// mode=ro keeps us out of the summarizer's way; it holds the only writer.
	dsn := fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(2000)", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open summaries.db: %w", err)
	}
	defer db.Close()

	if v := schemaVersionOf(db); v < requiredSchemaVersion {
		return nil, fmt.Errorf("summaries.db is at schema %d and predates the commits table (want %d) — run `loom summarize --rebuild`", v, requiredSchemaVersion)
	}

	invocations, err := loadInvocations(db)
	if err != nil {
		return nil, err
	}

	rep := &CostReport{Runs: []CostRun{}}
	if !since.IsZero() {
		rep.Since = since.Format(time.RFC3339)
	}
	if !until.IsZero() {
		rep.Until = until.Format(time.RFC3339)
	}

	for _, session := range groupBySession(invocations) {
		var wanted []int
		for i, inv := range session {
			if inRange(inv.startedAt, since, until) {
				wanted = append(wanted, i)
			}
		}
		if len(wanted) == 0 {
			continue
		}
		data, err := loadCostSession(db, session[0].agent, session[0].sessionID)
		if err != nil {
			return nil, err
		}
		for _, i := range wanted {
			// A run runs until the next /work invocation in the same session,
			// else to the end of the session.
			endIdx := math.MaxInt
			endsAt := session[i].sessionEnd
			if i+1 < len(session) {
				endIdx = session[i+1].idx - 1
				endsAt = session[i+1].startedAt
			}
			// Every recognized invocation gets a record, whatever the
			// compliance parser would make of it: dropping the runs it calls
			// unknown would bias the cost trend toward the runs that are
			// easiest to classify.
			rep.Runs = append(rep.Runs, measure(session[i], endIdx, endsAt, data))
		}
	}

	// Stable so runs that tie — same session, or no invocation timestamp at all
	// — keep the query's (agent, session, idx) order rather than an arbitrary
	// one, which is what makes two reports over the same range byte-identical.
	sort.SliceStable(rep.Runs, func(i, j int) bool {
		if rep.Runs[i].InvokedAt != rep.Runs[j].InvokedAt {
			return rep.Runs[i].InvokedAt < rep.Runs[j].InvokedAt
		}
		return rep.Runs[i].SessionID < rep.Runs[j].SessionID
	})
	return rep, nil
}

type costTurnRow struct {
	idx             int
	wallClockMs     int64
	inputTokens     int64
	outputTokens    int64
	cacheReadTokens int64
}

type costCallRow struct {
	turnIdx    int
	toolKind   string
	durationMs int64
}

type costSubagentRow struct {
	parentTurnIdx int
	durationMs    sql.NullInt64
}

// costSessionData is everything one session contributes to its runs' cost. A
// loader of its own rather than loadSession's: cost needs the metered columns
// and none of the transcript text the compliance parser reads.
type costSessionData struct {
	turns     []costTurnRow
	calls     []costCallRow
	subagents []costSubagentRow
	commits   []commitRow
}

func loadCostSession(db *sql.DB, agent, sessionID string) (*costSessionData, error) {
	data := &costSessionData{}

	turns, err := db.Query(`
		SELECT idx, wall_clock_ms, input_tokens, output_tokens, cache_read_tokens
		FROM turns WHERE agent = ? AND session_id = ? ORDER BY idx
	`, agent, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query turns: %w", err)
	}
	defer turns.Close()
	for turns.Next() {
		var (
			t                          costTurnRow
			wall, input, output, cache sql.NullInt64
		)
		if err := turns.Scan(&t.idx, &wall, &input, &output, &cache); err != nil {
			return nil, err
		}
		t.wallClockMs = wall.Int64
		t.inputTokens = input.Int64
		t.outputTokens = output.Int64
		t.cacheReadTokens = cache.Int64
		data.turns = append(data.turns, t)
	}
	if err := turns.Err(); err != nil {
		return nil, err
	}

	calls, err := db.Query(`
		SELECT turn_idx, tool_kind, duration_ms
		FROM tool_calls WHERE agent = ? AND session_id = ? ORDER BY seq
	`, agent, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query tool calls: %w", err)
	}
	defer calls.Close()
	for calls.Next() {
		var (
			c        costCallRow
			turnIdx  sql.NullInt64
			kind     sql.NullString
			duration sql.NullInt64
		)
		if err := calls.Scan(&turnIdx, &kind, &duration); err != nil {
			return nil, err
		}
		// A row with no turn to hang off belongs to no run's span; scanning
		// its NULL to 0 would charge it to whichever run owns turn 0.
		if !turnIdx.Valid {
			continue
		}
		c.turnIdx = int(turnIdx.Int64)
		c.toolKind = kind.String
		c.durationMs = duration.Int64
		data.calls = append(data.calls, c)
	}
	if err := calls.Err(); err != nil {
		return nil, err
	}

	subagents, err := db.Query(`
		SELECT parent_turn_idx, duration_ms
		FROM subagents WHERE agent = ? AND session_id = ? ORDER BY seq
	`, agent, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query subagents: %w", err)
	}
	defer subagents.Close()
	for subagents.Next() {
		var (
			s       costSubagentRow
			turnIdx sql.NullInt64
		)
		if err := subagents.Scan(&turnIdx, &s.durationMs); err != nil {
			return nil, err
		}
		// Same reason as the tool calls above: unattributed is not turn 0.
		if !turnIdx.Valid {
			continue
		}
		s.parentTurnIdx = int(turnIdx.Int64)
		data.subagents = append(data.subagents, s)
	}
	if err := subagents.Err(); err != nil {
		return nil, err
	}

	data.commits, err = loadCommits(db, agent, sessionID)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// measure costs one run: the turns from its invocation through endIdx, the tool
// calls and subagents those turns dispatched, and the commit that ended it.
func measure(inv invocationRow, endIdx int, endsAt time.Time, data *costSessionData) CostRun {
	run := CostRun{
		Ticket:          inv.ticket,
		SessionID:       inv.sessionID,
		Agent:           inv.agent,
		Runtime:         runtimeOf(inv.agent),
		ToolCallsByKind: map[string]int{},
	}
	if !inv.startedAt.IsZero() {
		run.InvokedAt = inv.startedAt.Format(time.RFC3339)
	}

	inSpan := func(idx int) bool { return idx >= inv.idx && idx <= endIdx }

	for _, t := range data.turns {
		if !inSpan(t.idx) {
			continue
		}
		run.Turns++
		run.InputTokens += t.inputTokens
		run.OutputTokens += t.outputTokens
		run.CacheReadTokens += t.cacheReadTokens
		// A turn's wall clock is a subtraction of transcript timestamps, not a
		// monotonic reading, so a negative span is representable. It is not a
		// cost, and adding it would net out another turn's.
		if t.wallClockMs >= 0 {
			run.ActiveMs += t.wallClockMs
		}
	}
	for _, c := range data.calls {
		if !inSpan(c.turnIdx) {
			continue
		}
		run.ToolCalls++
		kind := c.toolKind
		if kind == "" {
			kind = unknownToolKind
		}
		run.ToolCallsByKind[kind]++
		// A tool call runs inside its turn's wall clock, so the two terms
		// overlap by construction: active_ms is an upper bound on attention,
		// not a disjoint sum. Defined that way deliberately — the measure it
		// has to beat is a wall clock that counts the human's idle too.
		if c.durationMs >= 0 {
			run.ActiveMs += c.durationMs
		}
	}
	var subagentMs int64
	measured := false
	for _, s := range data.subagents {
		if !inSpan(s.parentTurnIdx) {
			continue
		}
		run.Subagents++
		if s.durationMs.Valid {
			subagentMs += s.durationMs.Int64
			measured = true
		}
	}
	// Stays null when no dispatch carried a duration — "not measured" must not
	// read as "returned instantly".
	if measured {
		run.SubagentDurationMs = &subagentMs
	}

	committedAt, ok := runCommit(inv, endsAt, data.commits)
	run.Committed = ok
	if ok && !inv.startedAt.IsZero() && !committedAt.IsZero() {
		ms := committedAt.Sub(inv.startedAt).Milliseconds()
		// A commit timestamp can precede its invocation — the recorded commit
		// time comes off the transcript, not from a monotonic clock — and a
		// negative span is not a cost. Report nothing rather than a number
		// that would drag a mean down.
		if ms >= 0 {
			run.WallClockMs = &ms
		}
	}
	return run
}
