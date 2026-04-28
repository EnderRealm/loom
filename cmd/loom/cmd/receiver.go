package cmd

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"loom/transport/receiver"
)

var receiverCmd = &cobra.Command{
	Use:   "receiver",
	Short: "Run the loom ingest receiver",
	Long:  "Listens on -addr (default :8765) and persists session deltas to <storage>/<agent>/<project>/<session_id>.jsonl.",
	RunE: func(cmd *cobra.Command, args []string) error {
		opts := receiver.Options{}
		opts.Addr, _ = cmd.Flags().GetString("addr")
		opts.Storage, _ = cmd.Flags().GetString("storage")
		opts.Token, _ = cmd.Flags().GetString("auth-token")

		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		return receiver.Run(ctx, opts)
	},
}

func init() {
	f := receiverCmd.Flags()
	f.String("addr", ":8765", "listen address")
	f.String("storage", "", "storage directory (default $LOOM_HOME/received)")
	f.String("auth-token", "", "shared bearer token (env: LOOM_RECEIVER_TOKEN)")
	rootCmd.AddCommand(receiverCmd)
}
