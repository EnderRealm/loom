package cmd

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"loom/internal/updater"
)

var updaterCmd = &cobra.Command{
	Use:   "updater",
	Short: "Self-managing auto-updater for the loom binary",
	Long: `Updater subcommands:

  loom updater daemon   stay running, poll origin/main, deploy on changes
  loom updater once     run a single check + deploy and exit (debugging)`,
}

var updaterDaemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Run the updater as a long-lived daemon",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		return updater.Daemon(ctx)
	},
}

var updaterOnceCmd = &cobra.Command{
	Use:   "once",
	Short: "Run a single update check + deploy and exit",
	RunE: func(cmd *cobra.Command, args []string) error {
		src, err := updater.SourceDir()
		if err != nil {
			return err
		}
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		return updater.Tick(ctx, src)
	},
}

func init() {
	updaterCmd.AddCommand(updaterDaemonCmd, updaterOnceCmd)
	rootCmd.AddCommand(updaterCmd)
}
