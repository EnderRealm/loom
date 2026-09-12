package runs

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"loom/internal/parse/summary"
	"loom/internal/summaries"
)

// fixtureRun is the run docs/execution-records.md and testdata/executions.jsonl
// describe.
const fixtureRun = "0f4c3a6e-2d1b-4b7e-9c8a-5e2f1d0a9b31"

var base = time.Date(2026, 9, 10, 9, 0, 0, 0, time.UTC)

func openStore(t *testing.T, path string) *summaries.Store {
	t.Helper()
	st, err := summaries.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	return st
}

// importFile folds one record file the way the summarize sweep does: stat for
// currency, then ImportExecutions under the file's own path.
func importFile(t *testing.T, st *summaries.Store, path string) summaries.ImportCounts {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	counts, err := st.ImportExecutions(context.Background(), path, f, info.Size(), info.ModTime())
	if err != nil {
		t.Fatalf("import %s: %v", path, err)
	}
	return counts
}

func testdata(name string) string {
	return filepath.Join("testdata", name)
}

func child(t *testing.T, n *Node, id string) *Node {
	t.Helper()
	for _, c := range n.Children {
		if c.ExecutionID == id {
			return c
		}
	}
	t.Fatalf("%s has no child %s (children: %v)", n.ExecutionID, id, childIDs(n))
	return nil
}

func childIDs(n *Node) []string {
	var out []string
	for _, c := range n.Children {
		out = append(out, c.ExecutionID)
	}
	return out
}

func count(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func wantLine(t *testing.T, n *Node, line int) {
	t.Helper()
	if n.Source == nil || n.Source.Line != line {
		t.Errorf("%s: source = %+v, want line %d", n.ExecutionID, n.Source, line)
	}
	if n.Source != nil && !strings.HasSuffix(n.Source.Path, "executions.jsonl") {
		t.Errorf("%s: source path = %q, want the fixture", n.ExecutionID, n.Source.Path)
	}
}

func wantTranscript(t *testing.T, n *Node, agent, sessionID string) {
	t.Helper()
	if n.Transcript == nil || n.Transcript.Agent != agent || n.Transcript.SessionID != sessionID {
		t.Errorf("%s: transcript = %+v, want %s/%s", n.ExecutionID, n.Transcript, agent, sessionID)
	}
}

func intv(p *int) int {
	if p == nil {
		return -1
	}
	return *p
}

// AC1: the fixture spanning a parent, nested Claude and Codex children, a
// routed lens and Weft stage attempts comes back as one tree, every node
// pointing at the record line it was read from.
func TestFixtureBuildsOneHierarchy(t *testing.T) {
	st := openStore(t, filepath.Join(t.TempDir(), "summaries.db"))
	defer st.Close()
	counts := importFile(t, st, testdata("executions.jsonl"))
	if counts.Runs != 2 || counts.Executions != 8 || counts.Diagnostics != 0 {
		t.Fatalf("counts = %+v, want runs=2 executions=8 diagnostics=0", counts)
	}

	run, err := Load(st.DB(), fixtureRun)
	if err != nil {
		t.Fatal(err)
	}
	if run.Origin != OriginRecord || run.Ticket != "loom/persist-execution-identities-2149" || run.Runtime != "claude-code" {
		t.Errorf("run = origin %q ticket %q runtime %q", run.Origin, run.Ticket, run.Runtime)
	}
	if run.Transcript == nil || run.Transcript.SessionID != "195f819e-1e11-4e08-8c16-a340f512f892" {
		t.Errorf("run transcript = %+v", run.Transcript)
	}
	// Start from the first record, end and outcome from the terminal one;
	// the source points at whichever record last wrote the row.
	if run.StartedAt != "2026-09-10T17:02:11Z" || run.EndedAt != "2026-09-10T17:40:00Z" || run.Outcome != "completed" {
		t.Errorf("run span = %s → %s %s", run.StartedAt, run.EndedAt, run.Outcome)
	}
	if run.ReportingCutoff != "2026-09-10T17:40:00Z" || run.Producer != "warp/work@1.4.0" {
		t.Errorf("run cutoff = %q producer = %q", run.ReportingCutoff, run.Producer)
	}
	if run.Source == nil || run.Source.Line != 10 {
		t.Errorf("run source = %+v, want line 10", run.Source)
	}
	if len(run.Unresolved) != 0 || len(run.Diagnostics) != 0 {
		t.Errorf("unresolved = %v diagnostics = %v, want none", run.Unresolved, run.Diagnostics)
	}

	root := run.Root
	if root == nil || root.ExecutionID != "root-195f819e" || root.Kind != KindRoot {
		t.Fatalf("root = %+v", root)
	}
	wantLine(t, root, 2)
	wantTranscript(t, root, "claude-code", "195f819e-1e11-4e08-8c16-a340f512f892")
	want := []string{"agent-a0e0c89b977fd6273", "lens-security-r1-a1", "stage-work-1-1", "stage-work-1-2", "stage-review-1-1"}
	if got := childIDs(root); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("root children = %v, want %v (started_at order)", got, want)
	}

	sub := child(t, root, "agent-a0e0c89b977fd6273")
	wantLine(t, sub, 3)
	wantTranscript(t, sub, "claude-code", "agent-a0e0c89b977fd6273")
	if sub.Kind != "subagent" || sub.DispatchID != "toolu_015BC3bRz7V19vyVDAMXf5FX" || sub.Outcome != "completed" {
		t.Errorf("subagent = %+v", sub)
	}
	codex := child(t, sub, "codex-01a0029b")
	wantLine(t, codex, 4)
	wantTranscript(t, codex, "codex-cli", "01a0029b-b39f-7802-8b5f-56ffe644403b")
	if codex.ParentExecutionID != sub.ExecutionID || codex.DispatchID != "call_7Hq2mK" {
		t.Errorf("codex child = %+v", codex)
	}

	lens := child(t, root, "lens-security-r1-a1")
	wantLine(t, lens, 5)
	wantTranscript(t, lens, "codex-cli", "01a0029c-4d61-7f0e-a2b3-9c7d5e1f2a44")
	if lens.Kind != "lens" || lens.Lens != "security" || intv(lens.Round) != 1 || intv(lens.Attempt) != 1 {
		t.Errorf("lens = %+v", lens)
	}
	if lens.StageOccurrence != nil {
		t.Errorf("lens stage_occurrence = %d, want null", *lens.StageOccurrence)
	}

	work1 := child(t, root, "stage-work-1-1")
	wantLine(t, work1, 6)
	if work1.Kind != "stage" || work1.Stage != "work" || intv(work1.StageOccurrence) != 1 || intv(work1.Attempt) != 1 || work1.Outcome != "failed" {
		t.Errorf("work attempt 1 = %+v", work1)
	}
	work2 := child(t, root, "stage-work-1-2")
	wantLine(t, work2, 7)
	if intv(work2.Attempt) != 2 || work2.Outcome != "completed" {
		t.Errorf("work attempt 2 = %+v", work2)
	}
	cmd := child(t, work2, "cmd-go-test-1")
	wantLine(t, cmd, 9)
	if cmd.Kind != "command" || cmd.Transcript != nil || cmd.Outcome != "completed" {
		t.Errorf("command = %+v", cmd)
	}
	review := child(t, root, "stage-review-1-1")
	wantLine(t, review, 8)
	if review.Stage != "review" || review.Outcome != "stopped" {
		t.Errorf("review attempt = %+v", review)
	}
}

// AC2: two invocations in one session and two simultaneous runs of one ticket
// are distinct runs with their own children, and importing the file again
// after a close and reopen changes no row count.
func TestDistinctRunsSurviveReimport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "summaries.db")
	st := openStore(t, path)
	importFile(t, st, testdata("duplicates.jsonl"))

	runs, err := List(st.DB(), time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	wantChild := map[string]string{
		"run-same-session-a": "lens-a",
		"run-same-session-b": "lens-b",
		"run-parallel-c":     "sub-c",
		"run-parallel-d":     "sub-d",
	}
	if len(runs) != len(wantChild) {
		t.Fatalf("List returned %d runs, want %d", len(runs), len(wantChild))
	}
	for _, r := range runs {
		c, ok := wantChild[r.RunID]
		if !ok {
			t.Errorf("unexpected run %s", r.RunID)
			continue
		}
		if r.Root == nil || len(r.Root.Children) != 1 || r.Root.Children[0].ExecutionID != c {
			t.Errorf("%s: root = %+v, want one child %s", r.RunID, r.Root, c)
		}
	}
	// Same session, same ticket: two run ids, two roots on one transcript.
	if runs[0].Transcript == nil || runs[1].Transcript == nil || *runs[0].Transcript != *runs[1].Transcript {
		t.Errorf("same-session runs carry transcripts %+v and %+v, want equal", runs[0].Transcript, runs[1].Transcript)
	}

	before := [3]int{count(t, st.DB(), "runs"), count(t, st.DB(), "executions"), count(t, st.DB(), "execution_diagnostics")}
	if before != [3]int{4, 8, 0} {
		t.Fatalf("row counts = %v, want [4 8 0]", before)
	}
	st.Close()

	st = openStore(t, path)
	defer st.Close()
	importFile(t, st, testdata("duplicates.jsonl"))
	after := [3]int{count(t, st.DB(), "runs"), count(t, st.DB(), "executions"), count(t, st.DB(), "execution_diagnostics")}
	if after != before {
		t.Errorf("row counts after reimport = %v, want %v", after, before)
	}
}

// workInvocation is the prompt Claude's /work skill expands into — the shape
// internal/workreport recognizes a historical run from.
func workInvocation(ticket string) string {
	return "<command-message>work</command-message>\n<command-name>/work</command-name>\n<command-args>" + ticket + "</command-args>"
}

func writeSession(t *testing.T, st *summaries.Store, sum *summary.SessionSummary) {
	t.Helper()
	if err := st.WriteSummary(context.Background(), sum, summaries.SourceInfo{Project: "loom"}); err != nil {
		t.Fatal(err)
	}
}

func claudeRun(sessionID, ticket string, start time.Time) *summary.SessionSummary {
	return &summary.SessionSummary{
		SessionID: sessionID,
		Agent:     summary.AgentClaude,
		StartTime: start,
		EndTime:   start.Add(time.Hour),
		Turns: []summary.Turn{
			{Idx: 0, UserMessage: workInvocation(ticket), AssistantText: "on it", StartedAt: start},
			{Idx: 1, UserMessage: "carry on", AssistantText: "done", StartedAt: start.Add(20 * time.Minute)},
		},
	}
}

func codexSession(sessionID, parent string, start time.Time) *summary.SessionSummary {
	return &summary.SessionSummary{
		SessionID:       sessionID,
		Agent:           summary.AgentCodex,
		ParentSessionID: parent,
		SpawnDepth:      1,
		StartTime:       start,
		EndTime:         start.Add(10 * time.Minute),
		Turns:           []summary.Turn{{Idx: 0, UserMessage: "review", AssistantText: "fine", StartedAt: start}},
	}
}

func runByID(t *testing.T, runs []Run, id string) *Run {
	t.Helper()
	for i := range runs {
		if runs[i].RunID == id {
			return &runs[i]
		}
	}
	t.Fatalf("no run %s in %d runs", id, len(runs))
	return nil
}

func hasSession(n *Node, sessionID string) bool {
	if n == nil {
		return false
	}
	if n.Transcript != nil && n.Transcript.SessionID == sessionID {
		return true
	}
	for _, c := range n.Children {
		if hasSession(c, sessionID) {
			return true
		}
	}
	return false
}

// AC3: uninstrumented transcripts still yield runs, with children only where
// the transcripts themselves say so. A Codex child whose parent session holds
// two runs is unresolved, and a Codex session in the same project at the
// same time with no parent evidence is attached nowhere.
func TestHistoricalRunsFromTranscripts(t *testing.T) {
	st := openStore(t, filepath.Join(t.TempDir(), "summaries.db"))
	defer st.Close()

	single := claudeRun("hist-single", "loom/hist-0001", base)
	single.Subagents = []summary.Subagent{
		{ParentTurnIdx: 0, AgentType: "reviewer"},
		{ParentTurnIdx: 1, AgentType: "security"},
	}
	writeSession(t, st, single)

	double := claudeRun("hist-double", "loom/hist-0002", base.Add(2*time.Hour))
	double.Turns = append(double.Turns,
		summary.Turn{Idx: 2, UserMessage: workInvocation("loom/hist-0003"), AssistantText: "again", StartedAt: base.Add(150 * time.Minute)},
	)
	// Dispatched by the second invocation: it belongs to that run, not the
	// first.
	double.Subagents = []summary.Subagent{{ParentTurnIdx: 2, AgentType: "reviewer"}}
	writeSession(t, st, double)

	writeSession(t, st, codexSession("codex-of-single", "hist-single", base.Add(5*time.Minute)))
	writeSession(t, st, codexSession("codex-of-double", "hist-double", base.Add(125*time.Minute)))
	writeSession(t, st, codexSession("codex-stray", "", base.Add(6*time.Minute)))

	// A session a run record claims is not re-recognized from its transcript.
	recorded := claudeRun("hist-recorded", "loom/hist-0004", base.Add(4*time.Hour))
	writeSession(t, st, recorded)
	dir := t.TempDir()
	path := filepath.Join(dir, "executions.jsonl")
	if err := os.WriteFile(path, []byte(`{"v":1,"kind":"run","run_id":"run-recorded","ticket":"loom/hist-0004","runtime":"claude-code","agent":"claude-code","session_id":"hist-recorded","started_at":"2026-09-10T13:00:00Z"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	importFile(t, st, path)

	runs, err := List(st.DB(), time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range runs {
		ids = append(ids, r.RunID)
	}
	want := []string{
		"transcript:claude-code:hist-single:0",
		"transcript:claude-code:hist-double:0",
		"transcript:claude-code:hist-double:2",
		"run-recorded",
	}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("runs = %v, want %v", ids, want)
	}

	one := runByID(t, runs, "transcript:claude-code:hist-single:0")
	if one.Origin != OriginTranscript || one.Ticket != "loom/hist-0001" || one.Runtime != "claude-code" || one.Source != nil {
		t.Errorf("historical run = %+v", one)
	}
	if one.StartedAt != base.Format(time.RFC3339Nano) || one.EndedAt != base.Add(time.Hour).Format(time.RFC3339Nano) {
		t.Errorf("historical span = %s → %s", one.StartedAt, one.EndedAt)
	}
	if one.Root == nil || one.Root.Kind != KindRoot || one.Root.Source != nil || one.Root.ExecutionID != one.RunID {
		t.Fatalf("historical root = %+v", one.Root)
	}
	wantTranscript(t, one.Root, "claude-code", "hist-single")
	got := childIDs(one.Root)
	wantChildren := []string{
		"transcript:claude-code:hist-single:0:subagent:0",
		"transcript:claude-code:hist-single:0:subagent:1",
		"transcript:codex-cli:codex-of-single",
	}
	if strings.Join(got, ",") != strings.Join(wantChildren, ",") {
		t.Errorf("children = %v, want %v", got, wantChildren)
	}
	codex := child(t, one.Root, "transcript:codex-cli:codex-of-single")
	wantTranscript(t, codex, "codex-cli", "codex-of-single")
	if codex.Kind != KindSubagent || codex.StartedAt == "" {
		t.Errorf("codex child = %+v", codex)
	}
	if len(one.Unresolved) != 0 || len(one.Diagnostics) != 0 {
		t.Errorf("unresolved = %v diagnostics = %v, want none", one.Unresolved, one.Diagnostics)
	}

	first := runByID(t, runs, "transcript:claude-code:hist-double:0")
	second := runByID(t, runs, "transcript:claude-code:hist-double:2")
	if len(first.Root.Children) != 0 {
		t.Errorf("first run of the double session has children %v, want none", childIDs(first.Root))
	}
	if got := childIDs(second.Root); strings.Join(got, ",") != "transcript:claude-code:hist-double:2:subagent:0" {
		t.Errorf("second run's children = %v, want the subagent dispatched at turn 2", got)
	}
	for _, r := range []*Run{first, second} {
		if len(r.Unresolved) != 1 || r.Unresolved[0].ExecutionID != "transcript:codex-cli:codex-of-double" {
			t.Errorf("%s: unresolved = %v, want the ambiguous codex child", r.RunID, r.Unresolved)
		}
		if len(r.Diagnostics) != 1 || r.Diagnostics[0].Code != DiagAmbiguousParent {
			t.Errorf("%s: diagnostics = %+v, want one %s", r.RunID, r.Diagnostics, DiagAmbiguousParent)
		}
		if hasSession(r.Root, "codex-of-double") {
			t.Errorf("%s: ambiguous codex child was attached to the tree", r.RunID)
		}
	}

	for _, r := range runs {
		if hasSession(r.Root, "codex-stray") {
			t.Errorf("%s: stray codex session attached with no parent evidence", r.RunID)
		}
		for _, u := range r.Unresolved {
			if hasSession(u, "codex-stray") {
				t.Errorf("%s: stray codex session listed as unresolved", r.RunID)
			}
		}
	}

	rec := runByID(t, runs, "run-recorded")
	if rec.Origin != OriginRecord {
		t.Errorf("recorded session's run has origin %q", rec.Origin)
	}

	// Load resolves a synthesized id too.
	loaded, err := Load(st.DB(), one.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Ticket != one.Ticket || len(loaded.Root.Children) != len(one.Root.Children) {
		t.Errorf("Load(%s) = %+v, want the listed run", one.RunID, loaded)
	}
}

// AC4: start-only records, then a terminal run record and late children, land
// on the same run id; failed and stopped attempts stay in the tree.
func TestLateRecordsReconcile(t *testing.T) {
	st := openStore(t, filepath.Join(t.TempDir(), "summaries.db"))
	defer st.Close()

	start, err := os.ReadFile(testdata("late_start.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "executions.jsonl")
	if err := os.WriteFile(path, start, 0o644); err != nil {
		t.Fatal(err)
	}
	importFile(t, st, path)

	run, err := Load(st.DB(), "run-late")
	if err != nil {
		t.Fatal(err)
	}
	if run.EndedAt != "" || run.Outcome != "" || run.Root == nil || len(run.Root.Children) != 0 {
		t.Fatalf("in-progress run = %+v root = %+v", run, run.Root)
	}

	terminal, err := os.ReadFile(testdata("late_terminal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(start, terminal...), 0o644); err != nil {
		t.Fatal(err)
	}
	importFile(t, st, path)

	run, err = Load(st.DB(), "run-late")
	if err != nil {
		t.Fatal(err)
	}
	if run.EndedAt != "2026-09-10T14:30:00Z" || run.Outcome != "completed" {
		t.Errorf("reconciled run = %s %s", run.EndedAt, run.Outcome)
	}
	if run.StartedAt != "2026-09-10T14:00:00Z" || run.Ticket != "loom/late-3333" {
		t.Errorf("terminal record erased earlier fields: %+v", run)
	}
	if run.Source == nil || run.Source.Line != 6 {
		t.Errorf("run source = %+v, want the terminal record at line 6", run.Source)
	}
	root := run.Root
	if root.EndedAt != "2026-09-10T14:30:00Z" || root.Outcome != "completed" {
		t.Errorf("root not reconciled: %+v", root)
	}
	failed := child(t, root, "stage-late-work-1-1")
	stopped := child(t, root, "stage-late-work-1-2")
	if failed.Outcome != "failed" || intv(failed.Attempt) != 1 {
		t.Errorf("failed attempt = %+v", failed)
	}
	if stopped.Outcome != "stopped" || intv(stopped.Attempt) != 2 {
		t.Errorf("stopped attempt = %+v", stopped)
	}
	if n := count(t, st.DB(), "runs"); n != 1 {
		t.Errorf("runs rows = %d, want 1", n)
	}
	if n := count(t, st.DB(), "executions"); n != 3 {
		t.Errorf("executions rows = %d, want 3", n)
	}
	if n := count(t, st.DB(), "execution_diagnostics"); n != 0 {
		t.Errorf("diagnostics rows = %d, want 0", n)
	}
}

var jsonFence = regexp.MustCompile("(?s)```json\n(.*?)```")

// AC5: the contract's example block is the fixture. Every ```json block in
// the doc, concatenated, must equal testdata/executions.jsonl and import to
// a run with children.
func TestDocExamplesAreTheFixture(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "execution-records.md"))
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, m := range jsonFence.FindAllStringSubmatch(string(doc), -1) {
		for _, l := range strings.Split(strings.TrimSpace(m[1]), "\n") {
			lines = append(lines, strings.TrimSpace(l))
		}
	}
	if len(lines) == 0 {
		t.Fatal("no ```json blocks in docs/execution-records.md")
	}
	fixture, err := os.ReadFile(testdata("executions.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(lines, "\n"), strings.TrimSpace(string(fixture)); got != want {
		t.Fatalf("doc examples differ from testdata/executions.jsonl:\n%s\n---\n%s", got, want)
	}

	path := filepath.Join(t.TempDir(), "executions.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st := openStore(t, filepath.Join(t.TempDir(), "summaries.db"))
	defer st.Close()
	counts := importFile(t, st, path)
	if counts.Diagnostics != 0 {
		t.Errorf("doc examples produced %d diagnostics", counts.Diagnostics)
	}
	run, err := Load(st.DB(), fixtureRun)
	if err != nil {
		t.Fatal(err)
	}
	if run.Root == nil || len(run.Root.Children) == 0 {
		t.Fatalf("doc examples yield root %+v, want children", run.Root)
	}
}

// AC5: every unsupported record yields exactly its diagnostic code, and no
// diagnostic carries a field value beyond ids and kinds.
func TestDiagnosticsNameRejectedRecords(t *testing.T) {
	st := openStore(t, filepath.Join(t.TempDir(), "summaries.db"))
	defer st.Close()
	counts := importFile(t, st, testdata("diagnostics.jsonl"))
	if counts.Runs != 2 || counts.Executions != 5 {
		t.Errorf("counts = %+v, want runs=2 executions=5", counts)
	}

	rows, err := st.DB().Query(`SELECT source_line, code, detail, run_id, execution_id FROM execution_diagnostics ORDER BY source_line, code`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[int]string{}
	var details []string
	for rows.Next() {
		var (
			line                  int
			code                  string
			detail, runID, execID sql.NullString
		)
		if err := rows.Scan(&line, &code, &detail, &runID, &execID); err != nil {
			t.Fatal(err)
		}
		if prev, dup := got[line]; dup {
			t.Errorf("line %d has two diagnostics: %s and %s", line, prev, code)
		}
		got[line] = code
		details = append(details, detail.String+" "+runID.String+" "+execID.String)
	}
	want := map[int]string{
		1:  summaries.DiagUnsupportedVersion,
		2:  summaries.DiagUnsupportedKind,
		3:  summaries.DiagMissingID,
		4:  summaries.DiagInvalidValue,
		5:  summaries.DiagInvalidValue,
		6:  summaries.DiagMalformedJSON,
		7:  summaries.DiagUnresolvedRun,
		10: summaries.DiagUnresolvedParent,
		13: summaries.DiagUnresolvedParent,
	}
	for line, code := range want {
		if got[line] != code {
			t.Errorf("line %d: code = %q, want %q", line, got[line], code)
		}
	}
	for line, code := range got {
		if _, ok := want[line]; !ok {
			t.Errorf("line %d: unexpected diagnostic %q", line, code)
		}
	}
	if counts.Diagnostics != len(want) {
		t.Errorf("counts.Diagnostics = %d, want %d", counts.Diagnostics, len(want))
	}
	// Values from the rejected records — the ticket, the bad outcome, the bad
	// timestamp — never reach a diagnostic; ids, kinds and field names do.
	for _, d := range details {
		for _, leaked := range []string{"loom/future-0001", "exploded", "yesterday", "loom/bad-000", "claude-code", "sess-"} {
			if strings.Contains(d, leaked) {
				t.Errorf("diagnostic %q carries a field value %q", d, leaked)
			}
		}
	}

	// The execution whose run nobody declared is still queryable.
	orphan, err := Load(st.DB(), "run-undeclared")
	if err != nil {
		t.Fatal(err)
	}
	if orphan.Source != nil || orphan.Root == nil || orphan.Root.ExecutionID != "exec-orphan" {
		t.Errorf("undeclared run = %+v root = %+v", orphan, orphan.Root)
	}
	if len(orphan.Diagnostics) != 1 || orphan.Diagnostics[0].Code != summaries.DiagUnresolvedRun ||
		orphan.Diagnostics[0].Source == nil || orphan.Diagnostics[0].Source.Line != 7 {
		t.Errorf("undeclared run diagnostics = %+v", orphan.Diagnostics)
	}

	// The execution whose parent nobody declared is unresolved, not on the root.
	ok, err := Load(st.DB(), "run-ok")
	if err != nil {
		t.Fatal(err)
	}
	if ok.Root == nil || len(ok.Root.Children) != 0 {
		t.Errorf("run-ok root = %+v, want no children", ok.Root)
	}
	if len(ok.Unresolved) != 1 || ok.Unresolved[0].ExecutionID != "exec-ok-stray" {
		t.Errorf("run-ok unresolved = %v", ok.Unresolved)
	}
	if len(ok.Diagnostics) != 1 || ok.Diagnostics[0].Code != summaries.DiagUnresolvedParent || ok.Diagnostics[0].ExecutionID != "exec-ok-stray" {
		t.Errorf("run-ok diagnostics = %+v", ok.Diagnostics)
	}

	// A parent declared under another run does not resolve the child: the
	// loader reads one run's executions, so the edge would be unreachable.
	other, err := Load(st.DB(), "run-other")
	if err != nil {
		t.Fatal(err)
	}
	if other.Root == nil || other.Root.ExecutionID != "exec-other-root" || len(other.Root.Children) != 0 {
		t.Errorf("run-other root = %+v, want exec-other-root with no children", other.Root)
	}
	if len(other.Unresolved) != 1 || other.Unresolved[0].ExecutionID != "exec-other-crossed" {
		t.Errorf("run-other unresolved = %v", other.Unresolved)
	}
	if len(other.Diagnostics) != 1 || other.Diagnostics[0].Code != summaries.DiagUnresolvedParent ||
		other.Diagnostics[0].ExecutionID != "exec-other-crossed" || other.Diagnostics[0].Source == nil || other.Diagnostics[0].Source.Line != 13 {
		t.Errorf("run-other diagnostics = %+v", other.Diagnostics)
	}

	// Declaring the parent later clears the diagnostic and attaches the node.
	path := filepath.Join(t.TempDir(), "executions.jsonl")
	if err := os.WriteFile(path, []byte(`{"v":1,"kind":"execution","execution_id":"exec-never-declared","run_id":"run-ok","parent_execution_id":"exec-ok-root","execution_kind":"subagent"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	importFile(t, st, path)
	ok, err = Load(st.DB(), "run-ok")
	if err != nil {
		t.Fatal(err)
	}
	if len(ok.Unresolved) != 0 || len(ok.Diagnostics) != 0 {
		t.Errorf("after declaring the parent: unresolved = %v diagnostics = %+v", ok.Unresolved, ok.Diagnostics)
	}
	if len(ok.Root.Children) != 1 || len(ok.Root.Children[0].Children) != 1 || ok.Root.Children[0].Children[0].ExecutionID != "exec-ok-stray" {
		t.Errorf("after declaring the parent: root = %+v", ok.Root)
	}
}

// A line over the importer's cap is skipped with a malformed_json diagnostic;
// the records on either side of it still import. The registry is append-only,
// so a fatal line would fail every later sweep the same way.
func TestOversizedLineIsSkipped(t *testing.T) {
	st := openStore(t, filepath.Join(t.TempDir(), "summaries.db"))
	defer st.Close()

	var b strings.Builder
	b.WriteString(`{"v":1,"kind":"run","run_id":"run-big","ticket":"loom/big-0006","runtime":"claude-code","started_at":"2026-09-10T12:00:00Z"}` + "\n")
	b.WriteString(`{"v":1,"kind":"execution","execution_id":"exec-big-pad","run_id":"run-big","execution_kind":"root","agent":"`)
	b.WriteString(strings.Repeat("x", 1024*1024+1))
	b.WriteString(`"}` + "\n")
	b.WriteString(`{"v":1,"kind":"execution","execution_id":"exec-big-root","run_id":"run-big","execution_kind":"root","started_at":"2026-09-10T12:00:00Z"}` + "\n")
	path := filepath.Join(t.TempDir(), "executions.jsonl")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	counts := importFile(t, st, path)
	if counts.Runs != 1 || counts.Executions != 1 || counts.Diagnostics != 1 {
		t.Errorf("counts = %+v, want runs=1 executions=1 diagnostics=1", counts)
	}

	var line int
	var code string
	if err := st.DB().QueryRow(`SELECT source_line, code FROM execution_diagnostics`).Scan(&line, &code); err != nil {
		t.Fatal(err)
	}
	if line != 2 || code != summaries.DiagMalformedJSON {
		t.Errorf("diagnostic = line %d %s, want line 2 %s", line, code, summaries.DiagMalformedJSON)
	}
	run, err := Load(st.DB(), "run-big")
	if err != nil {
		t.Fatal(err)
	}
	if run.Ticket != "loom/big-0006" || run.Root == nil || run.Root.ExecutionID != "exec-big-root" {
		t.Errorf("run = %+v root = %+v", run, run.Root)
	}
}

// A second root, or a parentless node of another kind, is unresolved with an
// unresolved_root diagnostic alongside the importer's, never guessed onto
// the root.
func TestSecondRootIsUnresolved(t *testing.T) {
	st := openStore(t, filepath.Join(t.TempDir(), "summaries.db"))
	defer st.Close()

	records := strings.Join([]string{
		`{"v":1,"kind":"run","run_id":"run-two-roots","ticket":"loom/roots-0007","runtime":"claude-code","started_at":"2026-09-10T12:00:00Z"}`,
		`{"v":1,"kind":"execution","execution_id":"root-first","run_id":"run-two-roots","execution_kind":"root","started_at":"2026-09-10T12:00:00Z"}`,
		`{"v":1,"kind":"execution","execution_id":"root-second","run_id":"run-two-roots","execution_kind":"root","started_at":"2026-09-10T12:01:00Z"}`,
		`{"v":1,"kind":"execution","execution_id":"sub-of-second","run_id":"run-two-roots","parent_execution_id":"root-second","execution_kind":"subagent","started_at":"2026-09-10T12:02:00Z"}`,
		`{"v":1,"kind":"execution","execution_id":"sub-parentless","run_id":"run-two-roots","execution_kind":"subagent","started_at":"2026-09-10T12:03:00Z"}`,
	}, "\n") + "\n"
	path := filepath.Join(t.TempDir(), "executions.jsonl")
	if err := os.WriteFile(path, []byte(records), 0o644); err != nil {
		t.Fatal(err)
	}
	counts := importFile(t, st, path)
	if counts.Diagnostics != 0 {
		t.Errorf("importer wrote %d diagnostics, want none: the tree is the loader's call", counts.Diagnostics)
	}

	run, err := Load(st.DB(), "run-two-roots")
	if err != nil {
		t.Fatal(err)
	}
	if run.Root == nil || run.Root.ExecutionID != "root-first" || len(run.Root.Children) != 0 {
		t.Fatalf("root = %+v, want root-first with no children", run.Root)
	}
	var unresolved []string
	for _, n := range run.Unresolved {
		unresolved = append(unresolved, n.ExecutionID)
	}
	if strings.Join(unresolved, ",") != "root-second,sub-parentless" {
		t.Errorf("unresolved = %v, want root-second,sub-parentless", unresolved)
	}
	if len(run.Unresolved) > 0 && (len(run.Unresolved[0].Children) != 1 || run.Unresolved[0].Children[0].ExecutionID != "sub-of-second") {
		t.Errorf("second root's children = %v, want sub-of-second under it", childIDs(run.Unresolved[0]))
	}
	if len(run.Diagnostics) != 2 {
		t.Fatalf("diagnostics = %+v, want two %s", run.Diagnostics, DiagUnresolvedRoot)
	}
	for i, want := range []struct{ id, kind string }{{"root-second", "root"}, {"sub-parentless", "subagent"}} {
		d := run.Diagnostics[i]
		if d.Code != DiagUnresolvedRoot || d.ExecutionID != want.id || d.RunID != "run-two-roots" || d.Detail != "execution_kind="+want.kind {
			t.Errorf("diagnostic %d = %+v, want %s for %s", i, d, DiagUnresolvedRoot, want.id)
		}
		if d.Source == nil || d.Source.Line != map[string]int{"root-second": 3, "sub-parentless": 5}[want.id] {
			t.Errorf("diagnostic %d source = %+v", i, d.Source)
		}
	}
	if _, err := json.Marshal(run); err != nil {
		t.Errorf("marshal run: %v", err)
	}
}

// An execution naming itself as parent is rejected at import. A longer cycle
// passes the importer — every parent exists — and the loader breaks it:
// each member is unresolved as its own top with a cyclic_parent diagnostic,
// keeping its non-cyclic descendants, and the run still serializes.
func TestCyclicParentsAreUnresolved(t *testing.T) {
	st := openStore(t, filepath.Join(t.TempDir(), "summaries.db"))
	defer st.Close()

	records := strings.Join([]string{
		`{"v":1,"kind":"run","run_id":"run-cycle","ticket":"loom/cycle-0010","runtime":"claude-code","started_at":"2026-09-10T12:00:00Z"}`,
		`{"v":1,"kind":"execution","execution_id":"root-cycle","run_id":"run-cycle","execution_kind":"root","started_at":"2026-09-10T12:00:00Z"}`,
		`{"v":1,"kind":"execution","execution_id":"self-parent","run_id":"run-cycle","parent_execution_id":"self-parent","execution_kind":"subagent","started_at":"2026-09-10T12:01:00Z"}`,
		`{"v":1,"kind":"execution","execution_id":"cycle-a","run_id":"run-cycle","parent_execution_id":"cycle-b","execution_kind":"subagent","started_at":"2026-09-10T12:02:00Z"}`,
		`{"v":1,"kind":"execution","execution_id":"cycle-b","run_id":"run-cycle","parent_execution_id":"cycle-a","execution_kind":"subagent","started_at":"2026-09-10T12:03:00Z"}`,
		`{"v":1,"kind":"execution","execution_id":"cycle-c","run_id":"run-cycle","parent_execution_id":"cycle-a","execution_kind":"command","started_at":"2026-09-10T12:04:00Z"}`,
	}, "\n") + "\n"
	path := filepath.Join(t.TempDir(), "executions.jsonl")
	if err := os.WriteFile(path, []byte(records), 0o644); err != nil {
		t.Fatal(err)
	}
	counts := importFile(t, st, path)
	if counts.Executions != 4 || counts.Diagnostics != 1 {
		t.Errorf("counts = %+v, want executions=4 diagnostics=1", counts)
	}
	if n := count(t, st.DB(), "executions"); n != 4 {
		t.Errorf("executions rows = %d, want 4: the self-parenting record is not stored", n)
	}
	var line int
	var code, detail string
	if err := st.DB().QueryRow(`SELECT source_line, code, detail FROM execution_diagnostics`).Scan(&line, &code, &detail); err != nil {
		t.Fatal(err)
	}
	if line != 3 || code != summaries.DiagInvalidValue || detail != "kind=execution field=parent_execution_id" {
		t.Errorf("importer diagnostic = line %d %s %q, want line 3 %s on parent_execution_id", line, code, detail, summaries.DiagInvalidValue)
	}

	run, err := Load(st.DB(), "run-cycle")
	if err != nil {
		t.Fatal(err)
	}
	if run.Root == nil || run.Root.ExecutionID != "root-cycle" || len(run.Root.Children) != 0 {
		t.Fatalf("root = %+v, want root-cycle with no children", run.Root)
	}
	var unresolved []string
	for _, n := range run.Unresolved {
		unresolved = append(unresolved, n.ExecutionID)
	}
	if strings.Join(unresolved, ",") != "cycle-a,cycle-b" {
		t.Fatalf("unresolved = %v, want cycle-a,cycle-b", unresolved)
	}
	if got := childIDs(run.Unresolved[0]); strings.Join(got, ",") != "cycle-c" {
		t.Errorf("cycle-a children = %v, want cycle-c alone", got)
	}
	if got := childIDs(run.Unresolved[1]); len(got) != 0 {
		t.Errorf("cycle-b children = %v, want none", got)
	}
	// The importer's diagnostic comes first, then the loader's two.
	if len(run.Diagnostics) != 3 || run.Diagnostics[0].Code != summaries.DiagInvalidValue {
		t.Fatalf("diagnostics = %+v, want %s then two %s", run.Diagnostics, summaries.DiagInvalidValue, DiagCyclicParent)
	}
	for i, want := range []struct {
		id, parent string
		line       int
	}{{"cycle-a", "cycle-b", 4}, {"cycle-b", "cycle-a", 5}} {
		d := run.Diagnostics[i+1]
		if d.Code != DiagCyclicParent || d.ExecutionID != want.id || d.RunID != "run-cycle" || d.Detail != "parent_execution_id="+want.parent {
			t.Errorf("diagnostic %d = %+v, want %s for %s", i, d, DiagCyclicParent, want.id)
		}
		if d.Source == nil || d.Source.Line != want.line {
			t.Errorf("diagnostic %d source = %+v, want line %d", i, d.Source, want.line)
		}
	}
	if _, err := json.Marshal(run); err != nil {
		t.Errorf("marshal run: %v", err)
	}
}

// List bounds on the run's start: a bounded range excludes runs outside it.
func TestListRange(t *testing.T) {
	st := openStore(t, filepath.Join(t.TempDir(), "summaries.db"))
	defer st.Close()
	importFile(t, st, testdata("duplicates.jsonl"))

	runs, err := List(st.DB(), time.Date(2026, 9, 10, 9, 30, 0, 0, time.UTC), time.Date(2026, 9, 10, 11, 1, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range runs {
		ids = append(ids, r.RunID)
	}
	if strings.Join(ids, ",") != "run-same-session-b,run-parallel-c" {
		t.Errorf("runs in range = %v", ids)
	}
}

// An execution_id belongs to the run that first declared it: a record naming
// it under another run is rejected with identity_conflict and the original
// keeps its run and fields, whether the record arrives in the same import,
// after a restart, or on a full replay.
func TestExecutionRunIDIsImmutable(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "summaries.db")
	st := openStore(t, dbPath)

	runA := strings.Join([]string{
		`{"v":1,"kind":"run","run_id":"run-a","ticket":"loom/ident-0008","runtime":"claude-code","started_at":"2026-09-10T12:00:00Z"}`,
		`{"v":1,"kind":"execution","execution_id":"root-a","run_id":"run-a","execution_kind":"root","started_at":"2026-09-10T12:00:00Z"}`,
		`{"v":1,"kind":"execution","execution_id":"exec-x","run_id":"run-a","parent_execution_id":"root-a","execution_kind":"subagent","dispatch_id":"disp-a","started_at":"2026-09-10T12:01:00Z","ended_at":"2026-09-10T12:02:00Z","outcome":"completed"}`,
	}, "\n") + "\n"
	runB := strings.Join([]string{
		`{"v":1,"kind":"run","run_id":"run-b","ticket":"loom/ident-0009","runtime":"claude-code","started_at":"2026-09-10T13:00:00Z"}`,
		`{"v":1,"kind":"execution","execution_id":"root-b","run_id":"run-b","execution_kind":"root","started_at":"2026-09-10T13:00:00Z"}`,
		`{"v":1,"kind":"execution","execution_id":"exec-x","run_id":"run-b","parent_execution_id":"root-b","execution_kind":"lens","dispatch_id":"disp-b","started_at":"2026-09-10T13:01:00Z","ended_at":"2026-09-10T13:02:00Z","outcome":"failed"}`,
	}, "\n") + "\n"
	path := filepath.Join(t.TempDir(), "executions.jsonl")

	check := func(phase string) {
		t.Helper()
		a, err := Load(st.DB(), "run-a")
		if err != nil {
			t.Fatal(err)
		}
		if a.Root == nil || a.Root.ExecutionID != "root-a" || len(a.Root.Children) != 1 {
			t.Fatalf("%s: run-a root = %+v, want root-a with one child", phase, a.Root)
		}
		x := a.Root.Children[0]
		if x.ExecutionID != "exec-x" || x.Kind != "subagent" || x.DispatchID != "disp-a" || x.Outcome != "completed" ||
			x.ParentExecutionID != "root-a" || x.StartedAt != "2026-09-10T12:01:00Z" {
			t.Errorf("%s: exec-x = %+v, want run-a's original fields", phase, x)
		}
		wantLine(t, x, 3)
		if len(a.Unresolved) != 0 || len(a.Diagnostics) != 0 {
			t.Errorf("%s: run-a unresolved = %v diagnostics = %+v, want none", phase, a.Unresolved, a.Diagnostics)
		}

		b, err := Load(st.DB(), "run-b")
		if err != nil {
			t.Fatal(err)
		}
		if b.Root == nil || b.Root.ExecutionID != "root-b" || len(b.Root.Children) != 0 || len(b.Unresolved) != 0 {
			t.Errorf("%s: run-b root = %+v unresolved = %v, want root-b alone", phase, b.Root, b.Unresolved)
		}
		if len(b.Diagnostics) != 1 {
			t.Fatalf("%s: run-b diagnostics = %+v, want one %s", phase, b.Diagnostics, summaries.DiagIdentityConflict)
		}
		d := b.Diagnostics[0]
		if d.Code != summaries.DiagIdentityConflict || d.RunID != "run-b" || d.ExecutionID != "exec-x" || d.Detail != "existing_run_id=run-a" {
			t.Errorf("%s: diagnostic = %+v", phase, d)
		}
		if d.Source == nil || d.Source.Line != 6 {
			t.Errorf("%s: diagnostic source = %+v, want line 6", phase, d.Source)
		}
		if n := count(t, st.DB(), "executions"); n != 3 {
			t.Errorf("%s: executions rows = %d, want 3", phase, n)
		}
		if n := count(t, st.DB(), "execution_diagnostics"); n != 1 {
			t.Errorf("%s: diagnostics rows = %d, want 1", phase, n)
		}
	}

	if err := os.WriteFile(path, []byte(runA+runB), 0o644); err != nil {
		t.Fatal(err)
	}
	if counts := importFile(t, st, path); counts.Executions != 3 || counts.Diagnostics != 1 {
		t.Errorf("counts = %+v, want executions=3 diagnostics=1", counts)
	}
	check("same import")

	// Restart: a fresh store holding run-a alone, closed and reopened before
	// run-b's records are appended and imported.
	st.Close()
	dbPath = filepath.Join(t.TempDir(), "summaries.db")
	st = openStore(t, dbPath)
	if err := os.WriteFile(path, []byte(runA), 0o644); err != nil {
		t.Fatal(err)
	}
	importFile(t, st, path)
	st.Close()
	st = openStore(t, dbPath)
	defer st.Close()
	if err := os.WriteFile(path, []byte(runA+runB), 0o644); err != nil {
		t.Fatal(err)
	}
	importFile(t, st, path)
	check("after restart")

	// Full replay of the same file: still one diagnostic, no new rows.
	importFile(t, st, path)
	check("after replay")
}
