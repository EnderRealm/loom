package runreport

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"loom/internal/parse/summary"
	"loom/internal/pricing"
	"loom/internal/runs"
)

// The list row and the report are the same document read two ways: a
// summary listed for a run equals the summary of that run's report, field
// for field, and the figures on it are the report's own.
func TestListSummariesAgreesWithTheReport(t *testing.T) {
	st, path := fixture(t)
	rep := build(t, st, fixtureRun)
	st.Close()

	rows, err := ListSummaries(path, time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	var row *Summary
	for i := range rows {
		if rows[i].RunID == fixtureRun {
			row = &rows[i]
		}
	}
	if row == nil {
		t.Fatalf("run %s not listed in %d rows", fixtureRun, len(rows))
	}
	if want := SummaryOf(rep); !reflect.DeepEqual(*row, want) {
		t.Errorf("listed row:\n%+v\nsummary of the report:\n%+v", *row, want)
	}

	total := rep.Metrics.Total
	if row.TotalTokens != total.TotalTokens || row.ToolCalls != total.ToolCalls || row.ToolTimeMs != rep.Time.ToolTimeMs {
		t.Errorf("row usage = %d tokens %d tools %dms tool time, report %d/%d/%d", row.TotalTokens, row.ToolCalls, row.ToolTimeMs, total.TotalTokens, total.ToolCalls, rep.Time.ToolTimeMs)
	}
	if row.Failures != total.Failures.Tool+total.Failures.API+total.Failures.Process+total.Failures.Other || row.Failures != 4 {
		t.Errorf("row failures = %d, report %+v", row.Failures, total.Failures)
	}
	if row.WallMs == nil || *row.WallMs != *rep.Time.WallMs || row.ExecutionTimeMs != rep.Time.ExecutionTimeMs || row.LegacyActiveMs != rep.Time.LegacyActiveMs {
		t.Errorf("row time = %v/%d/%d, report %+v", row.WallMs, row.ExecutionTimeMs, row.LegacyActiveMs, rep.Time)
	}
	cov := total.ExecutionTimeCoverage
	if row.TimedExecutions != cov.Timed || row.UntimedExecutions != cov.Untimed || cov.Timed == 0 {
		t.Errorf("row coverage = %d timed %d untimed, report %+v", row.TimedExecutions, row.UntimedExecutions, cov)
	}
	if !row.Metered || len(total.TokensByRuntime) == 0 {
		t.Errorf("row metered = %v, report tokens by runtime %v", row.Metered, total.TokensByRuntime)
	}
	if row.Children != rep.Metrics.Descendants.Executions || row.Children != 7 {
		t.Errorf("row children = %d, report descendants %d", row.Children, rep.Metrics.Descendants.Executions)
	}
	if row.Outcome != "completed" || row.TelemetryState != StatePartial || row.Pending != 1 {
		t.Errorf("row outcome %q telemetry %q pending %d, want the report's completed/partial/1", row.Outcome, row.TelemetryState, row.Pending)
	}
	if row.StartedAt != runStart || row.LastObservedAt != at("17:40:00") {
		t.Errorf("row started %v last observed %v", row.StartedAt, row.LastObservedAt)
	}
	// The codex model is unpriced, so the total is unavailable rather than 0.
	if row.CostUSD != nil || total.CostUSD != nil {
		t.Errorf("row cost = %v, report %v, want both unavailable", row.CostUSD, total.CostUSD)
	}
	if row.ParentCostUSD == nil || *row.ParentCostUSD != *rep.Metrics.Parent.CostUSD {
		t.Errorf("row parent cost = %v, report %v", row.ParentCostUSD, rep.Metrics.Parent.CostUSD)
	}
	if row.PricedDescendantCostUSD == nil || !row.DescendantCostUnavailable {
		t.Errorf("row priced descendant cost = %v unavailable = %v, want priced descendants beside the unpriced codex gap", row.PricedDescendantCostUSD, row.DescendantCostUnavailable)
	}
}

func TestSummaryCarriesPricedDescendantSubtotalAcrossGap(t *testing.T) {
	priced, duplicate := 1.25, 9.0
	rep := &Report{
		Metrics: Scopes{
			Parent:      Metrics{Executions: 1},
			Descendants: Metrics{Executions: 3},
		},
		Executions: []ExecutionMetrics{
			{Kind: runs.KindRoot, Counted: true, Metrics: Metrics{}},
			{Kind: runs.KindLens, Counted: true, Metrics: Metrics{CostUSD: &priced}},
			{Kind: runs.KindLens, Counted: true, Metrics: Metrics{}},
			{Kind: runs.KindSubagent, Counted: false, Metrics: Metrics{CostUSD: &duplicate}},
		},
	}
	row := SummaryOf(rep)
	if row.PricedDescendantCostUSD == nil || *row.PricedDescendantCostUSD != priced {
		t.Errorf("priced descendant subtotal = %v, want %v without the duplicate", row.PricedDescendantCostUSD, priced)
	}
	if !row.DescendantCostUnavailable {
		t.Error("mixed priced and unpriced descendants did not retain the pricing gap")
	}
}

func TestListSummariesBoundsTheRootTranscriptWithItsOwnInvocation(t *testing.T) {
	st, _ := openStore(t)
	const (
		runID         = "run-distinct-root-session"
		ticket        = "loom/distinct-root-session"
		recordSession = "record-session"
		rootSession   = "root-session"
	)
	importLines(t, st,
		`{"v":1,"kind":"run","run_id":"`+runID+`","ticket":"`+ticket+`","runtime":"claude-code","agent":"claude-code","session_id":"`+recordSession+`","started_at":"2026-09-10T10:00:00Z","ended_at":"2026-09-10T10:10:00Z","outcome":"completed","reporting_cutoff":"2026-09-10T10:10:00Z"}`,
		`{"v":1,"kind":"execution","execution_id":"root-distinct-session","run_id":"`+runID+`","execution_kind":"root","agent":"claude-code","session_id":"`+rootSession+`","started_at":"2026-09-10T10:00:00Z","ended_at":"2026-09-10T10:10:00Z","outcome":"completed"}`,
	)
	start := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	writeSession(t, st, &summary.SessionSummary{
		SessionID: recordSession,
		Agent:     summary.AgentClaude,
		Turns: []summary.Turn{{
			Idx: 7, UserMessage: workInvocation(ticket), StartedAt: start, EndedAt: start.Add(time.Minute),
			Model: claudeModel, InputTokens: 900, OutputTokens: 90,
		}},
	})
	writeSession(t, st, &summary.SessionSummary{
		SessionID: rootSession,
		Agent:     summary.AgentClaude,
		Turns: []summary.Turn{
			{Idx: 0, UserMessage: workInvocation(ticket), StartedAt: start, EndedAt: start.Add(time.Minute), Model: claudeModel, InputTokens: 100, OutputTokens: 10},
			{Idx: 1, UserMessage: "continue", StartedAt: start.Add(5 * time.Minute), EndedAt: start.Add(6 * time.Minute), Model: claudeModel, InputTokens: 200, OutputTokens: 20},
		},
	})

	table, err := pricing.Default()
	if err != nil {
		t.Fatal(err)
	}
	rows, err := Summaries(st.DB(), table, time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.RunID == runID {
			if row.TotalTokens != 330 {
				t.Errorf("root transcript tokens = %d, want 330 from its own invocation", row.TotalTokens)
			}
			return
		}
	}
	t.Fatalf("run %s not listed", runID)
}

func TestSummariesBoundTheRange(t *testing.T) {
	st, _ := fixture(t)
	table, err := pricing.Default()
	if err != nil {
		t.Fatal(err)
	}
	rows, err := Summaries(st.DB(), table, runStart.Add(time.Hour), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.RunID == fixtureRun {
			t.Errorf("run started at %v listed for a range from %v", r.StartedAt, runStart.Add(time.Hour))
		}
	}
}

func TestListSummariesWindowBudget(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".loom", "summaries.db")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Skip("~/.loom/summaries.db is absent")
	} else if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	if _, err := ListSummaries(path, time.Now().Add(-30*24*time.Hour), time.Time{}); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed >= 2*time.Second {
		t.Fatalf("ListSummaries took %s, want under 2s", elapsed)
	}
}

// The complete response is the stored body, keyed the way the report groups
// attempts, and it stays off the report itself.
func TestLoadDetailCarriesTheWholeLensResponse(t *testing.T) {
	st, path := fixture(t)
	st.Close()
	d, err := LoadDetail(path, fixtureRun)
	if err != nil {
		t.Fatal(err)
	}
	if d.Report == nil || d.Report.Run.RunID != fixtureRun {
		t.Fatalf("detail report = %+v", d.Report)
	}
	la := d.Report.Lenses[0].Attempts[0]
	raw, ok := d.LensResponses[LensKey("security", 1, la.Attempt)]
	if !ok || !strings.Contains(raw, `"verdict": "satisfied"`) || !strings.Contains(raw, `"summary": "No exposure."`) {
		t.Errorf("lens response = %q, %v; want the whole fenced body", raw, ok)
	}
}

// A sweep marker that does not parse is a defect in the freshness signal,
// carried on the detail; the report still loads.
func TestLoadDetailSurvivesAnUnreadableSweepMarker(t *testing.T) {
	st, path := fixture(t)
	if _, err := st.DB().Exec(`INSERT OR REPLACE INTO schema_meta(key, value) VALUES ('last_sweep_at', 'yesterday')`); err != nil {
		t.Fatal(err)
	}
	st.Close()
	d, err := LoadDetail(path, fixtureRun)
	if err != nil {
		t.Fatal(err)
	}
	if d.Report == nil || !d.SweptAt.IsZero() || d.SweepErr == nil || !strings.Contains(d.SweepErr.Error(), "parse last sweep") {
		t.Errorf("detail with a bad marker: report %v swept %s err %v", d.Report != nil, d.SweptAt, d.SweepErr)
	}
}

func TestLensResponsesSkipsAttemptsWithNoResponse(t *testing.T) {
	st, _ := fixture(t)
	run, err := runs.Load(st.DB(), fixtureRun)
	if err != nil {
		t.Fatal(err)
	}
	run.Lenses[0].ResponseID = "gone"
	got, err := LensResponses(st.DB(), run)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("responses = %v, want none for a response row that is gone", got)
	}
}
