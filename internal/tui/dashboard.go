package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type dashboardModel struct {
	projects []Project
	cursor   int
	offset   int
	width    int
	height   int
	sortCol  int
}

// sortColumn pairs a sortable header with the comparator for its natural
// sort direction. less reports whether a sorts before b.
type sortColumn struct {
	header string
	less   func(a, b Project) bool
}

// projName is the case-insensitive display name used as the deterministic
// final tiebreaker so ordering is stable regardless of incoming order.
func projName(p Project) string {
	n := p.Name
	if n == "" {
		n = p.Slug
	}
	return strings.ToLower(n)
}

// sortColumns is the ordered cycle of meaningful dashboard sort columns.
// Headers must match the displayed header text exactly.
var sortColumns = []sortColumn{
	{"PROJECT", func(a, b Project) bool {
		return projName(a) < projName(b)
	}},
	{"SESSIONS", func(a, b Project) bool {
		if a.SessionCount != b.SessionCount {
			return a.SessionCount > b.SessionCount
		}
		if !a.LastActivity.Equal(b.LastActivity) {
			return a.LastActivity.After(b.LastActivity)
		}
		return projName(a) < projName(b)
	}},
	{"AGENTS", func(a, b Project) bool {
		aj, bj := strings.Join(a.Agents, ","), strings.Join(b.Agents, ",")
		if aj != bj {
			return aj < bj
		}
		return projName(a) < projName(b)
	}},
	{"ACTIVITY", func(a, b Project) bool {
		if a.TurnCount != b.TurnCount {
			return a.TurnCount > b.TurnCount
		}
		if a.ToolCallCount != b.ToolCallCount {
			return a.ToolCallCount > b.ToolCallCount
		}
		return projName(a) < projName(b)
	}},
	{"SIZE", func(a, b Project) bool {
		if a.BytesTotal != b.BytesTotal {
			return a.BytesTotal > b.BytesTotal
		}
		return projName(a) < projName(b)
	}},
	{"PENDING", func(a, b Project) bool {
		if a.PendingCount != b.PendingCount {
			return a.PendingCount > b.PendingCount
		}
		if a.PendingBytes != b.PendingBytes {
			return a.PendingBytes > b.PendingBytes
		}
		return projName(a) < projName(b)
	}},
	{"TICKETS", func(a, b Project) bool {
		at, bt := ticketTotal(a), ticketTotal(b)
		if at != bt {
			return at > bt
		}
		return projName(a) < projName(b)
	}},
	{"SEEN", func(a, b Project) bool {
		if !a.LastActivity.Equal(b.LastActivity) {
			return a.LastActivity.After(b.LastActivity)
		}
		return projName(a) < projName(b)
	}},
}

func ticketTotal(p Project) int {
	if p.Tickets == nil {
		return 0
	}
	return p.Tickets.Total
}

func (m *dashboardModel) setSize(w, h int) {
	m.width = w
	m.height = h
	m.clampOffset()
}

func (m *dashboardModel) setProjects(p []Project) {
	var selectedSlug string
	if s := m.selected(); s != nil {
		selectedSlug = s.Slug
	}
	m.projects = p
	m.applySort()
	m.reselect(selectedSlug)
}

// applySort orders the visible rows by the active sort column.
func (m *dashboardModel) applySort() {
	less := sortColumns[m.sortCol].less
	sort.SliceStable(m.projects, func(i, j int) bool {
		return less(m.projects[i], m.projects[j])
	})
}

// reselect restores the cursor to the row matching slug, clamping when the
// slug is gone or empty so the cursor stays in range.
func (m *dashboardModel) reselect(slug string) {
	if slug != "" {
		for i, pr := range m.projects {
			if pr.Slug == slug {
				m.cursor = i
				break
			}
		}
	}
	if m.cursor >= len(m.projects) {
		m.cursor = max(0, len(m.projects)-1)
	}
	m.clampOffset()
}

// cycleSort advances to the next sort column and re-sorts, preserving the
// current selection across the reorder.
func (m *dashboardModel) cycleSort() {
	var selectedSlug string
	if s := m.selected(); s != nil {
		selectedSlug = s.Slug
	}
	m.sortCol = (m.sortCol + 1) % len(sortColumns)
	m.applySort()
	m.reselect(selectedSlug)
}

func (m dashboardModel) selected() *Project {
	if m.cursor >= 0 && m.cursor < len(m.projects) {
		return &m.projects[m.cursor]
	}
	return nil
}

func (m dashboardModel) visibleRows() int {
	rows := m.height - 1
	if rows < 1 {
		rows = 1
	}
	return rows
}

func (m *dashboardModel) clampOffset() {
	visible := m.visibleRows()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+visible {
		m.offset = m.cursor - visible + 1
	}
	if m.offset < 0 {
		m.offset = 0
	}
}

func (m dashboardModel) update(msg tea.Msg) (dashboardModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
				m.clampOffset()
			}
		case "down", "j":
			if m.cursor < len(m.projects)-1 {
				m.cursor++
				m.clampOffset()
			}
		case "pgup":
			m.cursor -= m.visibleRows()
			if m.cursor < 0 {
				m.cursor = 0
			}
			m.clampOffset()
		case "pgdown":
			m.cursor += m.visibleRows()
			if m.cursor > len(m.projects)-1 {
				m.cursor = max(0, len(m.projects)-1)
			}
			m.clampOffset()
		case "g":
			m.cursor = 0
			m.clampOffset()
		case "G":
			m.cursor = max(0, len(m.projects)-1)
			m.clampOffset()
		case "s":
			m.cycleSort()
		}
	case tea.MouseMsg:
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			m.cursor -= 3
			if m.cursor < 0 {
				m.cursor = 0
			}
			m.clampOffset()
		case tea.MouseButtonWheelDown:
			m.cursor += 3
			if m.cursor > len(m.projects)-1 {
				m.cursor = max(0, len(m.projects)-1)
			}
			m.clampOffset()
		}
	}
	return m, nil
}

// Column widths for the dashboard table. Each must hold its header plus
// at least two trailing spaces so adjacent header text never runs
// together (e.g. "WORKTREESAGENTS"). The cells render values shorter
// than the header in every realistic case.
const (
	colProject   = 22
	colRepo      = 28 // owner/repo from git remote, "—" otherwise
	colWorktrees = 11 // "WORKTREES" + 2
	colAgents    = 16
	colSessions  = 10 // "SESSIONS" + 2
	colActivity  = 18 // turns·tools·errors
	colSize      = 10
	colPending   = 10
	colTickets   = 14
	colAge       = 6
)

func (m dashboardModel) view() string {
	if m.width == 0 || m.height == 0 {
		return ""
	}

	var b strings.Builder
	// Header row.
	b.WriteString("  ")
	b.WriteString(padRight(m.headerCell("PROJECT"), colProject))
	b.WriteString(padRight(m.headerCell("REPO"), colRepo))
	b.WriteString(padRight(m.headerCell("WORKTREES"), colWorktrees))
	b.WriteString(padRight(m.headerCell("AGENTS"), colAgents))
	b.WriteString(padRight(m.headerCell("SESSIONS"), colSessions))
	b.WriteString(padRight(m.headerCell("ACTIVITY"), colActivity))
	b.WriteString(padRight(m.headerCell("SIZE"), colSize))
	b.WriteString(padRight(m.headerCell("PENDING"), colPending))
	b.WriteString(padRight(m.headerCell("TICKETS"), colTickets))
	b.WriteString(m.headerCell("SEEN"))
	b.WriteString("\n")

	visible := m.visibleRows()
	end := m.offset + visible
	if end > len(m.projects) {
		end = len(m.projects)
	}
	for i := m.offset; i < end; i++ {
		b.WriteString(m.renderRow(m.projects[i], i == m.cursor))
		b.WriteString("\n")
	}
	for i := end - m.offset; i < visible; i++ {
		b.WriteString("\n")
	}
	return b.String()
}

// headerCell renders a column header, marking the active sort column with
// the active style. Width is unchanged — the marker is style-only.
func (m dashboardModel) headerCell(name string) string {
	if sortColumns[m.sortCol].header == name {
		return StyleColHeaderActive.Render(name)
	}
	return StyleColHeader.Render(name)
}

func (m dashboardModel) renderRow(p Project, selected bool) string {
	selBg := lipgloss.NewStyle()
	if selected {
		selBg = lipgloss.NewStyle().Background(colorSurface)
	}
	var bg *lipgloss.Style
	if selected {
		bg = &selBg
	}

	name := p.Name
	if name == "" {
		name = p.Slug
	}
	projCell := padRightBg(selBg.Foreground(colorWhite).Render(truncate(name, colProject-1)), colProject, bg)

	var repoCell string
	if r := shortRepo(p.GitRemote); r != "" {
		repoCell = padRightBg(selBg.Foreground(colorInfo).Render(truncate(r, colRepo-1)), colRepo, bg)
	} else {
		repoCell = padRightBg(selBg.Foreground(colorMuted).Render("—"), colRepo, bg)
	}

	var wtCell string
	if n := len(p.Worktrees); n > 0 {
		wtCell = padRightBg(selBg.Foreground(colorInfo).Render(fmt.Sprintf("+%d", n)), colWorktrees, bg)
	} else {
		wtCell = padRightBg(selBg.Foreground(colorMuted).Render("—"), colWorktrees, bg)
	}

	agents := strings.Join(p.Agents, ",")
	agentCell := padRightBg(selBg.Foreground(colorGray).Render(truncate(agents, colAgents-1)), colAgents, bg)

	sessCell := padRightBg(selBg.Foreground(colorWhite).Render(fmt.Sprintf("%d", p.SessionCount)), colSessions, bg)

	actCell := padRightBg(renderActivityCell(p, selBg), colActivity, bg)

	sizeCell := padRightBg(selBg.Foreground(colorGray).Render(humanBytes(p.BytesTotal)), colSize, bg)

	var pending string
	if p.PendingCount > 0 {
		pending = selBg.Foreground(colorWarning).Render(
			fmt.Sprintf("%d/%s", p.PendingCount, humanBytes(p.PendingBytes)))
	} else {
		pending = selBg.Foreground(colorMuted).Render("—")
	}
	pendCell := padRightBg(pending, colPending, bg)

	var tickets string
	if p.Tickets != nil {
		open := p.Tickets.Status["open"] + p.Tickets.Status["ready"]
		tickets = selBg.Foreground(colorWhite).Render(fmt.Sprintf("%d", p.Tickets.Total)) +
			selBg.Foreground(colorMuted).Render(fmt.Sprintf(" (%d open)", open))
	} else {
		tickets = selBg.Foreground(colorMuted).Render("—")
	}
	tkCell := padRightBg(tickets, colTickets, bg)

	var age string
	if p.LastActivity.IsZero() {
		age = selBg.Foreground(colorMuted).Render("—")
	} else {
		age = selBg.Foreground(colorGray).Render(humanDuration(time.Since(p.LastActivity)))
	}

	sp := "  "
	if selected {
		sp = selBg.Render("  ")
	}
	line := sp + projCell + repoCell + wtCell + agentCell + sessCell + actCell + sizeCell + pendCell + tkCell + age
	if selected && m.width > 0 {
		rendered := lipgloss.Width(line)
		if rendered < m.width {
			line += selBg.Render(strings.Repeat(" ", m.width-rendered))
		}
	}
	return line
}

// renderActivityCell formats project-level summary metrics as
// "turns·tools·errors" with errors highlighted when nonzero. Returns "—"
// when the project has no summary data yet.
func renderActivityCell(p Project, bg lipgloss.Style) string {
	if p.TurnCount == 0 && p.ToolCallCount == 0 && p.ErrorCount == 0 {
		return bg.Foreground(colorMuted).Render("—")
	}
	turns := bg.Foreground(colorWhite).Render(compactInt(p.TurnCount))
	tools := bg.Foreground(colorGray).Render(compactInt(p.ToolCallCount))
	dot := bg.Foreground(colorMuted).Render("·")
	out := turns + dot + tools
	if p.ErrorCount > 0 {
		errStyle := bg.Foreground(colorWarning)
		if rate := float64(p.ErrorCount) / float64(max(1, p.ToolCallCount)); rate >= 0.10 {
			errStyle = bg.Foreground(colorDanger)
		}
		out += dot + errStyle.Render(compactInt(p.ErrorCount))
	}
	return out
}

// shortRepo collapses a git remote URL to "owner/repo" for display.
// Returns "" when the remote is empty so the caller can render a dash.
func shortRepo(remote string) string {
	if remote == "" {
		return ""
	}
	s := strings.TrimSpace(remote)
	s = strings.TrimSuffix(s, ".git")
	s = strings.TrimSuffix(s, "/")
	switch {
	case strings.HasPrefix(s, "git@"):
		s = strings.TrimPrefix(s, "git@")
		s = strings.Replace(s, ":", "/", 1)
	case strings.HasPrefix(s, "ssh://"):
		s = strings.TrimPrefix(s, "ssh://")
		s = strings.TrimPrefix(s, "git@")
	case strings.HasPrefix(s, "https://"):
		s = strings.TrimPrefix(s, "https://")
	case strings.HasPrefix(s, "http://"):
		s = strings.TrimPrefix(s, "http://")
	}
	// Strip the host so the column shows "owner/repo".
	if i := strings.Index(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	return s
}

// compactInt formats large counts with K/M suffixes so they stay narrow.
func compactInt(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 10_000:
		return fmt.Sprintf("%dk", n/1000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

func shortenPath(p string) string {
	home, err := homeDir()
	if err == nil && home != "" && strings.HasPrefix(p, home) {
		return "~" + p[len(home):]
	}
	return p
}

func humanBytes(n int64) string {
	const (
		k = 1024
		m = 1024 * 1024
		g = 1024 * 1024 * 1024
	)
	switch {
	case n >= g:
		return fmt.Sprintf("%.1f GB", float64(n)/float64(g))
	case n >= m:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(m))
	case n >= k:
		return fmt.Sprintf("%.1f KB", float64(n)/float64(k))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func humanDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
