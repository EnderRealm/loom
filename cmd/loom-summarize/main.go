// loom-summarize walks ~/.loom/received/{claude-code,codex-cli}/**/*.jsonl
// and folds each session into ~/.loom/summaries.db. Re-running is cheap: a
// session whose source file size and mtime match the DB is skipped.
//
// Modes:
//
//	loom-summarize           one-shot sweep then exit
//	loom-summarize -watch    do an initial sweep (catch-up), then loop on
//	                         a ticker re-sweeping the tree at -interval.
package main

import (
	"context"
	"flag"
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

func main() {
	var (
		receivedDir = flag.String("received", defaultReceivedDir(),
			"loom received tree to scan")
		dbPath = flag.String("db", defaultDBPath(),
			"output SQLite database path")
		force    = flag.Bool("force", false, "re-summarize even if unchanged")
		verbose  = flag.Bool("v", false, "verbose progress")
		watch    = flag.Bool("watch", false, "stay running and re-sweep on a ticker")
		interval = flag.Duration("interval", 30*time.Second,
			"watch-mode sweep interval")
	)
	flag.Parse()

	st, err := store.Open(*dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer st.Close()

	ctx, cancel := signalContext()
	defer cancel()

	// First pass — handles cold start and any sessions that landed while
	// we were down. The (size, mtime) check makes this cheap when nothing
	// changed.
	report(sweep(ctx, st, *receivedDir, *force, *verbose), *dbPath)

	if !*watch {
		return
	}

	tick := time.NewTicker(*interval)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			fmt.Println("watch: shutdown requested")
			return
		case <-tick.C:
			r := sweep(ctx, st, *receivedDir, *force, *verbose)
			// In watch mode, only print when there was actual work, so the
			// log isn't a wall of "0 parsed / 408 skipped" every tick.
			if r.parsed > 0 || r.errored > 0 {
				report(r, *dbPath)
			}
		}
	}
}

type sweepResult struct {
	seen, parsed, skipped, errored int
	duration                       time.Duration
}

// sweep walks both agent trees once, parsing and persisting any session whose
// source file has changed since last write. Returns counts for reporting.
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
			current, err := st.SessionAlreadyCurrent(sessionID,
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

func defaultReceivedDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "/tmp/loom-received"
	}
	return filepath.Join(home, ".loom", "received")
}

func defaultDBPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "/tmp/loom-summaries.db"
	}
	return filepath.Join(home, ".loom", "summaries.db")
}
