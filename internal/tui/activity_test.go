package tui

import (
	"strings"
	"testing"
	"time"

	"loom/internal/summaries"
)

// TestActivityViewStates confirms the activity overlay renders every degraded
// state without panicking: unloaded, DB missing, and outdated schema.
func TestActivityViewStates(t *testing.T) {
	var m activityModel
	m.setSize(120, 40)

	// Unloaded → loading hint.
	if !strings.Contains(m.view(), "loading") {
		t.Errorf("unloaded view should show a loading hint")
	}

	// DB missing.
	m.setData(&summaries.ActivityView{Available: false}, TicketActivity{})
	if !strings.Contains(m.view(), "loom summarize") {
		t.Errorf("missing-DB view should hint to run loom summarize")
	}

	// Outdated schema → rebuild hint.
	m.setData(&summaries.ActivityView{Outdated: true}, TicketActivity{})
	if !strings.Contains(m.view(), "--rebuild") {
		t.Errorf("outdated view should hint to run loom summarize --rebuild")
	}

	// Available DB, readable tk store, but a genuinely quiet window.
	m.setData(&summaries.ActivityView{Available: true, Since: time.Now().Add(-24 * time.Hour)},
		TicketActivity{Available: true})
	if !strings.Contains(m.view(), "no activity") {
		t.Errorf("empty view should report no activity")
	}

	// DB has activity but the tk store is unresolvable → explicit hint, and
	// the view must not falsely claim "no activity".
	m.setData(&summaries.ActivityView{
		Available: true,
		Since:     time.Now().Add(-24 * time.Hour),
		Repos:     []summaries.RepoActivity{{Repo: "https://github.com/EnderRealm/loom.git", Sessions: 1}},
	}, TicketActivity{Available: false})
	out := m.view()
	if !strings.Contains(out, "tk store unavailable") {
		t.Errorf("unavailable tk store should show a hint")
	}
	if strings.Contains(out, "no activity") {
		t.Errorf("must not claim no activity when the tk store is unavailable")
	}
}

// TestActivityViewPopulated renders a populated window with more commits than
// the cap and confirms the overflow indicator is visible.
func TestActivityViewPopulated(t *testing.T) {
	now := time.Now()
	av := &summaries.ActivityView{
		Available: true,
		Since:     now.Add(-24 * time.Hour),
		Repos:     []summaries.RepoActivity{{Repo: "https://github.com/EnderRealm/loom.git", Sessions: 2, Commits: 20}},
		Sessions: []summaries.SessionActivity{
			{Agent: "claude-code", Repo: "https://github.com/EnderRealm/loom.git", Start: now.Add(-time.Hour), Turns: 5, Tools: 12, Errors: 1},
		},
	}
	for i := 0; i < maxActCommits+5; i++ {
		av.Commits = append(av.Commits, summaries.CommitActivity{
			CommittedAt: now.Add(-time.Hour),
			Repo:        "https://github.com/EnderRealm/loom.git",
			Hash:        "abc1234",
			Subject:     "commit",
		})
	}
	ta := TicketActivity{
		Created: []TicketChange{{ID: "loom/x-1", Title: "New thing", Project: "loom", When: now}},
		Closed:  []TicketChange{{ID: "loom/y-2", Title: "Done thing", Project: "loom", When: now}},
	}

	var m activityModel
	m.setSize(120, 80)
	m.setData(av, ta)
	out := m.view()
	if !strings.Contains(out, "LAST 24 HOURS") {
		t.Errorf("expected header in activity view")
	}
	if !strings.Contains(out, "REPOS TOUCHED") || !strings.Contains(out, "COMMITS") {
		t.Errorf("expected section headers in activity view")
	}
	if !strings.Contains(out, "more") {
		t.Errorf("expected a '+N more' overflow indicator for capped commits")
	}
}
