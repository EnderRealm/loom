package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type detailModel struct {
	project *Project
	cursor  int
	offset  int
	width   int
	height  int
}

func newDetailModel(p *Project, w, h int) detailModel {
	return detailModel{project: p, width: w, height: h}
}

func (m *detailModel) setSize(w, h int) {
	m.width = w
	m.height = h
}

func (m detailModel) update(msg tea.Msg) (detailModel, tea.Cmd) {
	if _, ok := msg.(tea.KeyMsg); !ok {
		return m, nil
	}
	km := msg.(tea.KeyMsg)
	switch km.String() {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.project != nil && m.cursor < len(m.project.Sessions)-1 {
			m.cursor++
		}
	}
	return m, nil
}

func (m detailModel) view() string {
	if m.project == nil {
		return ""
	}
	p := m.project

	boxWidth := m.width - 4
	if boxWidth < 40 {
		boxWidth = 40
	}

	var b strings.Builder

	// Header: project name + reconstructed path + root slug.
	title := p.Name
	if title == "" {
		title = p.Slug
	}
	b.WriteString(StyleSection.Render(title))
	b.WriteString("\n")
	if p.Path != "" {
		b.WriteString(StyleDim.Render(shortenPath(p.Path)))
		b.WriteString("\n")
	}
	b.WriteString(StyleDim.Render("slug: " + p.Slug))
	b.WriteString("\n\n")

	// Stats grid.
	b.WriteString(statRow("Agents", strings.Join(p.Agents, ", ")))
	b.WriteString(statRow("Sessions", fmt.Sprintf("%d", p.SessionCount)))
	if n := len(p.Worktrees); n > 0 {
		b.WriteString(statRow("Worktrees",
			StyleInfo.Render(fmt.Sprintf("%d", n))+"  "+
				StyleDim.Render(truncate(strings.Join(p.Worktrees, ", "), 60))))
	}
	b.WriteString(statRow("Total bytes", humanBytes(p.BytesTotal)))
	if p.PendingCount > 0 {
		b.WriteString(statRow("Pending",
			StyleWarning.Render(fmt.Sprintf("%d session(s), %s not shipped",
				p.PendingCount, humanBytes(p.PendingBytes)))))
	} else {
		b.WriteString(statRow("Pending", StyleDim.Render("none")))
	}
	if !p.LastActivity.IsZero() {
		b.WriteString(statRow("Last seen",
			fmt.Sprintf("%s (%s ago)",
				p.LastActivity.Local().Format("2006-01-02 15:04"),
				humanDuration(time.Since(p.LastActivity)))))
	}
	b.WriteString("\n")

	// Tickets section.
	b.WriteString(StyleSection.Render("TICKETS"))
	b.WriteString("\n")
	if p.Tickets == nil {
		b.WriteString(StyleDim.Render("  no .tickets directory"))
	} else {
		b.WriteString(fmt.Sprintf("  %s  %s\n",
			StyleBold.Render(fmt.Sprintf("%d total", p.Tickets.Total)),
			StyleDim.Render(p.Tickets.Dir)))
		b.WriteString("  ")
		b.WriteString(renderStatusLine(p.Tickets.Status))
		b.WriteString("\n")
		if len(p.Tickets.Type) > 0 {
			b.WriteString("  ")
			b.WriteString(renderTypeLine(p.Tickets.Type))
		}
	}
	b.WriteString("\n\n")

	// Sessions section.
	b.WriteString(StyleSection.Render("SESSIONS"))
	b.WriteString("\n")
	b.WriteString(m.renderSessionTable(boxWidth))

	content := b.String()
	return StyleOverlayBorder.Width(boxWidth).Render(content)
}

func statRow(label, value string) string {
	return StyleFieldKey.Render(label) + "  " + value + "\n"
}

func renderStatusLine(counts map[string]int) string {
	order := []string{"open", "ready", "backlog", "done", "closed"}
	var parts []string
	for _, s := range order {
		if n, ok := counts[s]; ok && n > 0 {
			parts = append(parts, StatusPill(s, n))
		}
	}
	if len(parts) == 0 {
		return StyleDim.Render("no active tickets")
	}
	return strings.Join(parts, StyleDim.Render(" · "))
}

func renderTypeLine(counts map[string]int) string {
	order := []string{"feature", "bug", "epic"}
	var parts []string
	for _, t := range order {
		if n, ok := counts[t]; ok && n > 0 {
			c := ticketTypeColors[t]
			parts = append(parts,
				lipgloss.NewStyle().Foreground(c).Render(t)+
					StyleDim.Render(":")+
					lipgloss.NewStyle().Foreground(colorWhite).Render(fmt.Sprintf("%d", n)))
		}
	}
	return strings.Join(parts, StyleDim.Render(" · "))
}

func (m detailModel) renderSessionTable(width int) string {
	if m.project == nil || len(m.project.Sessions) == 0 {
		return StyleDim.Render("  (no sessions)")
	}
	// Pre-sort: staged first (live work), then by modified desc.
	sessions := make([]Session, len(m.project.Sessions))
	copy(sessions, m.project.Sessions)
	sort.Slice(sessions, func(i, j int) bool {
		if sessions[i].Pending != sessions[j].Pending {
			return sessions[i].Pending > sessions[j].Pending
		}
		return sessions[i].Modified.After(sessions[j].Modified)
	})

	var b strings.Builder
	b.WriteString("  ")
	b.WriteString(padRight(StyleColHeader.Render("AGENT"), 14))
	b.WriteString(padRight(StyleColHeader.Render("SESSION"), 14))
	b.WriteString(padRight(StyleColHeader.Render("WORKTREE"), 24))
	b.WriteString(padRight(StyleColHeader.Render("SIZE"), 12))
	b.WriteString(padRight(StyleColHeader.Render("PENDING"), 12))
	b.WriteString(padRight(StyleColHeader.Render("FLAGS"), 10))
	b.WriteString(StyleColHeader.Render("SEEN"))
	b.WriteString("\n")

	// Cap at what fits in the overlay.
	max := len(sessions)
	cap := m.height - 14
	if cap > 0 && max > cap {
		max = cap
	}
	for i := 0; i < max; i++ {
		s := sessions[i]
		b.WriteString("  ")
		b.WriteString(padRight(AgentBadge(s.Agent, false), 14))
		b.WriteString(padRight(StyleDim.Render(shortID(s.SessionID)), 14))

		wt := s.Worktree
		if wt == "" {
			wt = StyleDim.Render("—")
		} else {
			wt = StyleInfo.Render(truncate(wt, 23))
		}
		b.WriteString(padRight(wt, 24))

		size := s.ReceivedSize
		if s.StageSize > size {
			size = s.StageSize
		}
		b.WriteString(padRight(lipgloss.NewStyle().Foreground(colorGray).Render(humanBytes(size)), 12))

		if s.Pending > 0 {
			b.WriteString(padRight(StyleWarning.Render(humanBytes(s.Pending)), 12))
		} else {
			b.WriteString(padRight(StyleDim.Render("—"), 12))
		}

		flags := sessionFlags(s)
		b.WriteString(padRight(flags, 10))

		if !s.Modified.IsZero() {
			b.WriteString(StyleDim.Render(humanDuration(time.Since(s.Modified))))
		}
		b.WriteString("\n")
	}
	if max < len(sessions) {
		b.WriteString(StyleDim.Render(fmt.Sprintf("  … %d more", len(sessions)-max)))
	}
	return b.String()
}

func sessionFlags(s Session) string {
	var parts []string
	if s.Received {
		parts = append(parts, StyleSuccess.Render("R"))
	} else {
		parts = append(parts, StyleDim.Render("·"))
	}
	if s.Staged {
		parts = append(parts, StyleInfo.Render("S"))
	} else {
		parts = append(parts, StyleDim.Render("·"))
	}
	return strings.Join(parts, " ")
}

func shortID(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:8]
}
