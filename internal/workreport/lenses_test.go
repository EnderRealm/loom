package workreport

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom/internal/parse/claudeparse"
	"loom/internal/parse/codexparse"
	"loom/internal/parse/lens"
	"loom/internal/parse/summary"
	"loom/internal/summaries"
)

// lensFixture is the Claude transcript claudeparse's lens tests read: one
// /work run over two rounds with a routed security lens, a late quality
// verdict, a cut contract verdict retried, a failed quality dispatch, a
// security lens committed to but never sent, a coder dispatch described by
// the findings it addresses, a compaction summary quoting a whole contract
// verdict, a pasted note quoting a task notification with no dispatch, and
// a compaction summary opening with a quoted notification that names one of
// the run's dispatches.
// codexLensFixture is its Codex counterpart: a routed security lens and two
// inlined passes. codexReadbackFixture is eight Codex runs routing the
// security lens with its output redirected to a file: one never reads it
// back, one reads a README quoting a verdict before it reads the file, one
// gets a verdict with an unreadable findings field, one reads the README
// and the file in a single cat, one cats the README in the router command
// itself before reading the file alone, one reads the file back through
// `rg --passthru` rather than cat, one chains a cat of the README
// redirected to a notes file after the router before reading that file,
// and one runs two rounds appending the second verdict to the first's file
// with `>>` before reading it back. codexFileWriteFixture is a Codex run
// over two rounds whose contract and quality passes were written to files
// through apply_patch, the routed security lens after the writes in round 1
// and before them in round 2. handbackFixture is a Claude /work run of three
// rounds in one turn — no human message follows the invocation — whose
// contract and quality lenses are async subagents answering through
// hand-backs — round 2's two dispatched in one assistant message, round 3's
// contract answering on a queued_command attachment — and whose security
// lens is routed; a hand-back from an agent the session never launched lands
// in round 3.
const (
	handbackFixture       = "../parse/claudeparse/testdata/lens_handback.jsonl"
	lensFixture           = "../parse/claudeparse/testdata/lens_responses.jsonl"
	codexLensFixture      = "../parse/codexparse/testdata/lens_responses.jsonl"
	codexReadbackFixture  = "../parse/codexparse/testdata/lens_readback.jsonl"
	codexFileWriteFixture = "../parse/codexparse/testdata/lens_filewrite.jsonl"
)

// foldFixture parses a transcript fixture through its parser and writes it
// through the store, the way the summarizer does, under the fixture's path.
func foldFixture(t *testing.T, dbPath, fixture string, agent summary.Agent) {
	t.Helper()
	f, err := os.Open(fixture)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var sum *summary.SessionSummary
	switch agent {
	case summary.AgentClaude:
		sum, err = claudeparse.Parse(f)
	case summary.AgentCodex:
		sum, err = codexparse.Parse(f)
	}
	if err != nil {
		t.Fatal(err)
	}
	st, err := summaries.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.WriteSummary(context.Background(), sum, summaries.SourceInfo{Project: "loom", Path: fixture}); err != nil {
		t.Fatal(err)
	}
}

func openRO(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func fixtureInvocation(t *testing.T, db *sql.DB) Invocation {
	t.Helper()
	invs, err := Invocations(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(invs) != 1 {
		t.Fatalf("invocations = %+v, want one", invs)
	}
	return invs[0]
}

// fixtureRun returns the attempts of the fixture's run on one ticket.
func fixtureRun(t *testing.T, db *sql.DB, ticket string) []LensAttempt {
	t.Helper()
	invs, err := Invocations(db)
	if err != nil {
		t.Fatal(err)
	}
	for _, inv := range invs {
		if inv.Ticket != ticket {
			continue
		}
		attempts, err := Lenses(db, inv, nil)
		if err != nil {
			t.Fatal(err)
		}
		return attempts
	}
	t.Fatalf("no invocation on %s in %+v", ticket, invs)
	return nil
}

func shapeOf(attempts []LensAttempt) string {
	var shape []string
	for _, a := range attempts {
		shape = append(shape, fmt.Sprintf("%s/%d/%d/%s", a.Lens, a.Round, a.Attempt, a.Status))
	}
	return strings.Join(shape, " ")
}

func attempt(t *testing.T, attempts []LensAttempt, lensName string, round, n int) LensAttempt {
	t.Helper()
	for _, a := range attempts {
		if a.Lens == lensName && a.Round == round && a.Attempt == n {
			return a
		}
	}
	t.Fatalf("no %s round %d attempt %d in %+v", lensName, round, n, attempts)
	return LensAttempt{}
}

// AC1: a verdict past the 800-char result cut is stored whole, its verdict,
// criteria, findings and context fields intact and normalized.
func TestLensResponsesAreStoredWhole(t *testing.T) {
	path := filepath.Join(t.TempDir(), "summaries.db")
	foldFixture(t, path, lensFixture, summary.AgentClaude)
	db := openRO(t, path)

	rows, err := db.Query(`
		SELECT response_id, source_line, origin, dispatch_id, status, malformed_reason,
		       context_kind, context_state, context_received, length(raw),
		       criteria_json IS NOT NULL, findings_json IS NOT NULL, source_path
		FROM lens_responses WHERE agent = 'claude-code' AND session_id = 'lens-fixture' ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type row struct {
		id, origin, status, kind                  string
		dispatch, reason, state, received, source sql.NullString
		line, rawLen                              int
		hasCriteria, hasFindings                  bool
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.line, &r.origin, &r.dispatch, &r.status, &r.reason, &r.kind,
			&r.state, &r.received, &r.rawLen, &r.hasCriteria, &r.hasFindings, &r.source); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if len(got) != 8 {
		t.Fatalf("lens_responses holds %d rows, want 8", len(got))
	}
	// The routed security verdict came back as a tool result, which the
	// tool_calls table cuts at 800 chars; here it is whole.
	routed := got[0]
	if routed.origin != "tool_result" || routed.dispatch.String != "toolu_s1" || routed.line != 7 || routed.rawLen <= 800 {
		t.Errorf("routed row = %+v, want the toolu_s1 tool result on line 7 stored past 800 chars", routed)
	}
	if routed.status != "parsed" || routed.kind != "structured" || routed.state.String != "clean" || routed.received.String != "[]" {
		t.Errorf("routed row = %+v, want parsed with a structured clean context", routed)
	}
	if !routed.hasCriteria || !routed.hasFindings || !strings.HasSuffix(routed.source.String, "lens_responses.jsonl") {
		t.Errorf("routed row = %+v, want criteria and findings json and the source path", routed)
	}
	var text sql.NullString
	if err := db.QueryRow(`SELECT summary FROM lens_responses WHERE response_id = ?`, routed.id).Scan(&text); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(text.String, "No injection") {
		t.Errorf("routed summary = %q", text.String)
	}

	cut := got[3]
	if cut.status != "malformed" || cut.reason.String != "unterminated" || cut.kind != "historical" || cut.state.Valid || cut.received.Valid {
		t.Errorf("cut row = %+v, want malformed/unterminated with no context", cut)
	}

	// The contract verdict's criteria and findings are normalized; the cut one
	// gets no rows, and a null finding line is stored as null.
	var criteria, findings, unverified, nullLines int
	contract := got[1]
	if err := db.QueryRow(`SELECT COUNT(*) FROM lens_criteria WHERE response_id = ?`, contract.id).Scan(&criteria); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM lens_criteria WHERE response_id = ? AND status = 'unverified' AND id = 'AC3'`, contract.id).Scan(&unverified); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM lens_findings WHERE response_id = ?`, contract.id).Scan(&findings); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM lens_findings WHERE response_id = ? AND line IS NULL AND severity = 'suggestion' AND criterion IS NULL`, contract.id).Scan(&nullLines); err != nil {
		t.Fatal(err)
	}
	if criteria != 3 || unverified != 1 || findings != 2 || nullLines != 1 {
		t.Errorf("contract rows: criteria %d (unverified AC3 %d) findings %d (null line %d), want 3/1/2/1", criteria, unverified, findings, nullLines)
	}
	var cutRows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM lens_criteria WHERE response_id = ?`, cut.id).Scan(&cutRows); err != nil {
		t.Fatal(err)
	}
	if cutRows != 0 {
		t.Errorf("cut verdict has %d criteria rows, want none: a malformed block is not normalized", cutRows)
	}
}

// AC2, AC3: the fixture's attempts come back distinct by round and retry —
// dispatched, responded, parsed, failed and missing, the late one late — and
// only a whole, unreplaced response is successful.
func TestLensesModelRoundsAndRetries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "summaries.db")
	foldFixture(t, path, lensFixture, summary.AgentClaude)
	db := openRO(t, path)
	attempts, err := Lenses(db, fixtureInvocation(t, db), nil)
	if err != nil {
		t.Fatal(err)
	}

	var shape []string
	for _, a := range attempts {
		shape = append(shape, fmt.Sprintf("%s/%d/%d/%s", a.Lens, a.Round, a.Attempt, a.Status))
	}
	want := []string{
		"contract/1/1/parsed", "quality/1/1/parsed", "security/1/1/parsed",
		"contract/2/1/responded", "contract/2/2/parsed", "quality/2/1/failed", "security/2/0/missing",
	}
	if strings.Join(shape, " ") != strings.Join(want, " ") {
		t.Fatalf("attempts = %v, want %v", shape, want)
	}

	c1 := attempt(t, attempts, lens.Contract, 1, 1)
	if !c1.Dispatched || c1.Routed || c1.Provenance != "" || c1.DispatchID != "toolu_c1" || c1.DispatchTurnIdx != 0 || c1.ResponseTurnIdx != 1 || c1.Late || c1.Superseded {
		t.Errorf("contract r1 = %+v, want dispatched at turn 0, answered at turn 1, on time, standing", c1)
	}
	if c1.Source == nil || c1.Source.Line != 8 || !strings.HasSuffix(c1.Source.Path, "lens_responses.jsonl") {
		t.Errorf("contract r1 source = %+v, want the notification's line", c1.Source)
	}
	if !c1.Successful() || c1.Verdict != "findings" || c1.ContextKind != lens.ContextStructured || c1.ContextState != "clean" || c1.Contaminated {
		t.Errorf("contract r1 = %+v, want a successful findings verdict with a clean structured context", c1)
	}

	q1 := attempt(t, attempts, lens.Quality, 1, 1)
	if !q1.Late || q1.ResponseTurnIdx != 2 || !q1.Successful() {
		t.Errorf("quality r1 = %+v, want late (it landed after round 2 went out) and still successful", q1)
	}
	// AC4: no context field, so the prose decides and the kind says so.
	if q1.ContextKind != lens.ContextHistorical || q1.ContextState != "" || !q1.Contaminated {
		t.Errorf("quality r1 = %+v, want historical context read as contaminated from prose", q1)
	}

	s1 := attempt(t, attempts, lens.Security, 1, 1)
	if !s1.Dispatched || !s1.Routed || s1.Provenance != ProvenanceRouterResult || s1.DispatchID != "toolu_s1" || s1.ResponseTurnIdx != 0 || !s1.Successful() || s1.Contaminated {
		t.Errorf("security r1 = %+v, want the router call's own result, routed, successful and clean", s1)
	}

	c2a := attempt(t, attempts, lens.Contract, 2, 1)
	if c2a.Malformed != lens.ReasonUnterminated || !c2a.Superseded || c2a.Successful() || c2a.ResponseID == "" {
		t.Errorf("contract r2 a1 = %+v, want a superseded malformed response that is not successful", c2a)
	}
	c2b := attempt(t, attempts, lens.Contract, 2, 2)
	if c2b.DispatchID != "toolu_c3" || c2b.Superseded || !c2b.Successful() || c2b.Verdict != "satisfied" {
		t.Errorf("contract r2 a2 = %+v, want the retry standing as the successful verdict", c2b)
	}
	// The compaction summary on line 20 quotes a whole contract verdict with
	// every criterion passing. It is stored as a parsed user row and places
	// nothing: no lens answers as a plain user message, and the shape above
	// holds with the retry, not the quote, as the contract attempt that stands.
	var quoted, quotedStatus string
	if err := db.QueryRow(`SELECT origin, status FROM lens_responses WHERE source_line = 20`).Scan(&quoted, &quotedStatus); err != nil {
		t.Fatal(err)
	}
	if quoted != summary.OriginUser || quotedStatus != lens.StatusParsed {
		t.Errorf("line 20 row = %s/%s, want a parsed user row", quoted, quotedStatus)
	}
	for _, a := range attempts {
		if a.Source != nil && a.Source.Line == 20 {
			t.Errorf("attempt %+v was answered by the quoted verdict", a)
		}
	}
	// The pasted note on line 21 quotes a task notification carrying a whole
	// quality verdict past its own text, and the compaction summary on line
	// 22 opens with a quoted notification naming toolu_c3, the contract
	// retry's dispatch. Neither is the envelope the harness posts, so both
	// are stored as user rows naming no dispatch and place nothing: the
	// pasted note neither answers the failed quality dispatch nor opens an
	// attempt that would supersede it, the quoted notification opens no
	// further attempt on toolu_c3, and the shape above holds.
	for _, line := range []int{21, 22} {
		var quotedOrigin, quotedStatus string
		var quotedDispatch sql.NullString
		if err := db.QueryRow(`SELECT origin, status, dispatch_id FROM lens_responses WHERE source_line = ?`, line).Scan(&quotedOrigin, &quotedStatus, &quotedDispatch); err != nil {
			t.Fatal(err)
		}
		if quotedOrigin != summary.OriginUser || quotedStatus != lens.StatusParsed || quotedDispatch.Valid {
			t.Errorf("line %d row = %s/%s dispatch %v, want a parsed user row naming no dispatch", line, quotedOrigin, quotedStatus, quotedDispatch)
		}
		for _, a := range attempts {
			if a.Source != nil && a.Source.Line == line {
				t.Errorf("attempt %+v was answered by the quoted notification on line %d", a, line)
			}
		}
	}
	q2 := attempt(t, attempts, lens.Quality, 2, 1)
	if q2.DispatchID != "toolu_q2" || q2.ResponseID != "" || q2.Successful() || q2.Source != nil {
		t.Errorf("quality r2 = %+v, want failed with no response", q2)
	}
	s2 := attempt(t, attempts, lens.Security, 2, 0)
	if s2.Dispatched || s2.DispatchTurnIdx != -1 || s2.ResponseTurnIdx != -1 || s2.Successful() {
		t.Errorf("security r2 = %+v, want missing: committed to and never sent", s2)
	}

	// The compliance report reads the same rows: one contamination report
	// (the historical quality verdict), one unverified criterion in the
	// contract attempt that stands.
	rep, err := Load(path, time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	run := only(t, rep)
	if run.ContaminationReports != 1 {
		t.Errorf("contamination_reports = %d, want 1", run.ContaminationReports)
	}
	if run.CriteriaUnverified == nil || *run.CriteriaUnverified != 1 {
		t.Errorf("criteria_unverified = %v, want 1 from contract round 2 attempt 2", run.CriteriaUnverified)
	}
	if run.ReviewIterations == nil || *run.ReviewIterations != 2 || run.Classification != ClassCompliant {
		t.Errorf("run = %+v, want two rounds and compliant", run)
	}
}

// A single-turn run dispatching each lens once per round reads as one
// attempt per lens per round. Each hand-back is placed by time ahead of the
// next round's dispatch, so that dispatch finds its lens answered and applies
// the next commitment line rather than opening a retry in the round before;
// no round is left with a lens missing. The hand-back from an agent the
// session never launched places nothing.
func TestSingleTurnRunPlacesHandbacksBeforeTheNextRound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "summaries.db")
	foldFixture(t, path, handbackFixture, summary.AgentClaude)
	db := openRO(t, path)
	attempts := fixtureRun(t, db, "loom/handback-1")

	want := "contract/1/1/parsed quality/1/1/parsed security/1/1/parsed " +
		"contract/2/1/parsed quality/2/1/parsed security/2/1/parsed " +
		"contract/3/1/parsed quality/3/1/parsed security/3/1/parsed"
	if got := shapeOf(attempts); got != want {
		t.Fatalf("attempts = %v, want %v", got, want)
	}
	for _, a := range attempts {
		if a.Superseded || a.Late || !a.Dispatched || a.DispatchTurnIdx != 0 || a.ResponseTurnIdx != 0 {
			t.Errorf("attempt %+v, want dispatched and answered in turn 0, on time and standing", a)
		}
		if a.Source != nil && a.Source.Line == 37 {
			t.Errorf("attempt %+v was answered by the unlaunched agent's hand-back", a)
		}
	}
	for round, line := range map[int]int{1: 9, 2: 21, 3: 33} {
		c := attempt(t, attempts, lens.Contract, round, 1)
		if c.Routed || c.DispatchID != fmt.Sprintf("toolu_c%d", round) || c.Source == nil || c.Source.Line != line {
			t.Errorf("contract round %d = %+v, want its subagent dispatch answered by the hand-back on line %d", round, c, line)
		}
	}
}

// A Codex run's attempts: the routed security lens is dispatched and answered
// by its own call's output; the inlined contract and quality passes, written
// by the assistant with no dispatch on record, stand as undispatched attempts
// in the round the commitment line before them opened.
func TestCodexInlinedPassesAreUndispatchedAttempts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "summaries.db")
	foldFixture(t, path, codexLensFixture, summary.AgentCodex)
	db := openRO(t, path)
	attempts, err := Lenses(db, fixtureInvocation(t, db), nil)
	if err != nil {
		t.Fatal(err)
	}
	var shape []string
	for _, a := range attempts {
		shape = append(shape, fmt.Sprintf("%s/%d/%d/%s/%v", a.Lens, a.Round, a.Attempt, a.Status, a.Dispatched))
	}
	want := []string{"contract/1/1/parsed/false", "quality/1/1/parsed/false", "security/1/1/parsed/true"}
	if strings.Join(shape, " ") != strings.Join(want, " ") {
		t.Fatalf("attempts = %v, want %v", shape, want)
	}
	sec := attempt(t, attempts, lens.Security, 1, 1)
	if sec.DispatchID != "call_sec" || sec.Provenance != ProvenanceRouterResult || !sec.Successful() || sec.Source == nil || sec.Source.Line != 5 {
		t.Errorf("security = %+v, want answered by call_sec's own output on line 5", sec)
	}
	quality := attempt(t, attempts, lens.Quality, 1, 1)
	if quality.Provenance != "" || quality.ContextState != "shared" || quality.Contaminated || !quality.Successful() || quality.Verdict != "findings" {
		t.Errorf("quality = %+v, want a successful findings verdict with a shared, uncontaminated context", quality)
	}

	rep, err := Load(path, time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	run := only(t, rep)
	if run.Runtime != RuntimeCodex || !run.FanOutDispatched || run.Classification != ClassCompliant {
		t.Errorf("run = %+v, want a compliant codex run", run)
	}
	if run.ContaminationReports != 0 || run.CriteriaUnverified == nil || *run.CriteriaUnverified != 0 {
		t.Errorf("run = contamination %d criteria %v, want 0 and 0", run.ContaminationReports, run.CriteriaUnverified)
	}
}

// A Codex run whose inlined passes were written to files: each file_write
// row is a tool row of its lens, placed among the turn's calls by time, so
// the round-1 writes after the first router call land in round 1, and the
// round-2 writes ahead of the second router call — a second contract verdict
// with the first already parsed — apply the next commitment line and land in
// round 2, none superseding another. The same rows on a Claude
// runtime place nothing: a file the assistant wrote is not a lens answering
// there any more than its text is.
func TestCodexFileWritesAreInlinedPasses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "summaries.db")
	foldFixture(t, path, codexFileWriteFixture, summary.AgentCodex)
	db := openRO(t, path)
	attempts, err := Lenses(db, fixtureInvocation(t, db), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "contract/1/1/parsed quality/1/1/parsed security/1/1/parsed contract/2/1/parsed quality/2/1/parsed security/2/1/parsed"
	if got := shapeOf(attempts); got != want {
		t.Fatalf("attempts = %v, want %v", got, want)
	}
	for round, line := range map[int]int{1: 7, 2: 10} {
		for _, name := range []string{lens.Contract, lens.Quality} {
			a := attempt(t, attempts, name, round, 1)
			if a.Dispatched || a.DispatchID != "" || a.Superseded || a.Late || !a.Successful() || a.ResponseTurnIdx != 0 {
				t.Errorf("%s round %d = %+v, want an undispatched attempt standing as successful", name, round, a)
			}
			if a.Source == nil || a.Source.Line != line || !strings.HasSuffix(a.Source.Path, "lens_filewrite.jsonl") {
				t.Errorf("%s round %d source = %+v, want the FileChange item on line %d", name, round, a.Source, line)
			}
		}
		if a := attempt(t, attempts, lens.Security, round, 1); !a.Dispatched || a.DispatchID != fmt.Sprintf("call_sec%d", round) || !a.Successful() {
			t.Errorf("security round %d = %+v, want dispatched by call_sec%d and successful", round, a, round)
		}
	}
	if c1, c2 := attempt(t, attempts, lens.Contract, 1, 1), attempt(t, attempts, lens.Contract, 2, 1); c1.Verdict != "findings" || c2.Verdict != "satisfied" {
		t.Errorf("contract verdicts = %s then %s, want findings then satisfied: each round's write is its own attempt", c1.Verdict, c2.Verdict)
	}
	var stored int
	if err := db.QueryRow(`SELECT COUNT(*) FROM lens_responses WHERE origin = ? AND status = 'parsed'`, summary.OriginFileWrite).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 4 {
		t.Errorf("file_write rows stored = %d, want 4", stored)
	}
	rep, err := Load(path, time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if run := only(t, rep); run.Runtime != RuntimeCodex || !run.FanOutDispatched || run.Classification != ClassCompliant || run.ReviewIterations == nil || *run.ReviewIterations != 2 {
		t.Errorf("run = %+v, want a compliant two-round codex run", run)
	}

	// The same rows on a Claude runtime: the router calls dispatch the
	// security lens in both rounds and the file writes place nothing, so the
	// committed contract and quality lenses come out missing.
	const (
		ticket = "loom/claude-write-9012"
		router = "~/.codex/codex-lens.sh --lens security --payload /tmp/c/context.md"
	)
	at := func(sec int) time.Time { return base.Add(time.Duration(sec) * time.Second) }
	verdict := func(name string) string {
		return `{"lens": "` + name + `", "verdict": "satisfied", "summary": "Fine."}`
	}
	sum := &summary.SessionSummary{
		SessionID: "claude-file-writes",
		Agent:     summary.AgentClaude,
		StartTime: at(0),
		EndTime:   at(60),
		Turns: []summary.Turn{{
			Idx: 0, UserMessage: workInvocation(ticket), StartedAt: at(0),
			AssistantText: "dispatching (" + ticket + " round 1): contract, quality, security\ndispatching (" + ticket + " round 2): contract, quality, security",
		}},
		ToolCalls: []summary.ToolCall{
			{TurnIdx: 0, CallID: "call_sec1", Kind: summary.KindBash, ToolName: "Bash", KeyArg: router, StartedAt: at(5), DurationMs: 9000, ResultSummary: fenced(verdict(lens.Security))},
			{TurnIdx: 0, CallID: "call_sec2", Kind: summary.KindBash, ToolName: "Bash", KeyArg: router, StartedAt: at(20), DurationMs: 9000, ResultSummary: fenced(verdict(lens.Security))},
		},
	}
	for i, w := range []struct {
		sec  int
		name string
	}{{10, lens.Contract}, {11, lens.Quality}, {25, lens.Contract}, {26, lens.Quality}} {
		sum.LensResponses = append(sum.LensResponses, summary.LensResponse{
			TurnIdx: 0, Origin: summary.OriginFileWrite, SourceLine: 100 + i, At: at(w.sec),
			Block: lens.Extract(fenced(verdict(w.name)))[0],
		})
	}
	f := newFixture(t)
	f.add(sum)
	db = openRO(t, f.path)
	attempts, err = Lenses(db, fixtureInvocation(t, db), nil)
	if err != nil {
		t.Fatal(err)
	}
	want = "contract/1/0/missing quality/1/0/missing security/1/1/parsed contract/2/0/missing quality/2/0/missing security/2/1/parsed"
	if got := shapeOf(attempts); got != want {
		t.Fatalf("claude attempts = %v, want %v", got, want)
	}
}

// AC3: a routed dispatch whose verdict was redirected to a file stands
// dispatched until a shell read of that file answers it. A shell read of
// anything else — a doc quoting a verdict, a README fetched from anywhere —
// is stored and places nothing: it neither answers the outstanding attempt
// nor opens one that would supersede the real answer. A verdict that came
// back with a field outside the schema is a response, never a success.
func TestReadBackPairsByRedirectPathOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "summaries.db")
	foldFixture(t, path, codexReadbackFixture, summary.AgentCodex)
	db := openRO(t, path)

	never := fixtureRun(t, db, "loom/readback-1111")
	if got := shapeOf(never); got != "security/1/1/dispatched" {
		t.Fatalf("never-read run attempts = %s, want security/1/1/dispatched", got)
	}
	if a := never[0]; !a.Dispatched || a.DispatchID != "call_sec1" || a.Provenance != "" || a.ResponseID != "" || a.Source != nil || a.Successful() {
		t.Errorf("never-read attempt = %+v, want dispatched by call_sec1 with no response", a)
	}

	read := fixtureRun(t, db, "loom/readback-2222")
	if got := shapeOf(read); got != "security/1/1/parsed" {
		t.Fatalf("read-back run attempts = %s, want security/1/1/parsed", got)
	}
	var catID string
	if err := db.QueryRow(`SELECT response_id FROM lens_responses WHERE dispatch_id = 'call_cat'`).Scan(&catID); err != nil {
		t.Fatal(err)
	}
	if a := read[0]; a.DispatchID != "call_sec2" || a.Provenance != ProvenanceReadBack || a.ResponseID != catID || a.Source == nil || a.Source.Line != 21 || !a.Successful() || a.Verdict != "satisfied" {
		t.Errorf("read-back attempt = %+v, want call_sec2 answered by the cat result on line 21", a)
	}
	var stored int
	if err := db.QueryRow(`SELECT COUNT(*) FROM lens_responses WHERE dispatch_id IN ('call_doc', 'call_readme') AND status = 'parsed'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 2 {
		t.Errorf("quoted verdicts stored = %d, want both kept as evidence", stored)
	}

	// A read of the README and the verdict file in one cat carries the
	// README's verdict-shaped block first: the output cannot be attributed to
	// the file alone, so the attempt stays dispatched and both blocks are
	// kept as evidence.
	both := fixtureRun(t, db, "loom/readback-4444")
	if got := shapeOf(both); got != "security/1/1/dispatched" {
		t.Fatalf("batched-read run attempts = %s, want security/1/1/dispatched", got)
	}
	if a := both[0]; a.DispatchID != "call_sec4" || a.ResponseID != "" || a.Successful() {
		t.Errorf("batched-read attempt = %+v, want call_sec4 unanswered", a)
	}
	var batched int
	if err := db.QueryRow(`SELECT COUNT(*) FROM lens_responses WHERE dispatch_id = 'call_both' AND status = 'parsed'`).Scan(&batched); err != nil {
		t.Fatal(err)
	}
	if batched != 2 {
		t.Errorf("batched read stored %d verdicts, want both kept as evidence", batched)
	}

	// A router command that redirects its verdict and cats the README in the
	// same call has a result that is the README's, not the router's, and a
	// redirect that a compound command cannot attribute to the router: the
	// quoted block does not answer the dispatch, no path is recorded, the
	// later read of the file pairs nothing, and both blocks are kept as
	// evidence.
	chained := fixtureRun(t, db, "loom/readback-5555")
	if got := shapeOf(chained); got != "security/1/1/dispatched" {
		t.Fatalf("chained-router run attempts = %s, want security/1/1/dispatched", got)
	}
	if a := chained[0]; a.DispatchID != "call_sec5" || a.ResponseID != "" || a.Source != nil || a.Successful() {
		t.Errorf("chained-router attempt = %+v, want call_sec5 unanswered", a)
	}
	var chainedStored int
	if err := db.QueryRow(`SELECT COUNT(*) FROM lens_responses WHERE dispatch_id IN ('call_sec5', 'call_cat5') AND status = 'parsed'`).Scan(&chainedStored); err != nil {
		t.Fatal(err)
	}
	if chainedStored != 2 {
		t.Errorf("chained-router run stored %d verdicts, want the README's and the file's both kept", chainedStored)
	}

	// A redirect that belongs to a command chained after the router —
	// `codex-lens.sh …; cat README.md > notes.txt` — is the README's, not
	// the router's: no path is recorded for the dispatch, so a later cat of
	// the notes file cannot hand the README's quoted verdict to it, and both
	// blocks are kept as evidence.
	after := fixtureRun(t, db, "loom/readback-7777")
	if got := shapeOf(after); got != "security/1/1/dispatched" {
		t.Fatalf("redirect-after-router run attempts = %s, want security/1/1/dispatched", got)
	}
	if a := after[0]; a.DispatchID != "call_sec7" || a.ResponseID != "" || a.Source != nil || a.Successful() {
		t.Errorf("redirect-after-router attempt = %+v, want call_sec7 unanswered", a)
	}
	var afterStored int
	if err := db.QueryRow(`SELECT COUNT(*) FROM lens_responses WHERE dispatch_id IN ('call_sec7', 'call_cat7') AND status = 'parsed'`).Scan(&afterStored); err != nil {
		t.Fatal(err)
	}
	if afterStored != 2 {
		t.Errorf("redirect-after-router run stored %d verdicts, want the router's and the notes file's both kept", afterStored)
	}

	// A router command that appends (`>>`) to the file an earlier round's
	// verdict was redirected to records no path: a read of that file
	// delivers the earlier round's block first, which would answer the
	// later attempt with the wrong verdict. The round-2 attempt stays
	// dispatched and both blocks the read carried are kept as evidence.
	appended := fixtureRun(t, db, "loom/readback-8888")
	if got := shapeOf(appended); got != "security/1/1/parsed security/2/1/dispatched" {
		t.Fatalf("append-redirect run attempts = %s, want security/1/1/parsed security/2/1/dispatched", got)
	}
	if a := attempt(t, appended, lens.Security, 2, 1); a.DispatchID != "call_sec8b" || a.ResponseID != "" || a.Source != nil || a.Successful() {
		t.Errorf("append-redirect attempt = %+v, want call_sec8b unanswered", a)
	}
	var appendedStored int
	if err := db.QueryRow(`SELECT COUNT(*) FROM lens_responses WHERE dispatch_id = 'call_cat8b' AND status = 'parsed'`).Scan(&appendedStored); err != nil {
		t.Fatal(err)
	}
	if appendedStored != 2 {
		t.Errorf("append-redirect read stored %d verdicts, want both rounds' blocks kept", appendedStored)
	}

	// A read-back through rg is a search's output, not the file as written:
	// only a cat of the path alone pairs, so the attempt stays dispatched and
	// the verdict-shaped block rg printed is kept as evidence.
	searched := fixtureRun(t, db, "loom/readback-6666")
	if got := shapeOf(searched); got != "security/1/1/dispatched" {
		t.Fatalf("rg-read run attempts = %s, want security/1/1/dispatched", got)
	}
	if a := searched[0]; a.DispatchID != "call_sec6" || a.ResponseID != "" || a.Source != nil || a.Successful() {
		t.Errorf("rg-read attempt = %+v, want call_sec6 unanswered", a)
	}
	var rgStored int
	if err := db.QueryRow(`SELECT COUNT(*) FROM lens_responses WHERE dispatch_id = 'call_rg' AND status = 'parsed'`).Scan(&rgStored); err != nil {
		t.Fatal(err)
	}
	if rgStored != 1 {
		t.Errorf("rg read stored %d verdicts, want the block kept as evidence", rgStored)
	}

	broken := fixtureRun(t, db, "loom/readback-3333")
	if got := shapeOf(broken); got != "security/1/1/responded" {
		t.Fatalf("broken run attempts = %s, want security/1/1/responded", got)
	}
	if a := broken[0]; a.DispatchID != "call_sec3" || a.ResponseID == "" || a.Malformed != lens.ReasonInvalidFindings || a.Successful() {
		t.Errorf("broken attempt = %+v, want responded with invalid findings and not successful", a)
	}
	var findings int
	if err := db.QueryRow(`SELECT COUNT(*) FROM lens_findings WHERE response_id = ?`, broken[0].ResponseID).Scan(&findings); err != nil {
		t.Fatal(err)
	}
	if findings != 0 {
		t.Errorf("broken verdict has %d findings rows, want none", findings)
	}
}

// A read-back pairs only when the command past the shell wrapper is `cat`,
// an optional `--`, and exactly the redirect path; any other program, any
// other flag or anything else is not classified and fails closed.
func TestReadsOnlyClassifiesTheReadingCommand(t *testing.T) {
	const path = "/tmp/c/verdict.txt"
	cases := map[string]bool{
		"cat /tmp/c/verdict.txt":                         true,
		"cat -- /tmp/c/verdict.txt":                      true,
		`cat "/tmp/c/verdict.txt"`:                       true,
		"/bin/cat /tmp/c/verdict.txt":                    true,
		"bash -lc cat /tmp/c/verdict.txt":                true,
		"cat -n /tmp/c/verdict.txt":                      false,
		"rg --no-filename --passthru /tmp/c/verdict.txt": false,
		"cat README.md /tmp/c/verdict.txt":               false,
		"cat /tmp/c/verdict.txt README.md":               false,
		"cat /tmp/c/verdict.txt; cat README.md":          false,
		"cat README.md && cat /tmp/c/verdict.txt":        false,
		"cat /tmp/c/verdict.txt | head":                  false,
		"cat /tmp/c/other.txt":                           false,
		"cat":                                            false,
		"":                                               false,
	}
	for keyArg, want := range cases {
		if got := readsOnly(keyArg, path); got != want {
			t.Errorf("readsOnly(%q) = %v, want %v", keyArg, got, want)
		}
	}
}

// A router call's own result pairs only when the command is the router
// alone: an env-assignment prefix and a stderr redirect are fine; a stdout
// redirect, a chain, a pipeline, a newline or a cut command is not
// classified and fails closed.
func TestRouterAloneClassifiesTheRouterCommand(t *testing.T) {
	cases := map[string]bool{
		"~/.codex/codex-lens.sh --lens security --payload /tmp/c/context.md":                          true,
		"bash -lc ~/.codex/codex-lens.sh --lens security --payload /tmp/c/context.md":                 true,
		"SCRATCH=/tmp/s ~/.codex/codex-lens.sh --lens security --payload /tmp/c/context.md":           true,
		"~/.codex/codex-lens.sh --lens security --payload /tmp/c/context.md 2> /tmp/c/err.txt":        true,
		"~/.codex/codex-lens.sh --lens security --payload /tmp/c/context.md 2>/dev/null":              true,
		"~/.codex/codex-lens.sh --lens security --payload /tmp/c/context.md > /tmp/c/verdict.txt":     false,
		"~/.codex/codex-lens.sh --lens security --payload /tmp/c/context.md >> /tmp/c/verdict.txt":    false,
		"~/.codex/codex-lens.sh --lens security --payload /tmp/c/context.md 2>&1":                     false,
		"~/.codex/codex-lens.sh --lens security --payload /tmp/c/context.md; cat README.md":           false,
		"~/.codex/codex-lens.sh --lens security --payload /tmp/c/context.md && cat README.md":         false,
		"~/.codex/codex-lens.sh --lens security --payload /tmp/c/context.md || cat README.md":         false,
		"~/.codex/codex-lens.sh --lens security --payload /tmp/c/context.md | tee /tmp/c/verdict.txt": false,
		"~/.codex/codex-lens.sh --lens security --payload /tmp/c/context.md\ncat README.md":           false,
		"cat README.md; ~/.codex/codex-lens.sh --lens security --payload /tmp/c/context.md":           false,
		"~/.codex/codex-lens.sh --lens security --payload /tmp/c/context.md…":                         false,
		"": false,
	}
	for keyArg, want := range cases {
		if got := routerAlone(keyArg); got != want {
			t.Errorf("routerAlone(%q) = %v, want %v", keyArg, got, want)
		}
	}
}

// A redirect path is recorded only when the command is the router alone
// followed by exactly one stdout redirect, a stderr redirect on either side
// aside; a chain, a pipeline or a second stdout redirect records none, so a
// redirect belonging to another command in a compound line is never the
// router's.
func TestRouterRedirectRecordsTheRouterOwnRedirectOnly(t *testing.T) {
	cases := map[string]string{
		"~/.codex/codex-lens.sh --lens security --payload /tmp/c/context.md > /tmp/c/verdict.txt":                         "/tmp/c/verdict.txt",
		"~/.codex/codex-lens.sh --lens security --payload /tmp/c/context.md >> /tmp/c/verdict.txt":                        "",
		"~/.codex/codex-lens.sh --lens security --payload /tmp/c/context.md >/tmp/c/verdict.txt":                          "/tmp/c/verdict.txt",
		`~/.codex/codex-lens.sh --lens security --payload /tmp/c/context.md > "/tmp/c/verdict.txt"`:                       "/tmp/c/verdict.txt",
		"bash -lc ~/.codex/codex-lens.sh --lens security --payload /tmp/c/context.md > /tmp/c/verdict.txt":                "/tmp/c/verdict.txt",
		"SCRATCH=/tmp/s ~/.codex/codex-lens.sh --lens security --payload /tmp/c/context.md > /tmp/c/verdict.txt":          "/tmp/c/verdict.txt",
		"~/.codex/codex-lens.sh --lens security --payload /tmp/c/context.md > /tmp/c/verdict.txt 2> /tmp/c/err.txt":       "/tmp/c/verdict.txt",
		"~/.codex/codex-lens.sh --lens security --payload /tmp/c/context.md 2> /tmp/c/err.txt > /tmp/c/verdict.txt":       "/tmp/c/verdict.txt",
		"~/.codex/codex-lens.sh --lens security --payload /tmp/c/context.md":                                              "",
		"~/.codex/codex-lens.sh --lens security --payload /tmp/c/context.md; cat README.md > /tmp/c/notes.txt":            "",
		"~/.codex/codex-lens.sh --lens security --payload /tmp/c/context.md > /tmp/c/verdict.txt; cat README.md":          "",
		"~/.codex/codex-lens.sh --lens security --payload /tmp/c/context.md > /tmp/c/verdict.txt && cat README.md":        "",
		"~/.codex/codex-lens.sh --lens security --payload /tmp/c/context.md | tee /tmp/c/verdict.txt":                     "",
		"~/.codex/codex-lens.sh --lens security --payload /tmp/c/context.md > /tmp/c/verdict.txt > /tmp/c/other.txt":      "",
		"~/.codex/codex-lens.sh --lens security --payload /tmp/c/context.md > /tmp/c/verdict.txt 2>&1":                    "",
		"~/.codex/codex-lens.sh --lens security --payload /tmp/c/context.md > /tmp/c/verdict.txt\ncat /tmp/c/verdict.txt": "",
		"cat README.md > /tmp/c/notes.txt; ~/.codex/codex-lens.sh --lens security --payload /tmp/c/context.md":            "",
		"~/.codex/codex-lens.sh --lens security --payload /tmp/c/context.md > /tmp/c/verdict…":                            "",
		"": "",
	}
	for keyArg, want := range cases {
		if got := routerRedirect(keyArg); got != want {
			t.Errorf("routerRedirect(%q) = %q, want %q", keyArg, got, want)
		}
	}
}

// AC5: folding the same transcript twice yields the same response ids and
// the same row counts, with no finding or criterion duplicated.
func TestRefoldKeepsResponseIdentities(t *testing.T) {
	path := filepath.Join(t.TempDir(), "summaries.db")
	foldFixture(t, path, lensFixture, summary.AgentClaude)
	ids := func() []string {
		db := openRO(t, path)
		rows, err := db.Query(`SELECT response_id FROM lens_responses ORDER BY seq`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			out = append(out, id)
		}
		return out
	}
	counts := func() [3]int {
		db := openRO(t, path)
		var out [3]int
		for i, table := range []string{"lens_responses", "lens_criteria", "lens_findings"} {
			if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&out[i]); err != nil {
				t.Fatal(err)
			}
		}
		return out
	}
	before, beforeCounts := ids(), counts()
	if len(before) != 8 || beforeCounts != [3]int{8, 12, 3} {
		t.Fatalf("first fold: %d ids, counts %v, want 8 and [8 12 3]", len(before), beforeCounts)
	}
	for i, id := range before {
		if len(id) != 64 {
			t.Errorf("response_id[%d] = %q, want a hex sha256", i, id)
		}
	}

	foldFixture(t, path, lensFixture, summary.AgentClaude)
	after, afterCounts := ids(), counts()
	if strings.Join(after, ",") != strings.Join(before, ",") {
		t.Errorf("response ids changed across a re-fold:\n%v\n%v", before, after)
	}
	if afterCounts != beforeCounts {
		t.Errorf("row counts after re-fold = %v, want %v", afterCounts, beforeCounts)
	}
}

// A Claude transcript since 2.1.268 keeps no commitment line, so the round is
// read off the run's execution records: the routed security lens's record —
// which names no dispatch, since the script cannot know its tool_use_id —
// joins the router call whose window holds its start, and the turn's
// subagent dispatches take that record's round with it. A record starting
// outside every call's window joins nothing, and the round falls back to 0.
func TestRecordsPlaceARoundWithNoCommitmentLine(t *testing.T) {
	const (
		ticket = "loom/lens-attempts-0001"
		router = "~/.codex/codex-lens.sh --lens security --payload /tmp/c/context.md > /tmp/c/verdict.txt"
	)
	at := func(hhmmss string) time.Time {
		v, err := time.Parse("15:04:05", hhmmss)
		if err != nil {
			t.Fatal(err)
		}
		return time.Date(2026, 9, 12, v.Hour(), v.Minute(), v.Second(), 0, time.UTC)
	}
	verdict := func(name string) string {
		return `{"lens": "` + name + `", "verdict": "satisfied", "summary": "Fine."}`
	}
	f := newFixture(t)
	f.add(&summary.SessionSummary{
		SessionID: "no-commitment-line",
		Agent:     summary.AgentClaude,
		StartTime: at("04:00:00"),
		EndTime:   at("04:30:00"),
		Turns: []summary.Turn{
			{Idx: 0, UserMessage: workInvocation(ticket), AssistantText: "Fanning out.", StartedAt: at("04:00:00")},
			{Idx: 1, UserMessage: notification("toolu_c", verdict("contract")), AssistantText: "Contract in.", StartedAt: at("04:05:00")},
			{Idx: 2, UserMessage: notification("toolu_q", verdict("quality")), AssistantText: "Quality in.", StartedAt: at("04:06:00")},
			{Idx: 3, UserMessage: "carry on", AssistantText: "Fanning out again.", StartedAt: at("04:10:00")},
			{Idx: 4, UserMessage: notification("toolu_c2", verdict("contract")), AssistantText: "Contract in.", StartedAt: at("04:15:00")},
			{Idx: 5, UserMessage: notification("toolu_q2", verdict("quality")), AssistantText: "Quality in.", StartedAt: at("04:16:00")},
		},
		ToolCalls: []summary.ToolCall{
			{TurnIdx: 0, CallID: "toolu_c", Kind: summary.KindTask, ToolName: "Agent", KeyArg: "Contract lens round 1", StartedAt: at("04:03:10"), DurationMs: 120000},
			{TurnIdx: 0, CallID: "toolu_q", Kind: summary.KindTask, ToolName: "Agent", KeyArg: "Quality lens round 1", StartedAt: at("04:03:11"), DurationMs: 120000},
			{TurnIdx: 0, CallID: "toolu_b", Kind: summary.KindBash, ToolName: "Bash", KeyArg: router, StartedAt: at("04:03:13"), DurationMs: 10367},
			{TurnIdx: 3, CallID: "toolu_c2", Kind: summary.KindTask, ToolName: "Agent", KeyArg: "Contract lens round 2", StartedAt: at("04:12:10"), DurationMs: 120000},
			{TurnIdx: 3, CallID: "toolu_q2", Kind: summary.KindTask, ToolName: "Agent", KeyArg: "Quality lens round 2", StartedAt: at("04:12:11"), DurationMs: 120000},
			{TurnIdx: 3, CallID: "toolu_b2", Kind: summary.KindBash, ToolName: "Bash", KeyArg: router, StartedAt: at("04:12:13"), DurationMs: 10000},
		},
	})
	db := openRO(t, f.path)
	inv := fixtureInvocation(t, db)

	attempts, err := Lenses(db, inv, []LensExecution{
		{ExecutionID: "lens-security-r2-a1", Lens: "security", Round: 2, StartedAt: at("04:12:15")},
		{ExecutionID: "lens-security-r1-a1", Lens: "security", Round: 1, StartedAt: at("04:03:15")},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "contract/1/1/parsed quality/1/1/parsed security/1/1/dispatched contract/2/1/parsed quality/2/1/parsed security/2/1/dispatched"
	if got := shapeOf(attempts); got != want {
		t.Fatalf("attempts = %v, want %v", got, want)
	}
	for round, id := range map[int]string{1: "lens-security-r1-a1", 2: "lens-security-r2-a1"} {
		if a := attempt(t, attempts, "security", round, 1); a.ExecutionID != id || a.DispatchID == "" {
			t.Errorf("security round %d = %+v, want joined to %s through its router dispatch", round, a, id)
		}
		if a := attempt(t, attempts, "contract", round, 1); a.ExecutionID != "" || a.Late {
			t.Errorf("contract round %d = %+v, want no execution and not late", round, a)
		}
	}

	// The same transcript against a record that started long after the
	// router call ended: nothing joins, and nothing places a round.
	attempts, err = Lenses(db, inv, []LensExecution{
		{ExecutionID: "lens-security-stray", Lens: "security", Round: 1, StartedAt: at("04:20:00")},
	})
	if err != nil {
		t.Fatal(err)
	}
	want = "contract/0/1/parsed contract/0/2/parsed quality/0/1/parsed quality/0/2/parsed security/0/1/dispatched security/0/2/dispatched"
	if got := shapeOf(attempts); got != want {
		t.Fatalf("attempts with a stray record = %v, want %v", got, want)
	}
	for _, a := range attempts {
		if a.ExecutionID != "" {
			t.Errorf("attempt %+v joined the stray record", a)
		}
	}
}

// A response naming a known lens other than the one its dispatch named is
// the dispatch answered wrong, not a verdict of either lens: the attempt is
// responded with the mismatch as its reason, its verdict fields empty, and
// it never stands as successful — so the compliance report reads no contract
// criteria off it. A mismatched response for a dispatch that already
// answered places nothing: the standing answer is neither superseded nor
// joined by a further attempt.
func TestMismatchedLensResponseNeverPairsAsAVerdict(t *testing.T) {
	const ticket = "loom/lens-mismatch-0001"
	at := func(min int) time.Time { return base.Add(time.Duration(min) * time.Minute) }
	f := newFixture(t)
	f.add(&summary.SessionSummary{
		SessionID: "lens-mismatch",
		Agent:     summary.AgentClaude,
		StartTime: at(0),
		EndTime:   at(30),
		Turns: []summary.Turn{
			{Idx: 0, UserMessage: workInvocation(ticket), StartedAt: at(0),
				AssistantText: "dispatching (" + ticket + " round 1): contract, quality"},
			// The contract dispatch comes back with a whole quality verdict,
			// criteria and all.
			{Idx: 1, UserMessage: notification("toolu_c", `{"lens": "quality", "verdict": "satisfied", "summary": "Fine.", "criteria": []}`),
				AssistantText: "Contract in.", StartedAt: at(5)},
			{Idx: 2, UserMessage: notification("toolu_q", `{"lens": "quality", "verdict": "findings", "summary": "One nit."}`),
				AssistantText: "Quality in.", StartedAt: at(6)},
			// The quality dispatch notifies again, this time with a contract
			// verdict.
			{Idx: 3, UserMessage: notification("toolu_q", `{"lens": "contract", "verdict": "satisfied", "summary": "All met.", "criteria": [{"id": "AC1", "status": "pass"}]}`),
				AssistantText: "Odd.", StartedAt: at(7)},
		},
		ToolCalls: []summary.ToolCall{
			{TurnIdx: 0, CallID: "toolu_c", Kind: summary.KindTask, ToolName: "Agent", KeyArg: "Contract lens round 1", StartedAt: at(1)},
			{TurnIdx: 0, CallID: "toolu_q", Kind: summary.KindTask, ToolName: "Agent", KeyArg: "Quality lens round 1", StartedAt: at(1)},
		},
	})
	db := openRO(t, f.path)
	attempts, err := Lenses(db, fixtureInvocation(t, db), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := shapeOf(attempts), "contract/1/1/responded quality/1/1/parsed"; got != want {
		t.Fatalf("attempts = %v, want %v", got, want)
	}
	c := attempt(t, attempts, lens.Contract, 1, 1)
	if c.Malformed != "lens mismatch: response names quality, dispatch named contract" || c.Successful() || c.Superseded {
		t.Errorf("contract = %+v, want responded with the mismatch as its reason, standing and not successful", c)
	}
	if c.ResponseID == "" || c.ResponseTurnIdx != 1 || c.Source == nil || c.Source.Line != 5 || c.Verdict != "" || c.Summary != "" {
		t.Errorf("contract = %+v, want the response located and none of its verdict copied", c)
	}
	q := attempt(t, attempts, lens.Quality, 1, 1)
	if q.Superseded || !q.Successful() || q.Verdict != "findings" || q.ResponseTurnIdx != 2 {
		t.Errorf("quality = %+v, want the first notification standing as the successful verdict", q)
	}
	for _, a := range attempts {
		if a.Source != nil && a.Source.Line == 9 {
			t.Errorf("attempt %+v was placed by the second, mismatched notification on toolu_q", a)
		}
	}
	var stored int
	if err := db.QueryRow(`SELECT COUNT(*) FROM lens_responses WHERE dispatch_id IN ('toolu_c', 'toolu_q') AND status = 'parsed'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 3 {
		t.Errorf("stored responses = %d, want all three kept as evidence", stored)
	}

	run := only(t, f.load(time.Time{}, time.Time{}))
	if run.CriteriaUnverified != nil {
		t.Errorf("criteria_unverified = %d, want null: the mismatched quality verdict's empty criteria are not the contract's", *run.CriteriaUnverified)
	}
}

func TestChildSessionResponsesFailClosedAndDoNotDuplicateParentResponses(t *testing.T) {
	const (
		ticket = "loom/child-lens-0001"
		router = "~/.cursor/codex-lens.sh --runtime cursor --lens security --payload /tmp/c/payload.md"
	)
	at := func(min int) time.Time { return base.Add(time.Duration(min) * time.Minute) }
	response := func(sessionID, lensName, text string) *summary.SessionSummary {
		raw := fenced(`{"lens":"` + lensName + `","verdict":"satisfied","summary":"` + text + `"}`)
		return &summary.SessionSummary{
			SessionID: sessionID,
			Agent:     summary.AgentCodex,
			StartTime: at(1),
			EndTime:   at(9),
			Turns: []summary.Turn{{
				Idx: 0, UserMessage: "review", AssistantText: raw, StartedAt: at(1), EndedAt: at(2),
			}},
			LensResponses: []summary.LensResponse{{
				TurnIdx: 0, Origin: summary.OriginAssistant, SourceLine: 4, At: at(2),
				Block: lens.Extract(raw)[0],
			}},
		}
	}

	f := newFixture(t)
	parent := &summary.SessionSummary{
		SessionID: "child-lens-parent",
		Agent:     summary.AgentCursor,
		StartTime: at(0),
		EndTime:   at(20),
		Turns: []summary.Turn{
			{Idx: 0, UserMessage: "$work " + ticket, AssistantText: "dispatching (" + ticket + " round 1): security", StartedAt: at(0)},
			{Idx: 1, UserMessage: "continue", AssistantText: "dispatching (" + ticket + " round 2): security", StartedAt: at(5)},
			{Idx: 2, UserMessage: "continue", AssistantText: "dispatching (" + ticket + " round 3): security", StartedAt: at(10)},
			{Idx: 3, UserMessage: "continue", AssistantText: "dispatching (" + ticket + " round 4): security", StartedAt: at(15)},
		},
		ToolCalls: []summary.ToolCall{
			{TurnIdx: 0, CallID: "call-missing", Kind: summary.KindBash, ToolName: "Shell", KeyArg: router, StartedAt: at(1), DurationMs: 1000},
			{TurnIdx: 1, CallID: "call-mismatch", Kind: summary.KindBash, ToolName: "Shell", KeyArg: router, StartedAt: at(6), DurationMs: 1000},
			{TurnIdx: 2, CallID: "call-parent", Kind: summary.KindBash, ToolName: "Shell", KeyArg: router, StartedAt: at(11), DurationMs: 1000},
			{TurnIdx: 3, CallID: "call-subagent", Kind: summary.KindTask, ToolName: "Agent", KeyArg: "security lens review", StartedAt: at(16), DurationMs: 1000},
		},
	}
	parentRaw := fenced(`{"lens":"security","verdict":"satisfied","summary":"parent response"}`)
	parent.LensResponses = []summary.LensResponse{{
		TurnIdx: 2, Origin: summary.OriginToolResult, DispatchID: "call-parent", SourceLine: 20, At: at(12),
		Block: lens.Extract(parentRaw)[0],
	}}
	f.add(parent)
	f.add(response("child-mismatch", "quality", "wrong lens"))
	f.add(response("child-parent-too", "security", "child duplicate"))
	f.add(response("child-subagent", "security", "not a routed response"))
	f.add(response("unrelated-child", "security", "must not attach"))
	db := openRO(t, f.path)

	attempts, err := Lenses(db, fixtureInvocation(t, db), []LensExecution{
		{ExecutionID: "exec-missing", Lens: "security", Round: 1, Attempt: 1, Agent: "codex-cli", SessionID: "child-with-no-response", StartedAt: at(1)},
		{ExecutionID: "exec-mismatch", Lens: "security", Round: 2, Attempt: 1, Agent: "codex-cli", SessionID: "child-mismatch", StartedAt: at(6)},
		{ExecutionID: "exec-parent", Lens: "security", Round: 3, Attempt: 1, Agent: "codex-cli", SessionID: "child-parent-too", StartedAt: at(11)},
		{ExecutionID: "exec-subagent", Lens: "security", Round: 4, Attempt: 1, DispatchID: "call-subagent", Agent: "codex-cli", SessionID: "child-subagent", StartedAt: at(16)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := shapeOf(attempts), "security/1/1/dispatched security/2/1/responded security/3/1/parsed security/4/1/dispatched"; got != want {
		t.Fatalf("attempts = %s, want %s", got, want)
	}
	mismatch := attempt(t, attempts, lens.Security, 2, 1)
	if mismatch.Successful() || mismatch.Verdict != "" || mismatch.Malformed != "lens mismatch: response names quality, dispatch named security" ||
		mismatch.Provenance != ProvenanceChildSession {
		t.Errorf("mismatched child response = %+v", mismatch)
	}
	parentAttempt := attempt(t, attempts, lens.Security, 3, 1)
	if parentAttempt.Summary != "parent response" || parentAttempt.Provenance != ProvenanceRouterResult {
		t.Errorf("parent response was replaced or duplicated: %+v", parentAttempt)
	}
	if subagent := attempt(t, attempts, lens.Security, 4, 1); subagent.Routed || subagent.ResponseID != "" {
		t.Errorf("ordinary subagent child response was attached: %+v", subagent)
	}
}

// A dispatch a record names by dispatch id takes the record's identity: it
// is a dispatch of the record's lens even when its key argument reads as no
// lens dispatch, and its attempt lands in the record's round and attempt
// number rather than the commitment line's round and the next number there.
// The line still moves the walk's round for the rows the records leave
// alone, and still derives what it committed to and never got.
func TestRecordIdentityWinsForAJoinedDispatch(t *testing.T) {
	const ticket = "loom/lens-records-0002"
	at := func(min int) time.Time { return base.Add(time.Duration(min) * time.Minute) }
	verdict := func(name string) string {
		return `{"lens": "` + name + `", "verdict": "satisfied", "summary": "Fine."}`
	}
	f := newFixture(t)
	f.add(&summary.SessionSummary{
		SessionID: "lens-records",
		Agent:     summary.AgentClaude,
		StartTime: at(0),
		EndTime:   at(30),
		Turns: []summary.Turn{
			{Idx: 0, UserMessage: workInvocation(ticket), StartedAt: at(0),
				AssistantText: "dispatching (" + ticket + " round 1): contract, quality"},
			{Idx: 1, UserMessage: notification("toolu_c", verdict("contract")), AssistantText: "Contract in.", StartedAt: at(5)},
			{Idx: 2, UserMessage: notification("toolu_q", verdict("quality")), AssistantText: "Quality in.", StartedAt: at(6)},
			{Idx: 3, UserMessage: "carry on", StartedAt: at(10),
				AssistantText: "dispatching (" + ticket + " round 2): contract, quality"},
			{Idx: 4, UserMessage: notification("toolu_c2", verdict("contract")), AssistantText: "Contract in.", StartedAt: at(15)},
			{Idx: 5, UserMessage: notification("toolu_x", verdict("quality")), AssistantText: "Quality in.", StartedAt: at(16)},
		},
		ToolCalls: []summary.ToolCall{
			{TurnIdx: 0, CallID: "toolu_c", Kind: summary.KindTask, ToolName: "Agent", KeyArg: "Contract lens round 1", StartedAt: at(1)},
			{TurnIdx: 0, CallID: "toolu_q", Kind: summary.KindTask, ToolName: "Agent", KeyArg: "Quality lens round 1", StartedAt: at(1)},
			{TurnIdx: 3, CallID: "toolu_c2", Kind: summary.KindTask, ToolName: "Agent", KeyArg: "Contract lens round 2", StartedAt: at(11)},
			// Described by neither lens nor review: only the record says
			// what it was.
			{TurnIdx: 3, CallID: "toolu_x", Kind: summary.KindTask, ToolName: "Agent", KeyArg: "Second opinion on the diff", StartedAt: at(11)},
		},
	})
	db := openRO(t, f.path)
	inv := fixtureInvocation(t, db)

	// Without records the heuristic reads the transcript alone: toolu_x is
	// no lens dispatch, and the line commits quality to round 2 unmet.
	attempts, err := Lenses(db, inv, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := shapeOf(attempts), "contract/1/1/parsed quality/1/1/parsed contract/2/1/parsed quality/2/0/missing"; got != want {
		t.Fatalf("attempts without records = %v, want %v", got, want)
	}

	// With records: the second contract dispatch is round 1's second try,
	// whatever the line said, and toolu_x is the quality lens of round 2.
	// The line's round-2 contract commitment is then met by nothing.
	attempts, err = Lenses(db, inv, []LensExecution{
		{ExecutionID: "lens-contract-r1-a1", Lens: "contract", Round: 1, Attempt: 1, DispatchID: "toolu_c", StartedAt: at(1)},
		{ExecutionID: "lens-contract-r1-a2", Lens: "contract", Round: 1, Attempt: 2, DispatchID: "toolu_c2", StartedAt: at(11)},
		{ExecutionID: "lens-quality-r2-a1", Lens: "quality", Round: 2, Attempt: 1, DispatchID: "toolu_x", StartedAt: at(11)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := shapeOf(attempts), "contract/1/1/parsed contract/1/2/parsed quality/1/1/parsed contract/2/0/missing quality/2/1/parsed"; got != want {
		t.Fatalf("attempts with records = %v, want %v", got, want)
	}
	if a := attempt(t, attempts, lens.Contract, 1, 1); a.ExecutionID != "lens-contract-r1-a1" || !a.Superseded {
		t.Errorf("contract r1 a1 = %+v, want joined to its record and superseded by the second try", a)
	}
	if a := attempt(t, attempts, lens.Contract, 1, 2); a.ExecutionID != "lens-contract-r1-a2" || a.DispatchID != "toolu_c2" || !a.Successful() {
		t.Errorf("contract r1 a2 = %+v, want toolu_c2 joined to its record and standing", a)
	}
	if a := attempt(t, attempts, lens.Quality, 2, 1); a.ExecutionID != "lens-quality-r2-a1" || a.DispatchID != "toolu_x" || !a.Dispatched || a.Routed || !a.Successful() || a.ResponseTurnIdx != 5 {
		t.Errorf("quality r2 = %+v, want toolu_x as a quality subagent dispatch answered by its notification", a)
	}
	if a := attempt(t, attempts, lens.Quality, 1, 1); a.ExecutionID != "" || !a.Successful() {
		t.Errorf("quality r1 = %+v, want unjoined and standing", a)
	}
}

// A router call whose key argument the 200-char cut ended before `--lens` —
// a scratchpad preamble ahead of the script pushes the flags past the cut
// — reads as no lens dispatch on its own, so it joins the earliest record
// of any lens naming no dispatch that started in its window and takes the
// record's lens, round and attempt. The same cut row with no record in its
// window still opens nothing, and a cut row that chained another command
// after the router is not a router call.
func TestTruncatedRouterCallJoinsItsRecordByWindow(t *testing.T) {
	const (
		ticket = "loom/lens-truncated-0001"
		router = "S=/private/tmp/claude-501/-Users-steve-code-loom/e377ca19-58fa-43ea-bd4c-94f2a6eb0c3d/scratchpad\nmkdir -p $S/lens-logs $S/verdicts\n/Users/steve/.claude/work-policy/d0ad82d1d69e/codex-lens.sh --runtime…"
	)
	at := func(min int) time.Time { return base.Add(time.Duration(min) * time.Minute) }
	verdict := func(name string) string {
		return `{"lens": "` + name + `", "verdict": "satisfied", "summary": "Fine."}`
	}
	f := newFixture(t)
	f.add(&summary.SessionSummary{
		SessionID: "truncated-router",
		Agent:     summary.AgentClaude,
		StartTime: at(0),
		EndTime:   at(30),
		Turns: []summary.Turn{
			{Idx: 0, UserMessage: workInvocation(ticket), StartedAt: at(0),
				AssistantText: "dispatching (" + ticket + " round 1): contract, security"},
			{Idx: 1, UserMessage: notification("toolu_c", verdict("contract")), AssistantText: "Contract in.", StartedAt: at(5)},
		},
		ToolCalls: []summary.ToolCall{
			{TurnIdx: 0, CallID: "toolu_c", Kind: summary.KindTask, ToolName: "Agent", KeyArg: "Contract lens round 1", StartedAt: at(1), DurationMs: 120000},
			{TurnIdx: 0, CallID: "toolu_b", Kind: summary.KindBash, ToolName: "Bash", KeyArg: router, StartedAt: at(2), DurationMs: 10000},
		},
	})
	db := openRO(t, f.path)
	inv := fixtureInvocation(t, db)

	// Without a record the cut row is nothing, and the line's security
	// commitment is unmet.
	attempts, err := Lenses(db, inv, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := shapeOf(attempts), "contract/1/1/parsed security/1/0/missing"; got != want {
		t.Fatalf("attempts without records = %v, want %v", got, want)
	}

	// With the record in the call's window: the row is the record's
	// security dispatch at the record's attempt number, routed, and nothing
	// is missing.
	attempts, err = Lenses(db, inv, []LensExecution{
		{ExecutionID: "lens-security-r1-a1-e52f617f3d55", Lens: "security", Round: 1, Attempt: 1, StartedAt: at(2).Add(2 * time.Second)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := shapeOf(attempts), "contract/1/1/parsed security/1/1/dispatched"; got != want {
		t.Fatalf("attempts with a record = %v, want %v", got, want)
	}
	if a := attempt(t, attempts, lens.Security, 1, 1); a.ExecutionID != "lens-security-r1-a1-e52f617f3d55" || a.DispatchID != "toolu_b" || !a.Routed || !a.Dispatched {
		t.Errorf("security r1 = %+v, want the cut router row joined to its record and routed", a)
	}

	// A record outside the window joins nothing: the cut row stays nothing.
	attempts, err = Lenses(db, inv, []LensExecution{
		{ExecutionID: "lens-security-stray", Lens: "security", Round: 1, Attempt: 1, StartedAt: at(20)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := shapeOf(attempts), "contract/1/1/parsed security/1/0/missing"; got != want {
		t.Fatalf("attempts with a stray record = %v, want %v", got, want)
	}
	for _, a := range attempts {
		if a.ExecutionID != "" {
			t.Errorf("attempt %+v joined the stray record", a)
		}
	}
}

// truncatedRouterCall accepts a cut key argument only when the router is the
// last command before the cut and no `--lens` survived it: a row that names
// a lens, known or not, was classified on that name.
func TestTruncatedRouterCallClassifiesTheCutCommand(t *testing.T) {
	cases := map[string]bool{
		"~/.codex/codex-lens.sh --runtime…":                                    true,
		"S=/tmp/s\nmkdir -p $S/logs\n/x/work-policy/abc/codex-lens.sh --runt…": true,
		"cd /tmp && ~/.codex/codex-lens.sh --runtime claude --payload /tmp/…":  true,
		"~/.codex/codex-lens.sh --runtime claude 2> /tmp/err.log --payload /…": true,
		"~/.codex/codex-lens.sh --runtime claude --lens bogus --round 1 --at…": false,
		"~/.codex/codex-lens.sh --runtime claude --lens security --round 1 -…": false,
		"~/.codex/codex-lens.sh --runtime claude":                              false,
		"~/.codex/codex-lens.sh --runtime claude; cat /tmp/verdict.txt…":       false,
		"~/.codex/codex-lens.sh --runtime claude | tee /tmp/out.txt…":          false,
		"~/.codex/codex-lens.sh --runtime claude && cat /tmp/verdict…":         false,
		"~/.codex/codex-lens.sh --runtime claude\ncat /tmp/verdict.txt…":       false,
		"bash -lc S=/tmp/s\n~/.codex/codex-lens.sh --runtime codex --payload…": true,
		"echo codex-lens.sh is at /x…":                                         false,
		"cat README.md…":                                                       false,
	}
	for keyArg, want := range cases {
		if got := truncatedRouterCall(callRow{toolKind: string(summary.KindBash), keyArg: keyArg}); got != want {
			t.Errorf("truncatedRouterCall(%q) = %v, want %v", keyArg, got, want)
		}
	}
	if truncatedRouterCall(callRow{toolKind: subagentKind, keyArg: "~/.codex/codex-lens.sh --runtime…"}) {
		t.Error("a subagent row read as a cut router call")
	}
}
