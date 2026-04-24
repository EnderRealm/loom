package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	colorRed     = lipgloss.Color("#d75f5f")
	colorGreen   = lipgloss.Color("#87af5f")
	colorYellow  = lipgloss.Color("#d7af5f")
	colorBlue    = lipgloss.Color("#5f87af")
	colorMagenta = lipgloss.Color("#af87af")
	colorCyan    = lipgloss.Color("#5fafaf")
	colorWhite   = lipgloss.Color("#d0d0d0")
	colorGray    = lipgloss.Color("#767676")
	colorBlack   = lipgloss.Color("#303030")

	colorSurface = lipgloss.Color("#4a4a4a")
	colorSubtle  = lipgloss.Color("#4e4e4e")
	colorBadgeBg = lipgloss.Color("#444444")

	colorDanger  = colorRed
	colorSuccess = colorGreen
	colorWarning = colorYellow
	colorInfo    = colorBlue
	colorAccent  = colorCyan
	colorMuted   = colorGray
)

var agentColors = map[string]lipgloss.Color{
	"claude-code": colorCyan,
	"codex-cli":   colorMagenta,
}

func agentColor(agent string) lipgloss.Color {
	if c, ok := agentColors[agent]; ok {
		return c
	}
	return colorWhite
}

// Status colors mirror the ticket TUI so the two dashboards read the same.
var ticketStatusColors = map[string]lipgloss.Color{
	"backlog": colorGray,
	"ready":   colorCyan,
	"open":    colorYellow,
	"done":    colorGreen,
	"closed":  colorGray,
}

var ticketTypeColors = map[string]lipgloss.Color{
	"feature": colorGreen,
	"bug":     colorRed,
	"epic":    colorMagenta,
}

// Priority palette matches ~/code/ticket/internal/tui/styles.go: P0 critical
// (red), P1 high (yellow), P2 normal (white), P3/P4 low (gray).
var ticketPriorityColors = map[int]lipgloss.Color{
	0: colorRed,
	1: colorYellow,
	2: colorWhite,
	3: colorGray,
	4: colorGray,
}

// PriorityBadge renders a "P#" pill, matching the ticket TUI's badge style.
func PriorityBadge(p int) string {
	c, ok := ticketPriorityColors[p]
	if !ok {
		c = colorWhite
	}
	label := "P?"
	if p >= 0 && p <= 4 {
		label = string(rune('P')) + string(rune('0'+p))
	}
	return lipgloss.NewStyle().
		Foreground(colorBlack).
		Background(c).
		Bold(true).
		Padding(0, 1).
		Render(label)
}

var (
	StyleBold     = lipgloss.NewStyle().Bold(true)
	StyleDim      = lipgloss.NewStyle().Foreground(colorMuted)
	StyleAccent   = lipgloss.NewStyle().Foreground(colorAccent)
	StyleDanger   = lipgloss.NewStyle().Foreground(colorDanger)
	StyleSuccess  = lipgloss.NewStyle().Foreground(colorSuccess)
	StyleWarning  = lipgloss.NewStyle().Foreground(colorWarning)
	StyleInfo     = lipgloss.NewStyle().Foreground(colorInfo)
	StyleHelp     = lipgloss.NewStyle().Foreground(colorMuted)
	StyleFieldKey = lipgloss.NewStyle().Bold(true).Foreground(colorInfo).Width(14)
	StyleSection  = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	StyleColHeader = lipgloss.NewStyle().Bold(true).Foreground(colorCyan)

	StyleOverlayBorder = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(colorMuted)
)

// AgentBadge renders an agent name as a fixed-width colored pill.
func AgentBadge(agent string, selected bool) string {
	bg := colorBadgeBg
	if selected {
		bg = colorSurface
	}
	return lipgloss.NewStyle().
		Foreground(agentColor(agent)).
		Background(bg).
		Padding(0, 1).
		Render(agent)
}

// StatusPill renders a ticket status short label.
func StatusPill(status string, count int) string {
	c, ok := ticketStatusColors[status]
	if !ok {
		c = colorWhite
	}
	return lipgloss.NewStyle().Foreground(c).Render(status) +
		StyleDim.Render(":") +
		lipgloss.NewStyle().Foreground(colorWhite).Render(itoa(count))
}

// ProgressBar draws a simple filled/empty bar matching the ticket TUI.
func ProgressBar(done, total, width int) string {
	if total == 0 || width <= 0 {
		return ""
	}
	filled := (done * width) / total
	if filled > width {
		filled = width
	}
	empty := width - filled
	bar := strings.Repeat("━", filled) + strings.Repeat("─", empty)

	var c lipgloss.Color
	switch {
	case done == total:
		c = colorSuccess
	case done*2 >= total:
		c = colorWarning
	default:
		c = colorMuted
	}
	return lipgloss.NewStyle().Foreground(c).Render(bar)
}

func padRight(s string, width int) string {
	w := lipgloss.Width(s)
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
}

func padRightBg(s string, width int, bg *lipgloss.Style) string {
	w := lipgloss.Width(s)
	if w >= width {
		return s
	}
	pad := strings.Repeat(" ", width-w)
	if bg != nil {
		pad = bg.Render(pad)
	}
	return s + pad
}

// itoa without pulling in strconv from every caller.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
