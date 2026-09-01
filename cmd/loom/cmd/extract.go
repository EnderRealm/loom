package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"loom/internal/extract"
)

// extractCmd is built by a constructor so a test can parse real argv against a
// command of its own, rather than mutating the flag state of the registered one.
var extractCmd = newExtractCmd()

func newExtractCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "extract",
		Short: "Extract knowledge candidates from newly summarized sessions",
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := extract.Options{}
			opts.Watch, _ = cmd.Flags().GetBool("watch")
			opts.Interval, _ = cmd.Flags().GetDuration("interval")
			opts.Idle, _ = cmd.Flags().GetDuration("idle")
			opts.Backfill, _ = cmd.Flags().GetBool("backfill")
			opts.Scopes, _ = cmd.Flags().GetStringSlice("scope")
			opts.Limit, _ = cmd.Flags().GetInt("limit")
			opts.DryRun, _ = cmd.Flags().GetBool("dry-run")
			opts.MinTurns, _ = cmd.Flags().GetInt("min-turns")

			// A negative threshold reads as "disabled" to the selection, which
			// is what 0 already says; rejecting it keeps the one spelling.
			if opts.MinTurns < 0 {
				return fmt.Errorf("--min-turns must not be negative (0 disables the threshold)")
			}
			// The selection treats any non-positive limit as unbounded, so a
			// mistyped negative bound would run the whole backlog at real LLM
			// cost. Zero is the flag's own default and keeps meaning unbounded.
			if opts.Limit < 0 {
				return fmt.Errorf("--limit must not be negative (0 means unbounded)")
			}
			// The backfill-only flags mean nothing to a sweep, so they fail
			// loudly rather than leaving an operator to believe a bounded run
			// happened. Judged on being passed, not on their values: an
			// explicit --limit 0 or --dry-run=false is still a backfill flag
			// handed to a sweep, and reading the value would let it through.
			if !opts.Backfill && (cmd.Flags().Changed("scope") || cmd.Flags().Changed("limit") || cmd.Flags().Changed("dry-run")) {
				return fmt.Errorf("--scope, --limit and --dry-run require --backfill")
			}
			if opts.Backfill && opts.Watch {
				return fmt.Errorf("--backfill and --watch are mutually exclusive; a backfill is a single pass")
			}
			return extract.Run(opts)
		},
	}

	f := cmd.Flags()
	f.Bool("watch", false, "stay running and re-sweep on a ticker")
	f.Duration("interval", extract.DefaultInterval, "watch-mode sweep interval")
	f.Duration("idle", extract.DefaultIdle, "how long a session artifact must be untouched before it is extracted")
	f.Bool("backfill", false, "extract the historical backlog the watermark excludes, in one pass")
	f.StringSlice("scope", nil, "backfill only these knowledge scopes (repeatable, or comma-separated)")
	f.Int("limit", 0, "backfill at most this many sessions (0 means unbounded)")
	f.Bool("dry-run", false, "report what a backfill would extract without running it")
	// Applies to a sweep as well as a backfill, so it is deliberately outside
	// the backfill-only guard above. Its default is the persisted tunable, so
	// the LaunchAgent's threshold is retunable without a rebuilt plist.
	f.Int("min-turns", extract.DefaultMinTurns(), "skip sessions with fewer turns than this (0 disables the threshold)")

	return cmd
}

func init() {
	rootCmd.AddCommand(extractCmd)
}
