package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"loom/internal/runreport"
)

func TestRunDetailSummaryAndDisclosures(t *testing.T) {
	d, err := runreport.LoadDetail(fixtureDB(t), fixtureRun)
	if err != nil {
		t.Fatal(err)
	}
	m := newRunDetailModel(fixtureRun, 0, 120, 40)
	m.setDetail(d, nil)
	v := stripANSI(m.view())
	for _, want := range []string{fixtureTicket, "Completed", "37m 49s", "Unavailable", "Tokens (partial)", "Attention", "Metrics incomplete", "Reviews", "Satisfied", "[e]", "[a]", "[d]"} {
		if !strings.Contains(v, want) {
			t.Errorf("summary lacks %q:\n%s", want, v)
		}
	}
	for _, hidden := range []string{"Legacy", "stage work/1 #1", "no execution record", "superseded", "Producer"} {
		if strings.Contains(v, hidden) {
			t.Errorf("summary exposed diagnostic detail %q", hidden)
		}
	}
	if strings.Index(v, "Attention") > strings.Index(v, "Reviews") {
		t.Error("attention must precede the review details")
	}
	for _, k := range []string{"e", "a", "d"} {
		m, _ = m.update(key(k))
	}
	all := stripANSI(strings.Join(m.lines(m.contentWidth()).lines, "\n"))
	for _, want := range []string{"stage work/1 #1", "stage work/1 #2", "security round 1", "Legacy", "Last seen", "Pipeline"} {
		if !strings.Contains(all, want) {
			t.Errorf("expanded detail lacks %q", want)
		}
	}
	for _, k := range []string{"e", "a", "d"} {
		m, _ = m.update(key(k))
	}
	if m.showExecutions || m.showHistory || m.showDiagnostics {
		t.Error("disclosures did not collapse")
	}
}

func TestRunDetailLatestReviewDoesNotReuseAnOlderSuccess(t *testing.T) {
	d := threeNodeDetail()
	d.Report.Lenses = []runreport.LensGroup{
		{Lens: "security", Round: 2, Attempts: []runreport.LensAttempt{{Attempt: 1, Outcome: "completed"}}},
		{Lens: "security", Round: 0, Attempts: []runreport.LensAttempt{
			{Attempt: 2, Recorded: true, Status: "parsed", Verdict: "satisfied"},
			{Attempt: 1, Recorded: true, Status: "parsed", Verdict: "findings", Superseded: true},
		}},
	}
	d.LensResponses = map[string]string{runreport.LensKey("security", 0, 2): "older response", runreport.LensKey("security", 0, 1): "first response"}
	m := newRunDetailModel("r", 0, 120, 50)
	m.setDetail(d, nil)
	if len(m.lenses) != 1 || m.lenses[0] != (lensRef{group: 0, attempt: 0}) {
		t.Fatalf("latest review = %+v", m.lenses)
	}
	v := stripANSI(m.view())
	if strings.Contains(v, "Satisfied") || !strings.Contains(v, "No verdict recorded") {
		t.Fatalf("missing verdict masked by an older success:\n%s", v)
	}
	m, _ = m.update(key("enter"))
	if v := m.view(); !strings.Contains(v, "no response stored") || strings.Contains(v, "older response") {
		t.Fatalf("latest response was replaced: %s", v)
	}
	m, _ = m.update(key("esc"))
	m, _ = m.update(key("a"))
	m, _ = m.update(key("n"))
	m, _ = m.update(key("enter"))
	if !strings.Contains(m.view(), "older response") {
		t.Error("history cannot open the older stored response")
	}
}

func TestRunDetailSummaryPreservesUnknownAndPartialMetrics(t *testing.T) {
	d := threeNodeDetail()
	d.Report.Metrics.Parent = runreport.Metrics{Executions: 1}
	d.Report.Metrics.Total = runreport.Metrics{Executions: 3}
	m := newRunDetailModel("r", 0, 120, 50)
	m.setDetail(d, nil)
	v := stripANSI(strings.Join(m.summaryLines(m.contentWidth()), "\n"))
	if strings.Count(v, unavailable) != 3 || !strings.Contains(v, "Unavailable") || strings.Contains(v, "0 human") || strings.Contains(v, "0 in tools") {
		t.Errorf("unmeasured metrics:\n%s", v)
	}
	cost := 10.1889
	d.Report.Metrics.Parent.CostUSD = &cost
	m.setDetail(d, nil)
	v = stripANSI(m.view())
	if !strings.Contains(v, "Parent $10.19 only") || !strings.Contains(v, "Total cost unavailable") {
		t.Errorf("partial cost appears complete:\n%s", v)
	}
	d.Report.Metrics.Total.CostUSD = &cost
	m.setDetail(d, nil)
	if v := m.view(); !strings.Contains(v, "Recorded cost (partial)") {
		t.Errorf("priced partial telemetry lost its qualifier:\n%s", v)
	}
}

func TestRunDetailSharedContextKeepsTheVerdictVisible(t *testing.T) {
	for _, verdict := range []string{"findings", "satisfied"} {
		d := threeNodeDetail()
		d.Report.Lenses = []runreport.LensGroup{{Lens: "security", Round: 1, Attempts: []runreport.LensAttempt{{
			Attempt: 1, Recorded: true, Status: "parsed", Verdict: verdict, ContextState: "shared",
		}}}}
		m := newRunDetailModel("r", 0, 120, 50)
		m.setDetail(d, nil)
		v := stripANSI(m.view())
		if !strings.Contains(v, sentenceCase(verdict)+" · shared context") || !strings.Contains(v, "Security: "+verdict+" · shared context") {
			t.Errorf("%s verdict hidden by its context qualification:\n%s", verdict, v)
		}
	}
}

func TestRunDetailWorkerUsageDoesNotEstablishHumanInteractions(t *testing.T) {
	d := threeNodeDetail()
	d.Report.Metrics.Parent = runreport.Metrics{Executions: 1}
	d.Report.Metrics.Total.HumanInteractions = 0
	m := newRunDetailModel("r", 0, 120, 50)
	m.setDetail(d, nil)
	v := stripANSI(strings.Join(m.summaryLines(m.contentWidth()), "\n"))
	if strings.Contains(v, "0 human interactions") || !strings.Contains(v, "human interactions unavailable") || !strings.Contains(v, "1.5k") {
		t.Errorf("descendant usage made the parent count appear known:\n%s", v)
	}
}

func TestRunDetailHistoryPreservesTheSelectedReview(t *testing.T) {
	d := threeNodeDetail()
	d.Report.Lenses = []runreport.LensGroup{
		{Lens: "contract", Round: 1, Attempts: []runreport.LensAttempt{{Attempt: 1}, {Attempt: 2}}},
		{Lens: "quality", Round: 1, Attempts: []runreport.LensAttempt{{Attempt: 1}}},
	}
	d.LensResponses = map[string]string{runreport.LensKey("quality", 1, 1): "quality response"}
	m := newRunDetailModel("r", 0, 120, 60)
	m.setDetail(d, nil)
	m, _ = m.update(key("n"))
	for _, k := range []string{"a", "a", "a"} {
		m, _ = m.update(key(k))
		name, selected := m.selectedLensIdentity()
		if name != "quality" || selected != runreport.LensKey("quality", 1, 1) {
			t.Fatalf("history=%v switched review to %s %s", m.showHistory, name, selected)
		}
		m, _ = m.update(key("enter"))
		if !strings.Contains(m.view(), "quality response") {
			t.Error("Enter opened an unrelated review")
		}
		m, _ = m.update(key("esc"))
	}
	m, _ = m.update(key("p"))
	m, _ = m.update(key("p"))
	m, _ = m.update(key("a"))
	if _, selected := m.selectedLensIdentity(); selected != runreport.LensKey("contract", 1, 2) {
		t.Fatalf("collapsing an old attempt did not select that lens's latest: %s", selected)
	}
	// A refresh may reorder groups; selection is tied to identity, not row.
	reloaded := *d
	report := *d.Report
	report.Lenses = []runreport.LensGroup{report.Lenses[1], report.Lenses[0]}
	reloaded.Report = &report
	m.setDetail(&reloaded, nil)
	if _, selected := m.selectedLensIdentity(); selected != runreport.LensKey("contract", 1, 2) {
		t.Errorf("refresh moved selection to %s", selected)
	}
}

func TestRunDetailFitsTerminalWidthsAndWrapsResponses(t *testing.T) {
	d, err := runreport.LoadDetail(fixtureDB(t), fixtureRun)
	if err != nil {
		t.Fatal(err)
	}
	d.Report.Run.Ticket = strings.Repeat("長い名前", 30)
	d.LensResponses[runreport.LensKey("security", 1, 1)] = strings.Repeat("long response ", 30) + "END-OF-RESPONSE"
	for _, width := range []int{20, 40, 60, 80, 100, 120, 240} {
		m := newRunDetailModel(fixtureRun, 0, width, 200)
		m.setDetail(d, nil)
		for _, expanded := range []bool{false, true} {
			m.showExecutions, m.showHistory, m.showDiagnostics = expanded, expanded, expanded
			m.rebuildLenses()
			for _, l := range strings.Split(m.view(), "\n") {
				if got := lipgloss.Width(l); got > width {
					t.Errorf("width %d expanded %v: %d cells: %q", width, expanded, got, l)
				}
			}
			app := New(Options{RunID: fixtureRun})
			app.width, app.height, app.runDetail = width, 204, m
			for _, l := range strings.Split(app.View(), "\n") {
				if got := lipgloss.Width(l); got > width {
					t.Errorf("app width %d expanded %v: %d cells: %q", width, expanded, got, l)
				}
			}
			dl := m.lines(m.contentWidth())
			for i := range m.lenses {
				if dl.lensAt[i] >= len(dl.lines) {
					t.Errorf("cursor %d points outside physical rows", i)
				}
			}
		}
		m, _ = m.update(key("enter"))
		v := stripANSI(m.view())
		if !strings.Contains(strings.ReplaceAll(stripANSI(m.responseView()), "\n", ""), "END-OF-RESPONSE") && width >= 40 {
			t.Errorf("width %d truncated the response tail:\n%s", width, v)
		}
		for _, l := range strings.Split(v, "\n") {
			if lipgloss.Width(l) > width {
				t.Errorf("response exceeds width %d: %s", width, l)
			}
		}
	}
}
