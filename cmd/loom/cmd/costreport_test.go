package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"loom/internal/parse/summary"
	"loom/internal/summaries"
	"loom/internal/workreport"
)

// seedCostSessions folds two /work runs, an hour apart in one session, into a
// fresh summaries.db the way the summarizer does, and returns its path.
func seedCostSessions(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "summaries.db")
	st, err := summaries.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	at := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	sum := &summary.SessionSummary{
		SessionID: "s1",
		Agent:     summary.AgentClaude,
		StartTime: at,
		EndTime:   at.Add(2 * time.Hour),
	}
	for i, ticket := range []string{"loom/cost-1234", "loom/cost-5678"} {
		started := at.Add(time.Duration(i) * time.Hour)
		sum.Turns = append(sum.Turns, summary.Turn{
			Idx: i,
			UserMessage: "<command-message>work</command-message>\n<command-name>/work</command-name>" +
				"\n<command-args>" + ticket + "</command-args>",
			AssistantText:   "Implemented and committed.",
			StartedAt:       started,
			EndedAt:         started.Add(4 * time.Minute),
			InputTokens:     100,
			OutputTokens:    40,
			CacheReadTokens: 500,
		})
		sum.ToolCalls = append(sum.ToolCalls, summary.ToolCall{
			TurnIdx: i, Kind: summary.KindBash, ToolName: "Bash", KeyArg: "git commit",
			StartedAt: started.Add(5 * time.Minute), DurationMs: 900,
			ResultSummary: "[main abc1234] [" + ticket + "] Do the thing\n 2 files changed, 20 insertions(+)",
		})
	}
	if err := st.WriteSummary(context.Background(), sum, summaries.SourceInfo{Project: "loom"}); err != nil {
		t.Fatal(err)
	}
	return path
}

// runCostReport drives a command built exactly like the registered one.
func runCostReport(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newCostReportCmd()
	cmd.SetArgs(args)
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	err := cmd.Execute()
	return out.String(), err
}

func TestCostReportPrintsJSON(t *testing.T) {
	db := seedCostSessions(t)

	out, err := runCostReport(t, "--db", db, "--since", "2026-07-01T00:00:00Z")
	if err != nil {
		t.Fatalf("cost-report: %v", err)
	}

	var rep workreport.CostReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if rep.Since != "2026-07-01T00:00:00Z" || rep.Until != "" {
		t.Fatalf("range echo = %q/%q, want the since bound echoed", rep.Since, rep.Until)
	}
	if len(rep.Runs) != 2 {
		t.Fatalf("report holds %d runs, want 2", len(rep.Runs))
	}
	run := rep.Runs[0]
	if run.Ticket != "loom/cost-1234" || run.Runtime != workreport.RuntimeClaude || !run.Committed {
		t.Fatalf("run = %+v, want the first ticket's committed claude run", run)
	}
	if run.WallClockMs == nil || *run.WallClockMs != 5*60*1000 {
		t.Fatalf("wall_clock_ms = %v, want 300000", run.WallClockMs)
	}
	if run.ActiveMs != 4*60*1000+900 {
		t.Fatalf("active_ms = %d, want 240900", run.ActiveMs)
	}

	// Diff-stable: the same DB and range render byte for byte the same.
	again, err := runCostReport(t, "--db", db, "--since", "2026-07-01T00:00:00Z")
	if err != nil {
		t.Fatalf("cost-report: %v", err)
	}
	if again != out {
		t.Fatal("two reports over the same range differ; they are meant to be diffed")
	}
}

func TestCostReportBoundsTheRange(t *testing.T) {
	db := seedCostSessions(t)

	// until is exclusive, so only the first run falls before the second's
	// invocation.
	out, err := runCostReport(t, "--db", db, "--until", "2026-08-01T10:00:00Z")
	if err != nil {
		t.Fatalf("cost-report: %v", err)
	}
	var rep workreport.CostReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if len(rep.Runs) != 1 || rep.Runs[0].Ticket != "loom/cost-1234" {
		t.Fatalf("runs = %+v, want only the first", rep.Runs)
	}

	out, err = runCostReport(t, "--db", db, "--since", "2026-08-01T10:00:00Z")
	if err != nil {
		t.Fatalf("cost-report: %v", err)
	}
	rep = workreport.CostReport{}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if len(rep.Runs) != 1 || rep.Runs[0].Ticket != "loom/cost-5678" {
		t.Fatalf("runs = %+v, want only the second", rep.Runs)
	}
}

func TestCostReportRejectsABadBound(t *testing.T) {
	if _, err := runCostReport(t, "--db", seedCostSessions(t), "--since", "last tuesday"); err == nil {
		t.Fatal("cost-report accepted an unparseable --since")
	}
}

func TestCostReportRejectsAnInvertedRange(t *testing.T) {
	db := seedCostSessions(t)
	if _, err := runCostReport(t, "--db", db, "--since", "2026-08-02", "--until", "2026-08-01"); err == nil {
		t.Fatal("cost-report accepted --since after --until")
	}
}
