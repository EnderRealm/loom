// Package summarize walks the received tree and folds sessions into the
// summary database. The same Run entry point backs both the legacy
// loom-summarize binary and the loom CLI subcommand.
package summarize

import (
	"context"
	"errors"
	"fmt"
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
	"loom/internal/parse/store"
	"loom/internal/parse/summary"
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
	st, err := store.Open(opts.DBPath)
	if err != nil {
		if errors.Is(err, store.ErrSchemaOutdated) {
			return fmt.Errorf("%w\n\n  re-run with --rebuild to drop and re-fold from %s", err, opts.ReceivedDir)
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
			fmt.Println("watch: shutdown requested")
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

func sweep(ctx context.Context, st *store.Store, receivedDir string,
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

func walkAgent(ctx context.Context, st *store.Store, agent summary.Agent,
	root string, force, verbose bool, r *sweepResult) {
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return
	}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry,
		err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil || d.IsDir() {
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
		source := store.SourceInfo{
			Project: project,
			Path:    path,
			Size:    info.Size(),
			Mtime:   info.ModTime(),
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

func summarizeOne(ctx context.Context, st *store.Store, agent summary.Agent,
	sessionID string, source store.SourceInfo, verbose bool) error {
	f, err := os.Open(source.Path)
	if err != nil {
		return err
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
		return err
	}
	if sum.SessionID == "" {
		sum.SessionID = sessionID
	}
	if err := st.WriteSummary(ctx, sum, source); err != nil {
		return err
	}
	if verbose {
		fmt.Printf("[%s] %s turns=%d tools=%d errs=%d unknown=%d\n",
			agent, sessionID, len(sum.Turns), len(sum.ToolCalls),
			len(sum.Errors), len(sum.Unknown))
	}
	return nil
}

func report(r sweepResult, dbPath string) {
	fmt.Printf("seen=%d parsed=%d skipped=%d errored=%d in %s db=%s\n",
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
