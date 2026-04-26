package cmd

import (
	"time"

	"github.com/spf13/cobra"

	"loom/internal/summarize"
)

var summarizeCmd = &cobra.Command{
	Use:   "summarize",
	Short: "Fold received sessions into the summary database",
	RunE: func(cmd *cobra.Command, args []string) error {
		opts := summarize.Options{}
		opts.ReceivedDir, _ = cmd.Flags().GetString("received")
		opts.DBPath, _ = cmd.Flags().GetString("db")
		opts.Force, _ = cmd.Flags().GetBool("force")
		opts.Verbose, _ = cmd.Flags().GetBool("v")
		opts.Watch, _ = cmd.Flags().GetBool("watch")
		opts.Interval, _ = cmd.Flags().GetDuration("interval")
		return summarize.Run(opts)
	},
}

func init() {
	f := summarizeCmd.Flags()
	f.String("received", summarize.DefaultReceivedDir(), "loom received tree to scan")
	f.String("db", summarize.DefaultDBPath(), "output SQLite database path")
	f.Bool("force", false, "re-summarize even if unchanged")
	f.BoolP("v", "v", false, "verbose progress")
	f.Bool("watch", false, "stay running and re-sweep on a ticker")
	f.Duration("interval", 30*time.Second, "watch-mode sweep interval")

	rootCmd.AddCommand(summarizeCmd)
}
