package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loom/internal/parse/summary"
	"loom/internal/runreport"
	"loom/internal/summaries"
	"loom/internal/summarize"
)

func TestToolAveragesExcludeUntimedCallsAcrossSlugs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOOM_HOME", home)
	st, err := summaries.Open(filepath.Join(home, "summaries.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, slug := range []string{"timed", "untimed"} {
		s := &summary.SessionSummary{Agent: summary.AgentCursor, SessionID: slug,
			ToolCalls: []summary.ToolCall{{ToolName: "Read", Kind: summary.KindRead, DurationMs: 1000, DurationUnavailable: slug == "untimed"}}}
		if err := st.WriteSummary(context.Background(), s, summaries.SourceInfo{Project: slug}); err != nil {
			t.Fatal(err)
		}
	}
	v, err := summaries.Load()
	if err != nil {
		t.Fatal(err)
	}
	p := &Project{Slugs: []string{"timed", "untimed"}}
	attachSummary(p, v)
	if len(p.TopTools) != 1 || p.TopTools[0].Calls != 2 || p.TopTools[0].AvgMs != 1000 {
		t.Errorf("mixed timing average = %+v", p.TopTools)
	}
	p = &Project{Slugs: []string{"untimed"}}
	attachSummary(p, v)
	detail := newDetailModel(p, 100, 40)
	if text := detail.renderPatterns(100); !strings.Contains(text, unavailable) {
		t.Errorf("untimed tool average is not shown as unavailable: %s", text)
	}
}

func TestCursorSessionsAndUnmeasuredUsageDisplay(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOOM_HOME", home)
	received := filepath.Join(home, "received")
	parent := "963ac97f-c33f-46de-9624-3e5946b61a0a"
	child := "5aabb54a-05c7-403c-9721-1bf0448847c1"
	for _, item := range []struct{ fixture, rel string }{
		{"parent", parent + ".jsonl"},
		{"child", filepath.Join(parent, "subagents", child+".jsonl")},
	} {
		data, err := os.ReadFile(filepath.Join("..", "parse", "cursorparse", "testdata", item.fixture+".jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(received, "cursor-cli", "loom", item.rel)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	db := filepath.Join(home, "summaries.db")
	if err := summarize.Run(summarize.Options{ReceivedDir: received, DBPath: db, Strict: true}); err != nil {
		t.Fatal(err)
	}
	var ids []string
	if err := walkAgentSlugSessions(received, func(agent, slug, id, path string, info os.FileInfo) {
		if agent != "cursor-cli" || slug != "loom" {
			t.Errorf("identity = %s/%s", agent, slug)
		}
		ids = append(ids, id)
	}); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Fatalf("session view missed child: %v", ids)
	}
	detail, err := runreport.LoadDetail(db, "transcript:cursor-cli:"+parent+":1")
	if err != nil {
		t.Fatal(err)
	}
	metrics := detail.Report.Metrics.Total
	if metrics.ToolCalls != 1 || metrics.CostUSD != nil {
		t.Fatalf("metrics = %+v", metrics)
	}
	if cell := tokensCell(metrics); !strings.Contains(cell, unavailable) || strings.Contains(cell, "0") {
		t.Fatalf("unknown tokens display = %q", cell)
	}
	row := runreport.SummaryOf(detail.Report)
	if !row.Metered || !row.TokensUnavailable || row.Runtime != "cursor-cli" || row.ToolCalls != 1 {
		t.Fatalf("run row = %+v", row)
	}
	st, err := summaries.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.DB().Exec(`DELETE FROM sessions WHERE agent = 'cursor-cli' AND session_id = ?`, child); err != nil {
		t.Fatal(err)
	}
	detail, err = runreport.LoadDetail(db, "transcript:cursor-cli:"+parent+":0")
	if err != nil {
		t.Fatal(err)
	}
	m := newRunDetailModel(detail.Report.Run.RunID, 0, 120, 50)
	m.setDetail(detail, nil)
	if text := stripANSI(strings.Join(m.timeLines(120), "\n")); !strings.Contains(text, "tool timing incomplete") {
		t.Errorf("missing child tool time is shown as measured: %s", text)
	}
	if text := stripANSI(strings.Join(m.summaryLines(120), "\n")); !strings.Contains(text, unavailable+" in tools") {
		t.Errorf("summary shows partial tool time as measured: %s", text)
	}
}
