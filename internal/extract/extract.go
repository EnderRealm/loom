// Package extract drives knowledge extraction without a human command. It
// sweeps the summary DB for sessions summarized since the trigger was first
// installed that have not been through extractors/extract.py, derives each
// session's knowledge scope from its checkout's .loom-project marker or, when
// there is none to read on this host, from its git remote, and shells out to the
// extractor so candidates land in ~/.loom/knowledge/_candidates/. Sessions
// whose scope can't be resolved, or whose agent the extractor can't read, are
// skipped with a logged reason, and every session is visited at most once.
//
// The same Run entry point backs the `loom extract` subcommand and the
// com.loom.extractor LaunchAgent (`loom extract --watch`).
package extract

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"loom/internal/config"
	"loom/internal/knowledge"
	"loom/internal/parse/summary"
	"loom/internal/summaries"
)

// AgentLabel is the launchd label for the extraction daemon.
const AgentLabel = "com.loom.extractor"

// DefaultInterval is the watch-mode sweep cadence. Extraction is minutes of
// LLM time per session, so sweeping faster than this buys nothing.
const DefaultInterval = 15 * time.Minute

// DefaultIdle is how long a session's artifact must sit untouched before it
// is extracted. A live session's jsonl keeps growing and the summarizer
// re-folds it on every tick; extracting the first partial copy would compress
// a fragment and, since a session is visited at most once, permanently lose
// the rest.
const DefaultIdle = 30 * time.Minute

// maxPerSweep bounds how many extractions one sweep starts. Each is several
// LLM calls, so the cap spreads a burst of freshly summarized sessions over
// successive sweeps instead of spending it in one go.
const maxPerSweep = 4

// The extractor's own defaults (codex / gpt-5) are rejected under a ChatGPT
// account — see loom/extract-py-default-d2f2 — so the trigger always passes
// an explicit provider and model. Both are tunable so the pair can be
// retuned on a host without a rebuild.
const (
	defaultProvider = "claude"
	defaultModel    = "sonnet"
)

// extractType pins extract.py's --extract-type instead of relying on its
// default, for the same reason provider and model are pinned: the trigger
// runs unattended, so a change to the script's defaults must not silently
// redirect what it produces.
const extractType = "truth"

// Options configure one Run. Zero Interval/Idle mean "no wait", which only
// tests want; the CLI supplies the Default* values.
type Options struct {
	Watch    bool
	Interval time.Duration
	Idle     time.Duration

	// Backfill covers the historical backlog the watermark excludes. It is
	// operator-initiated — one pass, no watch loop, no maxPerSweep cap — and
	// bounded by Scopes and Limit instead. DryRun reports what such a run
	// would do without spending anything.
	Backfill bool
	Scopes   []string
	Limit    int
	DryRun   bool
}

// LogPath returns the canonical extractor log file path.
func LogPath() string {
	return filepath.Join(config.Home(), "extractor.log")
}

// teeToLog appends this process's log output to extractor.log alongside its
// current destination, and returns a function restoring that destination. The
// LaunchAgent's output lands there because launchd redirects its stdout, but a
// backfill is always a foreground invocation (--backfill and --watch are
// mutually exclusive), and a multi-hour run spending real LLM quota must leave
// the same audit trail without the operator remembering a redirect. Stderr is
// kept so the run stays watchable in the terminal it was started from.
func teeToLog() func() {
	prev := log.Writer()
	p := LogPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		log.Printf("open %s: %v", p, err)
		return func() {}
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		log.Printf("open %s: %v", p, err)
		return func() {}
	}
	log.SetOutput(io.MultiWriter(prev, f))
	return func() {
		log.SetOutput(prev)
		f.Close()
	}
}

// ExtractorsDir returns the directory holding extract.py. The script lives in
// a loom checkout and is not part of the release tarball the updater
// installs, so the agent resolves it from the LOOM_EXTRACTORS_DIR tunable and
// falls back to the conventional checkout location.
func ExtractorsDir() string {
	if v := tunable(EnvExtractorsDir); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join("code", "loom", "extractors")
	}
	return filepath.Join(home, "code", "loom", "extractors")
}

// knowledgeRoot returns the store the extractor loads references from and
// writes candidates into. LOOM_KNOWLEDGE_ROOT is a persisted tunable so the
// Go side and the extract.py child agree on the store even when the agent's
// environment doesn't carry it.
func knowledgeRoot() string {
	if v := tunable(EnvKnowledgeRoot); v != "" {
		return v
	}
	return knowledge.Root()
}

// ScriptPath returns the absolute path to extract.py, or an error naming the
// path that was tried. A missing script is not fatal: sweeps no-op and say so
// in the log rather than crash-looping the agent.
func ScriptPath() (string, error) {
	p := filepath.Join(ExtractorsDir(), "extract.py")
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("%s not found — point LOOM_EXTRACTORS_DIR at a loom checkout", p)
	}
	return p, nil
}

// Run performs one sweep and, in watch mode, keeps sweeping on a ticker until
// SIGINT/SIGTERM. With Backfill set it performs one backfill pass instead.
func Run(opts Options) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if opts.Backfill {
		// Before the log is teed: a rejected run spends nothing, so it has no
		// trail to leave in extractor.log.
		if err := validateScopes(opts.Scopes); err != nil {
			return err
		}
		restore := teeToLog()
		defer restore()
		backfill(ctx, opts)
		return nil
	}

	sweep(ctx, opts)

	if !opts.Watch {
		return nil
	}

	tick := time.NewTicker(opts.Interval)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Print("watch: shutdown requested")
			return nil
		case <-tick.C:
			sweep(ctx, opts)
		}
	}
}

type sweepResult struct {
	extracted, skipped, failed, deferred int
}

func sweep(ctx context.Context, opts Options) sweepResult {
	r := sweepResult{}

	script, err := ScriptPath()
	if err != nil {
		log.Printf("extractor unavailable: %v", err)
		return r
	}
	st, err := loadState()
	if err != nil {
		log.Printf("load state: %v", err)
		return r
	}
	// Sessions summarized before the trigger existed are the batch runner's
	// job (loom/batch-runner-session-12da); an unattended sweep must not
	// spend the user's LLM quota chewing through the historical backlog.
	sessions, err := summaries.LoadSessionSources(st.Watermark)
	if err != nil {
		log.Printf("load sessions: %v", err)
		return r
	}

	// A repo's marker facts are stated once per sweep, not once per session.
	seen := logOnce{}

	for _, s := range sessions {
		if ctx.Err() != nil {
			break
		}
		if st.visited(s.Agent, s.SessionID) {
			continue
		}
		if s.Agent != string(summary.AgentClaude) {
			// extractors/preprocess.py dispatches on Claude Code's top-level
			// record types; a codex rollout's session_meta/response_item
			// envelope preprocesses to an empty transcript, so extracting one
			// would spend a full round trip on nothing.
			r.skipped++
			markSkip(st, s, fmt.Sprintf("unsupported agent %q (preprocess.py reads claude-code jsonl only)", s.Agent))
			continue
		}
		info, err := os.Stat(s.SourcePath)
		if err != nil {
			r.skipped++
			markSkip(st, s, fmt.Sprintf("artifact unreadable: %v", err))
			continue
		}
		if time.Since(info.ModTime()) < opts.Idle {
			// Still being written to; revisit on a later sweep.
			r.deferred++
			continue
		}
		res, err := resolveScope(s.CwdRaw, s.GitRemote, seen)
		if err != nil {
			r.skipped++
			markSkip(st, s, err.Error())
			continue
		}
		if r.extracted+r.failed >= maxPerSweep {
			r.deferred++
			continue
		}

		outcome := extractOne(ctx, st, script, s, res)
		if outcome == "" {
			// Interrupted; the session stays unvisited for the next run.
			break
		}
		if outcome == outcomeFailed {
			r.failed++
			continue
		}
		r.extracted++
	}

	log.Printf("sweep sessions=%d extracted=%d skipped=%d failed=%d deferred=%d",
		len(sessions), r.extracted, r.skipped, r.failed, r.deferred)
	return r
}

// extractOne runs one session through the extractor and records the outcome in
// the ledger. Returns outcomeExtracted or outcomeFailed, or "" when a shutdown
// killed the child rather than the extractor failing — the caller stops there
// and leaves the session unvisited so the next run retries it.
func extractOne(ctx context.Context, st *state, script string, s summaries.SessionSource, res resolution) string {
	log.Printf("extract %s/%s scope=%s source=%s input=%s", logSafe(s.Agent), logSafe(s.SessionID),
		res.scope, res.source, logSafe(s.SourcePath))
	start := time.Now()
	run, err := runExtractor(ctx, script, s.SourcePath, res.scope)
	if err != nil {
		if ctx.Err() != nil {
			log.Printf("extract %s/%s: interrupted", logSafe(s.Agent), logSafe(s.SessionID))
			return ""
		}
		// Recorded as visited so one poisoned session can't consume the
		// per-sweep budget forever. Re-running it means deleting its entry
		// from the state file.
		log.Printf("extract %s/%s: FAILED after %s: %v", logSafe(s.Agent), logSafe(s.SessionID),
			time.Since(start).Round(time.Second), err)
		st.mark(s.Agent, s.SessionID, record{Outcome: outcomeFailed, Scope: res.scope, Reason: err.Error()})
		return outcomeFailed
	}
	log.Printf("extract %s/%s: ok in %s (candidates=%d score=%.2f)", logSafe(s.Agent), logSafe(s.SessionID),
		time.Since(start).Round(time.Second), run.Candidates, run.Score)
	st.mark(s.Agent, s.SessionID, record{
		Outcome:    outcomeExtracted,
		Scope:      res.scope,
		Candidates: run.Candidates,
		Score:      run.Score,
	})
	return outcomeExtracted
}

// markSkip records the skip as visited so it isn't re-logged every sweep.
func markSkip(st *state, s summaries.SessionSource, reason string) {
	logSkip(s, reason)
	st.mark(s.Agent, s.SessionID, record{Outcome: outcomeSkipped, Reason: reason})
}

// logSkip escapes the reason as well as the identity: the "artifact
// unreadable" case wraps an *os.PathError whose path is derived from the
// session id, so the id reaches the line through the reason too. The ledger
// keeps the raw reason — json.MarshalIndent escapes it there.
func logSkip(s summaries.SessionSource, reason string) {
	log.Printf("skip %s/%s: %s", logSafe(s.Agent), logSafe(s.SessionID), logSafe(reason))
}

// logSafe renders a client-supplied value for extractor.log. Agent, session
// id and source path all originate from a remote shipper (the receiver stores
// what it is sent, the summarizer copies it into sessions), so a control
// character in one would otherwise write extra lines into the log — a
// fabricated "skip claude-code/x: extracted" makes an unhandled session look
// handled, and the log is the audit record for what the sweep declined to do.
// Quoting only values that aren't plain printable text, rather than %q
// everywhere, keeps routine lines (UUID ids, ordinary paths) unquoted so the
// log stays readable enough that operators keep reading it.
func logSafe(s string) string {
	if !strings.ContainsFunc(s, func(r rune) bool { return !unicode.IsPrint(r) }) {
		return s
	}
	return strconv.Quote(s)
}

// scopeEchoLimit bounds how much of a rejected name a log line echoes. A
// rejected name is arbitrary content — a marker's value is repo-controlled text
// reached through a client-supplied cwd — and the line is the audit record in
// extractor.log, so it must not be able to carry a whole line of someone's file
// into it. Long enough to read back a typo'd project name, which is what the
// line is for. resolve_project.py bounds the same echo at the same width
// (VALUE_ECHO_LIMIT).
const scopeEchoLimit = 40

// boundEcho cuts a value to scopeEchoLimit and reports whether it cut.
// Characters rather than bytes: resolve_project.py slices its own echo by
// character, so both echo the same 40 characters of the same name, and a byte
// cut would also split a multi-byte rune into \xNN escape noise.
func boundEcho(s string) (string, bool) {
	if utf8.RuneCountInString(s) <= scopeEchoLimit {
		return s, false
	}
	return string([]rune(s)[:scopeEchoLimit]), true
}

// echoScope renders a rejected name for a log line: bounded, and quoted so a
// control character in it can't forge a line of its own.
func echoScope(scope string) string {
	if bounded, cut := boundEcho(scope); cut {
		return strconv.Quote(bounded) + "…"
	}
	return strconv.Quote(scope)
}

// echoRemote renders a client-supplied remote, or the scope derived from it,
// for the disagreement line. Neither has been through validScope on that path,
// so both are the arbitrary-length client content scopeEchoLimit exists to
// bound — but the line also states the ordinary case, so it keeps logSafe's
// quote-only-what-needs-it rule rather than quoting every remote URL.
func echoRemote(s string) string {
	if bounded, cut := boundEcho(s); cut {
		return logSafe(bounded) + "…"
	}
	return logSafe(s)
}

// logOnce dedupes lines that state a fact about a repo rather than about a
// session. Scope resolution runs per session, so a backfill over a repo's whole
// history would otherwise restate the same marker disagreement once per session
// — hundreds of identical lines ahead of the selection report a dry run exists
// to be read from.
type logOnce map[string]bool

// printf logs a line the first time this run states it. A nil logOnce is a
// caller that isn't deduping — writing the record of the line into it would
// panic, so it logs every line instead.
func (o logOnce) printf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if o == nil {
		log.Print(msg)
		return
	}
	if o[msg] {
		return
	}
	o[msg] = true
	log.Print(msg)
}

// extractRun is what one extract.py invocation reported through --json-out:
// how many candidates it emitted, and how its coverage against the scope's
// references scored.
type extractRun struct {
	Candidates int     `json:"candidates_valid"`
	Score      float64 `json:"mean_score"`
}

// runExtractor invokes extract.py against one session artifact. A package var
// so tests substitute a stub — the real path costs several LLM calls per
// session.
//
// Success is the presence of the --json-out result, not the exit status:
// extract.py exits non-zero when its coverage score is below --threshold,
// which for a scope holding a handful of validated truths is the ordinary
// case, and it emits candidates and appends to log.md before that exit. A
// CLI or provider error dies earlier and leaves no result file, which is the
// failure worth recording.
var runExtractor = func(ctx context.Context, script, input, scope string) (extractRun, error) {
	// extract.py derives its raw-output and intermediate-summary paths from
	// --json-out, so the whole run's scratch lives in one throwaway dir.
	dir, err := os.MkdirTemp("", "loom-extract-")
	if err != nil {
		return extractRun{}, err
	}
	defer os.RemoveAll(dir)
	jsonOut := filepath.Join(dir, "result.json")

	cmd := exec.CommandContext(ctx, script,
		"--input", input,
		"--scope", scope,
		"--input-format", "raw",
		"--extract-type", extractType,
		"--provider", tunableOr(EnvProvider, defaultProvider),
		"--model", tunableOr(EnvModel, defaultModel),
		// Keyword scoring: the score is recorded for auditing only, and the
		// llm judge would spend a second model call per session to compute it.
		"--judge", "keyword",
		"--json-out", jsonOut,
		// Raw transcripts are summarized before extraction; the extra call
		// buys markedly better candidates than extracting the transcript.
		"--summarize",
	)
	// extract.py defaults the store to ~/.loom/knowledge; pin it so candidates
	// land in the same store the TUI reads under a non-default LOOM_HOME.
	cmd.Env = append(os.Environ(), EnvKnowledgeRoot+"="+knowledgeRoot())
	out, runErr := cmd.CombinedOutput()

	data, err := os.ReadFile(jsonOut)
	if err != nil {
		if runErr == nil {
			runErr = err
		}
		return extractRun{}, fmt.Errorf("%s: %w: %s", filepath.Base(script), runErr, tail(string(out), 800))
	}
	var run extractRun
	if err := json.Unmarshal(data, &run); err != nil {
		return extractRun{}, fmt.Errorf("%s: parse result: %w", filepath.Base(script), err)
	}
	return run, nil
}

// tail returns the last n bytes of s, so a failure log carries the
// extractor's actual error without the whole transcript of its progress.
func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
