package cmd

import (
	"path/filepath"

	"github.com/spf13/cobra"

	"loom/internal/config"
	"loom/internal/friction"
)

// frictionCmd is built by a constructor so a test can run it against a
// fixture DB of its own, rather than mutating the registered command's flags.
var frictionCmd = newFrictionCmd()

func newFrictionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "friction",
		Short: "Rank harness-friction signatures by events per active day",
		Long: "Reads the friction table in ~/.loom/summaries.db — one row per hook ask or deny, " +
			"permission refused by the user or the auto-mode classifier, tool error and user " +
			"interrupt the summarizer read out of every session, subagent transcripts included " +
			"and with no knowledge-scope gate — and prints its signatures ranked by events per " +
			"active day, each with its total events, first and last seen, active-day count and " +
			"sessions affected. Density is the sort because absolute count is dominated by " +
			"chronic noise already tolerated.\n\n" +
			"This is a counter and a ranked view. Nothing here has a threshold, triggers " +
			"extraction or calls a model: --sessions names the session ids carrying a signature " +
			"so a knowledge pass can be pointed at them by hand.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			dbPath, _ := cmd.Flags().GetString("db")
			if dbPath == "" {
				dbPath = filepath.Join(config.Home(), "summaries.db")
			}
			top, _ := cmd.Flags().GetInt("top")
			withSessions, _ := cmd.Flags().GetBool("sessions")
			sinceArg, _ := cmd.Flags().GetString("since")
			since, err := parseReportBound("since", sinceArg)
			if err != nil {
				return err
			}

			events, err := friction.Load(dbPath, since)
			if err != nil {
				return err
			}
			friction.Render(cmd.OutOrStdout(), friction.Rank(events), top, withSessions)
			return nil
		},
	}

	f := cmd.Flags()
	f.String("db", "", "summary database to read (default $LOOM_HOME/summaries.db)")
	f.Int("top", 25, "rows to print; 0 prints every signature")
	f.Bool("sessions", false, "list the session ids carrying each signature")
	f.String("since", "", "only count events at or after this time (YYYY-MM-DD or RFC3339)")

	return cmd
}

func init() {
	rootCmd.AddCommand(frictionCmd)
}
