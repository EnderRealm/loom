package cmd

import (
	"github.com/spf13/cobra"

	"loom/internal/extract"
)

var extractCmd = &cobra.Command{
	Use:   "extract",
	Short: "Extract knowledge candidates from newly summarized sessions",
	RunE: func(cmd *cobra.Command, args []string) error {
		opts := extract.Options{}
		opts.Watch, _ = cmd.Flags().GetBool("watch")
		opts.Interval, _ = cmd.Flags().GetDuration("interval")
		opts.Idle, _ = cmd.Flags().GetDuration("idle")
		return extract.Run(opts)
	},
}

func init() {
	f := extractCmd.Flags()
	f.Bool("watch", false, "stay running and re-sweep on a ticker")
	f.Duration("interval", extract.DefaultInterval, "watch-mode sweep interval")
	f.Duration("idle", extract.DefaultIdle, "how long a session artifact must be untouched before it is extracted")

	rootCmd.AddCommand(extractCmd)
}
