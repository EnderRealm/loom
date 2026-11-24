package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"loom/internal/version"
)

var helpText = `loom - session ingest, summarization, and dashboard

Usage: loom <command> [args]

Daemons (run by launchd; manual invocation supported for debugging):
  shipper daemon               Long-lived capture+ship loop on interval_minutes
  shipper once                 Single capture+ship pass; idempotent
  shipper health               Last sync, pending sessions, uncaptured bytes
  receiver                     Run the ingest server (HTTP)

Interactive:
  ui                           Open the dashboard (alias: tui)
  summarize [--watch|--rebuild|--strict]  Fold received sessions into summaries.db

Knowledge:
  relevant --for-ticket <id>   Rank stored truths/decisions relevant to a ticket
  retrospect <id>              Extract candidates from the sessions that closed a ticket
  synthesis-input --project <name> --since <window>
                               A project's new, done and edited tickets and the
                                 sessions behind them, as JSON
  project-state [--project <name>] [--json]
                               Each knowledge scope's liveness — last and recent
                                 commits, sessions and ticket closes, open
                                 tickets, dormancy — from summaries.db
  knowledge write              Apply one JSON write plan (stdin) to the knowledge
                                 store, committing what it touched
  knowledge scope add <name>   Create truths/<name>/ so that project's sessions
                                 are extracted rather than skipped
  knowledge dupes [--no-judge] Report truth candidates that look like the same
                                 truth (read-only)

Reports:
  work-report [--since|--until]  /work-run compliance metrics from summaries.db, as JSON
  cost-report [--since|--until]  Per-/work-run cost — turns, tokens, tools, time — as JSON
  tool-output --since <t> --until <t> --match <regex> [--command <substr>] [--json]
                               Tool calls whose full output matches, read from the
                                 received transcripts (read-only)

Lifecycle:
  install <component>          Install a launchd agent for the running loom binary
                                 server | receiver | summarizer | shipper
  uninstall                    Remove all loom launchd agents (state preserved)
  status [--json]              Show component status + sync health; --json
                                 prints both as one JSON object

Global flags:
  -h, --help                   Show help

Environment:
  LOOM_HOME             state root (default: ~/.loom)
  LOOM_RECEIVER_TOKEN   shared bearer token; on first 'install receiver' it's
                        persisted to ~/.loom/receiver-token (0600), or prompted
                        for interactively, so later installs need not re-export it
`

var rootCmd = &cobra.Command{
	Use:     "loom",
	Short:   "Session ingest, summarization, and dashboard",
	Long:    helpText,
	Version: version.String(),
	// Execute is the one printer: it owns the exit code and errReported, and
	// adds usage only for an invocation cobra rejected before the command ran.
	// Silenced here, cobra's own copy is off for every subcommand.
	SilenceErrors: true,
	SilenceUsage:  true,
}

func init() {
	// Override only the root's help so subcommands keep cobra's built-in
	// auto-generated help (which respects subcommand flags and Long text).
	defaultHelp := rootCmd.HelpFunc()
	rootCmd.SetHelpFunc(func(c *cobra.Command, args []string) {
		if c == rootCmd {
			fmt.Println(helpText)
			return
		}
		defaultHelp(c, args)
	})
}

// errReported marks a failure the command already wrote to stderr in its own
// format, so a reader parsing that stream sees nothing else on it.
var errReported = errors.New("reported on stderr")

// runStarted records that a command's Run or RunE was entered, set by the
// wrappers markRuns installs.
var runStarted bool

func Execute() {
	markRuns(rootCmd)
	cmd, err := rootCmd.ExecuteC()
	if err != nil {
		printError(os.Stderr, err)
		if !runStarted {
			fmt.Fprint(os.Stderr, cmd.UsageString())
		}
		os.Exit(1)
	}
}

// markRuns wraps every Run and RunE in the tree so Execute can tell a failure
// of the work from a malformed invocation. cobra validates args, flags,
// required flags and flag groups before Run, interleaved with the PreRun
// hooks, so reaching Run is the only point that separates the two.
func markRuns(c *cobra.Command) {
	if run := c.RunE; run != nil {
		c.RunE = func(cmd *cobra.Command, args []string) error {
			runStarted = true
			return run(cmd, args)
		}
	} else if run := c.Run; run != nil {
		c.Run = func(cmd *cobra.Command, args []string) {
			runStarted = true
			run(cmd, args)
		}
	}
	for _, sub := range c.Commands() {
		markRuns(sub)
	}
}

func printError(w io.Writer, err error) {
	if !errors.Is(err, errReported) {
		fmt.Fprintln(w, err)
	}
}
