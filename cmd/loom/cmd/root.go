package cmd

import (
	"fmt"
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
  summarize [--watch|--rebuild]  Fold received sessions into summaries.db

Knowledge:
  relevant --for-ticket <id>   Rank stored truths/decisions relevant to a ticket

Lifecycle:
  install <component>          Install a launchd agent for the running loom binary
                                 server | receiver | summarizer | shipper
  uninstall                    Remove all loom launchd agents (state preserved)
  status                       Show component status + sync health

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

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
