package cmd

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"loom/transport/shipper"
)

var shipperCmd = &cobra.Command{
	Use:   "shipper",
	Short: "Ship agent session deltas to the loom-receiver",
	Long: `Shipper subcommands:

  loom shipper once     ship any new session bytes and exit
  loom shipper daemon   stay running, ship every interval_minutes (config.json)
  loom shipper health   show last-sync / pending-session state`,
}

var shipperOnceCmd = &cobra.Command{
	Use:   "once",
	Short: "Ship any new session bytes and exit",
	Run: func(cmd *cobra.Command, args []string) {
		shipper.Once()
	},
}

var shipperDaemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Run the shipper as a long-lived daemon",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		return shipper.Daemon(ctx)
	},
}

var shipperHealthCmd = &cobra.Command{
	Use:   "health",
	Short: "Show last-sync / pending-session state",
	RunE: func(cmd *cobra.Command, args []string) error {
		return shipper.PrintHealth(os.Stdout)
	},
}

func init() {
	shipperCmd.AddCommand(shipperOnceCmd, shipperDaemonCmd, shipperHealthCmd)
	rootCmd.AddCommand(shipperCmd)
}
