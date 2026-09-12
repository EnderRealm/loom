package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom/internal/parse/summary"
	"loom/internal/runreport"
	"loom/internal/summaries"
)

// fixtureRun is the run internal/runs/testdata/executions.jsonl declares.
const fixtureRun = "0f4c3a6e-2d1b-4b7e-9c8a-5e2f1d0a9b31"

// seedRunFixture folds the execution-record fixture and the root's own
// session — one metered turn under the /work invocation — into a fresh
// summaries.db and returns its path.
func seedRunFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "summaries.db")
	st, err := summaries.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	records := filepath.Join("..", "..", "..", "internal", "runs", "testdata", "executions.jsonl")
	f, err := os.Open(records)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ImportExecutions(context.Background(), records, f, info.Size(), info.ModTime()); err != nil {
		t.Fatal(err)
	}

	at := time.Date(2026, 9, 10, 17, 2, 11, 0, time.UTC)
	sum := &summary.SessionSummary{
		SessionID: "195f819e-1e11-4e08-8c16-a340f512f892",
		Agent:     summary.AgentClaude,
		StartTime: at,
		EndTime:   at.Add(time.Hour),
		Turns: []summary.Turn{{
			Idx: 0,
			UserMessage: "<command-message>work</command-message>\n<command-name>/work</command-name>" +
				"\n<command-args>loom/persist-execution-identities-2149</command-args>",
			AssistantText: "Implemented and committed.",
			StartedAt:     at,
			EndedAt:       at.Add(4 * time.Minute),
			Model:         "claude-opus-5",
			InputTokens:   100,
			OutputTokens:  40,
		}},
		ToolCalls: []summary.ToolCall{{
			TurnIdx: 0, Kind: summary.KindBash, ToolName: "Bash", KeyArg: "go test ./...",
			StartedAt: at.Add(time.Minute), DurationMs: 900,
		}},
	}
	if err := st.WriteSummary(context.Background(), sum, summaries.SourceInfo{Project: "loom"}); err != nil {
		t.Fatal(err)
	}
	return path
}

// runRunReport drives a command built exactly like the registered one.
func runRunReport(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newRunReportCmd()
	cmd.SetArgs(args)
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	err := cmd.Execute()
	return out.String(), err
}

func TestRunReportPrintsJSON(t *testing.T) {
	db := seedRunFixture(t)

	out, err := runRunReport(t, "--db", db, "--run", fixtureRun)
	if err != nil {
		t.Fatalf("run-report: %v", err)
	}
	var rep runreport.Report
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if rep.Run.RunID != fixtureRun || rep.Run.Outcome != "completed" || rep.Run.Ticket != "loom/persist-execution-identities-2149" {
		t.Fatalf("run = %+v", rep.Run)
	}
	if rep.Metrics.Parent.Turns != 1 || rep.Metrics.Parent.ToolCalls != 1 || rep.Metrics.Parent.TotalTokens != 140 {
		t.Fatalf("parent = %+v", rep.Metrics.Parent)
	}
	if rep.Metrics.Total.Executions != 8 || rep.Telemetry.State != runreport.StatePartial {
		t.Fatalf("total executions = %d telemetry = %+v", rep.Metrics.Total.Executions, rep.Telemetry)
	}
	if rep.Tree == nil || len(rep.Tree.Children) != 5 || len(rep.Executions) != 8 || len(rep.Stages) != 2 {
		t.Fatalf("tree children = %d executions = %d stages = %d", len(rep.Tree.Children), len(rep.Executions), len(rep.Stages))
	}
	// Every empty list renders as [], so a UI can index it without a null check.
	for _, key := range []string{`"children": null`, `"unresolved": null`, `"diagnostics": null`, `"lenses": null`, `"gaps": null`, `"pricing_warnings": null`, `"models": null`} {
		if strings.Contains(out, key) {
			t.Errorf("output renders %s", key)
		}
	}

	// Diff-stable: the same DB and run render byte for byte the same.
	again, err := runRunReport(t, "--db", db, "--run", fixtureRun)
	if err != nil {
		t.Fatalf("run-report: %v", err)
	}
	if again != out {
		t.Fatal("two reports of the same run differ")
	}
}

func TestRunReportRefusesAnUnknownRun(t *testing.T) {
	_, err := runRunReport(t, "--db", seedRunFixture(t), "--run", "nope")
	if err == nil || err.Error() != "run not found: nope" {
		t.Fatalf("err = %v, want run not found: nope", err)
	}
}

func TestRunReportRequiresARun(t *testing.T) {
	if _, err := runRunReport(t, "--db", seedRunFixture(t)); err == nil {
		t.Fatal("run-report accepted no --run")
	}
}

// The existing report commands still answer over the same database.
func TestRunReportLeavesCostReportIntact(t *testing.T) {
	db := seedRunFixture(t)
	out, err := runCostReport(t, "--db", db)
	if err != nil {
		t.Fatalf("cost-report: %v", err)
	}
	if !strings.Contains(out, `"ticket": "loom/persist-execution-identities-2149"`) {
		t.Fatalf("cost-report output = %s", out)
	}
}
