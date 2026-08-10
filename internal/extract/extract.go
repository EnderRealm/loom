// Package extract drives knowledge extraction without a human command. It
// sweeps the summary DB for sessions summarized since the trigger was first
// installed that have not been through extractors/extract.py, derives each
// session's knowledge scope from its git remote, and shells out to the
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
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

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
}

// LogPath returns the canonical extractor log file path.
func LogPath() string {
	return filepath.Join(config.Home(), "extractor.log")
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
// SIGINT/SIGTERM.
func Run(opts Options) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

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
		scope, err := resolveScope(s.GitRemote)
		if err != nil {
			r.skipped++
			markSkip(st, s, err.Error())
			continue
		}
		if r.extracted+r.failed >= maxPerSweep {
			r.deferred++
			continue
		}

		log.Printf("extract %s/%s scope=%s input=%s", s.Agent, s.SessionID, scope, s.SourcePath)
		start := time.Now()
		run, err := runExtractor(ctx, script, s.SourcePath, scope)
		if err != nil {
			if ctx.Err() != nil {
				// Shutdown killed the child, not the extractor failing; leave
				// the session unvisited so the next run retries it.
				log.Printf("extract %s/%s: interrupted", s.Agent, s.SessionID)
				break
			}
			// Recorded as visited so one poisoned session can't consume the
			// per-sweep budget forever. Re-running it means deleting its entry
			// from the state file.
			r.failed++
			log.Printf("extract %s/%s: FAILED after %s: %v", s.Agent, s.SessionID,
				time.Since(start).Round(time.Second), err)
			st.mark(s.Agent, s.SessionID, record{Outcome: outcomeFailed, Scope: scope, Reason: err.Error()})
			continue
		}
		r.extracted++
		log.Printf("extract %s/%s: ok in %s (candidates=%d score=%.2f)", s.Agent, s.SessionID,
			time.Since(start).Round(time.Second), run.Candidates, run.Score)
		st.mark(s.Agent, s.SessionID, record{
			Outcome:    outcomeExtracted,
			Scope:      scope,
			Candidates: run.Candidates,
			Score:      run.Score,
		})
	}

	log.Printf("sweep sessions=%d extracted=%d skipped=%d failed=%d deferred=%d",
		len(sessions), r.extracted, r.skipped, r.failed, r.deferred)
	return r
}

func markSkip(st *state, s summaries.SessionSource, reason string) {
	log.Printf("skip %s/%s: %s", s.Agent, s.SessionID, reason)
	st.mark(s.Agent, s.SessionID, record{Outcome: outcomeSkipped, Reason: reason})
}

// scopePattern bounds a derived scope to one conservative path segment.
// git_remote is client-supplied — the shipper sends it, the receiver stores it
// verbatim, the summarizer copies it into sessions.git_remote — and the scope
// derived from it becomes both a --scope argument and a path under the
// knowledge store. Requiring a leading alphanumeric is what rejects "." and
// "..", which filepath.Join would otherwise clean into the store's parent.
var scopePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// resolveScope derives the knowledge scope from a session's git remote: the
// basename of the normalized remote (github.com/enderrealm/loom → loom). A
// session with no remote, one whose basename isn't a safe scope name, or one
// whose scope has no directory in the knowledge store, has nowhere correct to
// file candidates, so it is skipped rather than extracted into a default (or
// traversed) scope.
func resolveScope(gitRemote string) (string, error) {
	if strings.TrimSpace(gitRemote) == "" {
		return "", errors.New("no git remote")
	}
	scope := summaries.NormalizeRemote(gitRemote)
	if i := strings.LastIndex(scope, "/"); i >= 0 {
		scope = scope[i+1:]
	}
	if !scopePattern.MatchString(scope) {
		return "", fmt.Errorf("unsafe scope %q from git remote %q", scope, gitRemote)
	}
	// truths/<scope>/ is what extract.py loads as few-shot references; its
	// absence means the store has no such scope.
	truths := filepath.Join(knowledgeRoot(), "truths")
	dir := filepath.Join(truths, scope)
	// Belt and braces on the pattern above: the scope must still name a direct
	// child of truths/ after Join has cleaned the path.
	if filepath.Dir(dir) != truths {
		return "", fmt.Errorf("scope %q escapes %s", scope, truths)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("unknown scope %q (no %s)", scope, dir)
	}
	return scope, nil
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
