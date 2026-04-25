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
		b.WriteString(StyleDim.Render("  no tickets found for this project"))
	} else {
		t := p.Tickets
		header := StyleBold.Render(fmt.Sprintf("%d total", t.Total))
		if t.ProjectName != "" {
			header = StyleAccent.Render(t.ProjectName) + "  " + header
		}
		if t.Dir != "" {
			header += "  " + StyleDim.Render(shortenPath(t.Dir))
		}
		b.WriteString("  " + header + "\n")

		b.WriteString("  ")
		b.WriteString(renderStatusLine(t.Status))
		b.WriteString("\n")

		if len(t.Priority) > 0 {
			b.WriteString("  ")
			b.WriteString(renderPriorityLine(t.Priority))
			b.WriteString("\n")
		}
		if len(t.Type) > 0 {
			b.WriteString("  ")
			b.WriteString(renderTypeLine(t.Type))
			b.WriteString("\n")
		}

		if len(t.OpenTop) > 0 {
			b.WriteString("\n  ")
			b.WriteString(StyleSection.Render("OPEN"))
			b.WriteString("\n")
			titleWidth := boxWidth - 18
			if titleWidth < 20 {
				titleWidth = 20
			}
			for _, tk := range t.OpenTop {
				b.WriteString("  ")
				b.WriteString(PriorityBadge(tk.Priority))
				b.WriteString("  ")
				b.WriteString(StyleDim.Render(ticketIDSuffix(tk.ID)))
				b.WriteString("  ")
				b.WriteString(truncate(tk.Title, titleWidth))
				b.WriteString("\n")
			}
		}
	}
	b.WriteString("\n")

	// Sessions section.
	b.WriteString(StyleSection.Render("SESSIONS"))
	b.WriteString("\n")
	b.WriteString(m.renderSessionTable(boxWidth))

	// Patterns section — only when summaries.db has anything for this project.
	if patterns := m.renderPatterns(boxWidth); patterns != "" {
		b.WriteString("\n")
		b.WriteString(StyleSection.Render("PATTERNS"))
		b.WriteString("\n")
		b.WriteString(patterns)
	}

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

func renderPriorityLine(counts map[int]int) string {
	var parts []string
	for p := 0; p <= 4; p++ {
		if n, ok := counts[p]; ok && n > 0 {
			parts = append(parts,
				PriorityBadge(p)+
					StyleDim.Render(":")+
					lipgloss.NewStyle().Foreground(colorWhite).Render(fmt.Sprintf("%d", n)))
		}
	}
	if len(parts) == 0 {
		return StyleDim.Render("no priority data")
	}
	return strings.Join(parts, StyleDim.Render(" · "))
}

// ticketIDSuffix returns the 4-char hex suffix of a ticket ID (e.g. the
// "1a2b" in "foo-1a2b"), or the full ID for namespaced forms.
func ticketIDSuffix(id string) string {
	if i := strings.LastIndex(id, "-"); i >= 0 && i+1 < len(id) {
		return id[i+1:]
	}
	return id
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
	b.WriteString(padRight(StyleColHeader.Render("WORKTREE"), 22))
	b.WriteString(padRight(StyleColHeader.Render("TURNS"), 7))
	b.WriteString(padRight(StyleColHeader.Render("TOOLS"), 7))
	b.WriteString(padRight(StyleColHeader.Render("ERRS"), 6))
	b.WriteString(padRight(StyleColHeader.Render("SIZE"), 10))
	b.WriteString(padRight(StyleColHeader.Render("PENDING"), 10))
	b.WriteString(padRight(StyleColHeader.Render("FLAGS"), 8))
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
			wt = StyleInfo.Render(truncate(wt, 21))
		}
		b.WriteString(padRight(wt, 22))

		// Per-session summary metrics. Empty cells when summarizer hasn't
		// processed this session yet.
		if s.Summary != nil {
			b.WriteString(padRight(lipgloss.NewStyle().Foreground(colorWhite).Render(
				fmt.Sprintf("%d", s.Summary.TurnCount)), 7))
			b.WriteString(padRight(lipgloss.NewStyle().Foreground(colorGray).Render(
				fmt.Sprintf("%d", s.Summary.ToolCallCount)), 7))
			if s.Summary.ErrorCount > 0 {
				b.WriteString(padRight(StyleWarning.Render(
					fmt.Sprintf("%d", s.Summary.ErrorCount)), 6))
			} else {
				b.WriteString(padRight(StyleDim.Render("·"), 6))
			}
		} else {
			b.WriteString(padRight(StyleDim.Render("·"), 7))
			b.WriteString(padRight(StyleDim.Render("·"), 7))
			b.WriteString(padRight(StyleDim.Render("·"), 6))
		}

		size := s.ReceivedSize
		if s.StageSize > size {
			size = s.StageSize
		}
		b.WriteString(padRight(lipgloss.NewStyle().Foreground(colorGray).Render(humanBytes(size)), 10))

		if s.Pending > 0 {
			b.WriteString(padRight(StyleWarning.Render(humanBytes(s.Pending)), 10))
		} else {
			b.WriteString(padRight(StyleDim.Render("—"), 10))
		}

		flags := sessionFlags(s)
		b.WriteString(padRight(flags, 8))

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

// renderPatterns shows project-level summary insight: top tools by call
// count and by error rate, plus compaction count. Returns "" when there's
// no summary data for this project (summarizer hasn't run, or DB missing).
func (m detailModel) renderPatterns(width int) string {
	if m.project == nil {
		return ""
	}
	p := m.project
	if p.TurnCount == 0 && p.ToolCallCount == 0 && len(p.TopTools) == 0 {
		return ""
	}

	var b strings.Builder

	// Top-line aggregate row.
	b.WriteString("  ")
	b.WriteString(StyleFieldKey.Render("Turns"))
	b.WriteString("  ")
	b.WriteString(lipgloss.NewStyle().Foreground(colorWhite).Render(
		fmt.Sprintf("%d", p.TurnCount)))
	b.WriteString(StyleDim.Render("  ·  "))
	b.WriteString(StyleFieldKey.Render("Tool calls"))
	b.WriteString("  ")
	b.WriteString(lipgloss.NewStyle().Foreground(colorWhite).Render(
		fmt.Sprintf("%d", p.ToolCallCount)))
	b.WriteString(StyleDim.Render("  ·  "))
	b.WriteString(StyleFieldKey.Render("Errors"))
	b.WriteString("  ")
	if p.ErrorCount > 0 {
		b.WriteString(StyleWarning.Render(fmt.Sprintf("%d", p.ErrorCount)))
	} else {
		b.WriteString(StyleDim.Render("0"))
	}
	if p.Compactions > 0 {
		b.WriteString(StyleDim.Render("  ·  "))
		b.WriteString(StyleFieldKey.Render("Compactions"))
		b.WriteString("  ")
		b.WriteString(lipgloss.NewStyle().Foreground(colorWhite).Render(
			fmt.Sprintf("%d", p.Compactions)))
	}
	b.WriteString("\n\n")

	// Top tools by call count (top 6, capped to fit the box).
	if len(p.TopTools) > 0 {
		b.WriteString("  ")
		b.WriteString(StyleColHeader.Render(padRight("TOOL", 14)))
		b.WriteString(StyleColHeader.Render(padRight("CALLS", 10)))
		b.WriteString(StyleColHeader.Render(padRight("ERRORS", 14)))
		b.WriteString(StyleColHeader.Render("AVG"))
		b.WriteString("\n")

		max := len(p.TopTools)
		if max > 6 {
			max = 6
		}
		for i := 0; i < max; i++ {
			ts := p.TopTools[i]
			b.WriteString("  ")
			b.WriteString(padRight(
				lipgloss.NewStyle().Foreground(colorWhite).Render(ts.Kind), 14))
			b.WriteString(padRight(
				lipgloss.NewStyle().Foreground(colorGray).Render(
					fmt.Sprintf("%d", ts.Calls)), 10))

			// Error count + rate, color-graded.
			errCell := StyleDim.Render("·")
			if ts.Errors > 0 {
				rate := float64(ts.Errors) / float64(max1(ts.Calls)) * 100
				rateStr := fmt.Sprintf("(%.0f%%)", rate)
				style := StyleWarning
				if rate >= 10 {
					style = StyleDanger
				}
				errCell = style.Render(
					fmt.Sprintf("%d ", ts.Errors)) +
					StyleDim.Render(rateStr)
			}
			b.WriteString(padRight(errCell, 14))

			b.WriteString(StyleDim.Render(humanShortDuration(ts.AvgMs)))
			b.WriteString("\n")
		}
		// Shaved-off remainder line so users know when there's more.
		if len(p.TopTools) > max {
			b.WriteString(StyleDim.Render(fmt.Sprintf(
				"  … %d more tool kind(s)\n", len(p.TopTools)-max)))
		}
	}

	_ = width
	return b.String()
}

// max1 floors at 1 so we never divide by zero when computing error rates.
func max1(n int) int {
	if n < 1 {
		return 1
	}
	return n
}

// humanShortDuration formats a millisecond duration compactly: "8s", "2m",
// "1h", "0".
func humanShortDuration(ms int64) string {
	if ms <= 0 {
		return "0"
	}
	switch {
	case ms < 1000:
		return fmt.Sprintf("%dms", ms)
	case ms < 60_000:
		return fmt.Sprintf("%ds", ms/1000)
	case ms < 3_600_000:
		return fmt.Sprintf("%dm", ms/60_000)
	default:
		return fmt.Sprintf("%dh", ms/3_600_000)
	}
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
