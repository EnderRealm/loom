package workreport

import (
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"loom/internal/parse/lens"
	"loom/internal/parse/summary"
)

// lensSchemaVersion is the summaries.db schema that added the lens_responses
// tables (internal/summaries: lensSchemaVersion). The attempt model and the
// compliance report both read them, so an older database cannot answer either.
const lensSchemaVersion = 9

// Status values of a LensAttempt.
const (
	// AttemptMissing: a commitment line named the lens for the round and no
	// dispatch or response followed.
	AttemptMissing = "missing"
	// AttemptDispatched: the dispatch is on the record and nothing answered
	// it — a verdict redirected to a file and never read back, or still
	// running when the transcript ended.
	AttemptDispatched = "dispatched"
	// AttemptFailed: the dispatch's own result was an error and carried no
	// verdict.
	AttemptFailed = "failed"
	// AttemptResponded: a response landed but was malformed — cut short, not
	// JSON, or naming no known lens or verdict.
	AttemptResponded = "responded"
	// AttemptParsed: a whole verdict naming a known lens and verdict landed.
	AttemptParsed = "parsed"
)

// SourceRef locates the transcript record a response was read from.
type SourceRef struct {
	Path string `json:"path"`
	Line int    `json:"line"`
}

// LensAttempt is one try at one lens in one review round: what was dispatched,
// what came back, and how it stands against the round's other tries. Round 0
// holds attempts no commitment line placed. DispatchTurnIdx and
// ResponseTurnIdx are -1 where there was no dispatch or no response; Attempt
// is 0 for a missing attempt, which was never tried. A stored response is
// evidence of what the orchestrator saw, never proof the review passed.
type LensAttempt struct {
	Lens    string `json:"lens"`
	Round   int    `json:"round"`
	Attempt int    `json:"attempt"`
	Status  string `json:"status"`
	// Dispatched is false for an attempt known only from its response — a
	// Codex inlined pass, or a verdict read back with no dispatch on record.
	Dispatched      bool   `json:"dispatched"`
	DispatchID      string `json:"dispatch_id"`
	DispatchTurnIdx int    `json:"dispatch_turn_idx"`
	ResponseID      string `json:"response_id"`
	ResponseTurnIdx int    `json:"response_turn_idx"`
	Verdict         string `json:"verdict"`
	Summary         string `json:"summary"`
	ContextKind     string `json:"context_kind"`
	ContextState    string `json:"context_state"`
	Contaminated    bool   `json:"contaminated"`
	// Superseded is true when a later attempt exists for the same lens and
	// round: this one is history, whatever it said.
	Superseded bool `json:"superseded"`
	// Late is true when the response landed after the next round's
	// commitment line, or after the next /work invocation.
	Late bool `json:"late"`
	// Malformed is the reason a responded attempt did not parse.
	Malformed string     `json:"malformed"`
	Source    *SourceRef `json:"source"`
}

// Successful reports whether the attempt stands as a parsed verdict: a whole
// response that no later attempt replaced. It says nothing about what the
// verdict was.
func (a LensAttempt) Successful() bool {
	return a.Status == AttemptParsed && !a.Superseded
}

// Lenses returns the attempt model of one run: every lens attempt in the
// invocation's span, in (round, lens, attempt) order.
func Lenses(db *sql.DB, inv Invocation) ([]LensAttempt, error) {
	if v := schemaVersionOf(db); v < lensSchemaVersion {
		return nil, fmt.Errorf("summaries.db is at schema %d and predates the lens_responses table (want %d) — run `loom summarize --rebuild`", v, lensSchemaVersion)
	}
	data, err := loadSession(db, inv.Agent, inv.SessionID)
	if err != nil {
		return nil, err
	}
	return lensAttempts(runtimeOf(inv.Agent), inv.TurnIdx, inv.EndIdx, data), nil
}

// lensRow is one lens_responses row.
type lensRow struct {
	responseID   string
	turnIdx      int
	origin       string
	dispatchID   string
	sourcePath   string
	sourceLine   int
	lens         string
	verdict      string
	summary      string
	status       string
	reason       string
	contextKind  string
	contextState string
	criteriaJSON string
}

// contaminated reads the row the way lens.Contaminated reads a block: the
// structured state when the response carried one, else the summary's prose.
func (r lensRow) contaminated() bool {
	b := lens.Block{Summary: r.summary}
	if r.contextKind == lens.ContextStructured {
		b.Context = &lens.Context{State: r.contextState}
	}
	return lens.Contaminated(b)
}

// lensArgRe reads the lens a codex-lens.sh call routed; shellWrapperRe
// matches the `bash -lc` prefix a Codex shell call carries, its command
// array joined by spaces; routerInvocation matches one router invocation
// past that wrapper — env assignments, the script, then words free of every
// operator that would join another command or move its stdout (`;`, `&`,
// `|`, `>`, a newline), a `2> <path>` stderr redirect aside. routerAloneRe
// is that invocation and nothing else; routerRedirectRe is that invocation
// followed by exactly one `> <path>` stdout redirect, a `2> <path>` on
// either side of it aside, and nothing else, capturing the path. An append
// (`>>`) is not a redirect the read-back can be attributed to: the file can
// already hold an earlier round's verdict, which the read would deliver
// first. A key argument cut at 200 chars ends in the truncation mark and is
// not matched by either.
var (
	routerInvocation = `^(?:\w+=[^\s;&|>…]*[ \t]+)*[^\s;&|>…]*` + regexp.QuoteMeta(codexLensScript) + `(?:[ \t]+(?:2>[ \t]*)?[^\s;&|>…]+)*`
	lensArgRe        = regexp.MustCompile(`--lens\s+(\w+)`)
	shellWrapperRe   = regexp.MustCompile(`^(?:bash|sh|zsh)\s+-[a-z]*c[a-z]*\s+`)
	routerAloneRe    = regexp.MustCompile(routerInvocation + `$`)
	routerRedirectRe = regexp.MustCompile(routerInvocation + `[ \t]+>[ \t]*([^\s;&|>…]+)(?:[ \t]+2>[ \t]*[^\s;&|>…]+)?$`)
)

// dispatchLens names the lens a tool row dispatched, or "" when the row is
// not a lens dispatch: a subagent row that reads as a lens dispatch and
// names a lens, or a call to the lens router naming one.
func dispatchLens(c callRow) string {
	key := strings.ToLower(c.keyArg)
	if c.toolKind == subagentKind {
		if !lensSubagent(c) {
			return ""
		}
		for _, name := range lensOrder {
			if strings.Contains(key, name) {
				return name
			}
		}
		return ""
	}
	if codexLensCallRe.MatchString(key) {
		if m := lensArgRe.FindStringSubmatch(key); m != nil && lens.KnownLenses[m[1]] {
			return m[1]
		}
	}
	return ""
}

// routerAlone reports whether a router command's result is the router's own
// stdout: past the shell wrapper it is a single, unchained invocation with
// its stdout unredirected. A command that redirected its stdout, or joined
// another command — `codex-lens.sh … > verdict.txt; cat README.md` — has a
// result that cannot be attributed to the router, since anything the other
// command printed can hold a block shaped like a verdict; it is not
// classified and does not pair. Anything else unclassifiable fails closed.
func routerAlone(keyArg string) bool {
	return routerAloneRe.MatchString(shellWrapperRe.ReplaceAllString(strings.TrimSpace(keyArg), ""))
}

// routerRedirect returns the path a router command sent its stdout to, or ""
// when it cannot be attributed to the router: past the shell wrapper the
// command must be the router invocation routerAlone accepts followed by one
// `> <path>` and nothing else. A redirect anywhere in a compound command —
// `codex-lens.sh …; cat README.md > notes.txt` — belongs to whichever
// command precedes it, and recording it would let a later read of that file
// hand another command's output to the dispatch; an append (`>> <path>`)
// keeps whatever the file held, so a read of it would hand an earlier
// round's verdict to the dispatch. Neither records a path, and the attempt
// stays dispatched.
func routerRedirect(keyArg string) string {
	m := routerRedirectRe.FindStringSubmatch(shellWrapperRe.ReplaceAllString(strings.TrimSpace(keyArg), ""))
	if m == nil {
		return ""
	}
	return strings.Trim(m[1], `"'`)
}

// readsBack reports whether a tool row is the kind whose result can carry a
// verdict the run read back from a file — a shell call. A file-reading tool's
// result is not paired: a diff or a doc it reads can quote verdict blocks that
// answer nothing.
func readsBack(c callRow) bool {
	return c.toolKind == string(summary.KindBash) || c.toolKind == string(summary.KindCustom)
}

// readsOnly reports whether a command's key argument is a cat of path and
// nothing else: past the shell wrapper, the program is `cat` — bare, or a
// `/`-path ending in `/cat` — followed by an optional `--` and then exactly
// the one path, bare or quoted. Any other program, any other flag, a second
// operand, a chain, a pipeline — anything the command's output cannot be
// attributed to that file alone — is not classified and does not pair:
// `cat README.md <path>` would hand the README's first verdict-shaped block
// to the dispatch, and `rg --passthru <path>` prints a search's output, not
// the file as written.
func readsOnly(keyArg, path string) bool {
	words := strings.Fields(shellWrapperRe.ReplaceAllString(strings.TrimSpace(keyArg), ""))
	if len(words) == 0 || (words[0] != "cat" && !strings.HasSuffix(words[0], "/cat")) {
		return false
	}
	words = words[1:]
	if len(words) > 0 && words[0] == "--" {
		words = words[1:]
	}
	return len(words) == 1 && strings.Trim(words[0], `"'`) == path
}

// lensWalk accumulates attempts over a run's turns.
type lensWalk struct {
	attempts []*LensAttempt
	// byDispatch indexes the latest attempt opened by each dispatch id;
	// redirects holds the path each router dispatch redirected its output
	// to, which is what a later shell read of that path answers.
	byDispatch map[string]*LensAttempt
	redirects  map[string]string
	// round is the current round; committed is what each round's commitment
	// lines named.
	round     int
	committed map[int][]string
	// pending is the current turn's commitment lines not yet applied to a
	// tool dispatch, and applied whether any of them has been.
	pending []commitment
	applied bool
}

// lensAttempts walks the turns from startIdx through endIdx in order and
// returns the run's attempts.
//
// Within a turn the order is: responses on the user side, which open the
// turn; then the turn's tool rows in sequence, each dispatch with its own
// result — a subagent's, or a router call's when the command was the router
// alone, since a redirected or compound router command's result is not the
// router's output and its blocks stay in the store as evidence only, the
// attempt dispatched until an exclusive read of the path the router alone
// redirected to pairs it, which a compound command never records;
// then the user-side responses whose dispatch was only on record
// after those rows; then the assistant text, its commitment lines and inlined
// blocks in text order. A tool row has no position among the commitment
// lines, so the turn's first line is applied at its first lens dispatch and
// each later one when a lens that already answered in the current round is
// dispatched again — a retry follows a failed, malformed or unanswered attempt
// and does not open a round. The text pass then applies every line where it
// sits, which is idempotent, so an inlined block lands in the round of the
// line before it.
// Inlined blocks are placed only on a runtime that inlines its passes; on
// Claude the assistant quoting a verdict is not a lens answering.
func lensAttempts(runtime Runtime, startIdx, endIdx int, data *sessionData) []LensAttempt {
	w := &lensWalk{
		byDispatch: map[string]*LensAttempt{}, redirects: map[string]string{},
		committed: map[int][]string{},
	}
	for _, t := range data.turns {
		if t.idx < startIdx || t.idx > endIdx {
			continue
		}
		var inlined, deferred []lensRow
		for _, r := range data.lensesByTurn[t.idx] {
			switch r.origin {
			case summary.OriginTaskNotification:
				// A notification queued mid-turn lands in the turn of the
				// dispatch it answers; it is read again once the turn's
				// dispatches are on record. A user row that is not a
				// notification is not placed: no lens answers as a plain user
				// message, so it is quoted material.
				if r.dispatchID != "" && w.byDispatch[r.dispatchID] == nil {
					deferred = append(deferred, r)
					continue
				}
				w.respond(r)
			case summary.OriginAssistant:
				inlined = append(inlined, r)
			}
		}
		w.pending = commitments(t.assistantText)
		w.applied = false
		for _, c := range data.callsByTurn[t.idx] {
			if name := dispatchLens(c); name != "" {
				w.dispatch(name, c, t.idx)
				if c.toolKind == subagentKind || routerAlone(c.keyArg) {
					for _, r := range data.lensesByCall[c.callID] {
						w.respond(r)
					}
				}
			} else if readsBack(c) {
				for _, r := range data.lensesByCall[c.callID] {
					w.readBack(c, r)
				}
			}
		}
		for _, r := range deferred {
			w.respond(r)
		}
		if !inlinesLenses(runtime) {
			inlined = nil
		}
		w.text(t.assistantText, inlined)
	}
	// A notification for one of this run's dispatches can land after the
	// next invocation: it still answers the attempt, late.
	for _, r := range data.lenses {
		if r.turnIdx <= endIdx || r.origin != summary.OriginTaskNotification {
			continue
		}
		if a := w.byDispatch[r.dispatchID]; a != nil && a.ResponseID == "" {
			w.pair(a, r)
			a.Late = true
		}
	}
	for round, names := range w.committed {
		for _, name := range names {
			if w.latest(name, round) == nil {
				w.attempts = append(w.attempts, &LensAttempt{
					Lens: name, Round: round, Status: AttemptMissing,
					DispatchTurnIdx: -1, ResponseTurnIdx: -1,
				})
			}
		}
	}
	for _, a := range w.attempts {
		if l := w.latest(a.Lens, a.Round); l != nil && l != a {
			a.Superseded = true
		}
	}
	out := make([]LensAttempt, 0, len(w.attempts))
	for _, a := range w.attempts {
		out = append(out, *a)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Round != out[j].Round {
			return out[i].Round < out[j].Round
		}
		if ri, rj := lensRank(out[i].Lens), lensRank(out[j].Lens); ri != rj {
			return ri < rj
		}
		if out[i].Lens != out[j].Lens {
			return out[i].Lens < out[j].Lens
		}
		return out[i].Attempt < out[j].Attempt
	})
	return out
}

// text walks one turn's assistant text: every commitment line is applied
// where it sits, and the k-th lens block in the text places the k-th inlined
// response of the turn — the text is the turn's records joined in order, and
// so are the rows. Rows past the blocks the join shows are placed at the end.
func (w *lensWalk) text(assistantText string, inlined []lensRow) {
	lines := commitments(assistantText)
	blocks := lens.Extract(assistantText)
	for len(lines) > 0 || len(blocks) > 0 {
		if len(blocks) == 0 || (len(lines) > 0 && lines[0].offset < blocks[0].Offset) {
			w.apply(lines[0])
			lines = lines[1:]
			continue
		}
		if len(inlined) > 0 {
			w.respond(inlined[0])
			inlined = inlined[1:]
		}
		blocks = blocks[1:]
	}
	for _, r := range inlined {
		w.respond(r)
	}
	w.pending = nil
}

// lensRank orders the known lenses as lensOrder does, and anything else after.
func lensRank(name string) int {
	for i, n := range lensOrder {
		if n == name {
			return i
		}
	}
	return len(lensOrder)
}

// apply makes a commitment line's round current and records what it named.
func (w *lensWalk) apply(c commitment) {
	w.round = c.round
	for _, name := range c.lenses {
		found := false
		for _, have := range w.committed[c.round] {
			if have == name {
				found = true
			}
		}
		if !found {
			w.committed[c.round] = append(w.committed[c.round], name)
		}
	}
}

// latest is the highest-numbered attempt for a lens in a round, or nil.
func (w *lensWalk) latest(name string, round int) *LensAttempt {
	var out *LensAttempt
	for _, a := range w.attempts {
		if a.Lens == name && a.Round == round && (out == nil || a.Attempt > out.Attempt) {
			out = a
		}
	}
	return out
}

// open adds the next attempt for a lens in a round.
func (w *lensWalk) open(name string, round int, dispatched bool, dispatchID string, turnIdx int) *LensAttempt {
	a := &LensAttempt{
		Lens: name, Round: round, Attempt: 1,
		Dispatched: dispatched, DispatchID: dispatchID, DispatchTurnIdx: turnIdx,
		ResponseTurnIdx: -1,
	}
	if dispatched {
		a.Status = AttemptDispatched
	}
	if prev := w.latest(name, round); prev != nil {
		a.Attempt = prev.Attempt + 1
	}
	w.attempts = append(w.attempts, a)
	if dispatchID != "" {
		w.byDispatch[dispatchID] = a
	}
	return a
}

// dispatch opens an attempt for a lens dispatch row. A row whose own result
// was an error starts failed; a verdict in that result, paired right after,
// overrides it.
func (w *lensWalk) dispatch(name string, c callRow, turnIdx int) {
	if len(w.pending) > 0 {
		prev := w.latest(name, w.round)
		if !w.applied || (prev != nil && prev.Status == AttemptParsed) {
			w.apply(w.pending[0])
			w.pending = w.pending[1:]
			w.applied = true
		}
	}
	a := w.open(name, w.round, true, c.callID, turnIdx)
	if c.isError || (c.exitCode != nil && *c.exitCode != 0) {
		a.Status = AttemptFailed
	}
	if c.toolKind != subagentKind {
		if path := routerRedirect(c.keyArg); path != "" {
			w.redirects[c.callID] = path
		}
	}
}

// readBack places a verdict a shell call's result carried. It answers the
// unanswered dispatched attempt whose router call redirected its output to a
// path the reading command reads alone, and nothing else: with no such
// provenance the response stays in the store as evidence only, since any
// document the run read — a README fetched from anywhere — can hold a block
// shaped like a verdict, and a read of that file beside the README cannot say
// which block came from which. The key argument is the command cut at 200
// chars, so a redirect past the cut is not seen and the pairing fails closed.
func (w *lensWalk) readBack(c callRow, r lensRow) {
	var target *LensAttempt
	for _, a := range w.attempts {
		path, ok := w.redirects[a.DispatchID]
		if ok && a.Lens == r.lens && a.Status == AttemptDispatched && readsOnly(c.keyArg, path) {
			target = a
		}
	}
	if target != nil {
		w.pair(target, r)
	}
}

// respond places a response. One naming an attempt by dispatch id answers it,
// or opens a further attempt on the same dispatch, in the dispatch's round,
// when that one already answered — a task can notify more than once. A task
// notification places only that way: one whose dispatch id is not one of
// this run's lens attempts answers something else — another run's dispatch,
// a task that is not a lens — and one naming no dispatch at all is not a
// notification the harness posted but text quoting the marker — a pasted
// ticket, a compaction summary — so neither is placed, and both stay in the
// store as evidence only, as does a response naming no known lens.
// Otherwise — an inlined pass, an assistant row on a runtime that inlines —
// it answers the latest unanswered dispatched attempt of its lens in the
// current round, else opens an undispatched one. Two more never come here: a
// shell read-back, which readBack holds to its provenance, and a Claude user
// row that is not a task notification — a compaction summary reproducing a
// verdict, a human pasting one — since no lens answers as a plain user
// message and placing it would supersede the real answer.
func (w *lensWalk) respond(r lensRow) {
	if a := w.byDispatch[r.dispatchID]; a != nil {
		if a.ResponseID != "" {
			a = w.open(a.Lens, a.Round, true, a.DispatchID, a.DispatchTurnIdx)
		}
		w.pair(a, r)
		return
	}
	if r.origin == summary.OriginTaskNotification {
		return
	}
	if !lens.KnownLenses[r.lens] {
		return
	}
	if a := w.latest(r.lens, w.round); a != nil && a.Status == AttemptDispatched {
		w.pair(a, r)
		return
	}
	w.pair(w.open(r.lens, w.round, false, "", -1), r)
}

// pair records a response on an attempt.
func (w *lensWalk) pair(a *LensAttempt, r lensRow) {
	a.ResponseID = r.responseID
	a.ResponseTurnIdx = r.turnIdx
	a.Verdict, a.Summary = r.verdict, r.summary
	a.ContextKind, a.ContextState = r.contextKind, r.contextState
	a.Contaminated = r.contaminated()
	a.Malformed = r.reason
	a.Source = &SourceRef{Path: r.sourcePath, Line: r.sourceLine}
	if r.status == lens.StatusParsed {
		a.Status = AttemptParsed
	} else {
		a.Status = AttemptResponded
	}
	if a.Round < w.round {
		a.Late = true
	}
}
