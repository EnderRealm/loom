package summaries

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom/internal/parse/summary"
)

// A DB predating the commits table cannot answer which sessions closed a
// ticket, and an empty answer would read exactly like a ticket that landed no
// commits — so it errors rather than degrading.
func TestLoadSessionsForTicketRejectsAnOutdatedSchema(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LOOM_HOME", dir)

	st, err := Open(filepath.Join(dir, "summaries.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := st.DB().Exec(`UPDATE schema_meta SET value = '3' WHERE key = 'schema_version'`); err != nil {
		t.Fatalf("downgrade schema: %v", err)
	}
	st.Close()

	_, err = LoadSessionsForTicket("loom/some-ticket-0001")
	if err == nil {
		t.Fatal("LoadSessionsForTicket = nil error, want a failure on a pre-commits schema")
	}
	if !strings.Contains(err.Error(), "--rebuild") {
		t.Fatalf("error %v doesn't say how to fix it", err)
	}
}

// An epic's retrospect selects by every child id at once, and one session
// commonly lands commits for several of them: it is one transcript, so it is
// selected once, placed by the earliest commit it landed for any id in the set.
func TestLoadSessionsForTicketsSelectsASharedSessionOnce(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LOOM_HOME", dir)

	st, err := Open(filepath.Join(dir, "summaries.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	commit := func(at time.Time, subject string) summary.ToolCall {
		return summary.ToolCall{Kind: summary.KindBash, StartedAt: at,
			ResultSummary: "[main abc1234] " + subject}
	}
	src := SourceInfo{Path: "/tmp/loom/s.jsonl", GitRemote: "https://github.com/EnderRealm/loom.git"}

	// Landed for both children, the warp one first: one row in the answer,
	// ordered by that earlier commit.
	shared := &summary.SessionSummary{SessionID: "shared", Agent: summary.AgentClaude, ToolCalls: []summary.ToolCall{
		commit(base.Add(2*time.Hour), "[warp/child-0002] Second half"),
		commit(base.Add(3*time.Hour), "[loom/child-0001] First half"),
	}}
	// Landed for the loom child only, before the shared session's earliest.
	early := &summary.SessionSummary{SessionID: "early", Agent: summary.AgentClaude, ToolCalls: []summary.ToolCall{
		commit(base.Add(time.Hour), "[loom/child-0001] Groundwork"),
	}}
	// A mention without the marker, and a marker for a ticket outside the set.
	other := &summary.SessionSummary{SessionID: "other", Agent: summary.AgentClaude, ToolCalls: []summary.ToolCall{
		commit(base, "Follow-up to loom/child-0001"),
		commit(base, "[loom/child-0001x] Not the same id"),
	}}
	for _, sum := range []*summary.SessionSummary{shared, early, other} {
		if err := st.WriteSummary(ctx, sum, src); err != nil {
			t.Fatalf("WriteSummary %s: %v", sum.SessionID, err)
		}
	}

	got, err := LoadSessionsForTickets([]string{"_root/epic-0001", "loom/child-0001", "warp/child-0002"})
	if err != nil {
		t.Fatalf("LoadSessionsForTickets: %v", err)
	}
	var ids []string
	for _, s := range got {
		ids = append(ids, s.SessionID)
	}
	if want := []string{"early", "shared"}; strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("sessions = %v, want %v (each session once, by its earliest commit for any id)", ids, want)
	}

	// The single-id reader is the same query narrowed to one marker.
	got, err = LoadSessionsForTicket("warp/child-0002")
	if err != nil {
		t.Fatalf("LoadSessionsForTicket: %v", err)
	}
	if len(got) != 1 || got[0].SessionID != "shared" {
		t.Fatalf("LoadSessionsForTicket(warp/child-0002) = %v, want the shared session alone", got)
	}
}

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
