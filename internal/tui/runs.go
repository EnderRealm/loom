package tui

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"loom/internal/config"
	"loom/internal/runreport"
	"loom/transport/shipper"
)

// runsWindow bounds the runs list: the last 30 days, with no upper bound so
// a run still going is listed.
const runsWindow = 30 * 24 * time.Hour

// runDetailRefresh is how often an open run detail reloads itself.
const runDetailRefresh = 5 * time.Second

// listSummaries and loadRunDetail are the queries the overlays run off the
// update loop; lastShipperSync and pipelineCadences are the local shipper
// state and the configured cadences read beside a detail load. Variables
// so a test can stand in a loader that blocks and show the loop keeps
// taking keys while it does.
var (
	listSummaries    = runreport.ListSummaries
	loadRunDetail    = runreport.LoadDetail
	lastShipperSync  = shipper.LastSync
	pipelineCadences = shipper.Cadences
)

// pipelineState is what a detail load reads beside the report: the local
// shipper's last successful sync, meaningful only when shippedKnown, and
// how old the sweep marker and that sync may be before each reads stale.
type pipelineState struct {
	shippedAt       time.Time
	shippedKnown    bool
	sweepStaleAfter time.Duration
	shipStaleAfter  time.Duration
}

// minStaleAfter floors the stale thresholds so a cadence of a second or two
// does not flicker into stale between ticks.
const minStaleAfter = 30 * time.Second

// staleAfter is how old a marker may be before its producer is reported
// stale: two of its cadences, so the marker's age at the moment a tick is
// due does not read as stale, and never less than minStaleAfter.
func staleAfter(cadence time.Duration) time.Duration {
	return max(2*cadence, minStaleAfter)
}

// loadPipelineState reads the shipper's sync and the configured cadences.
// A config that cannot be read leaves the default cadences, so a bad
// config.json degrades the thresholds rather than the detail.
func loadPipelineState() pipelineState {
	ship, sweep, err := pipelineCadences()
	if err != nil {
		ship, sweep = shipper.DefaultIntervalMinutes*time.Minute, shipper.DefaultSummarizerInterval
	}
	shippedAt, known := lastShipperSync()
	return pipelineState{shippedAt: shippedAt, shippedKnown: known, sweepStaleAfter: staleAfter(sweep), shipStaleAfter: staleAfter(ship)}
}

func summariesPath() string {
	return filepath.Join(config.Home(), "summaries.db")
}

// runsLoadedMsg and runDetailLoadedMsg carry their query's error rather than
// reporting through errMsg: a summaries.db that is missing or behind schema
// is one overlay's body, not the dashboard's failure.
type runsLoadedMsg struct {
	rows []runreport.Summary
	err  error
}

// runDetailLoadedMsg carries gen so a result from an earlier opening of the
// same run is told from the current one's, the way runDetailTickMsg is.
type runDetailLoadedMsg struct {
	runID  string
	gen    int
	detail *runreport.Detail
	err    error
	// pipeline is read in the same goroutine as the detail.
	pipeline pipelineState
}

// runDetailTickMsg is one beat of an open run detail's refresh chain. The
// run id and gen name the opening that armed it: a beat for any other is
// dropped and not re-armed, so at most one chain runs at a time.
type runDetailTickMsg struct {
	runID string
	gen   int
}

func loadRunsCmd() tea.Cmd {
	return func() tea.Msg {
		rows, err := listSummaries(summariesPath(), time.Now().Add(-runsWindow), time.Time{})
		return runsLoadedMsg{rows: rows, err: err}
	}
}

func loadRunDetailCmd(runID string, gen int) tea.Cmd {
	return func() tea.Msg {
		d, err := loadRunDetail(summariesPath(), runID)
		return runDetailLoadedMsg{runID: runID, gen: gen, detail: d, err: err, pipeline: loadPipelineState()}
	}
}

func runDetailTickCmd(runID string, gen int) tea.Cmd {
	return tea.Tick(runDetailRefresh, func(time.Time) tea.Msg {
		return runDetailTickMsg{runID: runID, gen: gen}
	})
}

type runsModel struct {
	rows    []runreport.Summary
	cursor  int
	offset  int
	width   int
	height  int
	sortCol int
	loading bool
	loaded  bool
	err     error
}

// runSortColumn pairs a sortable header with the comparator for its natural
// direction, as the dashboard's sortColumn does. Every comparator falls
// through to runOrder, and a value the report could not measure sorts after
// every measured one rather than as a zero.
type runSortColumn struct {
	header string
	less   func(a, b runreport.Summary) bool
}

// runOrder is the final tiebreaker: newest first, then id.
func runOrder(a, b runreport.Summary) bool {
	if !a.StartedAt.Equal(b.StartedAt) {
		return a.StartedAt.After(b.StartedAt)
	}
	return a.RunID < b.RunID
}

func moreInt64(a, b int64, tie func() bool) bool {
	if a != b {
		return a > b
	}
	return tie()
}

// moreInt64Ptr orders two measures that may be unavailable: measured before
// unmeasured, larger first among the measured.
func moreInt64Ptr(a, b *int64, tie func() bool) bool {
	switch {
	case a == nil && b == nil:
		return tie()
	case a == nil:
		return false
	case b == nil:
		return true
	}
	return moreInt64(*a, *b, tie)
}

func moreFloatPtr(a, b *float64, tie func() bool) bool {
	switch {
	case a == nil && b == nil:
		return tie()
	case a == nil:
		return false
	case b == nil:
		return true
	case *a != *b:
		return *a > *b
	}
	return tie()
}

// runSortColumns is the cycle of sortable runs columns, left to right in
// table order so `s` advances one column right, skipping TICKET, OUTCOME,
// TELEMETRY and SEEN. Headers must match the displayed header text exactly.
var runSortColumns = []runSortColumn{
	{"DATE", runOrder},
	{"WALL", func(a, b runreport.Summary) bool {
		return moreInt64Ptr(a.WallMs, b.WallMs, func() bool { return runOrder(a, b) })
	}},
	{"EXEC", func(a, b runreport.Summary) bool {
		return moreInt64(a.ExecutionTimeMs, b.ExecutionTimeMs, func() bool { return runOrder(a, b) })
	}},
	{"TOOL TIME", func(a, b runreport.Summary) bool {
		return moreInt64(a.ToolTimeMs, b.ToolTimeMs, func() bool { return runOrder(a, b) })
	}},
	{"TOKENS", func(a, b runreport.Summary) bool {
		if a.TokensUnavailable != b.TokensUnavailable {
			return !a.TokensUnavailable
		}
		return moreInt64(a.TotalTokens, b.TotalTokens, func() bool { return runOrder(a, b) })
	}},
	{"TOOLS", func(a, b runreport.Summary) bool {
		return moreInt64(int64(a.ToolCalls), int64(b.ToolCalls), func() bool { return runOrder(a, b) })
	}},
	{"ERRORS", func(a, b runreport.Summary) bool {
		// Failures come only from metered transcripts: an unmetered run's
		// count is unknown, not zero, so it sorts after every metered one.
		if a.Metered != b.Metered {
			return a.Metered
		}
		return moreInt64(int64(a.Failures), int64(b.Failures), func() bool { return runOrder(a, b) })
	}},
	{"CHILDREN", func(a, b runreport.Summary) bool {
		return moreInt64(int64(a.Children), int64(b.Children), func() bool { return runOrder(a, b) })
	}},
	{"COST", func(a, b runreport.Summary) bool {
		return moreFloatPtr(a.CostUSD, b.CostUSD, func() bool { return runOrder(a, b) })
	}},
}

func (m *runsModel) setSize(w, h int) {
	m.width = w
	m.height = h
	m.clampOffset()
}

// setRows installs a load's result, keeping the selection by run id.
func (m *runsModel) setRows(rows []runreport.Summary, err error) {
	m.loading = false
	m.loaded = true
	m.err = err
	if err != nil {
		return
	}
	var selectedID string
	if s := m.selected(); s != nil {
		selectedID = s.RunID
	}
	m.rows = rows
	m.applySort()
	m.reselect(selectedID)
}

func (m *runsModel) applySort() {
	less := runSortColumns[m.sortCol].less
	sort.SliceStable(m.rows, func(i, j int) bool {
		return less(m.rows[i], m.rows[j])
	})
}

func (m *runsModel) reselect(runID string) {
	if runID != "" {
		for i, r := range m.rows {
			if r.RunID == runID {
				m.cursor = i
				break
			}
		}
	}
	if m.cursor >= len(m.rows) {
		m.cursor = max(0, len(m.rows)-1)
	}
	m.clampOffset()
}

func (m *runsModel) cycleSort() {
	var selectedID string
	if s := m.selected(); s != nil {
		selectedID = s.RunID
	}
	m.sortCol = (m.sortCol + 1) % len(runSortColumns)
	m.applySort()
	m.reselect(selectedID)
}

func (m runsModel) selected() *runreport.Summary {
	if m.cursor >= 0 && m.cursor < len(m.rows) {
		return &m.rows[m.cursor]
	}
	return nil
}

func (m runsModel) visibleRows() int {
	rows := m.height - 2
	if rows < 1 {
		rows = 1
	}
	return rows
}

func (m *runsModel) clampOffset() {
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

func (m runsModel) update(msg tea.Msg) (runsModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
				m.clampOffset()
			}
		case "down", "j":
			if m.cursor < len(m.rows)-1 {
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
			if m.cursor > len(m.rows)-1 {
				m.cursor = max(0, len(m.rows)-1)
			}
			m.clampOffset()
		case "g":
			m.cursor = 0
			m.clampOffset()
		case "G":
			m.cursor = max(0, len(m.rows)-1)
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
			if m.cursor > len(m.rows)-1 {
				m.cursor = max(0, len(m.rows)-1)
			}
			m.clampOffset()
		}
	}
	return m, nil
}

// Column widths for the runs table: each holds its header plus one space.
// TICKET takes whatever is left of the width, so the metric columns never
// fall off the right edge at 120 columns.
const (
	colRunDate      = 12 // "01-02 15:04" + 1
	colRunOutcome   = 10 // "completed" + 1
	colRunTelemetry = 13 // "partial (12)" + 1
	colRunSeen      = 5
	colRunWall      = 6
	colRunExec      = 6
	colRunToolTime  = 10 // "TOOL TIME" + 1
	colRunTokens    = 7
	colRunTools     = 6
	colRunErrors    = 7
	colRunChildren  = 9 // "CHILDREN" + 1
	colRunCost      = 8 // "$0.1234" + 1
	colRunTicketMin = 16
)

func (m runsModel) ticketWidth() int {
	fixed := 2 + colRunDate + colRunOutcome + colRunTelemetry + colRunSeen + colRunWall + colRunExec +
		colRunToolTime + colRunTokens + colRunTools + colRunErrors + colRunChildren + colRunCost
	return max(colRunTicketMin, m.width-fixed)
}

// unavailable is the cell for a value the report could not measure.
const unavailable = "—"

func (m runsModel) view() string {
	if m.width == 0 || m.height == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(StyleSection.Render("RUNS"))
	b.WriteString(StyleDim.Render(fmt.Sprintf("  last %d days", int(runsWindow.Hours()/24))))
	if m.loading {
		b.WriteString(StyleDim.Render("  loading…"))
	}
	b.WriteString("\n")
	if m.err != nil {
		b.WriteString(StyleWarning.Render("  " + truncate(sanitize(m.err.Error()), m.width-2)))
		b.WriteString("\n")
		return b.String()
	}
	if m.loaded && len(m.rows) == 0 {
		b.WriteString(StyleDim.Render("  no runs in the window"))
		b.WriteString("\n")
		return b.String()
	}

	ticketW := m.ticketWidth()
	b.WriteString("  ")
	b.WriteString(padRight(m.headerCell("TICKET"), ticketW))
	b.WriteString(padRight(m.headerCell("DATE"), colRunDate))
	b.WriteString(padRight(m.headerCell("OUTCOME"), colRunOutcome))
	b.WriteString(padRight(m.headerCell("TELEMETRY"), colRunTelemetry))
	b.WriteString(padRight(m.headerCell("SEEN"), colRunSeen))
	b.WriteString(padRight(m.headerCell("WALL"), colRunWall))
	b.WriteString(padRight(m.headerCell("EXEC"), colRunExec))
	b.WriteString(padRight(m.headerCell("TOOL TIME"), colRunToolTime))
	b.WriteString(padRight(m.headerCell("TOKENS"), colRunTokens))
	b.WriteString(padRight(m.headerCell("TOOLS"), colRunTools))
	b.WriteString(padRight(m.headerCell("ERRORS"), colRunErrors))
	b.WriteString(padRight(m.headerCell("CHILDREN"), colRunChildren))
	b.WriteString(m.headerCell("COST"))
	b.WriteString("\n")

	visible := m.visibleRows()
	end := m.offset + visible
	if end > len(m.rows) {
		end = len(m.rows)
	}
	for i := m.offset; i < end; i++ {
		b.WriteString(m.renderRow(m.rows[i], i == m.cursor))
		b.WriteString("\n")
	}
	for i := end - m.offset; i < visible; i++ {
		b.WriteString("\n")
	}
	return b.String()
}

func (m runsModel) headerCell(name string) string {
	if runSortColumns[m.sortCol].header == name {
		return StyleColHeaderActive.Render(name)
	}
	return StyleColHeader.Render(name)
}

func outcomeColor(outcome string) lipgloss.Color {
	switch outcome {
	case "completed":
		return colorSuccess
	case "failed":
		return colorDanger
	case "stopped":
		return colorWarning
	case runreport.OutcomeRunning:
		return colorInfo
	}
	return colorMuted
}

// telemetryCell is the completeness beside the outcome: complete, or partial
// with how many executions are still pending.
func telemetryCell(s runreport.Summary) string {
	if s.TelemetryState == runreport.StateComplete {
		return s.TelemetryState
	}
	if s.Pending > 0 {
		return fmt.Sprintf("%s (%d)", s.TelemetryState, s.Pending)
	}
	return s.TelemetryState
}

func costCell(usd *float64) string {
	if usd == nil {
		return unavailable
	}
	return fmt.Sprintf("$%.4f", *usd)
}

func (m runsModel) renderRow(s runreport.Summary, selected bool) string {
	selBg := lipgloss.NewStyle()
	if selected {
		selBg = lipgloss.NewStyle().Background(colorSurface)
	}
	var bg *lipgloss.Style
	if selected {
		bg = &selBg
	}
	muted := selBg.Foreground(colorMuted)
	white := selBg.Foreground(colorWhite)
	gray := selBg.Foreground(colorGray)

	ticketW := m.ticketWidth()
	var ticketCell string
	if s.Ticket == "" {
		ticketCell = padRightBg(muted.Render(unavailable), ticketW, bg)
	} else {
		ticketCell = padRightBg(white.Render(truncate(sanitize(s.Ticket), ticketW-1)), ticketW, bg)
	}

	var dateCell string
	if s.StartedAt.IsZero() {
		dateCell = padRightBg(muted.Render(unavailable), colRunDate, bg)
	} else {
		dateCell = padRightBg(gray.Render(s.StartedAt.Local().Format("01-02 15:04")), colRunDate, bg)
	}

	outcomeCell := padRightBg(selBg.Foreground(outcomeColor(s.Outcome)).Render(sanitize(s.Outcome)), colRunOutcome, bg)

	telStyle := selBg.Foreground(colorSuccess)
	if s.TelemetryState != runreport.StateComplete {
		telStyle = selBg.Foreground(colorWarning)
	}
	telCell := padRightBg(telStyle.Render(telemetryCell(s)), colRunTelemetry, bg)

	var seenCell string
	if s.LastObservedAt.IsZero() {
		seenCell = padRightBg(muted.Render(unavailable), colRunSeen, bg)
	} else {
		seenCell = padRightBg(gray.Render(humanDuration(time.Since(s.LastObservedAt))), colRunSeen, bg)
	}

	var wallCell string
	if s.WallMs == nil {
		wallCell = padRightBg(muted.Render(unavailable), colRunWall, bg)
	} else {
		wallCell = padRightBg(white.Render(humanShortDuration(*s.WallMs)), colRunWall, bg)
	}
	// An execution time with nothing timed, or usage with no transcript
	// metered, is unmeasured rather than zero.
	execCell := padRightBg(muted.Render(unavailable), colRunExec, bg)
	if s.TimedExecutions > 0 || s.UntimedExecutions == 0 {
		execCell = padRightBg(gray.Render(humanShortDuration(s.ExecutionTimeMs)), colRunExec, bg)
	}
	toolTimeCell := padRightBg(muted.Render(unavailable), colRunToolTime, bg)
	tokensCell := padRightBg(muted.Render(unavailable), colRunTokens, bg)
	toolsCell := padRightBg(muted.Render(unavailable), colRunTools, bg)
	if s.Metered {
		if !s.ToolTimeUnavailable {
			toolTimeCell = padRightBg(gray.Render(humanShortDuration(s.ToolTimeMs)), colRunToolTime, bg)
		}
		if !s.TokensUnavailable {
			tokensCell = padRightBg(white.Render(compactInt(int(s.TotalTokens))), colRunTokens, bg)
		}
		toolsCell = padRightBg(gray.Render(compactInt(s.ToolCalls)), colRunTools, bg)
	}

	errCell := padRightBg(muted.Render(unavailable), colRunErrors, bg)
	if s.Metered && s.Failures > 0 {
		errCell = padRightBg(selBg.Foreground(colorWarning).Render(compactInt(s.Failures)), colRunErrors, bg)
	} else if s.Metered {
		errCell = padRightBg(muted.Render("0"), colRunErrors, bg)
	}
	childCell := padRightBg(gray.Render(itoa(s.Children)), colRunChildren, bg)

	var cost string
	if s.CostUSD == nil {
		cost = muted.Render(costCell(nil))
	} else {
		cost = white.Render(costCell(s.CostUSD))
	}

	sp := "  "
	if selected {
		sp = selBg.Render("  ")
	}
	line := sp + ticketCell + dateCell + outcomeCell + telCell + seenCell + wallCell + execCell +
		toolTimeCell + tokensCell + toolsCell + errCell + childCell + cost
	if selected && m.width > 0 {
		rendered := lipgloss.Width(line)
		if rendered < m.width {
			line += selBg.Render(strings.Repeat(" ", m.width-rendered))
		}
	}
	return line
}
