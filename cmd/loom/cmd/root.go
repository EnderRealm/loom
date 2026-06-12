package cmd

import (
	"fmt"
	"os"
	"runtime/debug"

	"github.com/spf13/cobra"
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
  LOOM_RECEIVER_TOKEN   shared bearer token (required for install receiver)
`

var Version = "dev"

func version() string {
	if Version != "dev" {
		return Version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return Version
	}
	var revision, dirty string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			if s.Value == "true" {
				dirty = ", dirty"
			}
		}
	}
	if revision == "" {
		return Version
	}
	if len(revision) > 7 {
		revision = revision[:7]
	}
	return fmt.Sprintf("dev (%s%s)", revision, dirty)
}

var rootCmd = &cobra.Command{
	Use:     "loom",
	Short:   "Session ingest, summarization, and dashboard",
	Long:    helpText,
	Version: version(),
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
