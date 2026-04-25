// loom-summarize walks ~/.loom/received/{claude-code,codex-cli}/**/*.jsonl
// and folds each session into ~/.loom/summaries.db. Re-running is cheap: a
// session whose source file size and mtime match the DB is skipped.
package main

import (
	"context"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
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
		force   = flag.Bool("force", false, "re-summarize even if unchanged")
		verbose = flag.Bool("v", false, "verbose progress")
	)
	flag.Parse()

	st, err := store.Open(*dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	totals := struct {
		seen, parsed, skipped, errored int
	}{}

	walk := func(agent summary.Agent, root string) {
		if _, err := os.Stat(root); os.IsNotExist(err) {
			return
		}
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry,
			err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".jsonl") {
				return nil
			}
			totals.seen++
			info, err := d.Info()
			if err != nil {
				totals.errored++
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
			if !*force {
				current, err := st.SessionAlreadyCurrent(sessionID,
					source.Size, source.Mtime)
				if err != nil {
					log.Printf("check %s: %v", path, err)
				} else if current {
					totals.skipped++
					return nil
				}
			}
			f, err := os.Open(path)
			if err != nil {
				log.Printf("open %s: %v", path, err)
				totals.errored++
				return nil
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
				log.Printf("parse %s: %v", path, err)
				totals.errored++
				return nil
			}
			if sum.SessionID == "" {
				sum.SessionID = sessionID
			}
			if err := st.WriteSummary(ctx, sum, source); err != nil {
				log.Printf("write %s: %v", path, err)
				totals.errored++
				return nil
			}
			totals.parsed++
			if *verbose {
				fmt.Printf("[%s] %s turns=%d tools=%d errs=%d unknown=%d\n",
					agent, sessionID, len(sum.Turns), len(sum.ToolCalls),
					len(sum.Errors), len(sum.Unknown))
			}
			return nil
		})
	}

	start := time.Now()
	walk(summary.AgentClaude,
		filepath.Join(*receivedDir, "claude-code"))
	walk(summary.AgentCodex,
		filepath.Join(*receivedDir, "codex-cli"))
	dur := time.Since(start)

	fmt.Printf("seen=%d parsed=%d skipped=%d errored=%d in %s db=%s\n",
		totals.seen, totals.parsed, totals.skipped, totals.errored,
		dur.Round(time.Millisecond), *dbPath)
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
