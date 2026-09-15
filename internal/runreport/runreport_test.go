package runreport

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"loom/internal/parse/lens"
	"loom/internal/parse/summary"
	"loom/internal/pricing"
	"loom/internal/runs"
	"loom/internal/summaries"
)

// fixtureRun is the run docs/execution-records.md and
// internal/runs/testdata/executions.jsonl describe: a Claude root, a Claude
// subagent with a Codex child under it, a routed Codex lens, two Weft work
// attempts and a review attempt, and a command with no transcript.
const (
	fixtureRun     = "0f4c3a6e-2d1b-4b7e-9c8a-5e2f1d0a9b31"
	fixtureTicket  = "loom/persist-execution-identities-2149"
	rootSession    = "195f819e-1e11-4e08-8c16-a340f512f892"
	agentSession   = "agent-a0e0c89b977fd6273"
	codexSession   = "01a0029b-b39f-7802-8b5f-56ffe644403b"
	lensSession    = "01a0029c-4d61-7f0e-a2b3-9c7d5e1f2a44"
	work1Session   = "2b7d4c1e-8f3a-4e5b-9d6c-1a2b3c4d5e6f"
	work2Session   = "3c8e5d2f-9a4b-4f6c-8e7d-2b3c4d5e6f7a"
	reviewSession  = "4d9f6e3a-0b5c-4a7d-9f8e-3c4d5e6f7a8b"
	lensDispatchID = "toolu_01Wq9LensSecurityR1"
	claudeModel    = "claude-opus-5"
	codexModel     = "gpt-5.6"
)

var runStart = time.Date(2026, 9, 10, 17, 2, 11, 0, time.UTC)

func at(hhmmss string) time.Time {
	t, err := time.Parse("15:04:05", hhmmss)
	if err != nil {
		panic(err)
	}
	return time.Date(2026, 9, 10, t.Hour(), t.Minute(), t.Second(), 0, time.UTC)
}

func openStore(t *testing.T) (*summaries.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "summaries.db")
	st, err := summaries.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st, path
}

func importFile(t *testing.T, st *summaries.Store, path string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ImportExecutions(context.Background(), path, f, info.Size(), info.ModTime()); err != nil {
		t.Fatalf("import %s: %v", path, err)
	}
}

func importLines(t *testing.T, st *summaries.Store, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "executions.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	importFile(t, st, path)
	return path
}

func writeSession(t *testing.T, st *summaries.Store, sum *summary.SessionSummary) {
	t.Helper()
	if err := st.WriteSummary(context.Background(), sum, summaries.SourceInfo{Project: "loom"}); err != nil {
		t.Fatal(err)
	}
}

func workInvocation(ticket string) string {
	return "<command-message>work</command-message>\n<command-name>/work</command-name>\n<command-args>" + ticket + "</command-args>"
}

// notification is the shape a Claude lens verdict comes back in.
func notification(dispatchID, body string) string {
	return "<task-notification><tool-use-id>" + dispatchID + "</tool-use-id><result>```json\n" + body + "\n```</result></task-notification>"
}

// oneTurn is a child session with one metered turn and one tool call.
func oneTurn(agent summary.Agent, sessionID, model string, start, end time.Time, input, cacheRead, output int64, kind summary.ToolKind, toolMs int64) *summary.SessionSummary {
	return &summary.SessionSummary{
		SessionID: sessionID,
		Agent:     agent,
		StartTime: start,
		EndTime:   end,
		Turns: []summary.Turn{{
			Idx: 0, UserMessage: "review", AssistantText: "fine", StartedAt: start, EndedAt: end,
			Model: model, InputTokens: input, CacheReadTokens: cacheRead, OutputTokens: output,
		}},
		ToolCalls: []summary.ToolCall{{TurnIdx: 0, Kind: kind, ToolName: string(kind), StartedAt: start, DurationMs: toolMs}},
	}
}

// rootSessionSummary is the fixture run's own transcript: the /work
// invocation, a human turn, the lens verdict coming back, and a second /work
// invocation after the run ended that bounds the parent-only span.
func rootSessionSummary() *summary.SessionSummary {
	verdict := notification(lensDispatchID, `{"lens": "security", "verdict": "satisfied", "summary": "No exposure."}`)
	sum := &summary.SessionSummary{
		SessionID: rootSession,
		Agent:     summary.AgentClaude,
		StartTime: runStart,
		EndTime:   at("17:50:00"),
		Turns: []summary.Turn{
			{
				Idx: 0, UserMessage: workInvocation(fixtureTicket),
				AssistantText: "dispatching (" + fixtureTicket + " round 1): security",
				StartedAt:     runStart, EndedAt: runStart.Add(time.Minute),
				Model: claudeModel, Effort: "high", CLIVersion: "2.1.0",
				InputTokens: 100, OutputTokens: 50, CacheReadTokens: 900, CacheCreationTokens: 300, CacheCreation1hTokens: 100,
			},
			{
				Idx: 1, UserMessage: "carry on", AssistantText: "working",
				StartedAt: at("17:10:00"), EndedAt: at("17:12:00"),
				Model: claudeModel, Effort: "high", CLIVersion: "2.1.0",
				InputTokens: 200, OutputTokens: 80, CacheReadTokens: 1200,
			},
			{
				Idx: 2, UserMessage: verdict, AssistantText: "merged",
				StartedAt: at("17:16:00"), EndedAt: at("17:17:00"),
				Model: claudeModel, InputTokens: 50, OutputTokens: 10,
			},
			{
				Idx: 3, UserMessage: workInvocation("loom/other-0001"), AssistantText: "next",
				StartedAt: at("17:45:00"), EndedAt: at("17:46:00"),
				Model: claudeModel, InputTokens: 999, OutputTokens: 999,
			},
		},
		ToolCalls: []summary.ToolCall{
			{TurnIdx: 0, CallID: "toolu_read", Kind: summary.KindRead, ToolName: "Read", StartedAt: at("17:02:30"), DurationMs: 500},
			{TurnIdx: 0, CallID: lensDispatchID, Kind: summary.KindTask, ToolName: "Agent", KeyArg: "security lens review", StartedAt: at("17:03:00"), DurationMs: 200000},
			{TurnIdx: 1, CallID: "toolu_bash", Kind: summary.KindBash, ToolName: "Bash", KeyArg: "go test", StartedAt: at("17:11:00"), DurationMs: 1000, IsError: true},
			{TurnIdx: 3, CallID: "toolu_later", Kind: summary.KindRead, ToolName: "Read", StartedAt: at("17:45:30"), DurationMs: 100},
		},
		Errors: []summary.ErrorEvent{
			{TurnIdx: 0, Source: "stop_hook", Time: at("17:03:00")},
			{TurnIdx: 1, Source: "api_error", Time: at("17:11:00")},
			{TurnIdx: 1, Source: "tool_error", Time: at("17:11:30")},
			{TurnIdx: 1, Source: "turn_aborted", Time: at("17:12:00")},
			{TurnIdx: 3, Source: "api_error", Time: at("17:45:00")},
		},
	}
	for _, b := range lens.Extract(verdict) {
		sum.LensResponses = append(sum.LensResponses, summary.LensResponse{
			TurnIdx: 2, Origin: summary.OriginTaskNotification, DispatchID: lensDispatchID, SourceLine: 5, At: at("17:16:00"), Block: b,
		})
	}
	return sum
}

// fixture folds the record fixture and every session but the review stage's,
// which stays missing so partial coverage has something to name.
func fixture(t *testing.T) (*summaries.Store, string) {
	t.Helper()
	st, path := openStore(t)
	importFile(t, st, filepath.Join("..", "runs", "testdata", "executions.jsonl"))
	writeSession(t, st, rootSessionSummary())
	writeSession(t, st, oneTurn(summary.AgentClaude, agentSession, claudeModel, at("17:05:40"), at("17:08:00"), 1000, 0, 200, summary.KindBash, 3000))
	codex := oneTurn(summary.AgentCodex, codexSession, codexModel, at("17:06:00"), at("17:08:00"), 1000, 600, 100, summary.KindBash, 2000)
	codex.Errors = []summary.ErrorEvent{{TurnIdx: 0, Source: "exec_error", Time: at("17:07:00")}}
	writeSession(t, st, codex)
	writeSession(t, st, oneTurn(summary.AgentCodex, lensSession, codexModel, at("17:12:00"), at("17:15:00"), 500, 100, 50, summary.KindBash, 1500))
	writeSession(t, st, oneTurn(summary.AgentClaude, work1Session, claudeModel, at("17:20:00"), at("17:24:00"), 300, 0, 30, summary.KindEdit, 700))
	writeSession(t, st, oneTurn(summary.AgentClaude, work2Session, claudeModel, at("17:25:00"), at("17:33:00"), 400, 0, 40, summary.KindEdit, 800))
	return st, path
}

func build(t *testing.T, st *summaries.Store, runID string) *Report {
	t.Helper()
	run, err := runs.Load(st.DB(), runID)
	if err != nil {
		t.Fatal(err)
	}
	table, err := pricing.Default()
	if err != nil {
		t.Fatal(err)
	}
	rep, err := Build(st.DB(), run, table)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func execution(t *testing.T, rep *Report, id string) ExecutionMetrics {
	t.Helper()
	for _, e := range rep.Executions {
		if e.ExecutionID == id {
			return e
		}
	}
	t.Fatalf("no execution %s in report", id)
	return ExecutionMetrics{}
}

func deref(p *float64) float64 {
	if p == nil {
		return -1
	}
	return *p
}

func ids(list []ExecutionMetrics) []string {
	var out []string
	for _, e := range list {
		out = append(out, e.ExecutionID)
	}
	return out
}

func tokens(t *testing.T, m Metrics, runtime string) Tokens {
	t.Helper()
	tok := m.TokensByRuntime[runtime]
	if tok == nil {
		t.Fatalf("no %s tokens in %+v", runtime, m.TokensByRuntime)
	}
	return *tok
}

// AC1: the mixed-runtime fixture yields parent, descendant and total scopes
// and every breakdown, through Load as the command reads it.
func TestReportCoversTheMixedRuntimeFixture(t *testing.T) {
	st, path := fixture(t)
	st.Close()
	rep, err := Load(path, fixtureRun)
	if err != nil {
		t.Fatal(err)
	}

	if rep.Run.RunID != fixtureRun || rep.Run.Ticket != fixtureTicket || rep.Run.Runtime != "claude-code" || rep.Run.Origin != runs.OriginRecord {
		t.Errorf("run = %+v", rep.Run)
	}
	want := []string{"root-195f819e", "agent-a0e0c89b977fd6273", "codex-01a0029b", "lens-security-r1-a1",
		"stage-work-1-1", "stage-work-1-2", "cmd-go-test-1", "stage-review-1-1"}
	if got := ids(rep.Executions); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("executions = %v, want depth-first %v", got, want)
	}
	for _, e := range rep.Executions {
		if e.Placement != PlacementTree || !e.Counted {
			t.Errorf("%s: placement %q counted %v, want tree and counted", e.ExecutionID, e.Placement, e.Counted)
		}
	}

	parent, desc, total := rep.Metrics.Parent, rep.Metrics.Descendants, rep.Metrics.Total
	if parent.Executions != 1 || parent.Transcripts != 1 || parent.Turns != 3 {
		t.Errorf("parent = %d executions %d transcripts %d turns, want 1/1/3 (the invocation's span, not the whole session)", parent.Executions, parent.Transcripts, parent.Turns)
	}
	if desc.Executions != 7 || desc.Transcripts != 5 || desc.Turns != 5 {
		t.Errorf("descendants = %d executions %d transcripts %d turns, want 7/5/5", desc.Executions, desc.Transcripts, desc.Turns)
	}
	if total.Executions != 8 || total.Transcripts != 6 || total.Turns != 8 {
		t.Errorf("total = %d executions %d transcripts %d turns, want 8/6/8", total.Executions, total.Transcripts, total.Turns)
	}
	if parent.ToolCalls != 3 || parent.ToolCallsByKind["read"] != 1 || parent.ToolCallsByKind["task"] != 1 || parent.ToolCallsByKind["bash"] != 1 {
		t.Errorf("parent tool calls = %d %v, want 3 read/task/bash", parent.ToolCalls, parent.ToolCallsByKind)
	}
	if parent.ToolTimeMs != 201500 || parent.ToolCallsErrored != 1 {
		t.Errorf("parent tool time = %d errored = %d, want 201500 and 1", parent.ToolTimeMs, parent.ToolCallsErrored)
	}
	// 100·5 + 50·25 + 900·0.5 + 200·6.25 + 100·10, then 200·5 + 80·25 + 1200·0.5,
	// then 50·5 + 10·25, per million.
	if parent.CostUSD == nil || *parent.CostUSD != 0.00855 || !parent.Pricing.Available || parent.Pricing.Currency != "USD" {
		t.Errorf("parent cost = %v pricing = %+v, want 0.00855 USD available", deref(parent.CostUSD), parent.Pricing)
	}

	// Per-execution: a node's own span, and the transcript-less command
	// contributing only its recorded duration.
	sub := execution(t, rep, "agent-a0e0c89b977fd6273")
	if sub.Metrics.Turns != 1 || tokens(t, sub.Metrics, "claude-code").Input != 1000 || sub.DurationMs == nil || *sub.DurationMs != 202000 {
		t.Errorf("subagent = %+v", sub.Metrics)
	}
	cmd := execution(t, rep, "cmd-go-test-1")
	if cmd.Transcript != nil || cmd.Metrics.Turns != 0 || cmd.Metrics.Transcripts != 0 || cmd.DurationMs == nil || *cmd.DurationMs != 17000 {
		t.Errorf("command = %+v", cmd)
	}
	if len(cmd.Metrics.TokensByRuntime) != 0 || cmd.Metrics.Models == nil || len(cmd.Metrics.Models) != 0 {
		t.Errorf("command metrics carry %v tokens and models %v, want none", cmd.Metrics.TokensByRuntime, cmd.Metrics.Models)
	}

	// Stage breakdown: the work stage retried once, the review did not.
	if len(rep.Stages) != 2 {
		t.Fatalf("stages = %+v, want work/1 and review/1", rep.Stages)
	}
	work := rep.Stages[0]
	if work.Stage != "work" || work.Occurrence != 1 || work.Retries != 1 || len(work.Attempts) != 2 {
		t.Errorf("work stage = %+v", work)
	}
	if work.Attempts[0].Outcome != "failed" || work.Attempts[1].Outcome != "completed" || work.Attempts[1].Metrics.Turns != 1 {
		t.Errorf("work attempts = %+v", work.Attempts)
	}
	if rep.Stages[1].Stage != "review" || rep.Stages[1].Retries != 0 || rep.Stages[1].Attempts[0].Outcome != "stopped" {
		t.Errorf("review stage = %+v", rep.Stages[1])
	}

	// Lens breakdown: the execution record joined with the transcript's attempt.
	if len(rep.Lenses) != 1 || rep.Lenses[0].Lens != "security" || rep.Lenses[0].Round != 1 || len(rep.Lenses[0].Attempts) != 1 {
		t.Fatalf("lenses = %+v", rep.Lenses)
	}
	la := rep.Lenses[0].Attempts[0]
	if la.ExecutionID != "lens-security-r1-a1" || !la.Recorded || la.Status != "parsed" || la.Verdict != "satisfied" || la.Metrics == nil || la.Metrics.Turns != 1 {
		t.Errorf("lens attempt = %+v", la)
	}
	if len(rep.Attempts) != 4 {
		t.Errorf("attempts = %+v, want 3 stage and 1 lens", rep.Attempts)
	}

	// The tree, unresolved list and diagnostics render with [] for empty lists.
	if rep.Tree == nil || rep.Tree.ExecutionID != "root-195f819e" || len(rep.Tree.Children) != 5 {
		t.Fatalf("tree = %+v", rep.Tree)
	}
	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"children":null`) || strings.Contains(string(raw), `"unresolved":null`) || strings.Contains(string(raw), `"diagnostics":null`) || strings.Contains(string(raw), `:null,"models"`) {
		t.Errorf("report renders a null list:\n%s", raw)
	}
}

// AC2: every transcript is counted once, totals reconcile with parent plus
// descendants, and the review stage's missing session is a named gap rather
// than a zero.
func TestTotalsReconcileAndGapsAreExplicit(t *testing.T) {
	st, _ := fixture(t)
	rep := build(t, st, fixtureRun)
	parent, desc, total := rep.Metrics.Parent, rep.Metrics.Descendants, rep.Metrics.Total

	claude := tokens(t, parent, "claude-code")
	if claude != (Tokens{Input: 350, Output: 140, CacheRead: 2100, CacheWrite: 300, CacheWrite1h: 100, CacheSemantics: CacheSeparate, Total: 2890}) {
		t.Errorf("parent claude tokens = %+v", claude)
	}
	if _, ok := parent.TokensByRuntime["codex-cli"]; ok {
		t.Errorf("parent counts codex tokens: %+v", parent.TokensByRuntime)
	}
	codex := tokens(t, desc, "codex-cli")
	if codex != (Tokens{Input: 1500, Output: 150, CacheRead: 700, CacheSemantics: CacheReadInsideInput, Total: 1650}) {
		t.Errorf("descendant codex tokens = %+v, want cache read inside input and not re-added", codex)
	}
	if got := tokens(t, desc, "claude-code"); got.Input != 1700 || got.Output != 270 || got.Total != 1970 {
		t.Errorf("descendant claude tokens = %+v", got)
	}
	if desc.TotalTokens != 3620 || parent.TotalTokens != 2890 || total.TotalTokens != 6510 {
		t.Errorf("total tokens = %d/%d/%d, want 2890/3620/6510", parent.TotalTokens, desc.TotalTokens, total.TotalTokens)
	}

	for _, c := range []struct {
		name          string
		p, d, sum     int64
		bothNonEmpty  bool
		allowZeroBoth bool
	}{
		{"executions", int64(parent.Executions), int64(desc.Executions), int64(total.Executions), true, false},
		{"transcripts", int64(parent.Transcripts), int64(desc.Transcripts), int64(total.Transcripts), true, false},
		{"turns", int64(parent.Turns), int64(desc.Turns), int64(total.Turns), true, false},
		{"tool_calls", int64(parent.ToolCalls), int64(desc.ToolCalls), int64(total.ToolCalls), true, false},
		{"tool_time_ms", parent.ToolTimeMs, desc.ToolTimeMs, total.ToolTimeMs, true, false},
		{"total_tokens", parent.TotalTokens, desc.TotalTokens, total.TotalTokens, true, false},
		{"failures.tool", int64(parent.Failures.Tool), int64(desc.Failures.Tool), int64(total.Failures.Tool), true, false},
		{"failures.api", int64(parent.Failures.API), int64(desc.Failures.API), int64(total.Failures.API), false, false},
		{"hook_signals", int64(parent.HookSignals), int64(desc.HookSignals), int64(total.HookSignals), false, false},
		{"human_interactions", int64(parent.HumanInteractions), int64(desc.HumanInteractions), int64(total.HumanInteractions), false, false},
		{"execution_time_ms", parent.ExecutionTimeMs, desc.ExecutionTimeMs, total.ExecutionTimeMs, false, false},
		{"legacy_active_ms", parent.LegacyActiveMs, desc.LegacyActiveMs, total.LegacyActiveMs, true, false},
	} {
		if c.p+c.d != c.sum {
			t.Errorf("%s: parent %d + descendants %d != total %d", c.name, c.p, c.d, c.sum)
		}
		if c.bothNonEmpty && (c.p == 0 || c.d == 0) {
			t.Errorf("%s: parent %d descendants %d, want both non-zero for the reconciliation to mean anything", c.name, c.p, c.d)
		}
	}
	if total.Failures.Tool != 2 || desc.Failures.Tool != 1 {
		t.Errorf("tool failures = total %d descendants %d, want the root's tool_error and the codex exec_error", total.Failures.Tool, desc.Failures.Tool)
	}
	if desc.HumanInteractions != 0 || total.HumanInteractions != parent.HumanInteractions {
		t.Errorf("human_interactions = parent %d descendants %d total %d, want the descendants' agent-authored prompts uncounted", parent.HumanInteractions, desc.HumanInteractions, total.HumanInteractions)
	}

	tel := rep.Telemetry
	if tel.State != StatePartial || tel.RootSpan != SpanInvocation {
		t.Errorf("telemetry = %+v, want partial over the invocation span", tel)
	}
	if tel.ExecutionsTotal != 8 || tel.ExecutionsWithTranscript != 7 || tel.TranscriptsCounted != 6 {
		t.Errorf("telemetry counts = %+v", tel)
	}
	if !reflect.DeepEqual(tel.ExecutionsWithoutTranscript, []string{"cmd-go-test-1"}) {
		t.Errorf("without transcript = %v", tel.ExecutionsWithoutTranscript)
	}
	gaps := strings.Join(tel.Gaps, "\n")
	if !strings.Contains(gaps, "session claude-code/"+reviewSession+" not in summaries.db") {
		t.Errorf("gaps = %v, want the review stage's missing session named", tel.Gaps)
	}
	review := execution(t, rep, "stage-review-1-1")
	if !review.Counted || review.Metrics.Transcripts != 0 || review.Metrics.Turns != 0 || review.Metrics.Executions != 1 {
		t.Errorf("review stage with no session = %+v, want counted as an execution with no transcript metered", review.Metrics)
	}
}

// AC2: a session two executions name is counted once — by the first — and
// nested children and retries each count once. Also AC3: two stages running
// in parallel add to execution time and not to the wall.
func TestSharedSessionCountsOnceAndParallelChildrenAdd(t *testing.T) {
	st, _ := openStore(t)
	importLines(t, st,
		`{"v":1,"kind":"run","run_id":"run-par","ticket":"loom/par-0001","runtime":"claude-code","agent":"claude-code","session_id":"par-root","started_at":"2026-09-10T10:00:00Z","ended_at":"2026-09-10T10:10:00Z","outcome":"completed"}`,
		`{"v":1,"kind":"execution","execution_id":"root-par","run_id":"run-par","execution_kind":"root","agent":"claude-code","session_id":"par-root","started_at":"2026-09-10T10:00:00Z","ended_at":"2026-09-10T10:10:00Z","outcome":"completed"}`,
		`{"v":1,"kind":"execution","execution_id":"stage-a","run_id":"run-par","parent_execution_id":"root-par","execution_kind":"stage","stage":"work","stage_occurrence":1,"attempt":1,"agent":"claude-code","session_id":"par-a","started_at":"2026-09-10T10:00:00Z","ended_at":"2026-09-10T10:10:00Z","outcome":"failed"}`,
		`{"v":1,"kind":"execution","execution_id":"stage-a2","run_id":"run-par","parent_execution_id":"root-par","execution_kind":"stage","stage":"work","stage_occurrence":1,"attempt":2,"agent":"claude-code","session_id":"par-a2","started_at":"2026-09-10T10:00:30Z","ended_at":"2026-09-10T10:10:00Z","outcome":"completed"}`,
		`{"v":1,"kind":"execution","execution_id":"stage-b","run_id":"run-par","parent_execution_id":"root-par","execution_kind":"stage","stage":"work","stage_occurrence":2,"attempt":1,"agent":"claude-code","session_id":"par-b","started_at":"2026-09-10T10:01:00Z","ended_at":"2026-09-10T10:10:00Z","outcome":"completed"}`,
		`{"v":1,"kind":"execution","execution_id":"stage-c","run_id":"run-par","parent_execution_id":"stage-b","execution_kind":"stage","stage":"review","stage_occurrence":1,"attempt":1,"agent":"claude-code","session_id":"par-root","started_at":"2026-09-10T10:05:00Z","ended_at":"2026-09-10T10:08:00Z","outcome":"completed"}`,
	)
	start := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	root := oneTurn(summary.AgentClaude, "par-root", claudeModel, start, start.Add(10*time.Minute), 100, 0, 10, summary.KindBash, 100)
	root.Turns[0].UserMessage = workInvocation("loom/par-0001")
	// A later /work in the same session is another run's turn: stage-c's own
	// meter walks the whole session, but the run's last observation must not.
	root.Turns = append(root.Turns, summary.Turn{
		Idx: 1, UserMessage: workInvocation("loom/par-0002"), AssistantText: "next",
		StartedAt: start.Add(15 * time.Minute), EndedAt: start.Add(20 * time.Minute), Model: claudeModel, InputTokens: 50, OutputTokens: 5,
	})
	root.EndTime = start.Add(20 * time.Minute)
	writeSession(t, st, root)
	writeSession(t, st, oneTurn(summary.AgentClaude, "par-a", claudeModel, start, start.Add(10*time.Minute), 200, 0, 20, summary.KindEdit, 100))
	writeSession(t, st, oneTurn(summary.AgentClaude, "par-a2", claudeModel, start, start.Add(10*time.Minute), 300, 0, 30, summary.KindEdit, 100))
	writeSession(t, st, oneTurn(summary.AgentClaude, "par-b", claudeModel, start, start.Add(10*time.Minute), 400, 0, 40, summary.KindEdit, 100))

	rep := build(t, st, "run-par")
	c := execution(t, rep, "stage-c")
	if c.Counted || c.CountedBy != "root-par" {
		t.Errorf("stage-c sharing the root's session: counted %v by %q, want not counted, by root-par", c.Counted, c.CountedBy)
	}
	if c.Metrics.Turns != 2 {
		t.Errorf("stage-c's own metrics = %+v, want its session still metered", c.Metrics)
	}
	if rep.Run.LastObservedAt != "2026-09-10T10:10:00Z" {
		t.Errorf("last_observed_at = %q, want the run's end, not the next run's turn seen through stage-c", rep.Run.LastObservedAt)
	}
	total := rep.Metrics.Total
	if total.Executions != 5 || total.Transcripts != 4 || total.Turns != 4 || total.TotalTokens != 1100 {
		t.Errorf("total = %d executions %d transcripts %d turns %d tokens, want 5/4/4/1100", total.Executions, total.Transcripts, total.Turns, total.TotalTokens)
	}
	if rep.Metrics.Parent.TotalTokens+rep.Metrics.Descendants.TotalTokens != total.TotalTokens || rep.Metrics.Descendants.Transcripts != 3 {
		t.Errorf("scopes = parent %+v descendants %+v", rep.Metrics.Parent, rep.Metrics.Descendants)
	}
	if len(rep.Stages) != 3 || rep.Stages[0].Retries != 1 || len(rep.Stages[0].Attempts) != 2 || len(rep.Attempts) != 4 {
		t.Errorf("stages = %+v attempts = %+v", rep.Stages, rep.Attempts)
	}
	if rep.Telemetry.State != StateComplete || len(rep.Telemetry.Gaps) != 0 {
		t.Errorf("telemetry = %+v, want complete", rep.Telemetry)
	}

	// Wall is one 10-minute span; execution time sums four overlapping spans.
	wall := int64(10 * 60 * 1000)
	if rep.Time.WallMs == nil || *rep.Time.WallMs != wall || rep.Time.WallBasis != WallEnded {
		t.Fatalf("wall = %v %q, want %d %q", rep.Time.WallMs, rep.Time.WallBasis, wall, WallEnded)
	}
	wantExec := wall + wall + (wall - 30000) + (wall - 60000) + 180000
	if rep.Time.ExecutionTimeMs != wantExec || rep.Time.ExecutionTimeMs <= *rep.Time.WallMs {
		t.Errorf("execution_time_ms = %d, want %d and more than the wall", rep.Time.ExecutionTimeMs, wantExec)
	}
	if total.ExecutionTimeCoverage != (Coverage{Timed: 5}) {
		t.Errorf("execution time coverage = %+v", total.ExecutionTimeCoverage)
	}

	// Still running, the wall runs to the last observation, which the next
	// run's turn in the shared session must not extend.
	importLines(t, st,
		`{"v":1,"kind":"run","run_id":"run-live","ticket":"loom/live-0001","runtime":"claude-code","agent":"claude-code","session_id":"live-root","started_at":"2026-09-10T10:00:00Z"}`,
		`{"v":1,"kind":"execution","execution_id":"root-live","run_id":"run-live","execution_kind":"root","agent":"claude-code","session_id":"live-root","started_at":"2026-09-10T10:00:00Z"}`,
		`{"v":1,"kind":"execution","execution_id":"stage-live","run_id":"run-live","parent_execution_id":"root-live","execution_kind":"stage","stage":"review","stage_occurrence":1,"attempt":1,"agent":"claude-code","session_id":"live-root","started_at":"2026-09-10T10:05:00Z","ended_at":"2026-09-10T10:08:00Z","outcome":"completed"}`,
	)
	root.SessionID = "live-root"
	root.Turns[0].UserMessage = workInvocation("loom/live-0001")
	root.Turns[1].UserMessage = workInvocation("loom/live-0002")
	writeSession(t, st, root)
	live := build(t, st, "run-live")
	if live.Run.Outcome != OutcomeRunning || live.Run.LastObservedAt != "2026-09-10T10:10:00Z" {
		t.Errorf("running run: outcome %q last_observed_at %q, want running at the root turn's end", live.Run.Outcome, live.Run.LastObservedAt)
	}
	if live.Time.WallMs == nil || *live.Time.WallMs != wall || live.Time.WallBasis != WallLastObserved {
		t.Errorf("running wall = %v %q, want %d to the last observation", live.Time.WallMs, live.Time.WallBasis, wall)
	}
}

// AC3: the legacy active_ms is carried with its semantics beside it and
// beside the distinct wall, execution and tool times.
func TestLegacyActiveMsIsLabelled(t *testing.T) {
	st, _ := fixture(t)
	rep := build(t, st, fixtureRun)

	// Root span: turn wall clocks 60000+120000+60000, tools 500+200000+1000.
	if rep.Metrics.Parent.LegacyActiveMs != 441500 || rep.Metrics.Parent.LegacyActiveMsSemantics != LegacyActiveMsSemantics {
		t.Errorf("parent legacy_active_ms = %d %q", rep.Metrics.Parent.LegacyActiveMs, rep.Metrics.Parent.LegacyActiveMsSemantics)
	}
	tm := rep.Time
	if tm.WallMs == nil || *tm.WallMs != 2269000 || tm.WallBasis != WallEnded {
		t.Errorf("wall = %v %q, want 2269000 started_at→ended_at", tm.WallMs, tm.WallBasis)
	}
	// Every timed execution but the root, which never got a terminal record.
	if tm.ExecutionTimeMs != 202000+150000+200000+250000+520000+120000+17000 {
		t.Errorf("execution_time_ms = %d", tm.ExecutionTimeMs)
	}
	if rep.Metrics.Total.ExecutionTimeCoverage != (Coverage{Timed: 7, Untimed: 1}) {
		t.Errorf("execution time coverage = %+v", rep.Metrics.Total.ExecutionTimeCoverage)
	}
	// The top-level figure is the parent span's, comparable to cost-report's
	// active_ms; the sum over descendants stays under metrics.total.
	if tm.ToolTimeMs != 201500+3000+2000+1500+700+800 || tm.LegacyActiveMs != rep.Metrics.Parent.LegacyActiveMs || tm.LegacyActiveMsSemantics != LegacyActiveMsSemantics {
		t.Errorf("time = %+v", tm)
	}
	if rep.Metrics.Total.LegacyActiveMs <= rep.Metrics.Parent.LegacyActiveMs {
		t.Errorf("total legacy_active_ms %d does not exceed parent %d; descendants carry terms", rep.Metrics.Total.LegacyActiveMs, rep.Metrics.Parent.LegacyActiveMs)
	}
	if tm.LegacyActiveMs == tm.ExecutionTimeMs || tm.LegacyActiveMs == *tm.WallMs {
		t.Errorf("legacy_active_ms %d coincides with another measure; it is its own thing", tm.LegacyActiveMs)
	}
}

// AC4: failures are classed by source apart from hook signals, and the
// conditions in force are reported with how many turns carried them.
func TestFailuresConditionsAndHumanCoverage(t *testing.T) {
	st, _ := fixture(t)
	rep := build(t, st, fixtureRun)
	p := rep.Metrics.Parent

	if p.Failures.Tool != 1 || p.Failures.API != 1 || p.Failures.Process != 1 || p.Failures.Other != 0 || len(p.Failures.OtherBySource) != 0 {
		t.Errorf("parent failures = %+v", p.Failures)
	}
	if p.HookSignals != 1 {
		t.Errorf("hook_signals = %d, want the stop_hook counted apart", p.HookSignals)
	}
	if rep.Metrics.Total.Failures.Tool != 2 || rep.Metrics.Total.Failures.API != 1 || rep.Metrics.Total.HookSignals != 1 {
		t.Errorf("total failures = %+v hooks %d", rep.Metrics.Total.Failures, rep.Metrics.Total.HookSignals)
	}
	if p.HumanInteractions != 1 {
		t.Errorf("human_interactions = %d, want 1: the typed turn, not the invocation or the notification", p.HumanInteractions)
	}
	if !reflect.DeepEqual(p.Models, []string{claudeModel}) || !reflect.DeepEqual(p.Efforts, []string{"high"}) || !reflect.DeepEqual(p.CLIVersions, []string{"2.1.0"}) {
		t.Errorf("conditions = %v %v %v", p.Models, p.Efforts, p.CLIVersions)
	}
	if p.ConditionsCoverage != (ConditionsCoverage{TurnsWithModel: 3, TurnsWithEffort: 2, TurnsWithCLIVersion: 2, Turns: 3}) {
		t.Errorf("conditions coverage = %+v", p.ConditionsCoverage)
	}
	total := rep.Metrics.Total
	if !reflect.DeepEqual(total.Models, []string{claudeModel, codexModel}) {
		t.Errorf("total models = %v, want both runtimes' in first-seen order", total.Models)
	}
	if total.ConditionsCoverage.TurnsWithEffort != 2 || total.ConditionsCoverage.Turns != 8 {
		t.Errorf("total conditions coverage = %+v", total.ConditionsCoverage)
	}
	// A scope that recorded nothing reads as [] with a coverage of 0 of n.
	lensExec := execution(t, rep, "lens-security-r1-a1")
	if len(lensExec.Metrics.Efforts) != 0 || lensExec.Metrics.Efforts == nil || lensExec.Metrics.ConditionsCoverage != (ConditionsCoverage{TurnsWithModel: 1, Turns: 1}) {
		t.Errorf("lens efforts = %v coverage %+v", lensExec.Metrics.Efforts, lensExec.Metrics.ConditionsCoverage)
	}
}

// AC5: the outcome is the record's; telemetry completeness stands apart. A
// start-only record reads running with the wall to the last observation,
// and the terminal record arriving later flips it without changing usage.
func TestOutcomeStandsApartFromTelemetryAndLateRecords(t *testing.T) {
	st, _ := fixture(t)
	rep := build(t, st, fixtureRun)
	if rep.Run.Outcome != "completed" || rep.Telemetry.State != StatePartial {
		t.Errorf("fixture run: outcome %q telemetry %q, want completed and partial (root never closed, review session missing)", rep.Run.Outcome, rep.Telemetry.State)
	}
	if !reflect.DeepEqual(rep.Telemetry.ExecutionsPending, []string{"root-195f819e"}) {
		t.Errorf("pending = %v", rep.Telemetry.ExecutionsPending)
	}
	if rep.Run.LastObservedAt != "2026-09-10T17:40:00Z" {
		t.Errorf("last_observed_at = %q, want the run's end", rep.Run.LastObservedAt)
	}

	for _, outcome := range []string{"failed", "stopped"} {
		importLines(t, st,
			`{"v":1,"kind":"run","run_id":"run-`+outcome+`","ticket":"loom/o-0001","runtime":"claude-code","agent":"claude-code","session_id":"sess-`+outcome+`","started_at":"2026-09-10T12:00:00Z","ended_at":"2026-09-10T12:01:00Z","outcome":"`+outcome+`"}`,
			`{"v":1,"kind":"execution","execution_id":"root-`+outcome+`","run_id":"run-`+outcome+`","execution_kind":"root","agent":"claude-code","session_id":"sess-`+outcome+`","started_at":"2026-09-10T12:00:00Z"}`,
		)
		r := build(t, st, "run-"+outcome)
		if r.Run.Outcome != outcome || r.Telemetry.State != StatePartial {
			t.Errorf("%s run: outcome %q telemetry %q, want the record's outcome beside partial telemetry", outcome, r.Run.Outcome, r.Telemetry.State)
		}
	}

	// Late records.
	late, _ := openStore(t)
	start, err := os.ReadFile(filepath.Join("..", "runs", "testdata", "late_start.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "executions.jsonl")
	if err := os.WriteFile(path, start, 0o644); err != nil {
		t.Fatal(err)
	}
	importFile(t, late, path)
	begin := time.Date(2026, 9, 10, 14, 0, 0, 0, time.UTC)
	sess := oneTurn(summary.AgentClaude, "sess-late", claudeModel, begin, begin.Add(15*time.Minute), 700, 100, 70, summary.KindBash, 400)
	sess.Turns[0].UserMessage = workInvocation("loom/late-3333")
	writeSession(t, late, sess)

	running := build(t, late, "run-late")
	if running.Run.Outcome != OutcomeRunning || running.Run.EndedAt != "" {
		t.Errorf("start-only run: outcome %q ended %q, want running", running.Run.Outcome, running.Run.EndedAt)
	}
	if running.Run.LastObservedAt != "2026-09-10T14:15:00Z" {
		t.Errorf("last_observed_at = %q, want the turn's end", running.Run.LastObservedAt)
	}
	if running.Time.WallMs == nil || *running.Time.WallMs != 15*60*1000 || running.Time.WallBasis != WallLastObserved {
		t.Errorf("running wall = %v %q, want 900000 to the last observation", running.Time.WallMs, running.Time.WallBasis)
	}
	if running.Telemetry.State != StatePartial || !reflect.DeepEqual(running.Telemetry.ExecutionsPending, []string{"root-late"}) {
		t.Errorf("running telemetry = %+v", running.Telemetry)
	}
	if running.Metrics.Total.Turns != 1 || running.Metrics.Total.TotalTokens != 870 {
		t.Errorf("running usage = %d turns %d tokens", running.Metrics.Total.Turns, running.Metrics.Total.TotalTokens)
	}

	terminal, err := os.ReadFile(filepath.Join("..", "runs", "testdata", "late_terminal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(start, terminal...), 0o644); err != nil {
		t.Fatal(err)
	}
	importFile(t, late, path)
	done := build(t, late, "run-late")
	if done.Run.Outcome != "completed" || done.Run.EndedAt != "2026-09-10T14:30:00Z" {
		t.Errorf("reconciled run: outcome %q ended %q", done.Run.Outcome, done.Run.EndedAt)
	}
	if done.Time.WallMs == nil || *done.Time.WallMs != 30*60*1000 || done.Time.WallBasis != WallEnded {
		t.Errorf("reconciled wall = %v %q", done.Time.WallMs, done.Time.WallBasis)
	}
	if done.Telemetry.State != StatePartial || !reflect.DeepEqual(done.Telemetry.Gaps, []string{"execution stage-late-work-1-1 has no transcript", "execution stage-late-work-1-2 has no transcript"}) {
		t.Errorf("reconciled telemetry = %+v, want missing stage transcripts named", done.Telemetry)
	}
	before, after := running.Metrics.Parent, done.Metrics.Parent
	if before.Turns != after.Turns || before.ToolCalls != after.ToolCalls || before.LegacyActiveMs != after.LegacyActiveMs ||
		!reflect.DeepEqual(before.TokensByRuntime, after.TokensByRuntime) || deref(before.CostUSD) != deref(after.CostUSD) {
		t.Errorf("terminal record changed parent usage:\n%+v\n%+v", before, after)
	}
	// What did change is what the record said: the root now has an end.
	if before.ExecutionTimeCoverage != (Coverage{Untimed: 1}) || after.ExecutionTimeMs != 30*60*1000 {
		t.Errorf("execution time before %+v after %d", before.ExecutionTimeCoverage, after.ExecutionTimeMs)
	}
	if done.Metrics.Total.Turns != 1 || done.Metrics.Total.TotalTokens != 870 || done.Metrics.Total.Executions != 3 {
		t.Errorf("reconciled usage = %+v", done.Metrics.Total)
	}
	// Completed stage attempts without transcripts remain measurement gaps.
	if !reflect.DeepEqual(done.Telemetry.ExecutionsWithoutTranscript, []string{"stage-late-work-1-1", "stage-late-work-1-2"}) {
		t.Errorf("without transcript = %v", done.Telemetry.ExecutionsWithoutTranscript)
	}
}

// AC2/AC5: a run nobody instrumented reports unknown outcome and meters its
// subagent rows once each, naming the dispatch with no usage as a gap.
func TestHistoricalRunMetersSubagentRows(t *testing.T) {
	st, _ := openStore(t)
	start := time.Date(2026, 9, 10, 9, 0, 0, 0, time.UTC)
	measured := int64(30000)
	sum := &summary.SessionSummary{
		SessionID: "hist",
		Agent:     summary.AgentClaude,
		StartTime: start,
		EndTime:   start.Add(time.Hour),
		Turns: []summary.Turn{
			{Idx: 0, UserMessage: workInvocation("loom/hist-0001"), AssistantText: "on it", StartedAt: start, EndedAt: start.Add(time.Minute), Model: claudeModel, InputTokens: 100, OutputTokens: 10},
		},
		Subagents: []summary.Subagent{
			{ParentTurnIdx: 0, AgentType: "coder", DurationMs: &measured, Usage: &summary.SubagentUsage{Model: claudeModel, InputTokens: 2000, OutputTokens: 300, CacheReadTokens: 500}},
			{ParentTurnIdx: 0, AgentType: "reviewer"},
		},
	}
	writeSession(t, st, sum)
	writeSession(t, st, &summary.SessionSummary{
		SessionID: "hist-codex", Agent: summary.AgentCodex, ParentSessionID: "hist", SpawnDepth: 1,
		StartTime: start.Add(5 * time.Minute), EndTime: start.Add(8 * time.Minute),
		Turns: []summary.Turn{{Idx: 0, UserMessage: "review", AssistantText: "fine", StartedAt: start.Add(5 * time.Minute), EndedAt: start.Add(8 * time.Minute), Model: codexModel, InputTokens: 800, CacheReadTokens: 300, OutputTokens: 80}},
	})

	rep := build(t, st, "transcript:claude-code:hist:0")
	if rep.Run.Origin != runs.OriginTranscript || rep.Run.Outcome != OutcomeUnknown || rep.Run.Source != nil {
		t.Errorf("historical run = %+v", rep.Run)
	}
	if rep.Telemetry.RootSpan != SpanInvocation || rep.Time.WallMs == nil || *rep.Time.WallMs != 60*60*1000 {
		t.Errorf("telemetry = %+v time = %+v", rep.Telemetry, rep.Time)
	}
	coder := execution(t, rep, "transcript:claude-code:hist:0:subagent:0")
	if coder.AgentType != "coder" || coder.DurationMs == nil || *coder.DurationMs != 30000 || coder.Metrics.Transcripts != 0 {
		t.Errorf("coder dispatch = %+v", coder)
	}
	if got := tokens(t, coder.Metrics, "claude-code"); got.Input != 2000 || got.CacheRead != 500 || got.Total != 2800 {
		t.Errorf("coder tokens = %+v", got)
	}
	if coder.Metrics.CostUSD == nil || *coder.Metrics.CostUSD != 0.01775 {
		t.Errorf("coder cost = %v, want 0.01775", coder.Metrics.CostUSD)
	}
	reviewer := execution(t, rep, "transcript:claude-code:hist:0:subagent:1")
	if !reviewer.Metrics.TokenUsageUnavailable || !rep.Metrics.Total.TokenUsageUnavailable {
		t.Error("missing historical subagent usage reported as measured tokens")
	}
	if reviewer.DurationMs != nil || len(reviewer.Metrics.TokensByRuntime) != 0 || reviewer.Metrics.CostUSD != nil {
		t.Errorf("reviewer dispatch with no transcript = %+v, want nothing metered and no price", reviewer.Metrics)
	}
	gaps := strings.Join(rep.Telemetry.Gaps, "\n")
	if rep.Telemetry.State != StatePartial || !strings.Contains(gaps, "transcript:claude-code:hist:0:subagent:1 has no transcript usage") {
		t.Errorf("telemetry = %+v, want the usage-less dispatch named", rep.Telemetry)
	}
	// A dispatch row with no duration_ms was not measured; it is not pending.
	if !strings.Contains(gaps, "transcript:claude-code:hist:0:subagent:1 duration unmeasured") || strings.Contains(gaps, "still pending") {
		t.Errorf("gaps = %v, want the unmeasured duration named and nothing pending", rep.Telemetry.Gaps)
	}
	if len(rep.Telemetry.ExecutionsPending) != 0 {
		t.Errorf("pending = %v, want none for a historical run", rep.Telemetry.ExecutionsPending)
	}
	total := rep.Metrics.Total
	if total.Executions != 4 || total.Transcripts != 2 || total.TotalTokens != 110+2800+880 {
		t.Errorf("total = %d executions %d transcripts %d tokens", total.Executions, total.Transcripts, total.TotalTokens)
	}
	if total.CostUSD != nil || !strings.Contains(strings.Join(total.PricingWarnings, "\n"), "no transcript") {
		t.Errorf("total cost = %v warnings = %v, want null with the cause", total.CostUSD, total.PricingWarnings)
	}
}

// AC6: a model the rate table does not carry leaves cost null with the
// cause named, and every other metric standing.
func TestMissingRateLeavesMetricsReadable(t *testing.T) {
	st, _ := fixture(t)
	rep := build(t, st, fixtureRun)

	codex := execution(t, rep, "codex-01a0029b").Metrics
	if codex.CostUSD != nil || codex.Pricing.Available {
		t.Errorf("codex cost = %v pricing = %+v, want unavailable", codex.CostUSD, codex.Pricing)
	}
	if !reflect.DeepEqual(codex.PricingWarnings, []string{`unpriced model "` + codexModel + `" at 2026-09-10`}) {
		t.Errorf("codex warnings = %v", codex.PricingWarnings)
	}
	if codex.Turns != 1 || codex.ToolCalls != 1 || codex.TotalTokens != 1100 || codex.Failures.Tool != 1 {
		t.Errorf("codex metrics beside the missing rate = %+v", codex)
	}
	total := rep.Metrics.Total
	if total.CostUSD != nil || total.Pricing.Available || total.Pricing.Source == "" || total.Pricing.Checked == "" {
		t.Errorf("total cost = %v pricing = %+v", total.CostUSD, total.Pricing)
	}
	if total.Turns != 8 || total.TotalTokens != 6510 {
		t.Errorf("total metrics beside the missing rate = %d turns %d tokens", total.Turns, total.TotalTokens)
	}
	if rep.Metrics.Parent.CostUSD == nil {
		t.Error("parent cost is null though every parent turn priced")
	}

	// A Claude model absent from rates.json, on its own.
	other, _ := openStore(t)
	importLines(t, other,
		`{"v":1,"kind":"run","run_id":"run-unpriced","ticket":"loom/u-0001","runtime":"claude-code","agent":"claude-code","session_id":"sess-u","started_at":"2026-09-10T12:00:00Z","ended_at":"2026-09-10T12:10:00Z","outcome":"completed"}`,
		`{"v":1,"kind":"execution","execution_id":"root-u","run_id":"run-unpriced","execution_kind":"root","agent":"claude-code","session_id":"sess-u","started_at":"2026-09-10T12:00:00Z","ended_at":"2026-09-10T12:10:00Z","outcome":"completed"}`,
	)
	begin := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	sess := oneTurn(summary.AgentClaude, "sess-u", "claude-unknown-9", begin, begin.Add(time.Minute), 100, 50, 10, summary.KindRead, 200)
	sess.Turns[0].UserMessage = workInvocation("loom/u-0001")
	writeSession(t, other, sess)
	r := build(t, other, "run-unpriced")
	m := r.Metrics.Total
	if m.CostUSD != nil || !reflect.DeepEqual(m.PricingWarnings, []string{`unpriced model "claude-unknown-9" at 2026-09-10`}) || m.Pricing.Available {
		t.Errorf("unpriced run: cost %v warnings %v pricing %+v", m.CostUSD, m.PricingWarnings, m.Pricing)
	}
	if m.Turns != 1 || m.ToolCalls != 1 || m.TotalTokens != 160 || !reflect.DeepEqual(m.Models, []string{"claude-unknown-9"}) {
		t.Errorf("unpriced run metrics = %+v", m)
	}
	if r.Telemetry.State != StateComplete {
		t.Errorf("a missing rate is not a telemetry gap: %+v", r.Telemetry)
	}
}

// AC6: a child whose named session has not arrived leaves its own cost and
// every scope it counts toward null with the cause, while the parent stays
// priced; the session arriving prices it.
func TestCompletedChildWithoutTranscriptIsTelemetryGap(t *testing.T) {
	st, _ := openStore(t)
	importLines(t, st,
		`{"v":1,"kind":"run","run_id":"run-no-child-transcript","ticket":"loom/m-0001","runtime":"claude-code","agent":"claude-code","session_id":"measured-root","started_at":"2026-09-10T12:00:00Z","ended_at":"2026-09-10T12:10:00Z","outcome":"completed"}`,
		`{"v":1,"kind":"execution","execution_id":"root-measured","run_id":"run-no-child-transcript","execution_kind":"root","agent":"claude-code","session_id":"measured-root","started_at":"2026-09-10T12:00:00Z","ended_at":"2026-09-10T12:10:00Z","outcome":"completed"}`,
		`{"v":1,"kind":"execution","execution_id":"child-no-transcript","run_id":"run-no-child-transcript","parent_execution_id":"root-measured","execution_kind":"subagent","started_at":"2026-09-10T12:01:00Z","ended_at":"2026-09-10T12:05:00Z","outcome":"completed"}`,
	)
	begin := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	root := oneTurn(summary.AgentClaude, "measured-root", claudeModel, begin, begin.Add(time.Minute), 100, 0, 10, summary.KindRead, 200)
	root.Turns[0].UserMessage = workInvocation("loom/m-0001")
	writeSession(t, st, root)
	rep := build(t, st, "run-no-child-transcript")
	if rep.Telemetry.State != StatePartial || !reflect.DeepEqual(rep.Telemetry.Gaps, []string{"execution child-no-transcript has no transcript"}) {
		t.Errorf("completed child telemetry = %+v", rep.Telemetry)
	}
	if rep.Metrics.Parent.TokenUsageUnavailable || !rep.Metrics.Total.TokenUsageUnavailable || !rep.Metrics.Total.ToolTimeUnavailable || rep.Metrics.Total.CostUSD != nil {
		t.Errorf("missing child measurement availability = %+v", rep.Metrics)
	}
}

func TestMissingSessionLeavesCostUnknown(t *testing.T) {
	st, _ := openStore(t)
	importLines(t, st,
		`{"v":1,"kind":"run","run_id":"run-missing","ticket":"loom/m-0001","runtime":"claude-code","agent":"claude-code","session_id":"sess-m-root","started_at":"2026-09-10T12:00:00Z","ended_at":"2026-09-10T12:10:00Z","outcome":"completed"}`,
		`{"v":1,"kind":"execution","execution_id":"root-m","run_id":"run-missing","execution_kind":"root","agent":"claude-code","session_id":"sess-m-root","started_at":"2026-09-10T12:00:00Z","ended_at":"2026-09-10T12:10:00Z","outcome":"completed"}`,
		`{"v":1,"kind":"execution","execution_id":"stage-m-work-1-1","run_id":"run-missing","parent_execution_id":"root-m","execution_kind":"stage","stage":"work","stage_occurrence":1,"attempt":1,"agent":"claude-code","session_id":"sess-m-child","started_at":"2026-09-10T12:01:00Z","ended_at":"2026-09-10T12:05:00Z","outcome":"completed"}`,
	)
	begin := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	root := oneTurn(summary.AgentClaude, "sess-m-root", claudeModel, begin, begin.Add(time.Minute), 100, 0, 10, summary.KindRead, 200)
	root.Turns[0].UserMessage = workInvocation("loom/m-0001")
	writeSession(t, st, root)

	rep := build(t, st, "run-missing")
	child := execution(t, rep, "stage-m-work-1-1").Metrics
	warn := "stage-m-work-1-1: session not in summaries.db"
	if child.CostUSD != nil || child.Pricing.Available || !reflect.DeepEqual(child.PricingWarnings, []string{warn}) {
		t.Errorf("child with missing session: cost %v pricing %+v warnings %v", child.CostUSD, child.Pricing, child.PricingWarnings)
	}
	for name, m := range map[string]Metrics{"descendants": rep.Metrics.Descendants, "total": rep.Metrics.Total} {
		wire, _ := json.Marshal(m)
		if !m.TokenUsageUnavailable || !strings.Contains(string(wire), `"total_tokens":null`) {
			t.Errorf("%s missing transcript tokens encoded as measured: %s", name, wire)
		}
		if m.CostUSD != nil || m.Pricing.Available || !strings.Contains(strings.Join(m.PricingWarnings, "\n"), warn) {
			t.Errorf("%s: cost %v pricing %+v warnings %v, want null with the cause", name, m.CostUSD, m.Pricing, m.PricingWarnings)
		}
	}
	// 100·5 + 10·25 per million.
	parent := rep.Metrics.Parent
	if parent.TokenUsageUnavailable || tokens(t, rep.Metrics.Total, "claude-code").Total != parent.TotalTokens {
		t.Error("missing child erased measured parent usage")
	}
	if parent.CostUSD == nil || *parent.CostUSD != 0.00075 || !parent.Pricing.Available || len(parent.PricingWarnings) != 0 {
		t.Errorf("parent beside the missing session: cost %v pricing %+v warnings %v", deref(parent.CostUSD), parent.Pricing, parent.PricingWarnings)
	}
	if !strings.Contains(strings.Join(rep.Telemetry.Gaps, "\n"), "session claude-code/sess-m-child not in summaries.db") {
		t.Errorf("gaps = %v", rep.Telemetry.Gaps)
	}

	writeSession(t, st, oneTurn(summary.AgentClaude, "sess-m-child", claudeModel, begin.Add(time.Minute), begin.Add(5*time.Minute), 200, 0, 20, summary.KindEdit, 300))
	rep = build(t, st, "run-missing")
	child = execution(t, rep, "stage-m-work-1-1").Metrics
	if child.CostUSD == nil || *child.CostUSD != 0.0015 || !child.Pricing.Available || len(child.PricingWarnings) != 0 {
		t.Errorf("child after its session arrived: cost %v pricing %+v warnings %v", deref(child.CostUSD), child.Pricing, child.PricingWarnings)
	}
	total := rep.Metrics.Total
	if total.TokenUsageUnavailable || child.TokenUsageUnavailable {
		t.Error("arriving transcript did not resolve token availability")
	}
	if total.CostUSD == nil || *total.CostUSD != 0.00225 || !total.Pricing.Available || len(total.PricingWarnings) != 0 {
		t.Errorf("total after the session arrived: cost %v pricing %+v warnings %v", deref(total.CostUSD), total.Pricing, total.PricingWarnings)
	}
	if rep.Telemetry.State != StateComplete {
		t.Errorf("telemetry after the session arrived = %+v, want complete", rep.Telemetry)
	}
}

// A recorded run naming no transcript — the Codex render's form — reports
// its root as a gap rather than complete; once a codex-cli session holding
// the /work invocation for its ticket spans its start, the run is joined to
// it and the parent scope meters that session's turn under the invocation.
func TestRecordWithoutSessionIsAGapUntilJoined(t *testing.T) {
	st, _ := openStore(t)
	const ticket = "loom/cx-0001"
	importLines(t, st,
		`{"v":1,"kind":"run","run_id":"run-cx","ticket":"`+ticket+`","runtime":"codex-cli","started_at":"2026-09-10T12:02:00Z","ended_at":"2026-09-10T12:20:00Z","outcome":"completed"}`,
		`{"v":1,"kind":"execution","execution_id":"root-run-cx","run_id":"run-cx","execution_kind":"root","started_at":"2026-09-10T12:02:00Z","ended_at":"2026-09-10T12:20:00Z","outcome":"completed"}`,
		`{"v":1,"kind":"execution","execution_id":"lens-cx-security-r1-a1","run_id":"run-cx","parent_execution_id":"root-run-cx","execution_kind":"lens","lens":"security","round":1,"attempt":1,"agent":"codex-cli","session_id":"sess-cx-lens","started_at":"2026-09-10T12:10:00Z","ended_at":"2026-09-10T12:15:00Z","outcome":"completed"}`,
	)
	begin := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	writeSession(t, st, oneTurn(summary.AgentCodex, "sess-cx-lens", codexModel, begin.Add(10*time.Minute), begin.Add(15*time.Minute), 500, 100, 50, summary.KindBash, 1500))

	rep := build(t, st, "run-cx")
	if rep.Run.TranscriptBasis != "" || rep.Run.Transcript != nil {
		t.Errorf("unjoined run: transcript %+v basis %q, want none", rep.Run.Transcript, rep.Run.TranscriptBasis)
	}
	if rep.Telemetry.State != StatePartial || rep.Telemetry.RootSpan != "" {
		t.Errorf("unjoined telemetry = %+v, want partial with no root span", rep.Telemetry)
	}
	if !strings.Contains(strings.Join(rep.Telemetry.Gaps, "\n"), "root execution root-run-cx has no transcript; parent not metered") {
		t.Errorf("gaps = %v, want the root's missing transcript named", rep.Telemetry.Gaps)
	}
	if rep.Metrics.Parent.Turns != 0 || rep.Metrics.Descendants.Turns != 1 {
		t.Errorf("unjoined scopes = parent %d turns descendants %d turns, want 0 and the lens's 1", rep.Metrics.Parent.Turns, rep.Metrics.Descendants.Turns)
	}
	if !rep.Metrics.Parent.TokenUsageUnavailable || !rep.Metrics.Total.TokenUsageUnavailable || rep.Metrics.Descendants.TokenUsageUnavailable {
		t.Error("unnamed parent transcript availability was lost or erased measured descendants")
	}

	// The parent session, opened by the typed Codex invocation.
	parent := oneTurn(summary.AgentCodex, "sess-cx-parent", codexModel, begin, begin.Add(30*time.Minute), 2000, 800, 300, summary.KindBash, 4000)
	parent.Turns[0].UserMessage = "$work " + ticket
	writeSession(t, st, parent)

	rep = build(t, st, "run-cx")
	if rep.Run.TranscriptBasis != runs.BasisInvocation || rep.Run.Transcript == nil || rep.Run.Transcript.SessionID != "sess-cx-parent" {
		t.Fatalf("joined run: transcript %+v basis %q, want sess-cx-parent by invocation", rep.Run.Transcript, rep.Run.TranscriptBasis)
	}
	if rep.Telemetry.State != StateComplete || len(rep.Telemetry.Gaps) != 0 || rep.Telemetry.RootSpan != SpanInvocation {
		t.Errorf("joined telemetry = %+v, want complete over the invocation span", rep.Telemetry)
	}
	if rep.Executions[0].ExecutionID != "root-run-cx" || rep.Executions[0].Transcript == nil || rep.Executions[0].Transcript.SessionID != "sess-cx-parent" {
		t.Errorf("root execution = %+v, want the joined transcript", rep.Executions[0])
	}
	p := rep.Metrics.Parent
	if p.Turns != 1 || p.ToolCalls != 1 || p.ToolCallsByKind["bash"] != 1 || p.ToolTimeMs != 4000 {
		t.Errorf("parent = %d turns %d tool calls %v %dms", p.Turns, p.ToolCalls, p.ToolCallsByKind, p.ToolTimeMs)
	}
	if got := tokens(t, p, "codex-cli"); got != (Tokens{Input: 2000, Output: 300, CacheRead: 800, CacheSemantics: CacheReadInsideInput, Total: 2300}) {
		t.Errorf("parent codex tokens = %+v", got)
	}
	if rep.Metrics.Descendants.Turns != 1 || rep.Metrics.Total.Turns != 2 || rep.Metrics.Total.TotalTokens != 2300+550 {
		t.Errorf("scopes = descendants %d turns total %d turns %d tokens", rep.Metrics.Descendants.Turns, rep.Metrics.Total.Turns, rep.Metrics.Total.TotalTokens)
	}
}

// A recorded Claude run whose transcript holds no commitment line — none
// survives in a transcript since Claude Code 2.1.268 — still reports each
// lens once in the round its execution record declares: the record, naming
// no dispatch, joins the router call whose window holds its start, the
// subagent dispatches in the same turn follow it into that round, and the
// routed lens is one attempt carrying both sides of the join.
func TestRecordedRoundPlacesAttemptsWithNoCommitmentLine(t *testing.T) {
	st, _ := openStore(t)
	const (
		ticket  = "loom/no-line-0001"
		session = "sess-no-line"
		router  = "~/.codex/codex-lens.sh --lens security --payload /tmp/c/context.md > /tmp/c/verdict.txt"
	)
	importLines(t, st,
		`{"v":1,"kind":"run","run_id":"run-no-line","ticket":"`+ticket+`","runtime":"claude-code","agent":"claude-code","session_id":"`+session+`","started_at":"2026-09-12T04:00:00Z","ended_at":"2026-09-12T04:30:00Z","outcome":"completed"}`,
		`{"v":1,"kind":"execution","execution_id":"root-no-line","run_id":"run-no-line","execution_kind":"root","agent":"claude-code","session_id":"`+session+`","started_at":"2026-09-12T04:00:00Z","ended_at":"2026-09-12T04:30:00Z","outcome":"completed"}`,
		`{"v":1,"kind":"execution","execution_id":"lens-no-line-security-r1-a1","run_id":"run-no-line","parent_execution_id":"root-no-line","execution_kind":"lens","lens":"security","round":1,"attempt":1,"agent":"codex-cli","session_id":"sess-no-line-lens","started_at":"2026-09-12T04:03:15Z","ended_at":"2026-09-12T04:03:23Z","outcome":"completed"}`,
	)
	day := func(hhmmss string) time.Time { return at(hhmmss).AddDate(0, 0, 2) }
	verdict := func(name string) string {
		return `{"lens": "` + name + `", "verdict": "satisfied", "summary": "Fine."}`
	}
	sum := &summary.SessionSummary{
		SessionID: session,
		Agent:     summary.AgentClaude,
		StartTime: day("04:00:00"),
		EndTime:   day("04:30:00"),
		Turns: []summary.Turn{
			{Idx: 0, UserMessage: workInvocation(ticket), AssistantText: "Fanning out.", StartedAt: day("04:00:00"), EndedAt: day("04:04:00"), Model: claudeModel, InputTokens: 100, OutputTokens: 50},
			{Idx: 1, UserMessage: notification("toolu_c", verdict("contract")), AssistantText: "Contract in.", StartedAt: day("04:05:00"), EndedAt: day("04:05:10"), Model: claudeModel, InputTokens: 50, OutputTokens: 10},
			{Idx: 2, UserMessage: notification("toolu_q", verdict("quality")), AssistantText: "Quality in.", StartedAt: day("04:06:00"), EndedAt: day("04:06:10"), Model: claudeModel, InputTokens: 50, OutputTokens: 10},
		},
		ToolCalls: []summary.ToolCall{
			{TurnIdx: 0, CallID: "toolu_c", Kind: summary.KindTask, ToolName: "Agent", KeyArg: "Contract lens round 1", StartedAt: day("04:03:10"), DurationMs: 120000},
			{TurnIdx: 0, CallID: "toolu_q", Kind: summary.KindTask, ToolName: "Agent", KeyArg: "Quality lens round 1", StartedAt: day("04:03:11"), DurationMs: 120000},
			{TurnIdx: 0, CallID: "toolu_b", Kind: summary.KindBash, ToolName: "Bash", KeyArg: router, StartedAt: day("04:03:13"), DurationMs: 10367},
		},
	}
	for idx, id := range map[int]string{1: "toolu_c", 2: "toolu_q"} {
		for _, b := range lens.Extract(sum.Turns[idx].UserMessage) {
			sum.LensResponses = append(sum.LensResponses, summary.LensResponse{
				TurnIdx: idx, Origin: summary.OriginTaskNotification, DispatchID: id, SourceLine: idx + 1, At: sum.Turns[idx].StartedAt, Block: b,
			})
		}
	}
	writeSession(t, st, sum)
	writeSession(t, st, oneTurn(summary.AgentCodex, "sess-no-line-lens", codexModel, day("04:03:15"), day("04:03:23"), 500, 100, 50, summary.KindBash, 1500))

	rep := build(t, st, "run-no-line")
	var groups []string
	for _, g := range rep.Lenses {
		groups = append(groups, g.Lens+"/"+strconv.Itoa(g.Round)+"/"+strconv.Itoa(len(g.Attempts)))
	}
	if got := strings.Join(groups, " "); got != "contract/1/1 quality/1/1 security/1/1" {
		t.Fatalf("lens groups = %v, want each lens once in round 1", groups)
	}
	la := rep.Lenses[2].Attempts[0]
	if la.ExecutionID != "lens-no-line-security-r1-a1" || !la.Recorded || la.Status != "dispatched" || la.Outcome != "completed" || la.Metrics == nil || la.Metrics.Turns != 1 {
		t.Errorf("security attempt = %+v, want the record joined to the dispatched attempt", la)
	}
	if rep.Lenses[0].Attempts[0].Status != "parsed" || rep.Lenses[1].Attempts[0].Status != "parsed" {
		t.Errorf("contract = %+v quality = %+v, want both parsed", rep.Lenses[0].Attempts[0], rep.Lenses[1].Attempts[0])
	}
	if len(rep.Attempts) != 3 {
		t.Errorf("attempts = %+v, want one per lens", rep.Attempts)
	}
}

func TestLoadRefusesAnUnknownRun(t *testing.T) {
	st, path := fixture(t)
	st.Close()
	_, err := Load(path, "nope")
	if err == nil || err.Error() != "run not found: nope" {
		t.Fatalf("err = %v, want run not found: nope", err)
	}
}
