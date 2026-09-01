package cmd

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"loom/internal/config"
	"loom/internal/workreport"
)

// workReportCmd is built by a constructor so a test can run a report against a
// fixture DB of its own, rather than mutating the registered command's flags.
var workReportCmd = newWorkReportCmd()

func newWorkReportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "work-report",
		Short: "Report /work-run compliance metrics from the summary DB",
		Long: "Reads ~/.loom/summaries.db and prints one JSON record per /work run: whether the " +
			"review fan-out was dispatched, how many review rounds ran, how many lens verdicts " +
			"reported contaminated context, how many acceptance criteria the contract lens left " +
			"unverified, how long passed between the run's first and last ticket edit, and whether " +
			"the run committed.\n\n" +
			"Runs are recognized from transcript content, so history nobody instrumented is " +
			"covered. A run the parser cannot resolve is classified `unknown` and never counted " +
			"compliant. The output carries no generation timestamp: two reports are meant to be " +
			"diffed as a before and an after.",
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
			rep, err := workreport.Load(dbPath, since, until)
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

// parseReportBound accepts a plain date, read as local midnight, or a full
// RFC3339 timestamp. An empty value leaves that side of the range unbounded.
func parseReportBound(flag, value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	if t, err := time.ParseInLocation("2006-01-02", value, time.Local); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("--%s: %q is not a YYYY-MM-DD date or an RFC3339 timestamp", flag, value)
}

func init() {
	rootCmd.AddCommand(workReportCmd)
}
