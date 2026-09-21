package extract

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"loom/internal/config"
	"loom/internal/knowledge/store"
	"loom/internal/summaries"

	"github.com/EnderRealm/ticket/v8/pkg/ticket"
)

// ticketIDPattern mirrors TICKET_ID_RE in extractors/extract.py, which is the
// gate a transcript-derived ticket id clears before it reaches candidate
// frontmatter; keep the two in step. Load-bearing beyond argument hygiene here:
// the id is interpolated into a log.md entry, so an unbounded value could forge
// entries in the store's own history. Go's `$` is end-of-text (not Perl's
// before-a-trailing-newline), which is what the Python spells `\Z`.
//
// The namespace half admits the exact reserved Root namespace `_root` as the
// one alternative to a name starting alphanumeric: tk holds Root tickets there,
// and its leading underscore is what keeps a project from ever claiming the
// name. Only that literal — `_rootx` and `_other` are still refused — and the
// id half keeps its bounds. A Root id is a ticket reference alone: scopePattern
// in scope.go is not widened, because Root has no repository and so is never a
// knowledge scope.
var ticketIDPattern = regexp.MustCompile(`^(?:_root|[A-Za-z0-9][A-Za-z0-9._-]{0,60})/[A-Za-z0-9][A-Za-z0-9._-]{0,60}$`)

// The provider binaries extract.py invokes, by absolute path
// (extractors/extract.py, CLAUDE_BIN/CODEX_BIN). Mirrored rather than resolved
// through exec.LookPath: extract.py never consults $PATH, so a $PATH hit would
// report a backend available for a run that then dies on a path that isn't
// there. Kept in step with the script by hand, as summaries.commitLineRe is with
// its Python twin.
const (
	claudeBin = "/opt/homebrew/bin/claude"
	codexBin  = "/opt/homebrew/bin/codex"
)

// statBackend is os.Stat behind a package var so a test can drive the
// missing-backend path without depending on what the host has installed.
var statBackend = os.Stat

// ticketSelection is what a retrospect selects sessions by: the ids whose
// commit markers count, and the tk snapshot's own account of what it could
// not read, one line per cause, when the graph those ids came from is
// incomplete.
type ticketSelection struct {
	ids         []string
	diagnostics []string
}

// resolveTicketSelection expands a ticket id to the exact set of ids a
// retrospect covers, through one snapshot of the central tk store: an epic
// selects itself plus every child the graph resolves to it, across every
// namespace, so a Root or project epic whose children live in other projects
// covers each of them; a leaf selects itself alone. The expansion is exact
// rather than a prefix or a project filter — a ticket that merely names the
// epic is not its child, and a child in another namespace is.
//
// A store that cannot be resolved, or an id the snapshot does not hold, is an
// error the caller reports and then falls back from to the named id alone —
// the selection this command made before tk could answer the question — so a
// retrospect still runs against a store loom cannot read.
func resolveTicketSelection(ticketID string) (ticketSelection, error) {
	root, err := config.CentralStoreRoot()
	if err != nil {
		return ticketSelection{}, err
	}
	snap, err := ticket.NewMultiStore(filepath.Join(root, "tickets")).Snapshot()
	if err != nil {
		return ticketSelection{}, err
	}
	t, ok := snap.Get(ticketID)
	if !ok {
		return ticketSelection{}, fmt.Errorf("%s is not in the central store at %s", ticketID, root)
	}
	sel := ticketSelection{ids: []string{ticketID}}
	if t.Type == ticket.TypeEpic {
		for _, c := range snap.Children(ticketID) {
			sel.ids = append(sel.ids, c.ID)
		}
	}
	if !snap.Complete {
		sel.diagnostics = snap.Diagnostics()
	}
	return sel, nil
}

// retrospectTypes is what one retrospect runs per session. Both, because the
// command's job is to push everything a completed ticket learned back out for
// review, and the two extractors read the same transcript for different things.
var retrospectTypes = []string{extractTypeTruth, extractTypeDecision}

// RetrospectOptions configure one Retrospect run.
type RetrospectOptions struct {
	TicketID string
}

// Retrospect re-extracts every session whose commits carry a ticket's marker and
// files the result as candidates for human promotion. It writes only to
// _candidates/ (extract.py's own output tree) and to log.md; truths/ and
// decisions/ stay human-gated.
//
// An epic — a project's or Root's — covers its exact children through tk, each
// session once whatever it landed for, and each session keeps the scope its
// own checkout resolves to: the epic decides which sessions, never where their
// candidates file. The automatic trigger is leaf-driven (tk fires the command
// for the ticket that closed), so completing an epic re-extracts nothing.
//
// Unlike the sweep, this is a foreground command an operator asked for, so
// anything that stops it from running is an error rather than a logged no-op.
func Retrospect(opts RetrospectOptions) error {
	ticketID := strings.TrimSpace(opts.TicketID)
	if !ticketIDPattern.MatchString(ticketID) {
		return fmt.Errorf("%q is not a namespaced ticket id (project/id, letters, digits, . _ -)", ticketID)
	}
	script, err := ScriptPath()
	if err != nil {
		return err
	}
	if err := requireBackend(); err != nil {
		return err
	}

	// After the cheap validations, as the backfill tees after its own: a
	// rejected run spends nothing and so has no trail to leave. A run that gets
	// this far spends real LLM quota and must leave one.
	restore := teeToLog()
	defer restore()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Resolved after the tee so the expansion — and what the store could not
	// read — is on the run's own record. An epic's children are the graph's to
	// name; a snapshot that could not see every ticket may be missing some of
	// them, and that has to be readable rather than silently narrowing the run.
	sel, err := resolveTicketSelection(ticketID)
	if err != nil {
		log.Printf("retrospect %s: %v — selecting by the named id alone", ticketID, err)
		sel = ticketSelection{ids: []string{ticketID}}
	}
	for _, d := range sel.diagnostics {
		log.Printf("retrospect %s: tk store incomplete: %s", ticketID, logSafe(d))
	}
	if len(sel.ids) > 1 {
		log.Printf("retrospect %s: epic — %d child ticket(s) selected through tk", ticketID, len(sel.ids)-1)
	}

	sessions, err := summaries.LoadSessionsForTickets(sel.ids)
	if err != nil {
		return err
	}
	if len(sessions) == 0 {
		children := ""
		if len(sel.ids) > 1 {
			children = fmt.Sprintf(" or for any of its %d child ticket(s)", len(sel.ids)-1)
		}
		log.Printf("retrospect %s: no summarized session landed a commit marked [%s]%s — nothing to extract", ticketID, ticketID, children)
		return nil
	}

	// Stated before the first extraction, not after the last: selection keys off
	// commit subjects, which are transcript-derived and so client-supplied, and
	// this path has neither maxPerSweep nor the ledger to bound it. An
	// implausible count has to be readable while it can still be interrupted.
	log.Printf("retrospect %s: %d session(s) selected, %d extraction(s) to run",
		ticketID, len(sessions), len(sessions)*len(retrospectTypes))

	var extracted, skipped, failed int
	candidates := map[string]int{}
	scopes := map[string]bool{}
	// A repo's marker facts are stated once per run, not once per session.
	seen := logOnce{}

	for _, s := range sessions {
		if ctx.Err() != nil {
			break
		}
		if !supportedAgent(s.Agent) {
			skipped++
			logSkip(s, fmt.Sprintf("unsupported agent %q (supported: claude-code, cursor-cli)", s.Agent))
			continue
		}
		if _, err := os.Stat(s.SourcePath); err != nil {
			skipped++
			logSkip(s, fmt.Sprintf("artifact unreadable: %v", err))
			continue
		}
		res, err := resolveScope(s.CwdRaw, s.GitRemote, seen)
		if err != nil {
			skipped++
			logSkip(s, err.Error())
			continue
		}

		// The at-most-once ledger is deliberately neither consulted nor written.
		// It exists to stop the unattended trigger double-spending LLM quota,
		// and the installed sweep has usually already visited the sessions of a
		// ticket that just closed — so honoring it would make a retrospect
		// no-op in exactly the case the command exists for. A human naming a
		// ticket is asking for the re-run; extract.py stamps a run timestamp
		// into each candidate's filename, so a re-run produces siblings rather
		// than overwriting what the sweep filed.
		ran, interrupted := false, false
		for _, kind := range retrospectTypes {
			log.Printf("retrospect %s: extract %s/%s type=%s scope=%s source=%s input=%s", ticketID,
				logSafe(s.Agent), logSafe(s.SessionID), kind, res.scope, res.source, logSafe(s.SourcePath))
			start := time.Now()
			run, err := runExtractor(ctx, logKey(s), script, s.SourcePath, res.scope, kind)
			if err != nil {
				if ctx.Err() != nil {
					log.Printf("retrospect %s: %s/%s %s: interrupted", ticketID,
						logSafe(s.Agent), logSafe(s.SessionID), kind)
					interrupted = true
					break
				}
				// One type failing must not cost the other, nor the sessions
				// after it; the run still exits non-zero at the end.
				failed++
				log.Printf("retrospect %s: %s/%s %s FAILED after %s: %v", ticketID,
					logSafe(s.Agent), logSafe(s.SessionID), kind,
					time.Since(start).Round(time.Second), err)
				continue
			}
			log.Printf("retrospect %s: %s/%s %s ok in %s (candidates=%d score=%.2f)", ticketID,
				logSafe(s.Agent), logSafe(s.SessionID), kind,
				time.Since(start).Round(time.Second), run.Candidates, run.Score)
			candidates[kind] += run.Candidates
			scopes[res.scope] = true
			ran = true
		}
		// Counted off what reached disk, not off the session completing: a type
		// that finished before the interrupt already filed candidates, so the
		// summary line and the log.md gate below have to include it.
		if ran {
			extracted++
		}
		if interrupted {
			break
		}
	}

	// The log entry records that the run happened, so zero candidates from a
	// session that did go through the extractor still writes one. A run where
	// every session was skipped extracted nothing and produced no candidate
	// tree, so it says why here instead of claiming an extraction in log.md.
	if extracted > 0 {
		appendRetrospectLog(ticketID, formatScopes(scopes), candidates)
	} else {
		log.Printf("retrospect %s: no session was extracted (skipped=%d failed=%d) — no log.md entry", ticketID, skipped, failed)
	}

	log.Printf("retrospect %s: sessions=%d extracted=%d skipped=%d failed=%d truth candidates=%d decision candidates=%d",
		ticketID, len(sessions), extracted, skipped, failed,
		candidates[extractTypeTruth], candidates[extractTypeDecision])

	if failed > 0 {
		return fmt.Errorf("retrospect %s: %d extraction(s) failed — see %s", ticketID, failed, LogPath())
	}
	return nil
}

// requireBackend fails when the provider extract.py will invoke isn't installed
// at the path it invokes it by. Checked up front because the failure otherwise
// surfaces once per session, after the run has already been reported as started.
func requireBackend() error {
	provider := tunableOr(EnvProvider, defaultProvider)
	var bin string
	switch provider {
	case "claude":
		bin = claudeBin
	case "codex":
		bin = codexBin
	default:
		return fmt.Errorf("unknown extraction provider %q — %s must be claude or codex", provider, EnvProvider)
	}
	if _, err := statBackend(bin); err != nil {
		return fmt.Errorf("extraction backend %q is not installed at %s, which is where extract.py invokes it: %v", provider, bin, err)
	}
	return nil
}

// appendRetrospectLog records one entry per run in the knowledge store's
// log.md, and commits it. Mirrors extract.py's append_extract_log, including its
// skip when the file is absent — log.md is bootstrapped at store init, not by
// the extractor — except that the skip is logged, so an omission is auditable in
// extractor.log rather than invisible.
//
// The write goes through the store's own entry point, which commits what it
// wrote; nothing here knows about git. ApplyIn rather than Apply because this
// run resolves the store through the extractor's tunables, which may name a
// store the process environment does not, and the writes and the commit have to
// be held against the same one. A failure never fails the run: the candidates
// are the run's output and the entry is a record of it, so both a failed append
// and a failed commit are reported to extractor.log and the run stands.
func appendRetrospectLog(ticketID, scope string, candidates map[string]int) {
	root := knowledgeRoot()
	p := filepath.Join(root, "log.md")
	if _, err := os.Stat(p); err != nil {
		log.Printf("retrospect %s: %s does not exist — entry not recorded", ticketID, p)
		return
	}
	// The label is the entry's own summary, and the commit subject is that same
	// summary without the date scaffolding — the shape extract.py's run_label
	// gives a sweep, so retrospect runs read back the same way in the history.
	label := fmt.Sprintf("retrospect %s | %s | %d truth candidates, %d decision candidates",
		ticketID, scope, candidates[extractTypeTruth], candidates[extractTypeDecision])
	entry := fmt.Sprintf("\n## [%s] %s\n", time.Now().Format("2006-01-02"), label)

	warn, err := store.ApplyIn(root, label, func(tx *store.Tx) error {
		return tx.Append(p, entry)
	})
	if err != nil {
		// Unprefixed: the failure may be the append's or the store's own — a root
		// it could not open, a path it would not take — and each already names
		// what it was, where an "append <path>" prefix would blame an append that
		// never ran.
		log.Printf("retrospect %s: %v", ticketID, err)
		return
	}
	if warn.NotCommitted != "" {
		log.Printf("retrospect %s: entry not committed: %s", ticketID, warn.NotCommitted)
	}
	if warn.NotPushed != "" {
		log.Printf("retrospect %s: entry not pushed: %s", ticketID, warn.NotPushed)
	}
}

// formatScopes renders the scopes a run extracted into — normally one — sorted
// so the entry doesn't reshuffle with map iteration.
func formatScopes(scopes map[string]bool) string {
	if len(scopes) == 0 {
		return "no scope"
	}
	out := make([]string, 0, len(scopes))
	for s := range scopes {
		out = append(out, s)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}
