package workreport

import (
	"testing"
	"time"

	"loom/internal/parse/summary"
)

func (f *fixture) loadCost(since, until time.Time) *CostReport {
	f.t.Helper()
	rep, err := LoadCost(f.path, since, until)
	if err != nil {
		f.t.Fatal(err)
	}
	return rep
}

// onlyCost asserts the report holds exactly one run and returns it.
func onlyCost(t *testing.T, rep *CostReport) CostRun {
	t.Helper()
	if len(rep.Runs) != 1 {
		t.Fatalf("report holds %d runs, want 1: %+v", len(rep.Runs), rep.Runs)
	}
	return rep.Runs[0]
}

// costSession is one metered /work run: two turns carrying wall clock and
// tokens, four tool calls of four kinds — one of them recorded with no kind at
// all — a subagent dispatch, and a commit 20 minutes in.
func costSession() *summary.SessionSummary {
	dispatch := int64(30000)
	return &summary.SessionSummary{
		SessionID:       "cost",
		Agent:           summary.AgentClaude,
		StartTime:       base,
		EndTime:         base.Add(time.Hour),
		InputTokens:     300,
		OutputTokens:    130,
		CacheReadTokens: 2100,
		Turns: []summary.Turn{
			{
				Idx:             0,
				UserMessage:     workInvocation("loom/cost-1111"),
				AssistantText:   "dispatching (loom/cost-1111 round 1): contract, quality, security",
				StartedAt:       base,
				EndedAt:         base.Add(2 * time.Minute),
				InputTokens:     100,
				OutputTokens:    50,
				CacheReadTokens: 900,
			},
			{
				Idx:             1,
				UserMessage:     "carry on",
				AssistantText:   "Merged and committed.",
				StartedAt:       base.Add(10 * time.Minute),
				EndedAt:         base.Add(13 * time.Minute),
				InputTokens:     200,
				OutputTokens:    80,
				CacheReadTokens: 1200,
			},
		},
		ToolCalls: []summary.ToolCall{
			{TurnIdx: 0, Kind: summary.KindRead, ToolName: "Read", KeyArg: "internal/thing.go", StartedAt: base.Add(time.Minute), DurationMs: 500},
			{TurnIdx: 0, Kind: summary.KindTask, ToolName: "Agent", KeyArg: "Contract lens review", StartedAt: base.Add(time.Minute), DurationMs: 30000},
			{TurnIdx: 0, ToolName: "unrecorded", KeyArg: "something the parser could not categorize", StartedAt: base.Add(2 * time.Minute), DurationMs: 250},
			{TurnIdx: 1, Kind: summary.KindBash, ToolName: "Bash", KeyArg: "git commit", StartedAt: base.Add(20 * time.Minute), DurationMs: 1000,
				ResultSummary: commitResult("[loom/cost-1111] Do the thing")},
		},
		Subagents: []summary.Subagent{
			{ParentTurnIdx: 0, AgentType: "contract-lens", DurationMs: &dispatch},
		},
	}
}

func TestCostRunCarriesItsIdentity(t *testing.T) {
	f := newFixture(t)
	f.add(costSession())

	run := onlyCost(t, f.loadCost(time.Time{}, time.Time{}))
	if run.Ticket != "loom/cost-1111" {
		t.Fatalf("ticket = %q, want loom/cost-1111", run.Ticket)
	}
	if run.SessionID != "cost" {
		t.Fatalf("session_id = %q, want cost", run.SessionID)
	}
	if run.Agent != string(summary.AgentClaude) {
		t.Fatalf("agent = %q, want %q", run.Agent, summary.AgentClaude)
	}
	if run.Runtime != RuntimeClaude {
		t.Fatalf("runtime = %q, want claude", run.Runtime)
	}
	if run.InvokedAt != base.Format(time.RFC3339) {
		t.Fatalf("invoked_at = %q, want %q", run.InvokedAt, base.Format(time.RFC3339))
	}
}

func TestCostReportsWallClockAndActiveSeparately(t *testing.T) {
	f := newFixture(t)
	f.add(costSession())

	run := onlyCost(t, f.loadCost(time.Time{}, time.Time{}))
	// Invocation to commit, idle included.
	if run.WallClockMs == nil || *run.WallClockMs != 20*60*1000 {
		t.Fatalf("wall_clock_ms = %v, want 1200000", run.WallClockMs)
	}
	// Turn wall clock (120000 + 180000) plus tool duration (500 + 30000 + 250 + 1000).
	if run.ActiveMs != 331750 {
		t.Fatalf("active_ms = %d, want 331750", run.ActiveMs)
	}
	if run.ActiveMs == *run.WallClockMs {
		t.Fatal("active_ms equals wall_clock_ms; the gap between them is the signal")
	}
}

func TestCostRunCountsTurnsTokensToolsAndSubagents(t *testing.T) {
	f := newFixture(t)
	f.add(costSession())

	run := onlyCost(t, f.loadCost(time.Time{}, time.Time{}))
	if run.Turns != 2 {
		t.Fatalf("turns = %d, want 2", run.Turns)
	}
	if run.InputTokens != 300 || run.OutputTokens != 130 || run.CacheReadTokens != 2100 {
		t.Fatalf("tokens = %d/%d/%d, want 300/130/2100", run.InputTokens, run.OutputTokens, run.CacheReadTokens)
	}
	if run.ToolCalls != 4 {
		t.Fatalf("tool_calls = %d, want 4", run.ToolCalls)
	}
	want := map[string]int{
		string(summary.KindRead): 1,
		string(summary.KindTask): 1,
		string(summary.KindBash): 1,
		// The call the parser could not categorize; an empty key would be
		// unreadable JSON.
		unknownToolKind: 1,
	}
	for kind, n := range want {
		if run.ToolCallsByKind[kind] != n {
			t.Fatalf("tool_calls_by_kind = %v, want %v", run.ToolCallsByKind, want)
		}
	}
	if len(run.ToolCallsByKind) != len(want) {
		t.Fatalf("tool_calls_by_kind = %v, want %v", run.ToolCallsByKind, want)
	}
	if run.Subagents != 1 {
		t.Fatalf("subagents = %d, want 1", run.Subagents)
	}
	if run.SubagentDurationMs == nil || *run.SubagentDurationMs != 30000 {
		t.Fatalf("subagent_duration_ms = %v, want 30000", run.SubagentDurationMs)
	}
	if !run.Committed {
		t.Fatal("committed = false, want true")
	}
}

func TestCostRunWithoutACommitHasNoWallClock(t *testing.T) {
	f := newFixture(t)
	sum := costSession()
	sum.SessionID = "abandoned"
	sum.ToolCalls = sum.ToolCalls[:3] // drop the commit
	f.add(sum)

	run := onlyCost(t, f.loadCost(time.Time{}, time.Time{}))
	if run.Committed {
		t.Fatal("committed = true, want false")
	}
	if run.WallClockMs != nil {
		t.Fatalf("wall_clock_ms = %d, want null: an abandoned run must not run its span to the session end", *run.WallClockMs)
	}
	// The work it did do is still measured.
	if run.ActiveMs == 0 || run.Turns != 2 {
		t.Fatalf("active_ms = %d, turns = %d, want the run's own work still counted", run.ActiveMs, run.Turns)
	}
}

// threeRunSession invokes /work three times in one session, each invocation
// with a turn of its own. The session totals are the sum of the per-turn
// values, so a report that attributed the session to each run would exceed
// them threefold.
func threeRunSession() *summary.SessionSummary {
	sum := &summary.SessionSummary{
		SessionID: "three-runs",
		Agent:     summary.AgentClaude,
		StartTime: base,
		EndTime:   base.Add(3 * time.Hour),
	}
	for i, ticket := range []string{"loom/first-1111", "loom/second-2222", "loom/third-3333"} {
		at := base.Add(time.Duration(i) * time.Hour)
		turn := summary.Turn{
			Idx:             i,
			UserMessage:     workInvocation(ticket),
			AssistantText:   "Implemented and committed.",
			StartedAt:       at,
			EndedAt:         at.Add(5 * time.Minute),
			InputTokens:     100,
			OutputTokens:    40,
			CacheReadTokens: 500,
		}
		sum.Turns = append(sum.Turns, turn)
		sum.ToolCalls = append(sum.ToolCalls, summary.ToolCall{
			TurnIdx: i, Kind: summary.KindBash, ToolName: "Bash", KeyArg: "git commit",
			StartedAt: at.Add(4 * time.Minute), DurationMs: 800,
			ResultSummary: commitResult("[" + ticket + "] Do the thing"),
		})
		sum.InputTokens += turn.InputTokens
		sum.OutputTokens += turn.OutputTokens
		sum.CacheReadTokens += turn.CacheReadTokens
	}
	return sum
}

func TestCostIsAttributedToTheRunNotTheSession(t *testing.T) {
	f := newFixture(t)
	sum := threeRunSession()
	f.add(sum)

	rep := f.loadCost(time.Time{}, time.Time{})
	if len(rep.Runs) != 3 {
		t.Fatalf("report holds %d runs, want 3", len(rep.Runs))
	}
	var turns int
	var input, output, cache int64
	for _, run := range rep.Runs {
		turns += run.Turns
		input += run.InputTokens
		output += run.OutputTokens
		cache += run.CacheReadTokens
	}
	if turns > len(sum.Turns) {
		t.Fatalf("runs hold %d turns, more than the session's %d", turns, len(sum.Turns))
	}
	if input > sum.InputTokens || output > sum.OutputTokens || cache > sum.CacheReadTokens {
		t.Fatalf("runs hold %d/%d/%d tokens, more than the session's %d/%d/%d",
			input, output, cache, sum.InputTokens, sum.OutputTokens, sum.CacheReadTokens)
	}
	for _, run := range rep.Runs {
		if run.Turns != 1 {
			t.Fatalf("run %s holds %d turns, want its own one", run.Ticket, run.Turns)
		}
	}
}

func TestSubagentDurationIsNullWhenNoneWasMeasured(t *testing.T) {
	f := newFixture(t)
	sum := costSession()
	sum.SessionID = "unmeasured-subagents"
	// A background dispatch's span cannot be resolved, so the store holds a
	// NULL duration: "not measured" must not read as "returned instantly".
	sum.Subagents = []summary.Subagent{
		{ParentTurnIdx: 0, AgentType: "contract-lens"},
		{ParentTurnIdx: 0, AgentType: "quality-lens"},
	}
	f.add(sum)

	run := onlyCost(t, f.loadCost(time.Time{}, time.Time{}))
	if run.Subagents != 2 {
		t.Fatalf("subagents = %d, want 2", run.Subagents)
	}
	if run.SubagentDurationMs != nil {
		t.Fatalf("subagent_duration_ms = %d, want null", *run.SubagentDurationMs)
	}
}

func TestSubagentDurationSumsTheMeasuredOnes(t *testing.T) {
	f := newFixture(t)
	sum := costSession()
	sum.SessionID = "mixed-subagents"
	measured := int64(12000)
	sum.Subagents = []summary.Subagent{
		{ParentTurnIdx: 0, AgentType: "contract-lens", DurationMs: &measured},
		{ParentTurnIdx: 0, AgentType: "quality-lens"},
	}
	f.add(sum)

	run := onlyCost(t, f.loadCost(time.Time{}, time.Time{}))
	if run.Subagents != 2 {
		t.Fatalf("subagents = %d, want 2", run.Subagents)
	}
	if run.SubagentDurationMs == nil || *run.SubagentDurationMs != measured {
		t.Fatalf("subagent_duration_ms = %v, want %d", run.SubagentDurationMs, measured)
	}
}

func TestWallClockIsNullWhenTheCommitCarriesNoTimestamp(t *testing.T) {
	f := newFixture(t)
	sum := costSession()
	sum.SessionID = "untimed-commit"
	// A bash row whose transcript record carried no parseable time is stored
	// with the session start (internal/summaries writeCommits), which places
	// the commit in the day but not in the run: the span is unmeasurable.
	sum.ToolCalls[3].StartedAt = time.Time{}
	f.add(sum)

	run := onlyCost(t, f.loadCost(time.Time{}, time.Time{}))
	if !run.Committed {
		t.Fatal("committed = false, want true: the subject still names the ticket")
	}
	if run.WallClockMs != nil {
		t.Fatalf("wall_clock_ms = %d, want null: the commit carries no usable timestamp", *run.WallClockMs)
	}
}

func TestWallClockIsNullWhenTheCommitPrecedesTheInvocation(t *testing.T) {
	f := newFixture(t)
	sum := costSession()
	sum.SessionID = "commit-before-invocation"
	// Commit times come off the transcript, not a monotonic clock, so one can
	// land before the invocation it belongs to. A negative span is not a cost.
	sum.ToolCalls[3].StartedAt = base.Add(-10 * time.Minute)
	f.add(sum)

	run := onlyCost(t, f.loadCost(time.Time{}, time.Time{}))
	if !run.Committed {
		t.Fatal("committed = false, want true")
	}
	if run.WallClockMs != nil {
		t.Fatalf("wall_clock_ms = %d, want null: a negative span would drag the mean down", *run.WallClockMs)
	}
}

// sameTicketTwiceSession invokes /work on one ticket twice in a session, each
// invocation landing a commit naming it 30 minutes in. The second commit names
// the first run's ticket too, so a span that took the latest ticket-named
// commit would charge the first run with 2.5 hours of the session's 3.
func sameTicketTwiceSession() *summary.SessionSummary {
	const ticket = "loom/repeat-1111"
	sum := &summary.SessionSummary{
		SessionID: "same-ticket-twice",
		Agent:     summary.AgentClaude,
		StartTime: base,
		EndTime:   base.Add(3 * time.Hour),
	}
	for i, at := range []time.Time{base, base.Add(2 * time.Hour)} {
		sum.Turns = append(sum.Turns, summary.Turn{
			Idx:           i,
			UserMessage:   workInvocation(ticket),
			AssistantText: "Implemented and committed.",
			StartedAt:     at,
			EndedAt:       at.Add(5 * time.Minute),
		})
		sum.ToolCalls = append(sum.ToolCalls, summary.ToolCall{
			TurnIdx: i, Kind: summary.KindBash, ToolName: "Bash", KeyArg: "git commit",
			StartedAt: at.Add(30 * time.Minute), DurationMs: 800,
			ResultSummary: commitResult("[" + ticket + "] Do the thing"),
		})
	}
	return sum
}

func TestWallClockStopsAtTheRunsOwnCommit(t *testing.T) {
	f := newFixture(t)
	f.add(sameTicketTwiceSession())

	rep := f.loadCost(time.Time{}, time.Time{})
	if len(rep.Runs) != 2 {
		t.Fatalf("report holds %d runs, want 2: %+v", len(rep.Runs), rep.Runs)
	}
	for _, run := range rep.Runs {
		if !run.Committed {
			t.Fatalf("run invoked at %s: committed = false, want true", run.InvokedAt)
		}
		if run.WallClockMs == nil {
			t.Fatalf("run invoked at %s: wall_clock_ms = null, want 1800000", run.InvokedAt)
		}
		if *run.WallClockMs != 30*60*1000 {
			t.Fatalf("run invoked at %s: wall_clock_ms = %d, want 1800000 — each run ends at its own commit",
				run.InvokedAt, *run.WallClockMs)
		}
	}
}
