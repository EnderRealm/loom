// Package summarize walks the received tree and folds sessions into the
// summary database. The same Run entry point backs both the legacy
// loom-summarize binary and the loom CLI subcommand.
package summarize

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"loom/internal/extract"
	"loom/internal/parse/claudeparse"
	"loom/internal/parse/codexparse"
	"loom/internal/parse/cursorparse"
	"loom/internal/parse/summary"
	"loom/internal/summaries"
	"loom/internal/synthesis"
)

type Options struct {
	ReceivedDir string
	DBPath      string
	Force       bool
	Verbose     bool
	Watch       bool
	Rebuild     bool
	Interval    time.Duration
	Strict      bool
	// StateWindow is the liveness window project_state is computed over. Zero
	// leaves project_state alone, so a caller that only folds — the tests,
	// which have no tk or knowledge store of their own — never shells tk.
	StateWindow time.Duration
}

func Run(opts Options) error {
	ctx, cancel := signalContext()
	defer cancel()
	return run(ctx, opts)
}

func run(ctx context.Context, opts Options) error {
	if opts.Strict && opts.Watch {
		return errors.New("--strict has no exit to report under --watch")
	}
	if opts.Rebuild {
		if err := os.Remove(opts.DBPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", opts.DBPath, err)
		}
		// Best-effort: WAL/SHM siblings live alongside the DB file. SQLite
		// recreates them; leftover files from the old schema would be ignored
		// but cluttering them up is sloppy.
		_ = os.Remove(opts.DBPath + "-wal")
		_ = os.Remove(opts.DBPath + "-shm")
	}
	st, err := summaries.Open(opts.DBPath)
	if err != nil {
		if errors.Is(err, summaries.ErrSchemaOutdated) {
			return fmt.Errorf("%w\n\n  re-run with --rebuild to drop and re-fold from %s", err, opts.ReceivedDir)
		}
		// No --rebuild hint here: the DB is intact and holds data this binary
		// can't write, so dropping it would throw away the newer store.
		if errors.Is(err, summaries.ErrSchemaTooNew) {
			return fmt.Errorf("%w\n\n  update loom to a build that writes this schema", err)
		}
		return fmt.Errorf("open db: %w", err)
	}
	defer st.Close()

	r := sweep(ctx, st, opts.ReceivedDir, opts.Force, opts.Verbose)
	report(r, opts.DBPath)
	markSweep(ctx, st)
	rebuildProjectState(ctx, st, opts.StateWindow, opts.Verbose)
	stateAt := time.Now()

	// An interrupted sweep stopped walking, so its counts describe only the
	// part of the tree it reached: a clean tally is not a clean tree.
	if opts.Strict && ctx.Err() != nil {
		return fmt.Errorf("strict: sweep interrupted after seen=%d errored=%d", r.seen, r.errored)
	}
	if opts.Strict && r.errored > 0 {
		return fmt.Errorf("strict: errored=%d of seen=%d", r.errored, r.seen)
	}
	if !opts.Watch {
		return nil
	}

	tick := time.NewTicker(opts.Interval)
	defer tick.Stop()

	// Sweeps run one at a time on this goroutine: a tick that fires while a
	// sweep is still going waits for it rather than overlapping it.
	for {
		select {
		case <-ctx.Done():
			log.Print("watch: shutdown requested")
			return nil
		case <-tick.C:
			r := sweep(ctx, st, opts.ReceivedDir, opts.Force, opts.Verbose)
			if r.parsed > 0 || r.errored > 0 {
				report(r, opts.DBPath)
			}
			markSweep(ctx, st)
			if time.Since(stateAt) >= projectStateInterval {
				rebuildProjectState(ctx, st, opts.StateWindow, opts.Verbose)
				stateAt = time.Now()
			}
		}
	}
}

// markSweep stamps the store with the end of a sweep that ran to completion.
// A sweep cut short by shutdown leaves the old marker: it did not see the
// whole tree. A write failure only costs the freshness signal, so it is
// logged rather than ending the watch.
func markSweep(ctx context.Context, st *summaries.Store) {
	if ctx.Err() != nil {
		return
	}
	if err := st.SetLastSweep(time.Now()); err != nil {
		log.Printf("mark sweep: %v", err)
	}
}

// projectStateInterval spaces project_state rebuilds under --watch. A rebuild
// reads every session and commit, shells tk once and resolves every checkout
// to its tk namespace — about two seconds on a host with five thousand
// sessions — while the sweep ticks every few seconds; liveness over a window
// of weeks loses nothing to being minutes old. A failed rebuild waits out the
// interval too, so a missing tk is one log line per interval, not per sweep.
const projectStateInterval = 10 * time.Minute

// rebuildProjectState replaces project_state with one row per knowledge
// scope. A failure — tk missing or erroring, a knowledge store with no
// truths/ or no scope under it — is logged and leaves the previous rows in place: an empty or
// all-dormant table would read as a fact about the projects rather than about
// this rebuild, and the rows' computed_at already says how old they are. It
// never fails the sweep, whose fold is complete without it.
func rebuildProjectState(ctx context.Context, st *summaries.Store, window time.Duration, verbose bool) {
	if window <= 0 || ctx.Err() != nil {
		return
	}
	scopes, err := extract.Scopes()
	if err != nil {
		log.Printf("project state: knowledge scopes: %v — previous rows kept", err)
		return
	}
	if len(scopes) == 0 {
		log.Print("project state: knowledge store has no scopes — previous rows kept")
		return
	}
	diag := io.Discard
	if verbose {
		diag = log.Writer()
	}
	rows, err := synthesis.ProjectState(st.DB(), scopes, window, time.Now().UTC(), diag)
	if err != nil {
		log.Printf("project state: %v — previous rows kept", err)
		return
	}
	if err := st.ReplaceProjectState(ctx, rows); err != nil {
		log.Printf("project state: write: %v — previous rows kept", err)
		return
	}
	if verbose {
		for _, r := range rows {
			log.Printf("project state %s: commits=%d sessions=%d open=%d closed=%d dormant=%t",
				r.Project, r.CommitsInWindow, r.SessionsInWindow, r.OpenTickets,
				r.TicketsClosedInWindow, r.Dormant)
		}
	}
}

type sweepResult struct {
	seen, parsed, skipped, errored int
	duration                       time.Duration
}

func sweep(ctx context.Context, st *summaries.Store, receivedDir string,
	force, verbose bool) sweepResult {
	r := sweepResult{}
	start := time.Now()
	walkAgent(ctx, st, summary.AgentClaude,
		filepath.Join(receivedDir, "claude-code"), force, verbose, &r)
	walkAgent(ctx, st, summary.AgentCodex,
		filepath.Join(receivedDir, "codex-cli"), force, verbose, &r)
	walkAgent(ctx, st, summary.AgentCursor,
		filepath.Join(receivedDir, "cursor-cli"), force, verbose, &r)
	walkExecutions(ctx, st, filepath.Join(receivedDir, summaries.ExecutionsAgent), force, &r)
	r.duration = time.Since(start)
	return r
}

// walkExecutions folds every shipped execution-record registry
// (received/loom-executions/<host>/*.jsonl) into the runs tables. The same
// currency rule as sessions: a file whose size and mtime are unchanged since
// its last import is skipped. Log lines carry counts and the path only —
// never a record body.
func walkExecutions(ctx context.Context, st *summaries.Store, root string,
	force bool, r *sweepResult) {
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return
	}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		r.seen++
		info, err := d.Info()
		if err != nil {
			r.errored++
			return nil
		}
		if !force {
			current, err := st.ExecutionImportCurrent(path, info.Size(), info.ModTime())
			if err != nil {
				log.Printf("check %s: %v", path, err)
			} else if current {
				r.skipped++
				return nil
			}
		}
		f, err := os.Open(path)
		if err != nil {
			log.Printf("executions %s: %v", path, err)
			r.errored++
			return nil
		}
		defer f.Close()
		counts, err := st.ImportExecutions(ctx, path, f, info.Size(), info.ModTime())
		if err != nil {
			log.Printf("executions %s: %v", path, err)
			r.errored++
			return nil
		}
		log.Printf("executions %s runs=%d executions=%d diagnostics=%d",
			path, counts.Runs, counts.Executions, counts.Diagnostics)
		r.parsed++
		return nil
	})
}

func walkAgent(ctx context.Context, st *summaries.Store, agent summary.Agent,
	root string, force, verbose bool, r *sweepResult) {
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return
	}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry,
		err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return nil
		}
		// Subagent transcripts land under <session>/subagents/. They are
		// folded into their parent's summary, so walking them here would
		// file each one a second time as a standalone session.
		if d.IsDir() {
			if agent == summary.AgentClaude && d.Name() == "subagents" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		r.seen++
		info, err := d.Info()
		if err != nil {
			r.errored++
			return nil
		}
		project := projectSlug(root, path)
		sessionID := strings.TrimSuffix(filepath.Base(path), ".jsonl")
		cwdRaw, gitRemote := readMetaSidecar(path)
		// A background dispatch outlives the parent's last record, so a
		// subagent transcript can still be growing while the parent file
		// sits unchanged. Currency tracks the newer of the two, or those
		// rows keep a truncated span until the next --rebuild.
		mtime := info.ModTime()
		if subMtime, ok := newestSubagentMtime(path); ok && subMtime.After(mtime) {
			mtime = subMtime
		}
		source := summaries.SourceInfo{
			Project:   project,
			Path:      path,
			Size:      info.Size(),
			Mtime:     mtime,
			CwdRaw:    cwdRaw,
			GitRemote: gitRemote,
		}
		if !force {
			current, err := st.SessionAlreadyCurrent(string(agent), sessionID,
				source.Size, source.Mtime)
			if err != nil {
				log.Printf("check %s: %v", path, err)
			} else if current {
				r.skipped++
				return nil
			}
		}
		if err := summarizeOne(ctx, st, agent, sessionID, source, verbose); err != nil {
			log.Printf("summarize %s: %v", path, err)
			r.errored++
			return nil
		}
		r.parsed++
		return nil
	})
}

func summarizeOne(ctx context.Context, st *summaries.Store, agent summary.Agent,
	sessionID string, source summaries.SourceInfo, verbose bool) error {
	f, err := os.Open(source.Path)
	if err != nil {
		return err
	}
	defer f.Close()

	var sum *summary.SessionSummary
	switch agent {
	case summary.AgentClaude:
		sum, err = claudeparse.ParseWithSubagents(f, collectSubagents(source.Path))
	case summary.AgentCodex:
		sum, err = codexparse.Parse(f)
	case summary.AgentCursor:
		sum, err = cursorparse.Parse(f)
	}
	if err != nil {
		return err
	}
	if sum.SessionID == "" {
		sum.SessionID = sessionID
	}
	// Children first: the parent's row is the currency marker the next sweep
	// checks, so a write cut short here re-folds the whole family.
	for _, sa := range sum.Subagents {
		if sa.Session == nil {
			continue
		}
		sa.Session.ParentSessionID = sum.SessionID
		child, err := subagentSource(source, sa.SessionID)
		if err != nil {
			return err
		}
		if err := st.WriteSummary(ctx, sa.Session, child); err != nil {
			return err
		}
	}
	if err := st.WriteSummary(ctx, sum, source); err != nil {
		return err
	}
	if verbose {
		log.Printf("[%s] %s turns=%d tools=%d errs=%d subagents=%d unknown=%d",
			agent, sessionID, len(sum.Turns), len(sum.ToolCalls),
			len(sum.Errors), len(sum.Subagents), len(sum.Unknown))
	}
	return nil
}

// projectSlug returns the first path component below an agent root. Cursor
// children live below <project>/<parent>/subagents/, so filepath.Dir(path)
// would otherwise mislabel every child as project "subagents".
func projectSlug(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.Base(filepath.Dir(path))
	}
	if i := strings.IndexRune(rel, filepath.Separator); i >= 0 {
		return rel[:i]
	}
	return filepath.Base(filepath.Dir(path))
}

func report(r sweepResult, dbPath string) {
	log.Printf("seen=%d parsed=%d skipped=%d errored=%d in %s db=%s",
		r.seen, r.parsed, r.skipped, r.errored,
		r.duration.Round(time.Millisecond), dbPath)
}

func signalContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigs
		cancel()
	}()
	return ctx, cancel
}

// subagentDir is where the receiver lands the transcripts one session
// dispatched.
func subagentDir(sessionPath string) string {
	return filepath.Join(strings.TrimSuffix(sessionPath, ".jsonl"), "subagents")
}

// subagentSource is the provenance of one subagent transcript folded as its
// own session: its own file's path, size and mtime, so a reader comparing
// source_size against the file gets the same answer as for any session, and
// its own identity sidecar where the receiver wrote one, else the parent's.
func subagentSource(parent summaries.SourceInfo, sessionID string) (summaries.SourceInfo, error) {
	path := filepath.Join(subagentDir(parent.Path), sessionID+".jsonl")
	info, err := os.Stat(path)
	if err != nil {
		return summaries.SourceInfo{}, err
	}
	cwdRaw, gitRemote := readMetaSidecar(path)
	if cwdRaw == "" {
		cwdRaw = parent.CwdRaw
	}
	if gitRemote == "" {
		gitRemote = parent.GitRemote
	}
	return summaries.SourceInfo{
		Project:   parent.Project,
		Path:      path,
		Size:      info.Size(),
		Mtime:     info.ModTime(),
		CwdRaw:    cwdRaw,
		GitRemote: gitRemote,
	}, nil
}

// collectSubagents lists the subagent transcripts one Claude session
// dispatched, each paired with the dispatch metadata written beside it.
// Everything here is best-effort: a missing directory or an unreadable
// transcript costs subagent rows, never the session summary.
func collectSubagents(sessionPath string) []claudeparse.SubagentInput {
	dir := subagentDir(sessionPath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		// No subagents directory is the common case and says nothing. Any
		// other cause (permissions, a file where the directory belongs)
		// costs every row for this session, so it gets a line.
		if !os.IsNotExist(err) {
			log.Printf("subagents %s: %v", dir, err)
		}
		return nil
	}
	var inputs []claudeparse.SubagentInput
	for _, e := range entries {
		// Non-recursive: the shipper flattens nesting into the filename.
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		in := readSubagentSidecar(path)
		in.SessionID = strings.TrimSuffix(e.Name(), ".jsonl")
		in.Open = openTranscript(path)
		inputs = append(inputs, in)
	}
	return inputs
}

// openTranscript defers the open until the parser folds that transcript, so
// one descriptor is held at a time. A busy parent dispatches dozens and the
// summarizer runs as a launchd agent, where the soft descriptor limit is low.
func openTranscript(path string) func() (io.ReadCloser, error) {
	return func() (io.ReadCloser, error) {
		f, err := os.Open(path)
		if err != nil {
			// The row lands unmeasured either way; this log is the signal.
			log.Printf("subagent %s: %v", path, err)
			return nil, err
		}
		return f, nil
	}
}

// newestSubagentMtime reports the newest mtime across a session's subagent
// transcripts, false when it has none or the directory can't be read.
func newestSubagentMtime(sessionPath string) (time.Time, bool) {
	entries, err := os.ReadDir(subagentDir(sessionPath))
	if err != nil {
		return time.Time{}, false
	}
	var newest time.Time
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	return newest, !newest.IsZero()
}

// readSubagentSidecar pulls the dispatch metadata for one subagent
// transcript. Sidecar layout matches wire.Subagent. Missing or corrupt
// sidecar returns zero values silently — the transcript still yields a
// duration, just no dispatch to attribute it to.
func readSubagentSidecar(jsonlPath string) claudeparse.SubagentInput {
	metaPath := strings.TrimSuffix(jsonlPath, ".jsonl") + ".subagent.json"
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return claudeparse.SubagentInput{}
	}
	var m struct {
		AgentType string `json:"agent_type"`
		ToolUseID string `json:"tool_use_id"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return claudeparse.SubagentInput{}
	}
	return claudeparse.SubagentInput{
		AgentType: m.AgentType,
		ToolUseID: m.ToolUseID,
	}
}

// readMetaSidecar pulls the receiver-written project identity for one
// session. Sidecar layout matches wire.ProjectIdentity. Missing sidecar
// returns zero values silently — pre-identity sessions land before the
// shipper started populating it, and that's fine.
func readMetaSidecar(jsonlPath string) (cwd, gitRemote string) {
	metaPath := strings.TrimSuffix(jsonlPath, ".jsonl") + ".meta.json"
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return "", ""
	}
	var m struct {
		GitRemote string `json:"git_remote"`
		Cwd       string `json:"cwd"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return "", ""
	}
	return m.Cwd, m.GitRemote
}

func DefaultReceivedDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "/tmp/loom-received"
	}
	return filepath.Join(home, ".loom", "received")
}

func DefaultDBPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "/tmp/loom-summaries.db"
	}
	return filepath.Join(home, ".loom", "summaries.db")
}
