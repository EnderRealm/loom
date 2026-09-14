package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"loom/internal/runreport"
	"loom/internal/runs"
)

// runDetailModel is one run's report on screen: header, time, totals, the
// execution hierarchy, stages and lens attempts, windowed by offset. Two
// cursors move apart from the scroll: node walks the hierarchy, lens walks
// the lens attempts, and enter on a lens attempt opens the whole response
// the store kept for it. The detail reloads itself every runDetailRefresh
// with one load out at a time; a failed reload leaves the last good report
// up and marks it stale.
type runDetailModel struct {
	runID string
	// gen names this opening of the run: a tick or load result carrying
	// another gen belongs to an earlier opening and is dropped.
	gen    int
	detail *runreport.Detail
	// err is the failure with nothing to show; loading is true until the
	// first result of either kind.
	err     error
	loading bool
	// refreshing is true while a load is out. A tick or `r` that lands
	// then is coalesced into it rather than starting a second.
	refreshing bool
	// loadedAt is the last successful load; staleErr the refresh that
	// failed after it, cleared by the next success.
	loadedAt time.Time
	staleErr error
	// sweptAt is when the summarizer last completed a sweep of the
	// database (zero when none is recorded, or sweepErr says why it could
	// not be read); pipeline is the shipper's sync and the stale thresholds.
	sweptAt  time.Time
	sweepErr error
	pipeline pipelineState
	width    int
	height   int
	offset   int

	nodes  []treeNode
	node   int
	lenses []lensRef
	lens   int

	showResponse    bool
	responseScroll  int
	showExecutions  bool
	showHistory     bool
	showDiagnostics bool
	focusNodes      bool
}

// treeNode is one hierarchy line: the node, its depth, and whether it hangs
// under the root or is listed unresolved.
type treeNode struct {
	n          *runs.Node
	depth      int
	unresolved bool
}

// lensRef indexes one attempt in Report.Lenses.
type lensRef struct {
	group   int
	attempt int
}

// detailLines is the rendered screen with the line each cursor target sits
// on, so a cursor move can scroll its line into view.
type detailLines struct {
	lines  []string
	nodeAt []int
	lensAt []int
}

// newRunDetailModel is a detail whose first load the caller sends out with
// it, so refreshing starts true and a tick before that load returns is
// coalesced into it.
func newRunDetailModel(runID string, gen, w, h int) runDetailModel {
	return runDetailModel{runID: runID, gen: gen, loading: true, refreshing: true, width: w, height: h}
}

// refresh starts a load unless one is already out.
func (m *runDetailModel) refresh() tea.Cmd {
	if m.refreshing {
		return nil
	}
	m.refreshing = true
	return loadRunDetailCmd(m.runID, m.gen)
}

func (m *runDetailModel) setSize(w, h int) {
	m.width = w
	m.height = h
}

// setDetail installs a load's result. A failure after a successful load
// keeps that report up, marked stale with the error, so the body never
// blanks under the reader; a failure with nothing loaded is the body. A
// reload keeps the cursors and scroll where they were, clamped to the new
// tree; only the first load starts them at the top.
func (m *runDetailModel) setDetail(d *runreport.Detail, err error) {
	m.loading = false
	m.refreshing = false
	if err != nil {
		if m.detail != nil {
			m.staleErr = err
			return
		}
		m.err = err
		return
	}
	if d == nil {
		return
	}
	first := m.detail == nil
	lensName, lensKey := m.selectedLensIdentity()
	m.err, m.staleErr = nil, nil
	m.loadedAt = time.Now()
	m.detail = d
	m.sweptAt, m.sweepErr = d.SweptAt, d.SweepErr
	m.nodes, m.lenses = nil, nil
	rep := d.Report
	if rep.Tree != nil {
		m.flatten(rep.Tree, 0, false)
	}
	for _, n := range rep.Unresolved {
		m.flatten(n, 0, true)
	}
	m.rebuildLenses()
	m.restoreLensSelection(lensName, lensKey)
	if first {
		m.node, m.lens = 0, 0
		m.offset, m.responseScroll = 0, 0
		m.showResponse = false
		return
	}
	m.node = min(m.node, max(0, len(m.nodes)-1))
	m.lens = min(m.lens, max(0, len(m.lenses)-1))
	if len(m.lenses) == 0 {
		m.showResponse = false
		m.responseScroll = 0
	}
}

func (m *runDetailModel) flatten(n *runs.Node, depth int, unresolved bool) {
	m.nodes = append(m.nodes, treeNode{n: n, depth: depth, unresolved: unresolved})
	for _, c := range n.Children {
		m.flatten(c, depth+1, unresolved)
	}
}

func (m runDetailModel) bodyRows() int {
	r := m.height - 4
	if r < 1 {
		r = 1
	}
	return r
}

func (m runDetailModel) update(msg tea.Msg) (runDetailModel, tea.Cmd) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	if m.showResponse {
		switch km.String() {
		case "esc", "q", "backspace":
			m.showResponse = false
			m.responseScroll = 0
		case "up", "k":
			if m.responseScroll > 0 {
				m.responseScroll--
			}
		case "down", "j":
			m.responseScroll++
		case "pgup":
			m.responseScroll -= m.bodyRows()
			if m.responseScroll < 0 {
				m.responseScroll = 0
			}
		case "pgdown":
			m.responseScroll += m.bodyRows()
		case "g":
			m.responseScroll = 0
		}
		return m, nil
	}
	switch km.String() {
	case "e":
		m.showExecutions = !m.showExecutions
		m.focusNodes = m.showExecutions
		m.offset = 0
	case "a":
		m.showHistory = !m.showHistory
		m.rebuildLenses()
		m.focusNodes = false
		m.offset = 0
	case "d":
		m.showDiagnostics = !m.showDiagnostics
		m.offset = 0
	case "up":
		if m.offset > 0 {
			m.offset--
		}
	case "down":
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
	case "j", "tab":
		m.showExecutions, m.focusNodes = true, true
		if m.node < len(m.nodes)-1 {
			m.node++
		}
		m.scrollTo(m.lines(m.contentWidth()).nodeAt, m.node)
	case "k", "shift+tab":
		m.showExecutions, m.focusNodes = true, true
		if m.node > 0 {
			m.node--
		}
		m.scrollTo(m.lines(m.contentWidth()).nodeAt, m.node)
	case "n", "]":
		m.focusNodes = false
		if m.lens < len(m.lenses)-1 {
			m.lens++
			m.scrollTo(m.lines(m.contentWidth()).lensAt, m.lens)
		}
	case "p", "[":
		m.focusNodes = false
		if m.lens > 0 {
			m.lens--
			m.scrollTo(m.lines(m.contentWidth()).lensAt, m.lens)
		}
	case "enter", "v":
		if len(m.lenses) > 0 {
			m.showResponse = true
			m.responseScroll = 0
		}
	}
	return m, nil
}

// scrollTo brings the line at[i] into the window.
func (m *runDetailModel) scrollTo(at []int, i int) {
	if i < 0 || i >= len(at) {
		return
	}
	line, rows := at[i], m.bodyRows()
	if line < m.offset {
		m.offset = line
	}
	if line >= m.offset+rows {
		m.offset = line - rows + 1
	}
}

func (m runDetailModel) boxWidth() int {
	return max(3, min(132, m.width-4))
}

// contentWidth is what a line may take inside the border.
func (m runDetailModel) contentWidth() int {
	return m.boxWidth() - 2
}

func (m runDetailModel) view() string {
	box := StyleOverlayBorder.Width(m.boxWidth())
	title := StyleSection.Render("RUN") + "  " + white(sanitize(m.runID))
	switch {
	case m.loading:
		return box.Render(ansi.Wrap(title+"\n\n"+StyleDim.Render("  loading…"), m.contentWidth(), ""))
	case m.err != nil:
		return box.Render(ansi.Wrap(title+"\n\n"+StyleWarning.Render("  "+sanitize(m.err.Error())), m.contentWidth(), ""))
	case m.detail == nil:
		return box.Render(title)
	}
	if m.showResponse {
		return box.Render(m.responseView())
	}
	dl := m.lines(m.contentWidth())
	return box.Render(window(dl.lines, m.offset, m.bodyRows()))
}

// window is the rows of lines from off, with the count left below.
func window(lines []string, off, rows int) string {
	off = max(0, off)
	if off > len(lines)-1 {
		off = max(0, len(lines)-1)
	}
	end := off + max(0, rows)
	if end > len(lines) {
		end = len(lines)
	}
	content := strings.Join(lines[off:end], "\n")
	if end < len(lines) {
		content += "\n" + StyleDim.Render(fmt.Sprintf("  ↓ %d more line(s)", len(lines)-end))
	}
	return content
}

func field(label, value string) string {
	return StyleFieldKey.Render(label) + "  " + value
}

func white(s string) string {
	return lipgloss.NewStyle().Foreground(colorWhite).Render(s)
}

// orUnavailable renders an empty string as the unavailable marker.
func orUnavailable(s string) string {
	if s == "" {
		return StyleDim.Render(unavailable)
	}
	return white(s)
}

func msCell(ms *int64) string {
	if ms == nil {
		return StyleDim.Render(unavailable)
	}
	return white(humanShortDuration(*ms))
}

func stampCell(iso string) string {
	t, err := time.Parse(time.RFC3339Nano, iso)
	if err != nil {
		return StyleDim.Render(unavailable)
	}
	return white(t.Local().Format("2006-01-02 15:04:05"))
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	if strings.HasSuffix(noun, "y") {
		return fmt.Sprintf("%d %sies", n, strings.TrimSuffix(noun, "y"))
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// basisSegment is the header's transcript basis, present only when the run
// has a transcript to have come by.
func basisSegment(basis string) string {
	if basis == "" {
		return ""
	}
	return StyleDim.Render("  ·  transcript ") + white(sanitize(basis))
}

// headerLines is the run's identity with its outcome, its telemetry and its
// freshness on three lines of their own: the record's word, how complete the
// evidence is, and when anything was last seen are three different things.
func (m runDetailModel) headerLines(width int) []string {
	rep := m.detail.Report
	run := rep.Run
	out := []string{
		StyleSection.Render("RUN") + "  " + white(truncate(sanitize(run.RunID), width-6)),
		StyleDim.Render("ticket ") + orUnavailable(truncate(sanitize(run.Ticket), 48)) +
			StyleDim.Render("  ·  runtime ") + orUnavailable(sanitize(run.Runtime)) +
			StyleDim.Render("  ·  origin ") + orUnavailable(sanitize(run.Origin)) +
			basisSegment(run.TranscriptBasis) +
			StyleDim.Render("  ·  producer ") + orUnavailable(sanitize(run.Producer)),
		field("Outcome", lipgloss.NewStyle().Foreground(outcomeColor(run.Outcome)).Render(sanitize(run.Outcome))),
	}
	tel := rep.Telemetry
	telStyle := StyleSuccess
	telText := tel.State
	if tel.State != runreport.StateComplete {
		telStyle = StyleWarning
		telText = fmt.Sprintf("%s — %s, %d pending, %d of %d executions with transcript",
			tel.State, plural(len(tel.Gaps), "gap"), len(tel.ExecutionsPending), tel.ExecutionsWithTranscript, tel.ExecutionsTotal)
	}
	out = append(out, field("Telemetry", telStyle.Render(telText)))
	seen := StyleDim.Render(unavailable)
	if t, err := time.Parse(time.RFC3339Nano, run.LastObservedAt); err == nil {
		seen = white(t.Local().Format("2006-01-02 15:04:05")) + StyleDim.Render(" ("+humanDuration(time.Since(t))+" ago)")
	}
	out = append(out,
		field("Last seen", seen),
		field("Freshness", m.freshnessCell(width)),
		field("Pipeline", m.pipelineCell()),
		field("Started", stampCell(run.StartedAt)+StyleDim.Render("   ended  ")+stampCell(run.EndedAt)),
	)
	for _, g := range tel.Gaps {
		out = append(out, StyleDim.Render("  · "+truncate(sanitize(g), width-4)))
	}
	return out
}

// clockAgo is a wall-clock stamp with its age.
func clockAgo(t time.Time) string {
	return t.Local().Format("15:04:05") + " (" + humanDuration(time.Since(t)) + " ago)"
}

// freshnessCell is how current this view is: when it last loaded, or the
// load it is stale from and why, and whether a refresh is out.
func (m runDetailModel) freshnessCell(width int) string {
	loaded := white("loaded " + clockAgo(m.loadedAt))
	if m.staleErr != nil {
		loaded = StyleWarning.Render(truncate("stale — last successful load "+clockAgo(m.loadedAt)+": "+sanitize(m.staleErr.Error()), max(20, width-16)))
	}
	if m.refreshing {
		loaded += StyleDim.Render("  refreshing…")
	}
	return loaded
}

// pipelineCell is how current the data behind the view is: when the
// summarizer last completed a sweep of the database and when the local
// shipper last reached the receiver, each stale past its threshold. Either
// not recorded, or a sweep marker that could not be read, says so rather
// than reading as current.
func (m runDetailModel) pipelineCell() string {
	swept := StyleDim.Render("swept " + unavailable + "  no sweep recorded")
	switch {
	case m.sweepErr != nil:
		swept = StyleDim.Render("swept "+unavailable+"  ") + StyleWarning.Render(sanitize(m.sweepErr.Error()))
	case !m.sweptAt.IsZero():
		swept = StyleDim.Render("swept ") + white(clockAgo(m.sweptAt))
		if time.Since(m.sweptAt) > m.pipeline.sweepStaleAfter {
			swept += " " + StyleWarning.Render("stale")
		}
	}
	shipped := StyleDim.Render("shipped " + unavailable + "  no local shipper state")
	if m.pipeline.shippedKnown {
		shipped = StyleDim.Render("shipped ") + white(clockAgo(m.pipeline.shippedAt))
		if time.Since(m.pipeline.shippedAt) > m.pipeline.shipStaleAfter {
			shipped += " " + StyleWarning.Render("stale")
		}
	}
	return swept + StyleDim.Render("  ·  ") + shipped
}

func (m runDetailModel) timeLines(width int) []string {
	tm := m.detail.Report.Time
	total := m.detail.Report.Metrics.Total
	cov := total.ExecutionTimeCoverage
	wall := msCell(tm.WallMs)
	if tm.WallMs != nil {
		wall += StyleDim.Render("  " + tm.WallBasis)
	} else {
		wall += StyleDim.Render("  no start, or no end and nothing observed")
	}
	toolTime := StyleDim.Render(unavailable + "  no transcript metered")
	if len(total.TokensByRuntime) > 0 {
		toolTime = white(humanShortDuration(tm.ToolTimeMs)) + StyleDim.Render("  over every counted execution")
	}
	// Legacy is the parent span's transcript figure alone: with the root
	// session not folded there is none, not one of 0ms.
	legacy := StyleDim.Render(unavailable + "  root transcript not metered")
	if len(m.detail.Report.Metrics.Parent.TokensByRuntime) > 0 {
		legacy = white(humanShortDuration(tm.LegacyActiveMs)) + StyleDim.Render("  "+truncate(tm.LegacyActiveMsSemantics, max(10, width-24)))
	}
	// Execution time with nothing timed is unknown, not 0: the runs list
	// renders the same summary as unavailable.
	execution := white(humanShortDuration(tm.ExecutionTimeMs))
	if cov.Timed == 0 && cov.Untimed > 0 {
		execution = StyleDim.Render(unavailable)
	}
	return []string{
		StyleSection.Render("TIME"),
		field("Wall", wall),
		field("Execution", execution+StyleDim.Render(fmt.Sprintf("  %d timed · %d untimed, summed over executions", cov.Timed, cov.Untimed))),
		field("Tool time", toolTime),
		field("Legacy", legacy),
	}
}

func (m runDetailModel) totalsLines(width int) []string {
	sc := m.detail.Report.Metrics
	const col = 13
	row := func(label string, cell func(runreport.Metrics) string) string {
		return field(label, padRight(cell(sc.Parent), col)+padRight(cell(sc.Descendants), col)+cell(sc.Total))
	}
	out := []string{
		StyleSection.Render("TOTALS"),
		field("", padRight(StyleColHeader.Render("PARENT"), col)+padRight(StyleColHeader.Render("DESCENDANTS"), col)+StyleColHeader.Render("TOTAL")),
		row("Executions", func(x runreport.Metrics) string { return white(itoa(x.Executions)) }),
		row("Tokens", tokensCell),
		row("Tool calls", func(x runreport.Metrics) string {
			if len(x.TokensByRuntime) == 0 {
				return StyleDim.Render(unavailable)
			}
			return white(compactInt(x.ToolCalls))
		}),
		row("Hook signals", func(x runreport.Metrics) string {
			if len(x.TokensByRuntime) == 0 {
				return StyleDim.Render(unavailable)
			}
			return white(itoa(x.HookSignals))
		}),
		row("Human", func(x runreport.Metrics) string {
			if len(x.TokensByRuntime) == 0 {
				return StyleDim.Render(unavailable)
			}
			return white(itoa(x.HumanInteractions))
		}),
		row("Cost", func(x runreport.Metrics) string {
			if x.CostUSD == nil {
				return StyleDim.Render(costCell(nil))
			}
			return white(costCell(x.CostUSD))
		}),
	}
	// Failures come only from metered transcripts: with none metered the
	// counts are unknown, not four zeros.
	f := sc.Total.Failures
	failure := func(label string, n int) string {
		if n > 0 {
			return StyleWarning.Render(fmt.Sprintf("%s %d", label, n))
		}
		return StyleDim.Render(fmt.Sprintf("%s %d", label, n))
	}
	failures := StyleDim.Render(unavailable + "  no transcript metered")
	if len(sc.Total.TokensByRuntime) > 0 {
		failures = failure("tool", f.Tool) + StyleDim.Render(" · ") + failure("api", f.API) +
			StyleDim.Render(" · ") + failure("process", f.Process) + StyleDim.Render(" · ") + failure("other", f.Other)
	}
	out = append(out, field("Failures", failures))
	for _, w := range sc.Total.PricingWarnings {
		out = append(out, StyleDim.Render("  · "+truncate(sanitize(w), width-4)))
	}
	return out
}

// execution finds the report's metrics for an execution id.
func (m runDetailModel) execution(id string) *runreport.ExecutionMetrics {
	for i := range m.detail.Report.Executions {
		if m.detail.Report.Executions[i].ExecutionID == id {
			return &m.detail.Report.Executions[i]
		}
	}
	return nil
}

func (m runDetailModel) pending(id string) bool {
	for _, p := range m.detail.Report.Telemetry.ExecutionsPending {
		if p == id {
			return true
		}
	}
	return false
}

// outcomeCell is an execution's outcome, or pending when no record has
// closed it, or unavailable when nothing ever will (a historical dispatch).
func (m runDetailModel) outcomeCell(id, outcome string, sel lipgloss.Style) string {
	switch {
	case m.pending(id):
		return sel.Foreground(colorInfo).Render("pending")
	case outcome == "":
		return sel.Foreground(colorMuted).Render(unavailable)
	}
	return sel.Foreground(outcomeColor(outcome)).Render(sanitize(outcome))
}

// tokensCell is a scope's tokens, or unavailable when no transcript was
// metered for it: a missing session or a transcript-less command has no
// count, not a count of zero.
func tokensCell(x runreport.Metrics) string {
	if len(x.TokensByRuntime) == 0 {
		return StyleDim.Render(unavailable)
	}
	return white(compactInt(int(x.TotalTokens)))
}

func nodeLabel(n *runs.Node) string {
	switch n.Kind {
	case "stage":
		return fmt.Sprintf("stage %s/%s #%s", sanitize(n.Stage), intOrUnknown(n.StageOccurrence), intOrUnknown(n.Attempt))
	case "lens":
		return fmt.Sprintf("lens %s r%s #%s", sanitize(n.Lens), intOrUnknown(n.Round), intOrUnknown(n.Attempt))
	}
	return sanitize(n.Kind)
}

// intOrUnknown renders a position the record did not carry as "?", not 0.
func intOrUnknown(p *int) string {
	if p == nil {
		return "?"
	}
	return itoa(*p)
}

func (m runDetailModel) nodeLine(tn treeNode, selected bool, width int) string {
	sel := lipgloss.NewStyle()
	marker := "  "
	if selected {
		sel = sel.Background(colorSurface)
		marker = "▸ "
	}
	n := tn.n
	em := m.execution(n.ExecutionID)
	indent := strings.Repeat("  ", tn.depth)
	label := nodeLabel(n)
	if em != nil && em.AgentType != "" {
		label += " " + sanitize(em.AgentType)
	}
	line := sel.Render(marker+indent) + sel.Foreground(colorWhite).Render(label) +
		sel.Render("  ") + sel.Foreground(colorMuted).Render(truncate(sanitize(n.ExecutionID), 32)) +
		sel.Render("  ") + m.outcomeCell(n.ExecutionID, n.Outcome, sel)
	if em == nil {
		return line
	}
	dur := sel.Foreground(colorMuted).Render(unavailable)
	if em.DurationMs != nil {
		dur = sel.Foreground(colorWhite).Render(humanShortDuration(*em.DurationMs))
	}
	tokens := sel.Foreground(colorMuted).Render(unavailable)
	if len(em.Metrics.TokensByRuntime) > 0 {
		tokens = sel.Foreground(colorWhite).Render(compactInt(int(em.Metrics.TotalTokens)))
	}
	counted := sel.Foreground(colorMuted).Render("counted")
	switch {
	case em.Placement == runreport.PlacementUnresolved && em.CountedBy == "" && !em.Counted:
		counted = sel.Foreground(colorWarning).Render("unresolved")
	case !em.Counted:
		counted = sel.Foreground(colorMuted).Render("uncounted, by " + truncate(sanitize(em.CountedBy), 20))
	}
	line += sel.Render("  ") + dur + sel.Render("  ") + tokens + sel.Foreground(colorMuted).Render(" tok") + sel.Render("  ") + counted
	if selected {
		if rendered := lipgloss.Width(line); rendered < width {
			line += sel.Render(strings.Repeat(" ", width-rendered))
		}
	}
	return line
}

func (m runDetailModel) lensLine(g runreport.LensGroup, a runreport.LensAttempt, selected bool, width int) string {
	sel := lipgloss.NewStyle()
	marker := "    "
	if selected {
		sel = sel.Background(colorSurface)
		marker = "  ▸ "
	}
	id := sel.Foreground(colorMuted).Render("no execution record")
	if a.ExecutionID != "" {
		id = sel.Foreground(colorWhite).Render(truncate(sanitize(a.ExecutionID), 32))
	}
	dur := sel.Foreground(colorMuted).Render(unavailable)
	if a.DurationMs != nil {
		dur = sel.Foreground(colorWhite).Render(humanShortDuration(*a.DurationMs))
	}
	status := sel.Foreground(colorMuted).Render("unrecorded")
	if a.Recorded {
		status = sel.Foreground(colorAccent).Render(sanitize(a.Status))
		if a.Verdict != "" {
			status += sel.Render(" ") + sel.Foreground(colorWhite).Render(sanitize(a.Verdict))
		}
		if a.ContextState != "" {
			status += sel.Render(" ") + sel.Foreground(colorMuted).Render("ctx "+sanitize(a.ContextState))
		}
		var flags []string
		if a.Contaminated {
			flags = append(flags, "contaminated")
		}
		if a.Superseded {
			flags = append(flags, "superseded")
		}
		if a.Late {
			flags = append(flags, "late")
		}
		if a.Malformed != "" {
			flags = append(flags, "malformed: "+sanitize(a.Malformed))
		}
		if len(flags) > 0 {
			status += sel.Render(" ") + sel.Foreground(colorWarning).Render(strings.Join(flags, ", "))
		}
	}
	response := sel.Foreground(colorMuted).Render("no response")
	if _, ok := m.detail.LensResponses[runreport.LensKey(g.Lens, g.Round, a.Attempt)]; ok {
		response = sel.Foreground(colorInfo).Render("response")
	}
	line := sel.Render(marker) + sel.Foreground(colorMuted).Render(fmt.Sprintf("#%d", a.Attempt)) + sel.Render("  ") + id +
		sel.Render("  ") + m.outcomeCell(a.ExecutionID, a.Outcome, sel) + sel.Render("  ") + dur +
		sel.Render("  ") + status + sel.Render("  ") + response
	if selected {
		if rendered := lipgloss.Width(line); rendered < width {
			line += sel.Render(strings.Repeat(" ", width-rendered))
		}
	}
	return line
}

// responseView is the selected lens attempt's whole stored body.
func (m runDetailModel) responseView() string {
	if m.lens >= len(m.lenses) {
		return ""
	}
	ref := m.lenses[m.lens]
	g := m.detail.Report.Lenses[ref.group]
	a := g.Attempts[ref.attempt]
	title := StyleSection.Render("LENS RESPONSE") + "  " + white(fmt.Sprintf("%s round %d #%d", sanitize(g.Lens), g.Round, a.Attempt))
	if a.Recorded {
		title += StyleDim.Render("  " + sanitize(a.Status))
		if a.Verdict != "" {
			title += StyleDim.Render(" · " + sanitize(a.Verdict))
		}
	}
	raw, ok := m.detail.LensResponses[runreport.LensKey(g.Lens, g.Round, a.Attempt)]
	if !ok {
		return title + "\n\n" + StyleDim.Render("  no response stored for this attempt")
	}
	width := m.contentWidth()
	var lines []string
	for _, l := range strings.Split(raw, "\n") {
		lines = append(lines, strings.Split(ansi.Hardwrap(sanitize(l), width, true), "\n")...)
	}
	title = ansi.Wrap(title, width, "")
	return title + "\n\n" + window(lines, m.responseScroll, max(1, m.bodyRows()-lipgloss.Height(title)-1))
}
