package summaries

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"loom/internal/parse/summary"
)

// TestLoadActivityWindow writes sessions and commits both inside and outside
// the 24h window and asserts LoadActivity returns only the in-window rows,
// grouped per repo.
func TestLoadActivityWindow(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LOOM_HOME", dir)

	st, err := Open(filepath.Join(dir, "summaries.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	now := time.Now()
	inWindow := now.Add(-2 * time.Hour)
	outWindow := now.Add(-48 * time.Hour)

	// In-window session with a commit.
	recent := &summary.SessionSummary{
		SessionID: "recent",
		Agent:     summary.AgentClaude,
		StartTime: inWindow,
		EndTime:   inWindow.Add(time.Minute),
		Turns:     []summary.Turn{{Idx: 0}, {Idx: 1}},
		ToolCalls: []summary.ToolCall{
			{Kind: summary.KindBash, StartedAt: inWindow, ResultSummary: "[main aaaaaaa] recent work"},
		},
	}
	src := SourceInfo{
		Path:      "/tmp/loom/recent.jsonl",
		GitRemote: "https://github.com/EnderRealm/loom.git",
		CwdRaw:    "/Users/steve/code/loom",
	}
	if err := st.WriteSummary(ctx, recent, src); err != nil {
		t.Fatalf("WriteSummary recent: %v", err)
	}

	// Out-of-window session with a commit — must be excluded.
	old := &summary.SessionSummary{
		SessionID: "old",
		Agent:     summary.AgentClaude,
		StartTime: outWindow,
		EndTime:   outWindow.Add(time.Minute),
		ToolCalls: []summary.ToolCall{
			{Kind: summary.KindBash, StartedAt: outWindow, ResultSummary: "[main bbbbbbb] old work"},
		},
	}
	if err := st.WriteSummary(ctx, old, src); err != nil {
		t.Fatalf("WriteSummary old: %v", err)
	}

	av, err := LoadActivity(24 * time.Hour)
	if err != nil {
		t.Fatalf("LoadActivity: %v", err)
	}
	if !av.Available {
		t.Fatalf("Available = false, want true")
	}
	if av.Outdated {
		t.Fatalf("Outdated = true, want false")
	}
	if len(av.Sessions) != 1 {
		t.Errorf("sessions: got %d, want 1", len(av.Sessions))
	}
	if len(av.Commits) != 1 {
		t.Errorf("commits: got %d, want 1", len(av.Commits))
	}
	if len(av.Commits) == 1 && av.Commits[0].Hash != "aaaaaaa" {
		t.Errorf("commit hash: got %q, want aaaaaaa", av.Commits[0].Hash)
	}
	if len(av.Repos) != 1 {
		t.Fatalf("repos: got %d, want 1", len(av.Repos))
	}
	if av.Repos[0].Sessions != 1 || av.Repos[0].Commits != 1 {
		t.Errorf("repo rollup: got sessions=%d commits=%d, want 1/1",
			av.Repos[0].Sessions, av.Repos[0].Commits)
	}
}
