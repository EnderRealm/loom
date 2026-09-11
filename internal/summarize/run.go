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

	"loom/internal/parse/claudeparse"
	"loom/internal/parse/codexparse"
	"loom/internal/parse/summary"
	"loom/internal/summaries"
)

type Options struct {
	ReceivedDir string
	DBPath      string
	Force       bool
	Verbose     bool
	Watch       bool
	Rebuild     bool
	Interval    time.Duration
}

func Run(opts Options) error {
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

	ctx, cancel := signalContext()
	defer cancel()

	report(sweep(ctx, st, opts.ReceivedDir, opts.Force, opts.Verbose), opts.DBPath)

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
			r := sweep(ctx, st, opts.ReceivedDir, opts.Force, opts.Verbose)
			if r.parsed > 0 || r.errored > 0 {
				report(r, opts.DBPath)
			}
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
	r.duration = time.Since(start)
	return r
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
			if d.Name() == "subagents" {
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
		project := filepath.Base(filepath.Dir(path))
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
	}
	if err != nil {
		return err
	}
	if sum.SessionID == "" {
		sum.SessionID = sessionID
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
