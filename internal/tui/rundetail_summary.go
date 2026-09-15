package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"loom/internal/runreport"
)

// Latest means the greatest recorded round and attempt, including a newer
// attempt without a verdict. An older success must never mask missing evidence.
func (m runDetailModel) latestLenses() []lensRef {
	var refs []lensRef
	byName := map[string]int{}
	for g, group := range m.detail.Report.Lenses {
		for a, attempt := range group.Attempts {
			ref := lensRef{group: g, attempt: a}
			if i, ok := byName[group.Lens]; ok {
				old := refs[i]
				prev := m.detail.Report.Lenses[old.group]
				if group.Round > prev.Round || (group.Round == prev.Round && attempt.Attempt > prev.Attempts[old.attempt].Attempt) {
					refs[i] = ref
				}
			} else {
				byName[group.Lens] = len(refs)
				refs = append(refs, ref)
			}
		}
	}
	return refs
}

func (m *runDetailModel) rebuildLenses() {
	name, key := m.selectedLensIdentity()
	m.lenses = nil
	if m.detail == nil {
		return
	}
	if m.showHistory {
		for g, group := range m.detail.Report.Lenses {
			for a := range group.Attempts {
				m.lenses = append(m.lenses, lensRef{group: g, attempt: a})
			}
		}
	} else {
		m.lenses = m.latestLenses()
	}
	m.lens = min(m.lens, max(0, len(m.lenses)-1))
	m.restoreLensSelection(name, key)
}

func (m runDetailModel) selectedLensIdentity() (string, string) {
	if m.detail == nil || m.lens < 0 || m.lens >= len(m.lenses) {
		return "", ""
	}
	ref := m.lenses[m.lens]
	g := m.detail.Report.Lenses[ref.group]
	return g.Lens, runreport.LensKey(g.Lens, g.Round, g.Attempts[ref.attempt].Attempt)
}

func (m *runDetailModel) restoreLensSelection(name, key string) {
	fallback := -1
	for i, ref := range m.lenses {
		g := m.detail.Report.Lenses[ref.group]
		if g.Lens != name {
			continue
		}
		fallback = i
		if runreport.LensKey(g.Lens, g.Round, g.Attempts[ref.attempt].Attempt) == key {
			m.lens = i
			return
		}
	}
	// The collapsed list has one entry per lens: its latest attempt.
	if fallback >= 0 {
		m.lens = fallback
	}
}

func sentenceCase(s string) string {
	r := []rune(sanitize(s))
	if len(r) > 0 {
		r[0] = unicode.ToUpper(r[0])
	}
	return string(r)
}

func elapsedCell(ms *int64) string {
	if ms == nil {
		return unavailable
	}
	d := time.Duration(*ms) * time.Millisecond
	if d < time.Minute {
		return humanShortDuration(*ms)
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm %02ds", int(d/time.Minute), int(d/time.Second)%60)
	}
	return fmt.Sprintf("%dh %02dm", int(d/time.Hour), int(d/time.Minute)%60)
}

func (m runDetailModel) summaryLines(width int) []string {
	rep := m.detail.Report
	run, total := rep.Run, rep.Metrics.Total
	title := run.Ticket
	if title == "" {
		title = "Run " + run.RunID
	}
	outcome := run.Outcome
	if outcome == "" {
		outcome = runreport.OutcomeUnknown
	}
	status := lipgloss.NewStyle().Bold(true).Foreground(outcomeColor(outcome)).Render(sentenceCase(outcome))
	when := ""
	if t, err := time.Parse(time.RFC3339Nano, run.StartedAt); err == nil {
		when = "  " + t.Local().Format("Jan 2, 2006 at 15:04")
	}
	out := []string{
		StyleBold.Foreground(colorWhite).Render(sanitize(title)),
		status + StyleDim.Render("  "+sanitize(run.Runtime)+when),
		"",
	}
	cost, costNote := "Unavailable", "Total cost (USD)"
	if total.CostUSD != nil {
		cost = fmt.Sprintf("$%.2f", *total.CostUSD)
		if rep.Telemetry.State != runreport.StateComplete {
			costNote = "Recorded cost (partial)"
		}
	} else if p := rep.Metrics.Parent.CostUSD; p != nil {
		costNote = fmt.Sprintf("Parent $%.2f only", *p)
	}
	wallNote := "Elapsed"
	if rep.Time.WallBasis == runreport.WallLastObserved {
		wallNote = "Elapsed to last activity"
	}
	tokens, calls := unavailable, unavailable
	if len(total.TokensByRuntime) > 0 {
		if !total.TokenUsageUnavailable {
			tokens = compactInt(int(total.TotalTokens))
		}
		calls = compactInt(total.ToolCalls)
	}
	tokenNote, callNote := "Tokens incl. cache", "Tool calls"
	if rep.Telemetry.State != runreport.StateComplete {
		tokenNote, callNote = "Tokens (partial)", "Tool calls (partial)"
	}
	metrics := [][2]string{{elapsedCell(rep.Time.WallMs), wallNote}, {cost, costNote}, {tokens, tokenNote}, {calls, callNote}}
	columns := 4
	if width < 100 {
		columns = 2
	}
	if width < 48 {
		columns = 1
	}
	cellWidth := max(1, (width-(columns-1)*3)/columns)
	for start := 0; start < len(metrics); start += columns {
		var cells []string
		for _, metric := range metrics[start:min(start+columns, len(metrics))] {
			value := StyleBold.Foreground(colorWhite).Render(metric[0])
			if metric[0] == "Unavailable" || metric[0] == unavailable {
				value = StyleWarning.Render(metric[0])
			}
			cells = append(cells, lipgloss.NewStyle().Width(cellWidth).Render(value+"\n"+StyleDim.Render(metric[1])))
		}
		out = append(out, lipgloss.JoinHorizontal(lipgloss.Top, intersperse(cells, StyleDim.Render(" │ "))...))
		if start+columns < len(metrics) {
			out = append(out, "")
		}
	}
	activity := plural(total.Executions, "execution")
	if len(total.TokensByRuntime) > 0 {
		var toolTime *int64
		if !total.ToolTimeUnavailable && total.ToolTimeCoverage.Untimed == 0 {
			toolTime = &rep.Time.ToolTimeMs
		}
		activity += "  ·  " + elapsedCell(toolTime) + " in tools"
	}
	if len(rep.Metrics.Parent.TokensByRuntime) > 0 {
		activity += "  ·  " + plural(total.HumanInteractions, "human interaction")
	} else {
		activity += "  ·  human interactions unavailable"
	}
	out = append(out, "", StyleDim.Render(activity))
	if len(total.Models) > 0 {
		out = append(out, StyleDim.Render("Models  "+sanitize(strings.Join(total.Models, ", "))))
	}
	return out
}

func intersperse(items []string, separator string) []string {
	var out []string
	for i, item := range items {
		if i > 0 {
			out = append(out, separator)
		}
		out = append(out, item)
	}
	return out
}

// Review status describes the evidence, independently of execution outcome.
func reviewStatus(a runreport.LensAttempt) (string, lipgloss.Color) {
	switch {
	case a.Contaminated || a.ContextState == "contaminated":
		return "Contaminated context", colorWarning
	case a.Malformed != "":
		return "Malformed response", colorWarning
	case a.Superseded:
		return "Superseded", colorMuted
	case a.Late:
		return "Late response", colorWarning
	case a.ContextState == "shared":
		if a.Verdict != "" {
			return sentenceCase(a.Verdict) + " · shared context", colorWarning
		}
		return "Shared context", colorWarning
	case a.Recorded && a.Status == "parsed" && a.Verdict == "satisfied":
		return "Satisfied", colorSuccess
	case a.Verdict != "":
		return sentenceCase(a.Verdict), colorWarning
	case a.Outcome == "failed" || a.Outcome == "stopped":
		return sentenceCase(a.Outcome), colorWarning
	default:
		return "No verdict recorded", colorWarning
	}
}

func (m runDetailModel) attentionLines() []string {
	rep := m.detail.Report
	var out []string
	add := func(s string) { out = append(out, StyleWarning.Render("! ")+white(sanitize(s))) }
	if m.staleErr != nil {
		add("Refresh failed; showing the last successful load. " + m.staleErr.Error())
	}
	if m.sweepErr != nil {
		add("Data freshness unknown: " + m.sweepErr.Error())
	} else if !m.sweptAt.IsZero() && m.pipeline.sweepStaleAfter > 0 && time.Since(m.sweptAt) > m.pipeline.sweepStaleAfter {
		add("Summary data is stale. Open [d] for pipeline timestamps.")
	}
	if m.pipeline.shippedKnown && m.pipeline.shipStaleAfter > 0 && time.Since(m.pipeline.shippedAt) > m.pipeline.shipStaleAfter {
		add("Local shipping is stale. Open [d] for pipeline timestamps.")
	}
	if rep.Metrics.Total.CostUSD == nil {
		reason := "No price recorded for this run."
		if len(rep.Metrics.Total.PricingWarnings) > 0 {
			reason = rep.Metrics.Total.PricingWarnings[0]
			if n := len(rep.Metrics.Total.PricingWarnings) - 1; n > 0 {
				reason += fmt.Sprintf(" (+%d more; open [d])", n)
			}
		}
		add("Total cost unavailable: " + reason)
	}
	if rep.Telemetry.State != runreport.StateComplete {
		add(fmt.Sprintf("Metrics incomplete: %d of %d executions have transcripts; %d pending. Open [d] for coverage.", rep.Telemetry.ExecutionsWithTranscript, rep.Telemetry.ExecutionsTotal, len(rep.Telemetry.ExecutionsPending)))
	}
	if rep.Telemetry.Unresolved > 0 || len(rep.Diagnostics) > 0 {
		add(fmt.Sprintf("Attribution needs attention: %d unresolved executions, %d diagnostics. Open [d] for details.", rep.Telemetry.Unresolved, len(rep.Diagnostics)))
	}
	for _, ref := range m.latestLenses() {
		g := rep.Lenses[ref.group]
		a := g.Attempts[ref.attempt]
		status, _ := reviewStatus(a)
		if status != "Satisfied" {
			add(sentenceCase(g.Lens) + ": " + strings.ToLower(status) + ".")
		}
	}
	f := rep.Metrics.Total.Failures
	if len(rep.Metrics.Total.TokensByRuntime) > 0 && f.Tool+f.API+f.Process+f.Other > 0 {
		var failures []string
		for _, item := range []struct {
			n    int
			name string
		}{{f.Tool, "tool error"}, {f.API, "API error"}, {f.Process, "process error"}, {f.Other, "other error"}} {
			if item.n > 0 {
				failures = append(failures, plural(item.n, item.name))
			}
		}
		add(strings.Join(failures, ", ") + " recorded during this run.")
	}
	if len(out) == 0 {
		out = append(out, StyleDim.Render("No issues flagged in the recorded evidence."))
	}
	return out
}

func (m runDetailModel) reviewLine(ref lensRef, selected bool, width int) string {
	g := m.detail.Report.Lenses[ref.group]
	a := g.Attempts[ref.attempt]
	status, color := reviewStatus(a)
	marker := "  "
	if selected {
		marker = StyleAccent.Render("› ")
	}
	name := sentenceCase(g.Lens)
	line := marker + padRight(white(name), 16) + padRight(lipgloss.NewStyle().Foreground(color).Render(status), 24) + "  "
	meta := fmt.Sprintf("round %d, attempt %d", g.Round, a.Attempt)
	if _, ok := m.detail.LensResponses[runreport.LensKey(g.Lens, g.Round, a.Attempt)]; ok {
		meta += "  [enter] response"
	} else {
		meta += "  response unavailable"
	}
	if width < 80 {
		return marker + white(name) + "  " + lipgloss.NewStyle().Foreground(color).Render(status) + "\n  " + StyleDim.Render(meta)
	}
	return line + StyleDim.Render(meta)
}

func disclosure(label, key, summary string, expanded bool) string {
	mark := "▸ "
	if expanded {
		mark = "▾ "
	}
	return StyleSection.Render(mark+label) + StyleDim.Render("  ["+key+"]  "+summary)
}

func (m runDetailModel) lines(width int) detailLines {
	var dl detailLines
	if m.detail == nil {
		return dl
	}
	rep := m.detail.Report
	// Wrap before assigning cursor lines so navigation follows physical rows.
	add := func(s string) {
		dl.lines = append(dl.lines, strings.Split(ansi.Wrap(s, max(1, width), ""), "\n")...)
	}
	for _, l := range m.summaryLines(width) {
		add(l)
	}
	add("")
	add(StyleSection.Render("Attention"))
	for _, l := range m.attentionLines() {
		add(l)
	}
	add("")
	add(StyleSection.Render("Reviews") + StyleDim.Render("  latest recorded attempt per lens · n/p select"))
	refs := m.latestLenses()
	if len(refs) == 0 {
		add(StyleDim.Render("No reviews recorded."))
	}
	for i, ref := range refs {
		if !m.showHistory {
			dl.lensAt = append(dl.lensAt, len(dl.lines))
		}
		add(m.reviewLine(ref, !m.showHistory && !m.focusNodes && i == m.lens, width))
	}
	add("")
	add(disclosure("Execution detail", "e", plural(len(m.nodes), "execution"), m.showExecutions))
	if m.showExecutions {
		add(StyleDim.Render("  j/k select · shared transcripts counted once"))
		for i, tn := range m.nodes {
			dl.nodeAt = append(dl.nodeAt, len(dl.lines))
			add(m.nodeLine(tn, m.focusNodes && i == m.node, width))
		}
		for _, st := range rep.Stages {
			add("  " + white(fmt.Sprintf("%s/%d", sanitize(st.Stage), st.Occurrence)) + StyleDim.Render(fmt.Sprintf("  %s · %s", plural(len(st.Attempts), "attempt"), plural(st.Retries, "retry"))))
			for _, a := range st.Attempts {
				add("    " + white(fmt.Sprintf("#%d  %s", a.Attempt, sanitize(a.ExecutionID))) + "  " + m.outcomeCell(a.ExecutionID, a.Outcome, lipgloss.NewStyle()) + "  " + msCell(a.DurationMs) + "  " + tokensCell(a.Metrics) + StyleDim.Render(" tok"))
			}
		}
	}
	attempts := 0
	for _, group := range rep.Lenses {
		attempts += len(group.Attempts)
	}
	add(disclosure("Review history", "a", plural(attempts, "attempt"), m.showHistory))
	if m.showHistory {
		for i, ref := range m.lenses {
			g := rep.Lenses[ref.group]
			if ref.attempt == 0 {
				add("  " + white(fmt.Sprintf("%s round %d", sanitize(g.Lens), g.Round)) + StyleDim.Render(fmt.Sprintf("  %s · %s", plural(len(g.Attempts), "attempt"), plural(g.Retries, "retry"))))
			}
			dl.lensAt = append(dl.lensAt, len(dl.lines))
			add(m.lensLine(g, g.Attempts[ref.attempt], !m.focusNodes && i == m.lens, width))
		}
	}
	add(disclosure("Telemetry & accounting", "d", sanitize(rep.Telemetry.State), m.showDiagnostics))
	if m.showDiagnostics {
		for _, section := range [][]string{m.headerLines(width), m.timeLines(width), m.totalsLines(width)} {
			add("")
			for _, l := range section {
				add(l)
			}
		}
		for _, d := range rep.Diagnostics {
			add(StyleWarning.Render(sanitize(d.Code)) + "  " + white(sanitize(d.ExecutionID)) + "  " + StyleDim.Render(sanitize(d.Detail)))
		}
	}
	add("")
	add(m.freshnessCell(width))
	return dl
}
