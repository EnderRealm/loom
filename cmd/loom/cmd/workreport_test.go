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

// seedWorkSession folds one compliant /work session into a fresh summaries.db
// the way the summarizer does, and returns its path.
func seedWorkSession(t *testing.T) string {
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
		EndTime:   at.Add(time.Hour),
		Turns: []summary.Turn{{
			Idx: 0,
			UserMessage: "<command-message>work</command-message>\n<command-name>/work</command-name>" +
				"\n<command-args>loom/report-1234</command-args>",
			AssistantText: "dispatching (loom/report-1234 round 1): contract, quality, security",
			StartedAt:     at,
		}},
		ToolCalls: []summary.ToolCall{
			{TurnIdx: 0, Kind: summary.KindTask, ToolName: "Agent", KeyArg: "Contract lens review", StartedAt: at.Add(time.Minute)},
			{TurnIdx: 0, Kind: summary.KindTask, ToolName: "Agent", KeyArg: "Quality lens review", StartedAt: at.Add(time.Minute)},
			{TurnIdx: 0, Kind: summary.KindBash, ToolName: "Bash", KeyArg: "git commit", StartedAt: at.Add(5 * time.Minute),
				ResultSummary: "[main abc1234] [loom/report-1234] Do the thing\n 2 files changed, 20 insertions(+)"},
		},
	}
	if err := st.WriteSummary(context.Background(), sum, summaries.SourceInfo{Project: "loom"}); err != nil {
		t.Fatal(err)
	}
	return path
}

// runWorkReport drives a command built exactly like the registered one.
func runWorkReport(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newWorkReportCmd()
	cmd.SetArgs(args)
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	err := cmd.Execute()
	return out.String(), err
}

func TestWorkReportPrintsJSON(t *testing.T) {
	db := seedWorkSession(t)

	out, err := runWorkReport(t, "--db", db, "--since", "2026-07-01T00:00:00Z")
	if err != nil {
		t.Fatalf("work-report: %v", err)
	}

	var rep workreport.Report
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if rep.Since != "2026-07-01T00:00:00Z" || rep.Until != "" {
		t.Fatalf("range echo = %q/%q, want the since bound echoed", rep.Since, rep.Until)
	}
	if rep.Totals != (workreport.Totals{Runs: 1, Compliant: 1}) {
		t.Fatalf("totals = %+v, want one compliant run", rep.Totals)
	}
	run := rep.Runs[0]
	if run.Runtime != workreport.RuntimeClaude || run.Classification != workreport.ClassCompliant {
		t.Fatalf("run = %+v, want a compliant claude run", run)
	}
	if run.Ticket != "loom/report-1234" || run.ReviewIterations == nil || *run.ReviewIterations != 1 || !run.Committed {
		t.Fatalf("run = %+v, want the ticket, one review round and a commit", run)
	}

	// Diff-stable: the same DB and range render byte for byte the same.
	again, err := runWorkReport(t, "--db", db, "--since", "2026-07-01T00:00:00Z")
	if err != nil {
		t.Fatalf("work-report: %v", err)
	}
	if again != out {
		t.Fatal("two reports over the same range differ; they are meant to be diffed")
	}
}

func TestWorkReportRejectsABadBound(t *testing.T) {
	if _, err := runWorkReport(t, "--db", seedWorkSession(t), "--since", "last tuesday"); err == nil {
		t.Fatal("work-report accepted an unparseable --since")
	}
}

func TestWorkReportRejectsAnInvertedRange(t *testing.T) {
	db := seedWorkSession(t)
	if _, err := runWorkReport(t, "--db", db, "--since", "2026-08-02", "--until", "2026-08-01"); err == nil {
		t.Fatal("work-report accepted --since after --until")
	}
}
