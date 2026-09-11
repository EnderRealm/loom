package cmd

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"loom/internal/config"
	"loom/internal/workreport"
)

// costReportCmd is built by a constructor so a test can run a report against a
// fixture DB of its own, rather than mutating the registered command's flags.
var costReportCmd = newCostReportCmd()

func newCostReportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cost-report",
		Short: "Report what each /work run cost, from the summary DB",
		Long: "Reads ~/.loom/summaries.db and prints one JSON record per /work run: its turns, " +
			"input/output/cache tokens, tool calls broken down by kind, subagent count and summed " +
			"subagent duration, whether it committed, and two measures of time — wall clock from " +
			"invocation to commit, and active time (turn wall clock plus tool duration) inside the " +
			"run's span — plus the conditions it ran under: the models, efforts and CLI versions " +
			"its turns carried, its error count, and how many times the human interacted — and " +
			"what it cost in dollars: cost_usd prices the run's own turns and subagent_cost_usd " +
			"each dispatch's own transcript, at the rates in internal/pricing/rates.json in force " +
			"at the invocation; either is null, with pricing_warnings naming the cause, when " +
			"anything in it could not be priced.\n\n" +
			"Cost is attributed to the run, not the session: a session that invoked /work three " +
			"times holds three runs. A run that never committed reports a null wall clock rather " +
			"than running its span to the session end. The output carries no generation timestamp: " +
			"two reports are meant to be diffed as a before and an after.",
		Args: cobra.NoArgs,
		// The consumer is a diff, not a person: a refused report is one line on
		// stderr rather than cobra's error plus a usage dump.
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			sinceArg, _ := cmd.Flags().GetString("since")
			since, err := parseReportBound("since", sinceArg)
			if err != nil {
				return err
			}
			untilArg, _ := cmd.Flags().GetString("until")
			until, err := parseReportBound("until", untilArg)
			if err != nil {
				return err
			}
			if !since.IsZero() && !until.IsZero() && !since.Before(until) {
				return fmt.Errorf("--since must be before --until")
			}

			dbPath, _ := cmd.Flags().GetString("db")
			if dbPath == "" {
				dbPath = filepath.Join(config.Home(), "summaries.db")
			}
			rep, err := workreport.LoadCost(dbPath, since, until)
			if err != nil {
				return err
			}
			out, err := json.MarshalIndent(rep, "", "  ")
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), string(out))
			return nil
		},
	}

	f := cmd.Flags()
	f.String("since", "", "report runs invoked at or after this time (YYYY-MM-DD or RFC3339)")
	f.String("until", "", "report runs invoked before this time (YYYY-MM-DD or RFC3339)")
	f.String("db", "", "summary database to read (default $LOOM_HOME/summaries.db)")

	return cmd
}

func init() {
	rootCmd.AddCommand(costReportCmd)
}
