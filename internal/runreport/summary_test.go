package runreport

import (
	"reflect"
	"strings"
	"testing"
	"time"

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
