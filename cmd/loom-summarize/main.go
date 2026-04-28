// loom-summarize walks ~/.loom/received/{claude-code,codex-cli}/**/*.jsonl
// and folds each session into ~/.loom/summaries.db. Re-running is cheap: a
// session whose source file size and mtime match the DB is skipped.
//
// Modes:
//
//	loom-summarize           one-shot sweep then exit
//	loom-summarize -watch    do an initial sweep (catch-up), then loop on
//	                         a ticker re-sweeping the tree at -interval.
//
// This binary is preserved for the launchd plist; new callers should prefer
// `loom summarize`, which routes to the same internal/summarize package.
package main

import (
	"flag"
	"log"
	"time"

	"loom/internal/summarize"
)

func main() {
	var (
		receivedDir = flag.String("received", summarize.DefaultReceivedDir(),
			"loom received tree to scan")
		dbPath = flag.String("db", summarize.DefaultDBPath(),
			"output SQLite database path")
		force    = flag.Bool("force", false, "re-summarize even if unchanged")
		verbose  = flag.Bool("v", false, "verbose progress")
		watch    = flag.Bool("watch", false, "stay running and re-sweep on a ticker")
		rebuild  = flag.Bool("rebuild", false, "drop the summary DB and rebuild from received/")
		interval = flag.Duration("interval", 30*time.Second,
			"watch-mode sweep interval")
	)
	flag.Parse()

	if err := summarize.Run(summarize.Options{
		ReceivedDir: *receivedDir,
		DBPath:      *dbPath,
		Force:       *force,
		Verbose:     *verbose,
		Watch:       *watch,
		Rebuild:     *rebuild,
		Interval:    *interval,
	}); err != nil {
		log.Fatal(err)
	}
}
