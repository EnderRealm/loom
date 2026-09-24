package workreport

import (
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

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
	// JSON, naming no known lens or verdict, or naming a known lens other
	// than the one dispatched.
	AttemptResponded = "responded"
	// AttemptParsed: a whole verdict naming a known lens and verdict landed.
	AttemptParsed = "parsed"
)

// Provenance values of a LensAttempt: where the paired response came from
// when the walk could attribute it to the router.
const (
	// ProvenanceRouterResult: the router call's own result, the command
	// being the router alone.
	ProvenanceRouterResult = "router_result"
	// ProvenanceReadBack: an exclusive read of the path the router alone
	// redirected its output to.
	ProvenanceReadBack = "read_back"
	// ProvenanceChildSession: the final assistant response in the routed
	// execution's declared transcript.
	ProvenanceChildSession = "child_session"
)

// SourceRef locates the transcript record a response was read from.
type SourceRef struct {
	Path string `json:"path"`
	Line int    `json:"line"`
}

// LensAttempt is one try at one lens in one review round: what was dispatched,
// what came back, and how it stands against the round's other tries. Round 0
// holds attempts neither a commitment line nor a joined execution record
// placed. DispatchTurnIdx and ResponseTurnIdx are -1 where there was no
// dispatch or no response; Attempt is 0 for a missing attempt, which was
// never tried. A stored response is evidence of what the orchestrator saw,
// never proof the review passed.
type LensAttempt struct {
	Lens    string `json:"lens"`
	Round   int    `json:"round"`
	Attempt int    `json:"attempt"`
	Status  string `json:"status"`
	// Dispatched is false for an attempt known only from its response — a
	// Codex inlined pass, or a verdict read back with no dispatch on record.
	Dispatched bool `json:"dispatched"`
	// Routed is true when the dispatch was a call to the lens router rather
	// than a subagent.
	Routed bool `json:"routed"`
	// Provenance is where the paired response came from when it was the
	// router's own result, a read-back of its redirect, or its recorded
	// child session; "" for a subagent's notification, an inlined pass, or
	// no response. A routed attempt an inlined verdict answered has none.
	Provenance      string `json:"provenance"`
	DispatchID      string `json:"dispatch_id"`
	DispatchTurnIdx int    `json:"dispatch_turn_idx"`
	// ExecutionID names the recorded lens execution this dispatch joined,
	// "" when none did.
	ExecutionID     string `json:"execution_id"`
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

// LensExecution is a recorded lens execution (docs/execution-records.md) of
// the run whose attempts are read: what the record declared about the lens
// it ran, and the dispatch it named when it could. Round and Attempt are 0
// where the record carried none.
type LensExecution struct {
	ExecutionID string
	Lens        string
	Round       int
	Attempt     int
	DispatchID  string
	Agent       string
	SessionID   string
	StartedAt   time.Time
}

// Lenses returns the attempt model of one run: every lens attempt in the
// invocation's span, in (round, lens, attempt) order. execs are the run's
// recorded lens executions, each joined to the dispatch it names or to the
// router call whose window holds its start; a transcript-recognized run has
// none.
func Lenses(db *sql.DB, inv Invocation, execs []LensExecution) ([]LensAttempt, error) {
	if v := SchemaVersionOf(db); v < lensSchemaVersion {
		return nil, fmt.Errorf("summaries.db is at schema %d and predates the lens_responses table (want %d) — run `loom summarize --rebuild`", v, lensSchemaVersion)
	}
	data, err := loadSession(db, inv.Agent, inv.SessionID)
	if err != nil {
		return nil, err
	}
	attempts := lensAttempts(runtimeOf(inv.Agent), inv.TurnIdx, inv.EndIdx, data, execs)
	routed := map[string]bool{}
	for _, a := range attempts {
		if a.Routed && a.ResponseID == "" && a.ExecutionID != "" {
			routed[a.ExecutionID] = true
		}
	}
	children, err := loadChildLensResponses(db, inv, execs, routed)
	if err != nil {
		return nil, err
	}
	w := lensWalk{}
	for i := range attempts {
		if r, ok := children[attempts[i].ExecutionID]; ok {
			w.pair(&attempts[i], r)
			attempts[i].Provenance = ProvenanceChildSession
		}
	}
	return attempts, nil
}

// loadChildLensResponses reads the final assistant verdict from each routed
// execution's declared transcript. The execution identity is the join: a
// response from any other session is unrelated, whatever its lens or timing.
func loadChildLensResponses(db *sql.DB, inv Invocation, execs []LensExecution, routed map[string]bool) (map[string]lensRow, error) {
	out := map[string]lensRow{}
	for _, e := range execs {
		if !routed[e.ExecutionID] || e.Agent == "" || e.SessionID == "" ||
			(e.Agent == inv.Agent && e.SessionID == inv.SessionID) {
			continue
		}
		data, err := loadSession(db, e.Agent, e.SessionID)
		if err != nil {
			return nil, err
		}
		for i := len(data.lenses) - 1; i >= 0; i-- {
			if data.lenses[i].origin == summary.OriginAssistant {
				out[e.ExecutionID] = data.lenses[i]
				break
			}
		}
	}
	return out, nil
}

// lensRow is one lens_responses row.
type lensRow struct {
	responseID   string
	turnIdx      int
	origin       string
	dispatchID   string
	sourcePath   string
	sourceLine   int
	at           time.Time
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
// not matched by either. truncatedRouterRe matches a router call the cut
// took `--lens` from: the script as the program of its command — at the
// start, after an operator, env assignments ahead of it — then words free
// of every operator that would join another command (`;`, `&`, `|`, a
// newline), then the mark; so a command that chained something else after
// the router is not one, and neither is an `echo` mentioning the script.
var (
	routerInvocation  = `^(?:\w+=[^\s;&|>…]*[ \t]+)*[^\s;&|>…]*` + regexp.QuoteMeta(codexLensScript) + `(?:[ \t]+(?:2>[ \t]*)?[^\s;&|>…]+)*`
	lensArgRe         = regexp.MustCompile(`--lens\s+(\w+)`)
	shellWrapperRe    = regexp.MustCompile(`^(?:bash|sh|zsh)\s+-[a-z]*c[a-z]*\s+`)
	routerAloneRe     = regexp.MustCompile(routerInvocation + `$`)
	routerRedirectRe  = regexp.MustCompile(routerInvocation + `[ \t]+>[ \t]*([^\s;&|>…]+)(?:[ \t]+2>[ \t]*[^\s;&|>…]+)?$`)
	truncatedRouterRe = regexp.MustCompile(`(?:^|[\n;&|][ \t]*)(?:\w+=[^\s;&|>…]*[ \t]+)*[^\s;&|>…]*` + regexp.QuoteMeta(codexLensScript) + `\b[^\n;&|…]*…$`)
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

// truncatedRouterCall reports whether a shell row's key argument is a router
// call the 200-char cut ended before the flags that name its lens: the cut
// row cannot say which lens it routed, but it is still the router, so the
// record its window holds says for it. A cut row `--lens` survived on named
// its lens and was judged on it — an unknown name there is not a cue to
// take any lens's record.
func truncatedRouterCall(c callRow) bool {
	if c.toolKind == subagentKind {
		return false
	}
	cmd := shellWrapperRe.ReplaceAllString(strings.TrimSpace(strings.ToLower(c.keyArg)), "")
	return !lensArgRe.MatchString(cmd) && truncatedRouterRe.MatchString(cmd)
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
	// execs is the run's recorded lens executions; joined marks the ones a
	// dispatch has taken, and joins holds each dispatch's execution by call
	// id, as an index into execs.
	execs  []LensExecution
	joined []bool
	joins  map[string]int
}

// joinSlack is how far a record's start may sit outside its router call's
// window: the record is written by the routed script with second precision
// and the call's timestamps by the harness, so the two clocks are not the
// same clock.
const joinSlack = 5 * time.Second

// lensAttempts walks the turns from startIdx through endIdx in order and
// returns the run's attempts.
//
// Within a turn the order is: responses on the user side, which open the
// turn; then the turn's tool rows in sequence, each dispatch with its own
// result — a subagent's, or a router call's when the command was the router
// alone, since a redirected or compound router command's result is not the
// router's output and its blocks stay in the store as evidence only, the
// attempt dispatched until an exclusive read of the path the router alone
// redirected to pairs it, which a compound command never records; then the
// user-side responses whose dispatch was not on record when the turn opened,
// still unplaced; then the assistant text, its commitment lines and inlined
// blocks in text order. Such a response — a notification or a subagent's
// hand-back landing mid-turn, a whole /work run being one turn when no human
// message follows the invocation — takes its position among the tool rows
// by time instead, once its dispatch is on record: it is placed ahead of the
// first timed call it does not follow, so a lens that answered before its
// next round's dispatch reads as answered there and that dispatch applies
// the next commitment line. A file-write row — an inlined pass the agent
// wrote to a file — is a tool row too, and takes its position among the
// turn's tool rows by time: it is placed ahead of the first timed call it
// does not follow, and after the last call otherwise. A tool row has no
// position among the commitment lines, so the turn's first line is applied
// at its first lens dispatch and each later one when a lens that already
// answered in the current round is dispatched again — a retry follows a
// failed, malformed or unanswered attempt and does not open a round; a
// file-write row applies a line the same way, since a second verdict of a
// lens written in the same round is the next round's. The text pass then
// applies every line where it sits, which is idempotent, so an inlined block
// lands in the round of the line before it.
// Inlined blocks and file-write rows are placed only on a runtime that
// inlines its passes; on Claude the assistant quoting a verdict is not a
// lens answering.
//
// Each tool row joins at most one of execs, and each record joins once: the
// record naming the row's call id, else — for a router call, whose record
// cannot know its own call id — the earliest unjoined record of the same
// lens naming no dispatch whose start falls in the call's window, joinSlack
// either side — of any lens, for a router call whose key argument was cut
// before `--lens`. A row a record names by dispatch id is a dispatch of the
// record's lens whether or not its key argument reads as one, and its
// attempt takes the record's round and attempt number where the record
// carries them: the record is the producer's word, and the key-argument
// heuristic and the commitment line are the fallback for rows without one.
// A turn whose assistant text holds no commitment line — a Claude transcript
// since 2.1.268 keeps none — takes the walk's round from the records its
// dispatches joined instead: one line per joined row naming the record's
// round and no lenses, applied the same way, so nothing derives a missing
// attempt from a record. A text line, where one exists, still moves the
// walk's round for the unjoined rows and inlined passes.
func lensAttempts(runtime Runtime, startIdx, endIdx int, data *sessionData, execs []LensExecution) []LensAttempt {
	w := &lensWalk{
		byDispatch: map[string]*LensAttempt{}, redirects: map[string]string{},
		committed: map[int][]string{},
		execs:     execs, joined: make([]bool, len(execs)), joins: map[string]int{},
	}
	for _, t := range data.turns {
		if t.idx < startIdx || t.idx > endIdx {
			continue
		}
		var inlined, written, deferred []lensRow
		for _, r := range data.lensesByTurn[t.idx] {
			switch r.origin {
			case summary.OriginTaskNotification:
				// A notification queued mid-turn lands in the turn of the
				// dispatch it answers; it is placed among the turn's tool
				// rows by time once its dispatch is on record, or after
				// them. A user row that is not a notification is not
				// placed: no lens answers as a plain user message, so it is
				// quoted material.
				if r.dispatchID != "" && w.byDispatch[r.dispatchID] == nil {
					deferred = append(deferred, r)
					continue
				}
				w.respond(r)
			case summary.OriginAssistant:
				inlined = append(inlined, r)
			case summary.OriginFileWrite:
				written = append(written, r)
			}
		}
		if !inlinesLenses(runtime) {
			inlined, written = nil, nil
		}
		w.pending = commitments(t.assistantText)
		w.applied = false
		// The joins are settled before the rows are walked: with no
		// commitment line, the round the first dispatch opens is read off
		// whichever record any of the turn's rows joined.
		recorded := len(w.pending) == 0
		for _, c := range data.callsByTurn[t.idx] {
			i := w.join(dispatchLens(c), c)
			if i < 0 {
				continue
			}
			w.joins[c.callID] = i
			w.joined[i] = true
			if recorded && execs[i].Round > 0 {
				w.pending = append(w.pending, commitment{round: execs[i].Round})
			}
		}
		for _, c := range data.callsByTurn[t.idx] {
			// The rows are in stored order, which is transcript order, so
			// the head is the earliest not yet placed.
			for len(written) > 0 && !c.startedAt.IsZero() && !written[0].at.After(c.startedAt) {
				w.write(written[0])
				written = written[1:]
			}
			if !c.startedAt.IsZero() {
				kept := deferred[:0]
				for _, r := range deferred {
					if !r.at.After(c.startedAt) && w.byDispatch[r.dispatchID] != nil {
						w.respond(r)
						continue
					}
					kept = append(kept, r)
				}
				deferred = kept
			}
			if name := w.lensOf(c); name != "" {
				w.dispatch(name, c, t.idx)
				if c.toolKind == subagentKind || routerAlone(c.keyArg) {
					for _, r := range data.lensesByCall[c.callID] {
						if a := w.respond(r); a != nil && c.toolKind != subagentKind {
							a.Provenance = ProvenanceRouterResult
						}
					}
				}
			} else if readsBack(c) {
				for _, r := range data.lensesByCall[c.callID] {
					w.readBack(c, r)
				}
			}
		}
		for _, r := range written {
			w.write(r)
		}
		for _, r := range deferred {
			w.respond(r)
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

// join picks the execution record a tool row takes, or -1: the unjoined
// record naming the row's call id, whatever the row's key argument says,
// else for a router call of lens name the earliest unjoined record of that
// lens naming no dispatch that started in the call's window — from joinSlack
// before the call to joinSlack after it ended, or unbounded after when the
// call's duration is unknown — the parsers write 0 where no result timestamp
// bounded the call. A router call whose key argument was cut before `--lens`
// names no lens, and takes the earliest such record of any lens in its
// window: the record then names the lens for it. A subagent row, and a row
// the heuristic reads as no lens dispatch and not as a cut router call,
// joins by dispatch id alone. A row with no call id joins nothing, since
// the join is kept by call id.
func (w *lensWalk) join(name string, c callRow) int {
	if c.callID == "" {
		return -1
	}
	for i, e := range w.execs {
		if !w.joined[i] && e.DispatchID != "" && e.DispatchID == c.callID {
			return i
		}
	}
	if c.toolKind == subagentKind || c.startedAt.IsZero() {
		return -1
	}
	anyLens := name == "" && truncatedRouterCall(c)
	if name == "" && !anyLens {
		return -1
	}
	best := -1
	for i, e := range w.execs {
		if w.joined[i] || e.DispatchID != "" || e.Lens == "" || (!anyLens && e.Lens != name) || e.StartedAt.IsZero() {
			continue
		}
		if e.StartedAt.Before(c.startedAt.Add(-joinSlack)) {
			continue
		}
		if c.durationMs > 0 && e.StartedAt.After(c.startedAt.Add(time.Duration(c.durationMs)*time.Millisecond+joinSlack)) {
			continue
		}
		if best < 0 || e.StartedAt.Before(w.execs[best].StartedAt) {
			best = i
		}
	}
	return best
}

// lensOf names the lens a tool row dispatched: the joined record's, where
// a record names one, else what the key argument reads as.
func (w *lensWalk) lensOf(c callRow) string {
	if i, ok := w.joins[c.callID]; ok && w.execs[i].Lens != "" {
		return w.execs[i].Lens
	}
	return dispatchLens(c)
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

// latest is the highest-numbered attempt for a lens in a round, or nil; of
// two numbered alike — records naming the same attempt — the later placed.
func (w *lensWalk) latest(name string, round int) *LensAttempt {
	var out *LensAttempt
	for _, a := range w.attempts {
		if a.Lens == name && a.Round == round && (out == nil || a.Attempt >= out.Attempt) {
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

// advance applies the turn's next pending commitment line ahead of a lens's
// tool row: the first at the turn's first such row, each later one when
// the lens already answered in the current round.
func (w *lensWalk) advance(name string) {
	if len(w.pending) == 0 {
		return
	}
	prev := w.latest(name, w.round)
	if !w.applied || (prev != nil && prev.Status == AttemptParsed) {
		w.apply(w.pending[0])
		w.pending = w.pending[1:]
		w.applied = true
	}
}

// dispatch opens an attempt for a lens dispatch row. A row that joined a
// record takes the record's round and attempt number where it carries them,
// over the walk's round and the next number in it; the walk's round still
// advances on the row for everything after it. A row whose own result was
// an error starts failed; a verdict in that result, paired right after,
// overrides it.
func (w *lensWalk) dispatch(name string, c callRow, turnIdx int) {
	w.advance(name)
	round := w.round
	i, joined := w.joins[c.callID]
	if joined && w.execs[i].Round > 0 {
		round = w.execs[i].Round
	}
	a := w.open(name, round, true, c.callID, turnIdx)
	if joined {
		a.ExecutionID = w.execs[i].ExecutionID
		if w.execs[i].Attempt > 0 {
			a.Attempt = w.execs[i].Attempt
		}
	}
	a.Routed = c.toolKind != subagentKind
	if c.isError || (c.exitCode != nil && *c.exitCode != 0) {
		a.Status = AttemptFailed
	}
	if c.toolKind != subagentKind {
		if path := routerRedirect(c.keyArg); path != "" {
			w.redirects[c.callID] = path
		}
	}
}

// write places a verdict the agent wrote to a file, as a tool row of its
// lens: the next commitment line is applied first where the lens already
// answered in the current round, as at a re-dispatch, and the row then
// answers the unanswered dispatched attempt of its lens there or opens an
// undispatched one, as an inlined pass does.
func (w *lensWalk) write(r lensRow) {
	w.advance(r.lens)
	w.respond(r)
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
		target.Provenance = ProvenanceReadBack
	}
}

// respond places a response. One naming an attempt by dispatch id answers it,
// or opens a further attempt on the same dispatch, in the dispatch's round,
// when that one already answered — a task can notify more than once. One
// naming a known lens other than the attempt's is the dispatch answered
// wrong, not a further try: it opens nothing, and when the attempt already
// answered it places nothing, staying in the store as evidence. A task
// notification places only by dispatch id: one whose dispatch id is not one
// of this run's lens attempts answers something else — another run's
// dispatch, a task that is not a lens — and one naming no dispatch at all
// is not a notification the harness posted but text quoting the marker — a
// pasted ticket, a compaction summary — so neither is placed, and both stay
// in the store as evidence only, as does a response naming no known lens.
// Otherwise — an inlined pass, an assistant row on a runtime that inlines —
// it answers the latest unanswered dispatched attempt of its lens in the
// current round, else opens an undispatched one. Two more never come here: a
// shell read-back, which readBack holds to its provenance, and a Claude user
// row that is not a task notification — a compaction summary reproducing a
// verdict, a human pasting one — since no lens answers as a plain user
// message and placing it would supersede the real answer. It returns the
// attempt the response was placed on, or nil when it placed nothing.
func (w *lensWalk) respond(r lensRow) *LensAttempt {
	if a := w.byDispatch[r.dispatchID]; a != nil {
		if a.ResponseID != "" {
			if mismatched(a, r) {
				return nil
			}
			routed := a.Routed
			a = w.open(a.Lens, a.Round, true, a.DispatchID, a.DispatchTurnIdx)
			a.Routed = routed
		}
		w.pair(a, r)
		return a
	}
	if r.origin == summary.OriginTaskNotification {
		return nil
	}
	if !lens.KnownLenses[r.lens] {
		return nil
	}
	if a := w.latest(r.lens, w.round); a != nil && a.Status == AttemptDispatched {
		w.pair(a, r)
		return a
	}
	a := w.open(r.lens, w.round, false, "", -1)
	w.pair(a, r)
	return a
}

// mismatched reports whether a response names a known lens that is not the
// attempt's: the dispatch came back with another lens's verdict. A response
// naming no known lens is malformed on its own terms and is not this.
func mismatched(a *LensAttempt, r lensRow) bool {
	return lens.KnownLenses[r.lens] && r.lens != a.Lens
}

// pair records a response on an attempt. A mismatched response is recorded
// where it landed — id, turn, source — and as responded with the mismatch
// as its reason, and none of its verdict is copied: it is not an answer of
// the dispatched lens, and never a verdict of the one it names.
func (w *lensWalk) pair(a *LensAttempt, r lensRow) {
	a.ResponseID = r.responseID
	a.ResponseTurnIdx = r.turnIdx
	a.Source = &SourceRef{Path: r.sourcePath, Line: r.sourceLine}
	if a.Round < w.round {
		a.Late = true
	}
	if mismatched(a, r) {
		a.Status = AttemptResponded
		a.Malformed = fmt.Sprintf("lens mismatch: response names %s, dispatch named %s", r.lens, a.Lens)
		return
	}
	a.Verdict, a.Summary = r.verdict, r.summary
	a.ContextKind, a.ContextState = r.contextKind, r.contextState
	a.Contaminated = r.contaminated()
	a.Malformed = r.reason
	if r.status == lens.StatusParsed {
		a.Status = AttemptParsed
	} else {
		a.Status = AttemptResponded
	}
}
