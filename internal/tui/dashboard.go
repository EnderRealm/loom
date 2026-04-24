package tui

import (
	"fmt"
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
	if selectedSlug != "" {
		for i, pr := range m.projects {
			if pr.Slug == selectedSlug {
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

// Column widths for the dashboard table.
const (
	colProject   = 24
	colWorktrees = 10
	colAgents    = 22
	colSessions  = 10
	colSize      = 12
	colPending   = 10
	colTickets   = 14
	colAge       = 8
)

func (m dashboardModel) view() string {
	if m.width == 0 || m.height == 0 {
		return ""
	}

	var b strings.Builder
	// Header row.
	b.WriteString("  ")
	b.WriteString(padRight(StyleColHeader.Render("PROJECT"), colProject))
	b.WriteString(padRight(StyleColHeader.Render("WORKTREES"), colWorktrees))
	b.WriteString(padRight(StyleColHeader.Render("AGENTS"), colAgents))
	b.WriteString(padRight(StyleColHeader.Render("SESSIONS"), colSessions))
	b.WriteString(padRight(StyleColHeader.Render("SIZE"), colSize))
	b.WriteString(padRight(StyleColHeader.Render("PENDING"), colPending))
	b.WriteString(padRight(StyleColHeader.Render("TICKETS"), colTickets))
	b.WriteString(StyleColHeader.Render("SEEN"))
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

	var wtCell string
	if n := len(p.Worktrees); n > 0 {
		wtCell = padRightBg(selBg.Foreground(colorInfo).Render(fmt.Sprintf("+%d", n)), colWorktrees, bg)
	} else {
		wtCell = padRightBg(selBg.Foreground(colorMuted).Render("—"), colWorktrees, bg)
	}

	agents := strings.Join(p.Agents, ",")
	agentCell := padRightBg(selBg.Foreground(colorGray).Render(truncate(agents, colAgents-1)), colAgents, bg)

	sessCell := padRightBg(selBg.Foreground(colorWhite).Render(fmt.Sprintf("%d", p.SessionCount)), colSessions, bg)

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
	line := sp + projCell + wtCell + agentCell + sessCell + sizeCell + pendCell + tkCell + age
	if selected && m.width > 0 {
		rendered := lipgloss.Width(line)
		if rendered < m.width {
			line += selBg.Render(strings.Repeat(" ", m.width-rendered))
		}
	}
	return line
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
