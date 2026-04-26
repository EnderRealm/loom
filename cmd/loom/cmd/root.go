package cmd

import (
	"fmt"
	"os"
	"runtime/debug"

	"github.com/spf13/cobra"
)

var helpText = `loom - session ingest, summarization, and dashboard

Usage: loom <command> [args]

Interactive:
  tui                          Open the dashboard
  summarize [flags]            Fold received sessions into summaries.db

Lifecycle:
  install <component>          Build and install a component
                                 server | receiver | summarizer | shipper | tui
  uninstall                    Remove all loom launchd agents (state preserved)
  status                       Show component status

Global flags:
  -h, --help                   Show help

Environment:
  LOOM_HOME       state root (default: ~/.loom)
  LOOM_BIN_DIR    binary install dir (default: ~/.local/bin)
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
	rootCmd.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		fmt.Println(helpText)
	})
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
