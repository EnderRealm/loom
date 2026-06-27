package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"loom/internal/summaries"
)

// Per-section display caps. Overflow is always surfaced with a "+N more" line
// so nothing is silently dropped.
const (
	maxActRepos    = 12
	maxActCommits  = 15
	maxActSessions = 12
	maxActTickets  = 8
)

// activityModel is the rolling last-24-hours overlay: repos touched, sessions
// started, commits landed, and tickets created/closed. Content can exceed the
// viewport, so it scrolls vertically by offset.
type activityModel struct {
	width   int
	height  int
	offset  int
	loaded  bool
	data    *summaries.ActivityView
	tickets TicketActivity
}

func (m *activityModel) setSize(w, h int) {
	m.width = w
	m.height = h
}

func (m *activityModel) setData(av *summaries.ActivityView, ta TicketActivity) {
	m.data = av
	m.tickets = ta
	m.loaded = true
	m.offset = 0
}

func (m activityModel) update(msg tea.Msg) (activityModel, tea.Cmd) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch km.String() {
	case "up", "k":
		if m.offset > 0 {
			m.offset--
		}
	case "down", "j":
		m.offset++
	case "pgup":
		m.offset -= m.bodyRows()
		if m.offset < 0 {
			m.offset = 0
		}
	case "pgdown":
		m.offset += m.bodyRows()
	case "g":
		m.offset = 0
	}
	return m, nil
}

func (m activityModel) bodyRows() int {
	r := m.height - 4
	if r < 1 {
		r = 1
	}
	return r
}

func (m activityModel) view() string {
	boxWidth := m.width - 4
	if boxWidth < 40 {
		boxWidth = 40
	}
	if !m.loaded {
		return StyleOverlayBorder.Width(boxWidth).Render(
			StyleSection.Render("LAST 24 HOURS") + "\n\n" + StyleDim.Render("  loading…"))
	}

	lines := m.lines(boxWidth)
	rows := m.bodyRows()
	off := m.offset
	if off > len(lines)-1 {
		off = max(0, len(lines)-1)
	}
	end := off + rows
	if end > len(lines) {
		end = len(lines)
	}
	content := strings.Join(lines[off:end], "\n")
	if end < len(lines) {
		content += "\n" + StyleDim.Render(fmt.Sprintf("  ↓ %d more line(s)", len(lines)-end))
	}
	return StyleOverlayBorder.Width(boxWidth).Render(content)
}

// lines renders the whole screen to a flat slice so the viewport can window
// it. Each entry is one display row free of embedded newlines.
func (m activityModel) lines(width int) []string {
	var out []string
	add := func(s string) { out = append(out, s) }

	add(StyleSection.Render("LAST 24 HOURS"))
	av := m.data
	since := "—"
	if av != nil && !av.Since.IsZero() {
		since = av.Since.Local().Format("Mon Jan 2 15:04")
	}
	add(StyleDim.Render("since " + since))
	add("")

	switch {
	case av == nil || (!av.Available && !av.Outdated):
		add(StyleDim.Render("  summaries.db not found — run `loom summarize`"))
		return out
	case av.Outdated:
		add(StyleWarning.Render("  summary DB predates this view"))
		add(StyleDim.Render("  run `loom summarize --rebuild` to populate commits"))
		return out
	}

	created := len(m.tickets.Created)
	closed := len(m.tickets.Closed)
	repos := len(av.Repos)
	// Only claim a quiet window when the ticket store was actually readable;
	// an unresolved store leaves created/closed at 0 without proving it.
	if repos == 0 && len(av.Sessions) == 0 && len(av.Commits) == 0 &&
		created == 0 && closed == 0 && m.tickets.Available {
		add(StyleDim.Render("  no activity in the last 24 hours"))
		return out
	}

	add(m.summaryLine(repos, len(av.Sessions), len(av.Commits), created, closed))
	add("")
	out = append(out, m.repoSection(width)...)
	out = append(out, m.commitSection()...)
	out = append(out, m.sessionSection()...)
	out = append(out, m.ticketSection(width)...)
	return out
}

func (m activityModel) summaryLine(repos, sessions, commits, created, closed int) string {
	num := func(n int) string {
		return lipgloss.NewStyle().Foreground(colorWhite).Render(itoa(n))
	}
	dot := StyleDim.Render(" · ")
	return num(repos) + StyleDim.Render(" repos") + dot +
		num(sessions) + StyleDim.Render(" sessions") + dot +
		num(commits) + StyleDim.Render(" commits") + dot +
		StyleDim.Render("tickets ") +
		StyleAccent.Render("+"+itoa(created)) + StyleDim.Render("/") +
		StyleSuccess.Render("✓"+itoa(closed))
}

func (m activityModel) repoSection(width int) []string {
	out := []string{StyleSection.Render("REPOS TOUCHED")}
	if len(m.data.Repos) == 0 {
		return append(out, StyleDim.Render("  (none)"), "")
	}
	const sessCol, comCol = 10, 9
	repoW := width - sessCol - comCol - 2
	if repoW < 16 {
		repoW = 16
	}
	out = append(out, "  "+
		padRight(StyleColHeader.Render("REPO"), repoW)+
		padRight(StyleColHeader.Render("SESSIONS"), sessCol)+
		StyleColHeader.Render("COMMITS"))

	n := len(m.data.Repos)
	if n > maxActRepos {
		n = maxActRepos
	}
	for i := 0; i < n; i++ {
		r := m.data.Repos[i]
		out = append(out, "  "+
			padRight(lipgloss.NewStyle().Foreground(colorWhite).Render(truncate(repoDisplay(r.Repo), repoW-1)), repoW)+
			padRight(lipgloss.NewStyle().Foreground(colorGray).Render(itoa(r.Sessions)), sessCol)+
			lipgloss.NewStyle().Foreground(colorGray).Render(itoa(r.Commits)))
	}
	if len(m.data.Repos) > n {
		out = append(out, StyleDim.Render(fmt.Sprintf("  … +%d more", len(m.data.Repos)-n)))
	}
	return append(out, "")
}

func (m activityModel) commitSection() []string {
	out := []string{StyleSection.Render("COMMITS")}
	if len(m.data.Commits) == 0 {
		return append(out, StyleDim.Render("  (none)"), "")
	}
	n := len(m.data.Commits)
	if n > maxActCommits {
		n = maxActCommits
	}
	for i := 0; i < n; i++ {
		c := m.data.Commits[i]
		out = append(out, "  "+
			StyleInfo.Render(truncate(repoDisplay(c.Repo), 24))+
			StyleDim.Render(" · ")+
			lipgloss.NewStyle().Foreground(colorYellow).Render(shortHash(c.Hash))+
			StyleDim.Render(" · ")+
			lipgloss.NewStyle().Foreground(colorWhite).Render(truncate(c.Subject, 48)))
	}
	if len(m.data.Commits) > n {
		out = append(out, StyleDim.Render(fmt.Sprintf("  … +%d more", len(m.data.Commits)-n)))
	}
	return append(out, "")
}

func (m activityModel) sessionSection() []string {
	out := []string{StyleSection.Render("SESSIONS")}
	if len(m.data.Sessions) == 0 {
		return append(out, StyleDim.Render("  (none)"), "")
	}
	n := len(m.data.Sessions)
	if n > maxActSessions {
		n = maxActSessions
	}
	for i := 0; i < n; i++ {
		s := m.data.Sessions[i]
		metrics := lipgloss.NewStyle().Foreground(colorWhite).Render(itoa(s.Turns)) +
			StyleDim.Render("·") +
			lipgloss.NewStyle().Foreground(colorGray).Render(itoa(s.Tools)) +
			StyleDim.Render("·")
		if s.Errors > 0 {
			metrics += StyleWarning.Render(itoa(s.Errors))
		} else {
			metrics += StyleDim.Render("0")
		}
		when := StyleDim.Render("—")
		if !s.Start.IsZero() {
			when = StyleDim.Render(humanDuration(time.Since(s.Start)) + " ago")
		}
		out = append(out, "  "+
			AgentBadge(s.Agent, false)+" "+
			padRight(StyleInfo.Render(truncate(repoDisplay(s.Repo), 22)), 23)+
			padRight(metrics, 14)+when)
	}
	if len(m.data.Sessions) > n {
		out = append(out, StyleDim.Render(fmt.Sprintf("  … +%d more", len(m.data.Sessions)-n)))
	}
	return append(out, "")
}

func (m activityModel) ticketSection(width int) []string {
	out := []string{StyleSection.Render("TICKETS")}
	if !m.tickets.Available {
		return append(out, StyleDim.Render("  tk store unavailable"))
	}
	out = append(out, m.ticketList("Created", m.tickets.Created, width)...)
	out = append(out, m.ticketList("Closed", m.tickets.Closed, width)...)
	return out
}

func (m activityModel) ticketList(label string, items []TicketChange, width int) []string {
	out := []string{"  " + StyleBold.Render(label) + StyleDim.Render(fmt.Sprintf(" (%d)", len(items)))}
	if len(items) == 0 {
		return append(out, StyleDim.Render("    (none)"))
	}
	titleW := width - 30
	if titleW < 16 {
		titleW = 16
	}
	n := len(items)
	if n > maxActTickets {
		n = maxActTickets
	}
	for i := 0; i < n; i++ {
		t := items[i]
		out = append(out, "    "+
			padRight(StyleDim.Render(t.ID), 26)+
			lipgloss.NewStyle().Foreground(colorWhite).Render(truncate(t.Title, titleW)))
	}
	if len(items) > n {
		out = append(out, StyleDim.Render(fmt.Sprintf("    … +%d more", len(items)-n)))
	}
	return out
}

// repoDisplay shortens a repo identity for the column: git remotes collapse to
// "owner/repo", cwd paths to their basename, everything else (slugs) is shown
// raw. Mirrors the dashboard's shortRepo precedence.
func repoDisplay(repo string) string {
	if repo == "" {
		return "unknown"
	}
	if strings.Contains(repo, "://") || strings.HasPrefix(repo, "git@") {
		if r := shortRepo(repo); r != "" {
			return r
		}
	}
	if strings.HasPrefix(repo, "/") {
		return filepath.Base(repo)
	}
	return repo
}

func shortHash(h string) string {
	if len(h) > 7 {
		return h[:7]
	}
	return h
}
