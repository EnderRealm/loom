package cmd

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"loom/internal/updater"
)

var updaterCmd = &cobra.Command{
	Use:   "updater",
	Short: "Self-managing auto-updater for the loom binary",
	Long: `Updater subcommands:

  loom updater daemon   stay running, poll latest release, install on changes
  loom updater once     run a single check + install and exit (debugging)`,
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
	Short: "Run a single update check + install and exit",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		return updater.Tick(ctx)
	},
}

// updaterReexecCmd is the detached helper the running updater spawns to
// re-bootstrap its OWN launchd job after an install. A job can't bootout
// itself from within (the bootout kills the process mid-teardown), so the
// updater spawns this in a new session, exits, and this helper — after a
// brief delay to let the parent exit — bootout+bootstraps com.loom.updater
// fresh under the new binary. Hidden: it's an internal step, not a user
// command.
var updaterReexecCmd = &cobra.Command{
	Use:    "reexec",
	Short:  "Re-bootstrap the updater's own launchd job (internal)",
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Let the spawning updater process exit so its launchd job is idle
		// before we bootout+bootstrap it.
		time.Sleep(2 * time.Second)
		return installUpdater()
	},
}

func init() {
	updaterCmd.AddCommand(updaterDaemonCmd, updaterOnceCmd, updaterReexecCmd)
	rootCmd.AddCommand(updaterCmd)
}
