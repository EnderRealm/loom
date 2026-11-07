package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"loom/internal/config"
)

func dashboardSortHeader(m tea.Model) string {
	return sortColumns[m.(App).dashboard.sortCol].header
}

// TestDashboardSortPersistsAcrossLaunches: the column s lands on is the one the
// next launch opens on, through the file under LOOM_HOME rather than any state
// the first App carries.
func TestDashboardSortPersistsAcrossLaunches(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOOM_HOME", home)

	var m tea.Model = New()
	if got := dashboardSortHeader(m); got != "SESSIONS" {
		t.Fatalf("first launch sort = %s, want SESSIONS", got)
	}
	m, _ = m.Update(projectsLoadedMsg{
		{Slug: "small", Name: "small", BytesTotal: 1},
		{Slug: "big", Name: "big", BytesTotal: 100},
	})
	for dashboardSortHeader(m) != "SIZE" {
		m, _ = m.Update(key("s"))
	}
	if got := config.ReadDashboardSort(); got != "SIZE" {
		t.Fatalf("saved sort = %q after s, want SIZE", got)
	}

	var relaunched tea.Model = New()
	if got := dashboardSortHeader(relaunched); got != "SIZE" {
		t.Fatalf("relaunch sort = %s, want SIZE", got)
	}
	relaunched, _ = relaunched.Update(projectsLoadedMsg{
		{Slug: "small", Name: "small", BytesTotal: 1},
		{Slug: "big", Name: "big", BytesTotal: 100},
	})
	if first := relaunched.(App).dashboard.projects[0].Slug; first != "big" {
		t.Errorf("relaunch first row = %s, want big under SIZE", first)
	}
}

// TestDashboardSortFallsBackOnUnknownHeader: a saved value naming no current
// column — junk, or a header since removed — opens on SESSIONS.
func TestDashboardSortFallsBackOnUnknownHeader(t *testing.T) {
	for _, saved := range []string{"", "\x00garbage\n", "WORKTREES", "size"} {
		home := t.TempDir()
		t.Setenv("LOOM_HOME", home)
		if err := os.WriteFile(filepath.Join(home, "ui-dashboard-sort"), []byte(saved), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := dashboardSortHeader(New()); got != "SESSIONS" {
			t.Errorf("saved %q: sort = %s, want SESSIONS", saved, got)
		}
	}
}

// TestDashboardSortSaveFailureKeepsOrder: a save that cannot land still moves
// the sort and reports why on the status line.
func TestDashboardSortSaveFailureKeepsOrder(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOOM_HOME", filepath.Join(file, "loom"))

	var m tea.Model = New()
	before := dashboardSortHeader(m)
	m, cmd := m.Update(key("s"))
	if got := dashboardSortHeader(m); got == before {
		t.Errorf("sort stayed %s after a failed save", got)
	}
	msgs := drainCmd(cmd)
	if len(msgs) != 1 {
		t.Fatalf("s with a failed save returned %d messages, want 1", len(msgs))
	}
	status, ok := msgs[0].(statusMsg)
	if !ok || !strings.HasPrefix(string(status), "sort not saved: ") {
		t.Errorf("status = %#v, want a sort-not-saved line", msgs[0])
	}
}
