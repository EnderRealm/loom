// Package workreport measures /work-run compliance out of the session summary
// database: for every /work run in a date range it reports whether the review
// fan-out was dispatched, how many review rounds ran, what the lenses said, and
// how long its ticket edits spanned. Runs are recognized from transcript content
// alone, so the measurement covers history that was never instrumented.
//
// The report fails closed. A run this parser cannot resolve with confidence is
// classified unknown and is never counted as compliant — an over-reported
// compliance rate is the one failure that makes the whole number useless.
package workreport

import (
	"database/sql"
	"fmt"
	"math"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"loom/internal/parse/summary"
)

// requiredSchemaVersion is the summaries.db schema that introduced the commits
// table (internal/summaries: commitsSchemaVersion). Commit evidence separates a
// run that skipped the fan-out from one that never got that far, so a database
// that cannot hold commits cannot answer this report.
const requiredSchemaVersion = 4

// codexLensScript is the off-runtime lens router. A call to it is dispatch
// evidence on every runtime: on Claude it routes the security lens, on Codex it
// is the only tool row an inlined lens pass leaves behind.
const codexLensScript = "codex-lens.sh"

// codexLensCallRe matches an actual call to that router — the script followed by
// the --lens argument it is always invoked with — rather than the script's name
// anywhere in a command. A bare substring test let an `echo` mentioning the
// script forge the evidence this report exists to measure. The match is
// necessary but not sufficient: analyze also requires the call's recorded
// output to carry a lens verdict.
var codexLensCallRe = regexp.MustCompile(regexp.QuoteMeta(codexLensScript) + `\b[^\n]*--lens\b`)

// subagentKind is the normalized tool kind of a Claude subagent dispatch.
// Matched on the kind rather than the tool name: the same dispatch is recorded
// as "Task" or as "Agent" depending on the client, and both normalize here.
const subagentKind = string(summary.KindTask)

// ticketEditTool is the tk MCP call that moves a ticket's status. Matched as a
// substring: the same tool is registered under several plugin prefixes.
const ticketEditTool = "ticket_edit"

// Classification is the verdict on one run.
type Classification string

const (
	// ClassCompliant: the run committed to a fan-out and this runtime's
	// dispatch evidence backs it up.
	ClassCompliant Classification = "compliant"
	// ClassSkippedFanOut: the run landed a commit with no fan-out at all.
	ClassSkippedFanOut Classification = "skipped_fanout"
	// ClassIncomplete: no commit and no fan-out — blocked at the contract gate
	// or abandoned. Not a compliance failure.
	ClassIncomplete Classification = "incomplete"
	// ClassUnknown: the parser could not resolve the run. Never compliant.
	ClassUnknown Classification = "unknown"
)

// Run is one /work invocation and what it did.
type Run struct {
	Runtime        Runtime        `json:"runtime"`
	Agent          string         `json:"agent"`
	SessionID      string         `json:"session_id"`
	Ticket         string         `json:"ticket"`
	InvokedAt      string         `json:"invoked_at"`
	Classification Classification `json:"classification"`
	// FanOutDispatched is true when this runtime's dispatch evidence is
	// present — subagent lens calls on Claude, a lens router call or inlined
	// lens passes on Codex — not merely when the run said it would dispatch.
	FanOutDispatched bool `json:"fan_out_dispatched"`
	// ReviewIterations is the highest round the run's fan-out commitment lines
	// named, and is null when it wrote none — a run counted compliant on tool
	// evidence alone reports null rather than a self-contradictory zero rounds.
	ReviewIterations     *int `json:"review_iterations"`
	ContaminationReports int  `json:"contamination_reports"`
	// CriteriaUnverified counts the unverified criteria in the run's last
	// contract verdict, and is null when no contract verdict parsed whole — it
	// never means "zero unverified". Nulls are routine: a verdict that came
	// back as a tool result rather than as a task notification is stored cut at
	// 800 chars, and the cut usually lands inside the criteria list.
	CriteriaUnverified *int `json:"criteria_unverified"`
	// OpenToDoneMs is the span from the run's first ticket edit to its last.
	// The report reads that the ticket was edited, never which status an edit
	// set, so this is not a proven open→done transition.
	OpenToDoneMs *int64 `json:"open_to_done_ms"`
	Committed    bool   `json:"committed"`
}

// Totals is the per-classification rollup.
type Totals struct {
	Runs          int `json:"runs"`
	Compliant     int `json:"compliant"`
	SkippedFanOut int `json:"skipped_fanout"`
	Incomplete    int `json:"incomplete"`
	Unknown       int `json:"unknown"`
}

// Report is the whole document. It carries no generation timestamp: two reports
// are meant to be diffed as a before and an after.
type Report struct {
	Since  string `json:"since"`
	Until  string `json:"until"`
	Runs   []Run  `json:"runs"`
	Totals Totals `json:"totals"`
}

// Load reads dbPath and reports every /work run invoked in [since, until).
// A zero since or until is unbounded.
func Load(dbPath string, since, until time.Time) (*Report, error) {
	// A missing database is an error rather than an empty report: "no runs" is
	// itself the number being measured, and a zero that means "nothing to read"
	// reads exactly like a zero that means "nobody ran /work".
	if _, err := os.Stat(dbPath); err != nil {
		return nil, fmt.Errorf("summaries.db not found at %s — run `loom summarize`", dbPath)
	}

	// mode=ro keeps us out of the summarizer's way; it holds the only writer.
	dsn := fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(2000)", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open summaries.db: %w", err)
	}
	defer db.Close()

	if v := schemaVersionOf(db); v < requiredSchemaVersion {
		return nil, fmt.Errorf("summaries.db is at schema %d and predates the commits table (want %d) — run `loom summarize --rebuild`", v, requiredSchemaVersion)
	}

	invocations, err := loadInvocations(db)
	if err != nil {
		return nil, err
	}

	rep := &Report{Runs: []Run{}}
	if !since.IsZero() {
		rep.Since = since.Format(time.RFC3339)
	}
	if !until.IsZero() {
		rep.Until = until.Format(time.RFC3339)
	}

	for _, session := range groupBySession(invocations) {
		// The run boundaries need every invocation in the session, but only
		// the in-range ones are reported — and a session with none of those
		// need not be read at all.
		var wanted []int
		for i, inv := range session {
			if inRange(inv.startedAt, since, until) {
				wanted = append(wanted, i)
			}
		}
		if len(wanted) == 0 {
			continue
		}
		data, err := loadSession(db, session[0].agent, session[0].sessionID)
		if err != nil {
			return nil, err
		}
		for _, i := range wanted {
			// A run runs until the next /work invocation in the same session,
			// else to the end of the session.
			endIdx := math.MaxInt
			endsAt := session[i].sessionEnd
			if i+1 < len(session) {
				endIdx = session[i+1].idx - 1
				endsAt = session[i+1].startedAt
			}
			rep.Runs = append(rep.Runs, analyze(session[i], endIdx, endsAt, data))
		}
	}

	// Stable so runs that tie — same session, or no invocation timestamp at all
	// — keep the query's (agent, session, idx) order rather than an arbitrary
	// one, which is what makes two reports over the same range byte-identical.
	sort.SliceStable(rep.Runs, func(i, j int) bool {
		if rep.Runs[i].InvokedAt != rep.Runs[j].InvokedAt {
			return rep.Runs[i].InvokedAt < rep.Runs[j].InvokedAt
		}
		return rep.Runs[i].SessionID < rep.Runs[j].SessionID
	})

	rep.Totals.Runs = len(rep.Runs)
	for _, r := range rep.Runs {
		switch r.Classification {
		case ClassCompliant:
			rep.Totals.Compliant++
		case ClassSkippedFanOut:
			rep.Totals.SkippedFanOut++
		case ClassIncomplete:
			rep.Totals.Incomplete++
		default:
			rep.Totals.Unknown++
		}
	}
	return rep, nil
}

// invocationRow is one /work invocation turn plus the identity it belongs to.
type invocationRow struct {
	agent        string
	sessionID    string
	idx          int
	ticket       string
	startedAt    time.Time
	sessionStart time.Time
	sessionEnd   time.Time
}

type turnRow struct {
	idx           int
	userMessage   string
	assistantText string
}

type callRow struct {
	toolKind      string
	toolName      string
	keyArg        string
	startedAt     time.Time
	resultSummary string
}

type commitRow struct {
	committedAt time.Time
	subject     string
}

// sessionData is everything one session contributes to its runs.
type sessionData struct {
	turns       []turnRow
	callsByTurn map[int][]callRow
	commits     []commitRow
}

// loadInvocations scans the turns table for /work invocations. The SQL narrows
// the scan to turns that could be one; whether a turn *is* one is decided by
// the parser, which anchors on the start of the message. Deliberately loose —
// SQLite's TRIM strips spaces only, so a prefilter that anchored would drop an
// invocation behind a newline or a system-reminder block the parser handles.
func loadInvocations(db *sql.DB) ([]invocationRow, error) {
	rows, err := db.Query(`
		SELECT t.agent, t.session_id, t.idx, t.user_message, t.started_at, s.start_time, s.end_time
		FROM turns t
		LEFT JOIN sessions s ON s.agent = t.agent AND s.session_id = t.session_id
		WHERE t.user_message LIKE '%<command-name>/work</command-name>%'
		   OR t.user_message LIKE '%<name>work</name>%'
		   OR t.user_message LIKE '%#work%'
		   OR t.user_message LIKE '%$work%'
		ORDER BY t.agent, t.session_id, t.idx
	`)
	if err != nil {
		return nil, fmt.Errorf("query work invocations: %w", err)
	}
	defer rows.Close()

	var out []invocationRow
	for rows.Next() {
		var (
			inv       invocationRow
			message   sql.NullString
			startedAt sql.NullString
			startTime sql.NullString
			endTime   sql.NullString
		)
		if err := rows.Scan(&inv.agent, &inv.sessionID, &inv.idx, &message, &startedAt, &startTime, &endTime); err != nil {
			return nil, err
		}
		ticket, ok := invocation(message.String)
		if !ok {
			continue
		}
		inv.ticket = ticket
		inv.startedAt = parseTime(startedAt)
		inv.sessionStart = parseTime(startTime)
		inv.sessionEnd = parseTime(endTime)
		out = append(out, inv)
	}
	return out, rows.Err()
}

// groupBySession splits invocations into per-session slices, preserving the
// query's (agent, session, idx) order within and across groups.
func groupBySession(invocations []invocationRow) [][]invocationRow {
	var out [][]invocationRow
	for _, inv := range invocations {
		if n := len(out); n > 0 && out[n-1][0].agent == inv.agent && out[n-1][0].sessionID == inv.sessionID {
			out[n-1] = append(out[n-1], inv)
			continue
		}
		out = append(out, []invocationRow{inv})
	}
	return out
}

func loadSession(db *sql.DB, agent, sessionID string) (*sessionData, error) {
	data := &sessionData{callsByTurn: map[int][]callRow{}}

	turns, err := db.Query(`
		SELECT idx, user_message, assistant_text
		FROM turns WHERE agent = ? AND session_id = ? ORDER BY idx
	`, agent, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query turns: %w", err)
	}
	defer turns.Close()
	for turns.Next() {
		var (
			t                  turnRow
			message, assistant sql.NullString
		)
		if err := turns.Scan(&t.idx, &message, &assistant); err != nil {
			return nil, err
		}
		t.userMessage = message.String
		t.assistantText = assistant.String
		data.turns = append(data.turns, t)
	}
	if err := turns.Err(); err != nil {
		return nil, err
	}

	calls, err := db.Query(`
		SELECT turn_idx, tool_kind, tool_name, key_arg, started_at, result_summary
		FROM tool_calls WHERE agent = ? AND session_id = ? ORDER BY seq
	`, agent, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query tool calls: %w", err)
	}
	defer calls.Close()
	for calls.Next() {
		var (
			c                             callRow
			turnIdx                       sql.NullInt64
			kind, name, keyArg, startedAt sql.NullString
			result                        sql.NullString
		)
		if err := calls.Scan(&turnIdx, &kind, &name, &keyArg, &startedAt, &result); err != nil {
			return nil, err
		}
		c.toolKind = kind.String
		c.toolName = name.String
		c.keyArg = keyArg.String
		c.startedAt = parseTime(startedAt)
		c.resultSummary = result.String
		idx := int(turnIdx.Int64)
		data.callsByTurn[idx] = append(data.callsByTurn[idx], c)
	}
	if err := calls.Err(); err != nil {
		return nil, err
	}

	commits, err := db.Query(`
		SELECT committed_at, subject FROM commits WHERE agent = ? AND session_id = ?
	`, agent, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query commits: %w", err)
	}
	defer commits.Close()
	for commits.Next() {
		var (
			c                    commitRow
			committedAt, subject sql.NullString
		)
		if err := commits.Scan(&committedAt, &subject); err != nil {
			return nil, err
		}
		c.committedAt = parseTime(committedAt)
		c.subject = subject.String
		data.commits = append(data.commits, c)
	}
	return data, commits.Err()
}

// analyze measures one run: the turns from its invocation through endIdx, the
// tool calls those turns made, and the commits inside its time window.
func analyze(inv invocationRow, endIdx int, endsAt time.Time, data *sessionData) Run {
	run := Run{
		Runtime:   runtimeOf(inv.agent),
		Agent:     inv.agent,
		SessionID: inv.sessionID,
		Ticket:    inv.ticket,
	}
	if !inv.startedAt.IsZero() {
		run.InvokedAt = inv.startedAt.Format(time.RFC3339)
	}

	var (
		lines    dispatchLines
		verdicts []verdict
		// Lenses the assistant answered as itself (Codex's fan-out shape), kept
		// as a set: two blocks are a fan-out only when they are two lenses.
		inlinedLenses = map[string]bool{}
		agentLens     int
		lensRouter    int
		editTimes     []time.Time
		activity      bool
	)

	// Walked in turn order so "the last contract verdict" means the last one
	// the run actually saw, wherever it arrived from.
	for _, t := range data.turns {
		if t.idx < inv.idx || t.idx > endIdx {
			continue
		}
		lines.scan(t.assistantText)
		// A Claude lens verdict dispatched asynchronously arrives as a task
		// notification on the user side; a Codex one is written inline by the
		// assistant. The third path — a synchronous dispatch, whose verdict is
		// the subagent call's own result — is read off the tool rows below.
		verdicts = append(verdicts, parseVerdicts(t.userMessage)...)
		own := parseVerdicts(t.assistantText)
		for _, v := range own {
			// Only a whole block counts as an inlined pass: a truncated one
			// stands on its lens field alone, which is the one field a quoted
			// template also carries.
			if v.parsed {
				inlinedLenses[v.lens] = true
			}
		}
		verdicts = append(verdicts, own...)
		if strings.TrimSpace(t.assistantText) != "" {
			activity = true
		}

		for _, c := range data.callsByTurn[t.idx] {
			activity = true
			key := strings.ToLower(c.keyArg)
			if c.toolKind == subagentKind && (strings.Contains(key, "lens") || strings.Contains(key, "review")) {
				agentLens++
				verdicts = append(verdicts, parseVerdicts(c.resultSummary)...)
			}
			if codexLensCallRe.MatchString(key) {
				// The command alone is still transcript content: an `echo` of
				// the router's own invocation matches it. A real call's
				// recorded output carries the verdict of the lens it routed,
				// which an echo cannot produce.
				routed := parseVerdicts(c.resultSummary)
				if len(routed) > 0 {
					lensRouter++
				}
				verdicts = append(verdicts, routed...)
			}
			if strings.Contains(c.toolName, ticketEditTool) && !c.startedAt.IsZero() {
				editTimes = append(editTimes, c.startedAt)
			}
		}
	}

	if lines.count > 0 {
		rounds := lines.maxRound
		run.ReviewIterations = &rounds
	}
	// One block reaches us more than once — as a subagent's result and again as
	// the notification for the same dispatch, or re-quoted by the merge — so a
	// report is counted once per distinct (lens, summary).
	counted := map[[2]string]bool{}
	for _, v := range verdicts {
		key := [2]string{v.lens, v.summary}
		if counted[key] {
			continue
		}
		counted[key] = true
		if reportsContamination(v.summary) {
			run.ContaminationReports++
		}
	}
	// The last contract verdict is the one that stands, and only a block that
	// parsed whole can be counted: a truncated one would under-report its
	// unverified criteria.
	for _, v := range verdicts {
		if v.lens != lensContract || !v.parsed {
			continue
		}
		n := 0
		for _, c := range v.criteria {
			if c.Status == statusUnverif {
				n++
			}
		}
		run.CriteriaUnverified = &n
	}
	// Spanned from the extremes rather than the ends: tool rows are ordered by
	// the sequence they were recorded in, which is not always timestamp order,
	// and a first-to-last subtraction can come out negative.
	if len(editTimes) >= 2 {
		first, last := editTimes[0], editTimes[0]
		for _, t := range editTimes[1:] {
			if t.Before(first) {
				first = t
			}
			if t.After(last) {
				last = t
			}
		}
		ms := last.Sub(first).Milliseconds()
		run.OpenToDoneMs = &ms
	}
	run.Committed = committed(inv, endsAt, data.commits)

	// Tool evidence is unambiguous: a lens-named subagent or a call to the lens
	// router. Inlined verdict blocks are evidence too, but only on Codex, where
	// there is no subagent row to find.
	toolEvidence := false
	switch run.Runtime {
	case RuntimeClaude:
		// Two lens subagents, or one plus the routed security lens. The router
		// call is not required on its own: runs predating that routing sent all
		// three as subagents.
		toolEvidence = agentLens >= 2 || (agentLens >= 1 && lensRouter > 0)
	case RuntimeCodex, RuntimeCursor:
		toolEvidence = lensRouter > 0
	}
	// An inlined fan-out has to show two of the three lenses answering, contract
	// and quality among them: those two are the passes that runtime runs in its
	// own context, and requiring both distinct is what a transcript quoting one
	// block twice, or a verdict template, cannot produce.
	inlinedEvidence := inlinesLenses(run.Runtime) && inlinedLenses[lensContract] && inlinedLenses[lensQuality]
	evidence := toolEvidence || inlinedEvidence
	run.FanOutDispatched = evidence
	run.Classification = classify(run.Runtime, lines, evidence, toolEvidence, activity, run.Committed)
	return run
}

// inlinesLenses reports whether a runtime runs its lens passes in its own
// context instead of dispatching subagents. On those runtimes the absence of
// subagent rows is the expected shape, not a skipped fan-out.
func inlinesLenses(r Runtime) bool {
	return r == RuntimeCodex || r == RuntimeCursor
}

// classify resolves a run, refusing to guess. Every branch that is not clearly
// one of the three known shapes lands on unknown.
func classify(runtime Runtime, lines dispatchLines, evidence, toolEvidence, activity, committed bool) Classification {
	switch {
	case runtime == RuntimeUnknown:
		// The fan-out's shape is runtime-specific, so an unnamed runtime's
		// evidence cannot be read either way.
		return ClassUnknown
	case !activity:
		// An invocation with nothing after it: no assistant text, no tool call.
		return ClassUnknown
	case lines.unparseable:
		// A commitment line whose round we cannot read makes the iteration
		// count a guess.
		return ClassUnknown
	case lines.count > 0 && evidence:
		return ClassCompliant
	case lines.count == 0 && toolEvidence:
		// No commitment line, but the dispatch itself is on the record.
		return ClassCompliant
	case lines.count == 0 && !evidence && committed:
		return ClassSkippedFanOut
	case lines.count == 0 && !evidence && !committed:
		return ClassIncomplete
	default:
		// Conflicting signals — a line with no dispatch behind it, or inlined
		// blocks with no line to attribute them to.
		return ClassUnknown
	}
}

// committed reports whether the run landed a commit: one inside its time window,
// or one whose subject names its ticket.
func committed(inv invocationRow, endsAt time.Time, commits []commitRow) bool {
	for _, c := range commits {
		if subjectNamesTicket(c.subject, inv.ticket) {
			return true
		}
		if inv.startedAt.IsZero() || c.committedAt.IsZero() {
			continue
		}
		// A commit whose bash row carried no parseable timestamp is stored
		// with the session's start time (internal/summaries writeCommits),
		// which lands in the first run's window whichever run made it. With
		// no subject naming this run's ticket there is nothing to place it,
		// so it is not matched on the window either.
		if !inv.sessionStart.IsZero() && c.committedAt.Equal(inv.sessionStart) {
			continue
		}
		if c.committedAt.Before(inv.startedAt) {
			continue
		}
		if endsAt.IsZero() || c.committedAt.Before(endsAt) {
			return true
		}
	}
	return false
}

// inRange places a run's invocation in [since, until). A run whose invocation
// carries no timestamp cannot be placed, so it is excluded whenever a bound is
// set and included when the range is unbounded.
func inRange(t, since, until time.Time) bool {
	if since.IsZero() && until.IsZero() {
		return true
	}
	if t.IsZero() {
		return false
	}
	if !since.IsZero() && t.Before(since) {
		return false
	}
	if !until.IsZero() && !t.Before(until) {
		return false
	}
	return true
}

func parseTime(s sql.NullString) time.Time {
	if !s.Valid {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s.String)
	if err != nil {
		return time.Time{}
	}
	return t
}

// schemaVersionOf reads the DB's schema marker. A local copy of the summaries
// package's own reader: this package opens the database read-only for itself
// and needs nothing else from that package's internals.
func schemaVersionOf(db *sql.DB) int {
	var v sql.NullString
	if err := db.QueryRow(`SELECT value FROM schema_meta WHERE key = 'schema_version'`).Scan(&v); err != nil {
		return 0
	}
	if !v.Valid {
		return 0
	}
	n, err := strconv.Atoi(v.String)
	if err != nil {
		return 0
	}
	return n
}
