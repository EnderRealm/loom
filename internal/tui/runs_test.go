package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"loom/internal/parse/lens"
	"loom/internal/parse/summary"
	"loom/internal/runreport"
	"loom/internal/runs"
	"loom/internal/summaries"
)

func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func ms(n int64) *int64      { return &n }
func usd(f float64) *float64 { return &f }

var day = time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)

// sortRows is one run per column so every sort has a distinct winner.
func sortRows() []runreport.Summary {
	return []runreport.Summary{
		{RunID: "a", Ticket: "t/a", Metered: true, StartedAt: day.Add(4 * time.Hour), Outcome: "completed", WallMs: ms(100), ExecutionTimeMs: 1, ToolTimeMs: 1, TotalTokens: 1, ToolCalls: 1, Failures: 0, Children: 1, CostUSD: usd(0.1)},
		{RunID: "b", Ticket: "t/b", Metered: true, StartedAt: day.Add(3 * time.Hour), Outcome: "failed", WallMs: ms(900), ExecutionTimeMs: 2, ToolTimeMs: 2, TotalTokens: 2, ToolCalls: 2, Failures: 1, Children: 2, CostUSD: usd(0.2)},
		{RunID: "c", Ticket: "t/c", Metered: true, StartedAt: day.Add(2 * time.Hour), Outcome: "stopped", WallMs: nil, ExecutionTimeMs: 9, ToolTimeMs: 3, TotalTokens: 3, ToolCalls: 3, Failures: 2, Children: 3, CostUSD: nil},
		{RunID: "d", Ticket: "t/d", Metered: true, StartedAt: day.Add(1 * time.Hour), Outcome: "running", WallMs: ms(500), ExecutionTimeMs: 3, ToolTimeMs: 9, TotalTokens: 9, ToolCalls: 9, Failures: 9, Children: 9, CostUSD: usd(0.9)},
	}
}

func order(m runsModel) string {
	var ids []string
	for _, r := range m.rows {
		ids = append(ids, r.RunID)
	}
	return strings.Join(ids, "")
}

// AC1: `s` walks every metric column, each orders the rows by its own value
// with an unmeasured value last, and a sorted row still carries its ticket,
// date and outcome.
func TestRunsSortCyclesEveryMetricColumn(t *testing.T) {
	var m runsModel
	m.setSize(160, 20)
	m.setRows(sortRows(), nil)
	want := map[string]string{
		"DATE":      "abcd",
		"WALL":      "bdac", // c has no wall: last
		"EXEC":      "cdba",
		"TOOL TIME": "dcba",
		"TOKENS":    "dcba",
		"TOOLS":     "dcba",
		"ERRORS":    "dcba",
		"CHILDREN":  "dcba",
		"COST":      "dbac", // c is unpriced: last
	}
	if got := runSortColumns[m.sortCol].header; got != "DATE" {
		t.Fatalf("default sort = %s, want DATE", got)
	}
	seen := map[string]bool{}
	for range runSortColumns {
		header := runSortColumns[m.sortCol].header
		seen[header] = true
		if got := order(m); got != want[header] {
			t.Errorf("%s: order %s, want %s", header, got, want[header])
		}
		if !strings.Contains(m.view(), StyleColHeaderActive.Render(header)) {
			t.Errorf("%s: header not marked active", header)
		}
		for i, r := range m.rows {
			row := m.renderRow(r, false)
			date := r.StartedAt.Local().Format("01-02 15:04")
			if !strings.Contains(row, r.Ticket) || !strings.Contains(row, date) || !strings.Contains(row, r.Outcome) {
				t.Errorf("%s row %d lost ticket/date/outcome: %q", header, i, row)
			}
		}
		m, _ = m.update(key("s"))
	}
	for _, c := range runSortColumns {
		if !seen[c.header] {
			t.Errorf("cycle never reached %s", c.header)
		}
	}
	if len(want) != len(runSortColumns) {
		t.Errorf("%d sortable columns, test expects %d", len(runSortColumns), len(want))
	}
}

// A run with no metered transcript has no failure count, so ERRORS orders
// it after every metered run whatever its zero says.
func TestRunsSortErrorsPutsUnmeteredLast(t *testing.T) {
	var m runsModel
	m.setSize(160, 20)
	rows := sortRows()
	rows[0].Metered = false // a: newest, would otherwise tie-break first
	rows[0].Failures = 0
	m.setRows(rows, nil)
	for runSortColumns[m.sortCol].header != "ERRORS" {
		m, _ = m.update(key("s"))
	}
	if got := order(m); got != "dcba" {
		t.Errorf("ERRORS order %s, want dcba with the unmetered run last", got)
	}
	rows = sortRows()
	rows[3].Metered = false // d: the largest count, unknown once unmetered
	m.setRows(rows, nil)
	if got := order(m); got != "cbad" {
		t.Errorf("ERRORS order %s, want cbad with the unmetered run last", got)
	}
}

func TestRunsSortKeepsTheSelection(t *testing.T) {
	var m runsModel
	m.setSize(160, 20)
	m.setRows(sortRows(), nil)
	m, _ = m.update(key("down"))
	m, _ = m.update(key("down"))
	if m.selected().RunID != "c" {
		t.Fatalf("selected %s, want c", m.selected().RunID)
	}
	m, _ = m.update(key("s"))
	if m.selected().RunID != "c" {
		t.Errorf("selected %s after a re-sort, want c kept", m.selected().RunID)
	}
	m.setRows(sortRows(), nil)
	if m.selected().RunID != "c" {
		t.Errorf("selected %s after a reload, want c kept", m.selected().RunID)
	}
}

// AC4: what the report could not measure renders as unavailable, never as
// a zero, and a run's outcome, its telemetry and its freshness are three
// cells that read apart.
func TestRunsRenderUnknownAsUnavailable(t *testing.T) {
	var m runsModel
	m.setSize(160, 20)
	unknown := runreport.Summary{RunID: "u", Outcome: runreport.OutcomeUnknown, TelemetryState: runreport.StatePartial}
	row := m.renderRow(unknown, false)
	if n := strings.Count(row, unavailable); n < 4 {
		t.Errorf("row for a run with no ticket, start, wall or last observation shows %d unavailable marks:\n%s", n, row)
	}
	if strings.Contains(row, "$0") {
		t.Errorf("cost of an unpriced run rendered as a price:\n%s", row)
	}
	if strings.Contains(row, "01-") {
		t.Errorf("a zero start rendered as a date:\n%s", row)
	}

	// A run the report could not meter (root session not folded, or an
	// execution with no transcript) has no usage figure: ticket, date, wall,
	// seen and cost are present, so every marker left is exec, tool time,
	// tokens, tools and errors.
	unmetered := runreport.Summary{
		RunID: "m", Ticket: "loom/x-0002", StartedAt: time.Now().Add(-time.Hour), Outcome: "completed",
		TelemetryState: runreport.StatePartial, LastObservedAt: time.Now(), WallMs: ms(60000), CostUSD: usd(0),
		UntimedExecutions: 3, Children: 2,
	}
	row = m.renderRow(unmetered, false)
	if n := strings.Count(row, unavailable); n != 5 {
		t.Errorf("unmetered row shows %d unavailable marks, want exec, tool time, tokens, tools and errors:\n%s", n, row)
	}
	metered := unmetered
	metered.Metered = true
	metered.TimedExecutions = 1
	metered.ToolCalls = 5
	metered.TotalTokens = 1200
	metered.ExecutionTimeMs = 90000
	row = m.renderRow(metered, false)
	if strings.Contains(row, unavailable) || !strings.Contains(row, "1.2k") || !strings.Contains(row, "1m") {
		t.Errorf("metered row:\n%s", row)
	}

	live := runreport.Summary{
		RunID: "l", Ticket: "loom/x-0001", StartedAt: time.Now().Add(-time.Hour),
		Outcome: runreport.OutcomeRunning, TelemetryState: runreport.StatePartial, Pending: 2,
		LastObservedAt: time.Now().Add(-5 * time.Minute), WallMs: ms(55 * 60 * 1000), CostUSD: usd(0.5),
	}
	row = m.renderRow(live, false)
	for _, want := range []string{"running", "partial (2)", "5m", "55m", "$0.5000"} {
		if !strings.Contains(row, want) {
			t.Errorf("running row lacks %q:\n%s", want, row)
		}
	}
	done := live
	done.Outcome = "completed"
	done.TelemetryState = runreport.StateComplete
	done.Pending = 0
	row = m.renderRow(done, false)
	if !strings.Contains(row, "completed") || !strings.Contains(row, "complete ") || strings.Contains(row, "partial") {
		t.Errorf("completed row with complete telemetry:\n%s", row)
	}
}

// AC5: the list loads in a Cmd the update loop never runs, so keys land
// while the query is still out, and the rows arrive when it returns.
func TestRunsListLoadsOffTheUpdateLoop(t *testing.T) {
	release := make(chan struct{})
	prev := listSummaries
	listSummaries = func(string, time.Time, time.Time) ([]runreport.Summary, error) {
		<-release
		return sortRows(), nil
	}
	t.Cleanup(func() { listSummaries = prev })

	update := func(m tea.Model, msg tea.Msg) step { return updateWithin(t, m, msg) }

	var m tea.Model = New()
	m = update(m, tea.WindowSizeMsg{Width: 160, Height: 30}).m
	s := update(m, key("w"))
	app := s.m.(App)
	if app.overlay != overlayRuns || !app.runs.loading || s.cmd == nil {
		t.Fatalf("w: overlay %v loading %v cmd %v, want the runs overlay loading with a cmd out", app.overlay, app.runs.loading, s.cmd != nil)
	}
	if !strings.Contains(app.View(), "loading…") {
		t.Error("the overlay does not say it is loading")
	}
	loaded := make(chan tea.Msg, 1)
	go func() { loaded <- s.cmd() }()

	app = update(app, key("s")).m.(App)
	if runSortColumns[app.runs.sortCol].header != "WALL" {
		t.Errorf("s during the load: sort = %s, want WALL", runSortColumns[app.runs.sortCol].header)
	}
	app = update(app, key("esc")).m.(App)
	if app.overlay != overlayNone {
		t.Error("esc during the load did not close the overlay")
	}
	app = update(app, key("w")).m.(App)
	if app.overlay != overlayRuns || !app.runs.loading {
		t.Error("reopening during the load lost the loading state")
	}
	select {
	case <-loaded:
		t.Fatal("the loader returned before it was released")
	default:
	}

	close(release)
	msg := <-loaded
	app = update(app, msg).m.(App)
	if app.runs.loading || len(app.runs.rows) != 4 {
		t.Fatalf("after the load: loading %v rows %d", app.runs.loading, len(app.runs.rows))
	}
	if got := order(app.runs); got != "bdac" {
		t.Errorf("rows arrived under the sort chosen during the load: %s, want bdac", got)
	}
	if v := app.View(); !strings.Contains(v, "t/a") || strings.Contains(v, "loading…") {
		t.Errorf("view after the load:\n%s", v)
	}
}

type step struct {
	m   tea.Model
	cmd tea.Cmd
}

// updateWithin runs one Update on its own goroutine under a deadline: one
// that ran a loader inline would sit on its channel and fail here rather
// than hang the suite.
func updateWithin(t *testing.T, m tea.Model, msg tea.Msg) step {
	t.Helper()
	done := make(chan step, 1)
	go func() {
		next, cmd := m.Update(msg)
		done <- step{next, cmd}
	}()
	select {
	case s := <-done:
		return s
	case <-time.After(2 * time.Second):
		t.Fatalf("Update(%T) blocked on the loader", msg)
		return step{}
	}
}

// runCmd executes cmd off the test goroutine, delivering what it and every
// member of a batch produce to msgs. A refresh tick among them sleeps out
// its interval in the background and is never waited on.
func runCmd(cmd tea.Cmd, msgs chan<- tea.Msg) {
	if cmd == nil {
		return
	}
	go func() {
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, c := range batch {
				runCmd(c, msgs)
			}
			return
		}
		msgs <- msg
	}()
}

// fixtureRun is internal/runs/testdata/executions.jsonl's run, with its
// root session folded so the security lens has a stored response.
const (
	fixtureRun    = "0f4c3a6e-2d1b-4b7e-9c8a-5e2f1d0a9b31"
	fixtureTicket = "loom/persist-execution-identities-2149"
	rootSession   = "195f819e-1e11-4e08-8c16-a340f512f892"
	dispatchID    = "toolu_01Wq9LensSecurityR1"
)

func fixtureDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "summaries.db")
	st, err := summaries.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	records := filepath.Join("..", "runs", "testdata", "executions.jsonl")
	f, err := os.Open(records)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ImportExecutions(context.Background(), records, f, info.Size(), info.ModTime()); err != nil {
		t.Fatal(err)
	}

	start := time.Date(2026, 9, 10, 17, 2, 11, 0, time.UTC)
	verdict := "<task-notification><tool-use-id>" + dispatchID + "</tool-use-id><result>```json\n" +
		`{"lens": "security", "verdict": "satisfied", "summary": "No exposure.", "findings": []}` + "\n```</result></task-notification>"
	sum := &summary.SessionSummary{
		SessionID: rootSession,
		Agent:     summary.AgentClaude,
		StartTime: start,
		EndTime:   start.Add(40 * time.Minute),
		Turns: []summary.Turn{
			{Idx: 0, UserMessage: "<command-message>work</command-message>\n<command-name>/work</command-name>\n<command-args>" + fixtureTicket + "</command-args>",
				AssistantText: "dispatching (" + fixtureTicket + " round 1): security",
				StartedAt:     start, EndedAt: start.Add(time.Minute), Model: "claude-opus-5", InputTokens: 100, OutputTokens: 50},
			{Idx: 1, UserMessage: verdict, AssistantText: "merged", StartedAt: start.Add(14 * time.Minute), EndedAt: start.Add(15 * time.Minute), Model: "claude-opus-5", InputTokens: 50, OutputTokens: 10},
		},
		ToolCalls: []summary.ToolCall{
			{TurnIdx: 0, CallID: dispatchID, Kind: summary.KindTask, ToolName: "Agent", KeyArg: "security lens review", StartedAt: start.Add(time.Minute), DurationMs: 200000},
		},
	}
	for _, b := range lens.Extract(verdict) {
		sum.LensResponses = append(sum.LensResponses, summary.LensResponse{
			TurnIdx: 1, Origin: summary.OriginTaskNotification, DispatchID: dispatchID, SourceLine: 3, At: start.Add(14 * time.Minute), Block: b,
		})
	}
	if err := st.WriteSummary(context.Background(), sum, summaries.SourceInfo{Project: "loom"}); err != nil {
		t.Fatal(err)
	}
	return path
}

// pointLoaderAt reads run detail from path, and gives the dashboard's own
// loads an empty home so Init does not walk the host's ~/.loom.
func pointLoaderAt(t *testing.T, path string) {
	t.Helper()
	t.Setenv("LOOM_HOME", t.TempDir())
	prev := loadRunDetail
	loadRunDetail = func(_ string, runID string) (*runreport.Detail, error) {
		return runreport.LoadDetail(path, runID)
	}
	t.Cleanup(func() { loadRunDetail = prev })
}

// AC3: `--run` opens on that run, and its detail carries the hierarchy,
// the stage and lens attempts with their metrics, the failure classes and
// the whole lens response.
func TestRunOptionOpensTheExactRun(t *testing.T) {
	pointLoaderAt(t, fixtureDB(t))

	var m tea.Model = New(Options{RunID: fixtureRun})
	if m.(App).overlay != overlayRunDetail {
		t.Fatal("--run did not open on the run detail")
	}
	// Tall enough to hold the whole report: the sections are checked as
	// rendered, not as scrolled to.
	m, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 120})
	if v := m.View(); !strings.Contains(v, fixtureRun) || !strings.Contains(v, "loading…") {
		t.Errorf("view before the load:\n%s", v)
	}
	m, _ = m.Update(detailFromInit(t, m.Init()))
	app := m.(App)
	if app.runDetail.err != nil {
		t.Fatal(app.runDetail.err)
	}

	v := app.View()
	for _, want := range []string{
		fixtureTicket, "Outcome", "completed", "Telemetry", "partial", "Last seen", "HIERARCHY",
		"stage work/1 #1", "stage work/1 #2", "lens security r1 #1", "subagent", "pending",
		"STAGES", "work/1", "2 attempts · 1 retry", "review/1", "LENSES", "security round 1",
		"parsed", "satisfied", "Failures", "tool", "api", "process", "other", "response",
	} {
		if !strings.Contains(v, want) {
			t.Errorf("detail lacks %q", want)
		}
	}
	if t.Failed() {
		t.Log(v)
	}

	// The node cursor walks the tree in display order.
	app = press(app, "j", "j")
	if got := app.runDetail.nodes[app.runDetail.node].n.ExecutionID; got != "codex-01a0029b" {
		t.Errorf("node after j j = %s, want the codex child under the subagent", got)
	}
	app = press(app, "tab", "k")
	if got := app.runDetail.nodes[app.runDetail.node].n.ExecutionID; got != "codex-01a0029b" {
		t.Errorf("node after tab k = %s", got)
	}
	// Enter on the lens attempt opens the stored body in full.
	app = press(app, "enter")
	if !app.runDetail.showResponse {
		t.Fatal("enter did not open the response")
	}
	v = app.View()
	if !strings.Contains(v, "LENS RESPONSE") || !strings.Contains(v, `"summary": "No exposure."`) || !strings.Contains(v, `"findings": []`) {
		t.Errorf("response view:\n%s", v)
	}
	if !strings.Contains(app.helpLine(), "esc/q back") {
		t.Errorf("help = %q", app.helpLine())
	}
	app = press(app, "esc")
	if app.runDetail.showResponse || app.overlay != overlayRunDetail {
		t.Error("esc from the response left the detail")
	}
	// Leaving the detail lands on the runs list, loading it.
	m, cmd := app.Update(key("esc"))
	app = m.(App)
	if app.overlay != overlayRuns || !app.runs.loading || cmd == nil {
		t.Errorf("esc from the detail: overlay %v loading %v cmd %v, want the runs list loading", app.overlay, app.runs.loading, cmd != nil)
	}
}

// detailFromInit runs Init's members side by side and returns the detail
// load's message. Side by side rather than drained in turn: the refresh tick
// is among them and would hold the test for its whole interval.
func detailFromInit(t *testing.T, init tea.Cmd) runDetailLoadedMsg {
	t.Helper()
	msgs := make(chan tea.Msg, 8)
	batch, ok := init().(tea.BatchMsg)
	if !ok {
		t.Fatal("Init did not batch its commands")
	}
	for _, cmd := range batch {
		go func() { msgs <- cmd() }()
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case msg := <-msgs:
			if d, ok := msg.(runDetailLoadedMsg); ok {
				return d
			}
		case <-deadline:
			t.Fatal("Init dispatched no detail load")
		}
	}
}

func press(a App, keys ...string) App {
	for _, k := range keys {
		m, _ := a.Update(key(k))
		a = m.(App)
	}
	return a
}

func TestRunOptionReportsAnUnknownRun(t *testing.T) {
	pointLoaderAt(t, fixtureDB(t))

	var m tea.Model = New(Options{RunID: "nope"})
	m, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m, _ = m.Update(detailFromInit(t, m.Init()))
	app := m.(App)
	if app.err != nil {
		t.Fatalf("an unknown run took the whole UI down: %v", app.err)
	}
	if v := app.View(); !strings.Contains(v, "run not found: nope") {
		t.Errorf("view:\n%s", v)
	}
	// Keys still work and the list is reachable.
	app = press(app, "j", "n")
	m, cmd := app.Update(key("q"))
	if m.(App).overlay != overlayRuns || cmd == nil {
		t.Error("q from the failed detail did not open the runs list")
	}
}

// A detail arriving for a run the user has since left is dropped.
func TestStaleDetailLoadIsIgnored(t *testing.T) {
	a := New()
	m, _ := a.openRun("one")
	app := m.(App)
	m, _ = app.openRun("two")
	app = m.(App)
	m, _ = app.Update(runDetailLoadedMsg{runID: "one", detail: &runreport.Detail{Report: &runreport.Report{}}})
	if app = m.(App); !app.runDetail.loading || app.runDetail.detail != nil {
		t.Error("a stale load replaced the pending detail")
	}
}

// A scope with no metered transcript has no token or tool count in TOTALS,
// as its nodes have none, and a node whose record carried no position reads
// "?" rather than 0.
func TestRunDetailTotalsRenderUnmeteredAsUnavailable(t *testing.T) {
	rep := &runreport.Report{
		Run: runreport.RunInfo{RunID: "r", Outcome: "completed"},
		Metrics: runreport.Scopes{
			Parent:      runreport.Metrics{Executions: 1, ToolCalls: 0},
			Descendants: runreport.Metrics{Executions: 2, TokensByRuntime: map[string]*runreport.Tokens{"claude": {Total: 300}}, TotalTokens: 300, ToolCalls: 4},
			Total:       runreport.Metrics{Executions: 3, TokensByRuntime: map[string]*runreport.Tokens{"claude": {Total: 300}}, TotalTokens: 300, ToolCalls: 4},
		},
		Tree: &runs.Node{ExecutionID: "root", Kind: "stage", Stage: "work"},
	}
	m := newRunDetailModel("r", 0, 120, 60)
	m.setDetail(&runreport.Detail{Report: rep, LensResponses: map[string]string{}}, nil)
	lines := m.lines(m.contentWidth()).lines
	rows := map[string]string{}
	for _, l := range lines {
		plain := stripANSI(l)
		for _, label := range []string{"Tokens", "Tool calls", "Hook signals", "Human"} {
			if strings.HasPrefix(plain, label) {
				rows[label] = strings.TrimSpace(strings.TrimPrefix(plain, label))
			}
		}
	}
	if row := rows["Tokens"]; !strings.HasPrefix(row, unavailable) || !strings.Contains(row, "300") {
		t.Errorf("tokens row = %q, want the unmetered parent unavailable beside the metered scopes", row)
	}
	if row := rows["Tool calls"]; !strings.HasPrefix(row, unavailable) || !strings.Contains(row, "4") {
		t.Errorf("tool calls row = %q, want the unmetered parent unavailable beside the metered scopes", row)
	}
	// Hook signals and human interactions come only from metered transcripts
	// too: the unmetered parent has none, and its column reads unavailable
	// while the metered scopes read their count.
	for _, label := range []string{"Hook signals", "Human"} {
		if row := rows[label]; !strings.HasPrefix(row, unavailable) || strings.Count(row, "0") != 2 {
			t.Errorf("%s row = %q, want the unmetered parent unavailable beside two metered zeros", label, row)
		}
	}
	if v := strings.Join(lines, "\n"); !strings.Contains(v, "stage work/? #?") {
		t.Errorf("a node with no occurrence or attempt did not read as unknown:\n%s", v)
	}

	// Nothing metered in the whole scope: failures and tool time are
	// unknown, not zero.
	rep.Metrics.Total = runreport.Metrics{Executions: 3}
	rep.Metrics.Total.ExecutionTimeCoverage = runreport.Coverage{Timed: 0, Untimed: 3}
	m.setDetail(&runreport.Detail{Report: rep, LensResponses: map[string]string{}}, nil)
	var failures, toolTime, legacy, execution string
	for _, l := range m.lines(m.contentWidth()).lines {
		plain := stripANSI(l)
		switch {
		case strings.HasPrefix(plain, "Failures"):
			failures = plain
		case strings.HasPrefix(plain, "Tool time"):
			toolTime = plain
		case strings.HasPrefix(plain, "Legacy"):
			legacy = plain
		case strings.HasPrefix(plain, "Execution "): // not the TOTALS Executions row
			execution = strings.TrimSpace(strings.TrimPrefix(plain, "Execution "))
		}
	}
	// Execution time with nothing timed is unknown, as the runs list renders
	// it, not a leading 0 beside the coverage note.
	if !strings.HasPrefix(execution, unavailable) || strings.HasPrefix(execution, "0") {
		t.Errorf("execution row = %q, want unavailable with 0 timed executions", execution)
	}
	if !strings.Contains(failures, unavailable) || strings.Contains(failures, "tool 0") {
		t.Errorf("failures row = %q, want unavailable with no transcript metered", failures)
	}
	if !strings.Contains(toolTime, unavailable) || strings.Contains(toolTime, "over every") {
		t.Errorf("tool time row = %q, want unavailable with no transcript metered", toolTime)
	}
	// Legacy is the parent span's transcript figure: with the root session
	// not folded it is unknown, not 0ms.
	if !strings.Contains(legacy, unavailable) || strings.Contains(legacy, "0ms") {
		t.Errorf("legacy row = %q, want unavailable with the root transcript not metered", legacy)
	}
}

// Run records and lens responses are agent-written: a terminal control
// sequence in either must not reach the terminal when the report is opened.
func TestRunScreensStripTerminalControls(t *testing.T) {
	const osc, csi = "\x1b]52;c;AAAA\x07", "\x1b[2J"
	hasControl := func(s string) bool {
		plain := stripANSI(s) // lipgloss's own SGR is expected; anything else is injected
		return strings.ContainsAny(plain, "\x1b\x07\x9b") || strings.Contains(plain, "]52;c;AAAA") || strings.Contains(plain, "[2J")
	}
	rep := &runreport.Report{
		Run:         runreport.RunInfo{RunID: "r" + csi, Ticket: "loom/t" + osc, Runtime: "claude" + osc, Origin: "weft" + csi, Producer: "loom" + osc, Outcome: "completed" + csi},
		Telemetry:   runreport.Telemetry{State: runreport.StatePartial, Gaps: []string{"gap" + osc}},
		Tree:        &runs.Node{ExecutionID: "root" + osc, Kind: "stage", Stage: "work" + csi, Outcome: "completed" + osc},
		Executions:  []runreport.ExecutionMetrics{{ExecutionID: "root" + osc, AgentType: "subagent" + csi, CountedBy: "x" + osc}},
		Diagnostics: []runs.Diagnostic{{Code: "code" + csi, Detail: "detail" + osc, ExecutionID: "exec" + csi}},
		Stages:      []runreport.Stage{{Stage: "work" + osc, Occurrence: 1, Attempts: []runreport.StageAttempt{{Attempt: 1, ExecutionID: "root" + osc}}}},
		Lenses: []runreport.LensGroup{{Lens: "security" + csi, Round: 1, Attempts: []runreport.LensAttempt{{
			Attempt: 1, ExecutionID: "lens" + osc, Recorded: true, Status: "parsed" + csi, Verdict: "satisfied" + osc, ContextState: "fresh" + csi, Malformed: "bad" + osc,
		}}}},
	}
	rep.Metrics.Total.PricingWarnings = []string{"warn" + csi}
	body := map[string]string{runreport.LensKey(rep.Lenses[0].Lens, 1, 1): "one" + osc + "\ntwo" + csi + "\nthree"}
	m := newRunDetailModel(rep.Run.RunID, 0, 120, 80)
	m.setDetail(&runreport.Detail{Report: rep, LensResponses: body}, nil)
	v := m.view()
	for _, want := range []string{"loom/t", "claude", "weft", "loom", "gap", "root", "subagent", "code", "detail", "exec", "work", "security", "parsed", "satisfied", "fresh", "bad", "warn"} {
		if !strings.Contains(v, want) {
			t.Errorf("detail lost %q around a stripped control", want)
		}
	}
	if hasControl(v) {
		t.Errorf("detail view carries a terminal control:\n%q", v)
	}
	m, _ = m.update(key("enter"))
	v = m.view()
	if !strings.Contains(v, "one") || !strings.Contains(v, "two") || !strings.Contains(v, "three") {
		t.Errorf("response view lost its body:\n%s", v)
	}
	if hasControl(v) {
		t.Errorf("response view carries a terminal control:\n%q", v)
	}
	// A failed refresh keeps the body and reports the error on the
	// freshness line; a failure with nothing loaded is the body.
	m, _ = m.update(key("esc"))
	m.setDetail(nil, errors.New("run not found: "+csi))
	if v := m.view(); hasControl(v) || !strings.Contains(v, "run not found") || !strings.Contains(v, "HIERARCHY") {
		t.Errorf("stale view:\n%q", v)
	}
	fresh := newRunDetailModel("r", 0, 120, 80)
	fresh.setDetail(nil, errors.New("run not found: "+csi))
	if v := fresh.view(); hasControl(v) || !strings.Contains(v, "run not found") {
		t.Errorf("error view:\n%q", v)
	}

	var list runsModel
	list.setSize(160, 20)
	row := list.renderRow(runreport.Summary{RunID: "r", Ticket: "loom/t" + osc, Outcome: "completed" + csi, StartedAt: day}, false)
	if hasControl(row) || !strings.Contains(row, "loom/t") || !strings.Contains(row, "completed") {
		t.Errorf("list row:\n%q", row)
	}
	list.setRows(nil, errors.New("open: "+osc))
	if v := list.view(); hasControl(v) || !strings.Contains(v, "open: ") {
		t.Errorf("list error view:\n%q", v)
	}
}

// Shrinking the pane while a response is open must not take the TUI down:
// the window is floored at one row however small the body gets.
func TestRunDetailResponseViewSurvivesATinyHeight(t *testing.T) {
	rep := &runreport.Report{
		Run:    runreport.RunInfo{RunID: "r"},
		Lenses: []runreport.LensGroup{{Lens: "security", Round: 1, Attempts: []runreport.LensAttempt{{Attempt: 1}}}},
	}
	body := map[string]string{runreport.LensKey("security", 1, 1): "one\ntwo\nthree\nfour"}
	m := newRunDetailModel("r", 0, 80, 3)
	m.setDetail(&runreport.Detail{Report: rep, LensResponses: body}, nil)
	m, _ = m.update(key("enter"))
	for h := 0; h <= 6; h++ {
		m.setSize(80, h)
		if v := m.view(); !strings.Contains(v, "LENS RESPONSE") || !strings.Contains(v, "one") {
			t.Errorf("height %d: response view lost its body:\n%s", h, v)
		}
	}
}

// threeNodeDetail is a run with a root and two children, one of them still
// pending with no transcript metered, and one lens attempt.
func threeNodeDetail() *runreport.Detail {
	rep := &runreport.Report{
		Run:       runreport.RunInfo{RunID: "r", Outcome: runreport.OutcomeRunning},
		Telemetry: runreport.Telemetry{State: runreport.StatePartial, ExecutionsPending: []string{"lens-1"}},
		Metrics: runreport.Scopes{
			Parent:      runreport.Metrics{Executions: 1, TokensByRuntime: map[string]*runreport.Tokens{"claude-code": {Total: 1200}}, TotalTokens: 1200},
			Descendants: runreport.Metrics{Executions: 2, TokensByRuntime: map[string]*runreport.Tokens{"claude-code": {Total: 300}}, TotalTokens: 300},
			Total:       runreport.Metrics{Executions: 3, TokensByRuntime: map[string]*runreport.Tokens{"claude-code": {Total: 1500}}, TotalTokens: 1500},
		},
		Executions: []runreport.ExecutionMetrics{
			{ExecutionID: "root-1", Kind: "root", Metrics: runreport.Metrics{TokensByRuntime: map[string]*runreport.Tokens{"claude-code": {Total: 1200}}, TotalTokens: 1200}},
			{ExecutionID: "stage-1", Kind: "stage", Metrics: runreport.Metrics{TokensByRuntime: map[string]*runreport.Tokens{"claude-code": {Total: 300}}, TotalTokens: 300}},
			{ExecutionID: "lens-1", Kind: "lens", Lens: "security"},
		},
		Tree: &runs.Node{ExecutionID: "root-1", Kind: "root", Children: []*runs.Node{
			{ExecutionID: "stage-1", Kind: "stage", Stage: "work", Outcome: "completed"},
			{ExecutionID: "lens-1", Kind: "lens", Lens: "security"},
		}},
		Lenses: []runreport.LensGroup{{Lens: "security", Round: 1, Attempts: []runreport.LensAttempt{{Attempt: 1, ExecutionID: "lens-1"}}}},
	}
	return &runreport.Detail{Report: rep, LensResponses: map[string]string{}, SweptAt: time.Now()}
}

// stubDetailLoader stands in a loader that blocks until released, counting
// its calls, with no local shipper state.
func stubDetailLoader(t *testing.T, detail *runreport.Detail) (release chan struct{}, loads *int32) {
	t.Helper()
	t.Setenv("LOOM_HOME", t.TempDir())
	release = make(chan struct{}, 8)
	loads = new(int32)
	prevLoad, prevSync := loadRunDetail, lastShipperSync
	loadRunDetail = func(string, string) (*runreport.Detail, error) {
		atomic.AddInt32(loads, 1)
		<-release
		return detail, nil
	}
	lastShipperSync = func() (time.Time, bool) { return time.Time{}, false }
	t.Cleanup(func() { loadRunDetail, lastShipperSync = prevLoad, prevSync })
	return release, loads
}

func waitLoads(t *testing.T, loads *int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(loads) < want {
		if time.Now().After(deadline) {
			t.Fatalf("loader called %d times, want %d", atomic.LoadInt32(loads), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func detailMsg(t *testing.T, msgs <-chan tea.Msg) runDetailLoadedMsg {
	t.Helper()
	select {
	case msg := <-msgs:
		d, ok := msg.(runDetailLoadedMsg)
		if !ok {
			t.Fatalf("got %T, want runDetailLoadedMsg", msg)
		}
		return d
	case <-time.After(2 * time.Second):
		t.Fatal("no detail load arrived")
		return runDetailLoadedMsg{}
	}
}

// A refresh tick while a load is out starts no second load, keys still
// land during it, the body says it is refreshing, and the tick after the
// load returns loads again.
func TestRunDetailRefreshCoalescesIntoTheLoadInFlight(t *testing.T) {
	release, loads := stubDetailLoader(t, threeNodeDetail())
	msgs := make(chan tea.Msg, 16)

	var m tea.Model = New()
	m = updateWithin(t, m, tea.WindowSizeMsg{Width: 160, Height: 60}).m
	m, cmd := m.(App).openRun("r")
	app := m.(App)
	gen := app.runDetail.gen
	runCmd(cmd, msgs)
	waitLoads(t, loads, 1)

	s := updateWithin(t, app, runDetailTickMsg{runID: "r", gen: gen})
	app = s.m.(App)
	runCmd(s.cmd, msgs)
	app = updateWithin(t, app, key("down")).m.(App)
	if app.runDetail.offset != 1 {
		t.Errorf("down during the load: offset %d, want 1", app.runDetail.offset)
	}
	time.Sleep(20 * time.Millisecond)
	if n := atomic.LoadInt32(loads); n != 1 {
		t.Fatalf("a tick during the load started another: %d loads", n)
	}

	release <- struct{}{}
	app = updateWithin(t, app, detailMsg(t, msgs)).m.(App)
	if app.runDetail.refreshing || app.runDetail.detail == nil {
		t.Fatalf("after the load: refreshing %v detail %v", app.runDetail.refreshing, app.runDetail.detail != nil)
	}
	if !strings.Contains(app.View(), "loaded ") || strings.Contains(app.View(), "refreshing…") {
		t.Errorf("view after the load:\n%s", app.View())
	}

	s = updateWithin(t, app, runDetailTickMsg{runID: "r", gen: gen})
	app = s.m.(App)
	runCmd(s.cmd, msgs)
	waitLoads(t, loads, 2)
	if !app.runDetail.refreshing {
		t.Error("the tick after the load did not mark a refresh out")
	}
	// The last good body stays up while the refresh is out.
	if v := app.View(); !strings.Contains(v, "refreshing…") || !strings.Contains(v, "stage-1") {
		t.Errorf("view during the refresh:\n%s", v)
	}
	// `r` during the refresh is coalesced too.
	s = updateWithin(t, app, key("r"))
	app = s.m.(App)
	runCmd(s.cmd, msgs)
	time.Sleep(20 * time.Millisecond)
	if n := atomic.LoadInt32(loads); n != 2 {
		t.Fatalf("r during a refresh started another load: %d loads", n)
	}
	release <- struct{}{}
	app = updateWithin(t, app, detailMsg(t, msgs)).m.(App)
	if app.runDetail.refreshing {
		t.Error("refresh did not clear")
	}
}

// A tick for a run the user has left, or from an earlier opening of the
// one on screen, is dropped without re-arming; only the current opening's
// beat carries on.
func TestRunDetailStaleTicksAreDropped(t *testing.T) {
	stubDetailLoader(t, threeNodeDetail())
	m, _ := New().openRun("one")
	m, _ = m.(App).openRun("two")
	app := m.(App)
	gen := app.runDetail.gen
	for _, tick := range []runDetailTickMsg{{runID: "one", gen: gen - 1}, {runID: "two", gen: gen - 1}, {runID: "one", gen: gen}} {
		if _, cmd := app.Update(tick); cmd != nil {
			t.Errorf("tick %+v for another opening was re-armed", tick)
		}
	}
	if _, cmd := app.Update(runDetailTickMsg{runID: "two", gen: gen}); cmd == nil {
		t.Error("the current opening's tick was not re-armed")
	}
	m, _ = app.openRuns()
	if _, cmd := m.Update(runDetailTickMsg{runID: "two", gen: gen}); cmd != nil {
		t.Error("a tick after leaving the detail was re-armed")
	}
}

// A refresh that fails after a successful load keeps the last body up and
// marks it stale with the last success; the next success clears it. A
// reload keeps the cursors and scroll where they were.
func TestRunDetailFailedRefreshKeepsTheBody(t *testing.T) {
	stubDetailLoader(t, threeNodeDetail())
	var m tea.Model = New()
	m, _ = m.Update(tea.WindowSizeMsg{Width: 160, Height: 60})
	m, _ = m.(App).openRun("r")
	app := m.(App)
	gen := app.runDetail.gen
	loaded := func(d *runreport.Detail, err error) runDetailLoadedMsg {
		return runDetailLoadedMsg{runID: "r", gen: gen, detail: d, err: err, pipeline: loadPipelineState()}
	}

	app = updateWithin(t, app, loaded(threeNodeDetail(), nil)).m.(App)
	app = press(app, "j", "j", "down", "n")
	if app.runDetail.node != 2 || app.runDetail.offset != 1 {
		t.Fatalf("node %d offset %d before the reload", app.runDetail.node, app.runDetail.offset)
	}
	loadedAt := app.runDetail.loadedAt
	stamp := loadedAt.Local().Format("15:04:05")

	app = updateWithin(t, app, loaded(nil, errors.New("summaries.db is busy"))).m.(App)
	v := app.View()
	for _, want := range []string{"stale", "last successful load " + stamp, "summaries.db is busy", "stage-1", "lens-1", "HIERARCHY"} {
		if !strings.Contains(v, want) {
			t.Errorf("stale view lacks %q:\n%s", want, v)
		}
	}
	if !app.runDetail.loadedAt.Equal(loadedAt) {
		t.Error("a failed refresh moved the last successful load")
	}
	if app.runDetail.node != 2 || app.runDetail.offset != 1 {
		t.Errorf("failed refresh moved the cursor: node %d offset %d", app.runDetail.node, app.runDetail.offset)
	}

	app = updateWithin(t, app, loaded(threeNodeDetail(), nil)).m.(App)
	if v := app.View(); strings.Contains(v, "stale") || strings.Contains(v, "busy") {
		t.Errorf("a successful refresh left the stale mark:\n%s", v)
	}
	if app.runDetail.node != 2 || app.runDetail.offset != 1 || app.runDetail.lens != 0 {
		t.Errorf("reload moved the cursors: node %d offset %d lens %d", app.runDetail.node, app.runDetail.offset, app.runDetail.lens)
	}

	// A smaller tree clamps the cursor rather than leaving it past the end.
	small := threeNodeDetail()
	small.Report.Tree.Children = small.Report.Tree.Children[:1]
	app = updateWithin(t, app, loaded(small, nil)).m.(App)
	if app.runDetail.node != 1 {
		t.Errorf("node after a smaller reload = %d, want 1", app.runDetail.node)
	}
}

// The pipeline line reports the summarizer's sweep and the shipper's sync
// as recorded, each stale past its own threshold: a sweep or a sync older
// than the threshold is stale, no local shipper state and no sweep marker
// say so rather than reading as current, and a marker that cannot be read
// names the error rather than blanking the detail.
func TestRunDetailFreshnessLine(t *testing.T) {
	m := newRunDetailModel("r", 0, 160, 60)
	m.pipeline = pipelineState{sweepStaleAfter: time.Minute, shipStaleAfter: time.Minute}
	d := threeNodeDetail()
	d.SweptAt = time.Now().Add(-2 * time.Minute)
	m.setDetail(d, nil)
	v := stripANSI(m.view())
	if !strings.Contains(v, "stale") || !strings.Contains(v, "no local shipper state") {
		t.Errorf("stale sweep, no shipper state:\n%s", v)
	}
	d.SweptAt = time.Now()
	m.pipeline.shippedAt, m.pipeline.shippedKnown = time.Now().Add(-30*time.Second), true
	m.setDetail(d, nil)
	v = stripANSI(m.view())
	if strings.Contains(v, "stale") || strings.Contains(v, "no local shipper state") || !strings.Contains(v, "shipped "+m.pipeline.shippedAt.Local().Format("15:04:05")+" (30s ago)") {
		t.Errorf("fresh sweep, fresh shipper sync:\n%s", v)
	}
	m.pipeline.shippedAt = time.Now().Add(-time.Hour)
	m.setDetail(d, nil)
	v = stripANSI(m.view())
	if !strings.Contains(v, "shipped "+m.pipeline.shippedAt.Local().Format("15:04:05")+" (1h ago) stale") {
		t.Errorf("fresh sweep, stale shipper sync:\n%s", v)
	}
	d.SweptAt = time.Time{}
	m.setDetail(d, nil)
	if v := stripANSI(m.view()); !strings.Contains(v, "no sweep recorded") {
		t.Errorf("no sweep marker:\n%s", v)
	}
	d.SweepErr = errors.New("parse last sweep: bad marker")
	m.setDetail(d, nil)
	if v := stripANSI(m.view()); !strings.Contains(v, "swept —  parse last sweep: bad marker") || !strings.Contains(v, "HIERARCHY") {
		t.Errorf("unreadable sweep marker:\n%s", v)
	}
}

// The stale thresholds come from the configured cadences: twice each, so
// a marker due now is not yet stale, floored so a seconds cadence does not
// flicker; a config that cannot be read leaves the defaults.
func TestRunDetailStaleThresholdsFollowConfiguredCadences(t *testing.T) {
	stubDetailLoader(t, threeNodeDetail())
	prev := pipelineCadences
	t.Cleanup(func() { pipelineCadences = prev })

	pipelineCadences = func() (time.Duration, time.Duration, error) { return 30 * time.Second, 5 * time.Second, nil }
	got := loadPipelineState()
	if got.shipStaleAfter != time.Minute || got.sweepStaleAfter != minStaleAfter {
		t.Errorf("30s ship, 5s sweep: ship %s sweep %s; want 1m0s %s", got.shipStaleAfter, got.sweepStaleAfter, minStaleAfter)
	}

	pipelineCadences = func() (time.Duration, time.Duration, error) { return 0, 0, errors.New("parse config.json") }
	got = loadPipelineState()
	if got.shipStaleAfter != 20*time.Minute || got.sweepStaleAfter != time.Minute {
		t.Errorf("unreadable config: ship %s sweep %s; want 20m0s 1m0s", got.shipStaleAfter, got.sweepStaleAfter)
	}
}

// Tokens are what the report recorded and nothing else: two loads of the
// same report render the same totals, and a pending execution with no
// transcript metered reads pending with tokens unavailable, never a number.
func TestRunDetailTokensComeOnlyFromRecordedUsage(t *testing.T) {
	m := newRunDetailModel("r", 0, 160, 60)
	tokensRow := func() string {
		for _, l := range m.lines(m.contentWidth()).lines {
			if plain := stripANSI(l); strings.HasPrefix(plain, "Tokens") {
				return plain
			}
		}
		return ""
	}
	m.setDetail(threeNodeDetail(), nil)
	first := tokensRow()
	m.setDetail(threeNodeDetail(), nil)
	if second := tokensRow(); second != first || !strings.Contains(first, "1.5k") {
		t.Errorf("tokens row changed with no new usage: %q then %q", first, second)
	}
	var lensLine string
	for _, l := range m.lines(m.contentWidth()).lines {
		if plain := stripANSI(l); strings.Contains(plain, "lens security") {
			lensLine = plain
		}
	}
	if !strings.Contains(lensLine, "pending") || !strings.Contains(lensLine, unavailable+" tok") {
		t.Errorf("pending lens line = %q, want pending with tokens unavailable", lensLine)
	}
}
