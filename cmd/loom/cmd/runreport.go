package cmd

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"loom/internal/config"
	"loom/internal/runreport"
)

// runReportCmd is built by a constructor so a test can run a report against a
// fixture DB of its own, rather than mutating the registered command's flags.
var runReportCmd = newRunReportCmd()

func newRunReportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run-report",
		Short: "Report everything attributable to one run, from the summary DB",
		Long: "Reads ~/.loom/summaries.db and prints one JSON document for the run: its execution " +
			"tree, unresolved executions and diagnostics as internal/runs reads them; parent-only, " +
			"descendant and total metrics — turns, tool calls by kind, tokens per runtime under each " +
			"runtime's cache semantics, failures by class apart from hook signals, human " +
			"interactions, the models, efforts and CLI versions in force, and cost at the rates in " +
			"internal/pricing/rates.json — with per-execution, per-stage, per-lens and per-attempt " +
			"breakdowns; and the run's time under each definition: wall clock as one span, " +
			"execution time as a sum, tool time over every counted execution, and the parent " +
			"span's legacy active_ms — cost-report's figure for the same run — labelled with its " +
			"semantics.\n\n" +
			"Every transcript is counted once however many executions name it. The outcome is the " +
			"record's own and telemetry completeness is reported beside it: a session missing from " +
			"the database, an execution still pending or a dispatch with no usage is named as a gap, " +
			"never reported as zero. A model with no rate leaves cost null, with the cause in " +
			"pricing_warnings, and every other metric standing.",
		Args: cobra.NoArgs,
		// The consumer is a diff or a UI, not a person: a refused report is one
		// line on stderr rather than cobra's error plus a usage dump.
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			runID, _ := cmd.Flags().GetString("run")
			dbPath, _ := cmd.Flags().GetString("db")
			if dbPath == "" {
				dbPath = filepath.Join(config.Home(), "summaries.db")
			}
			rep, err := runreport.Load(dbPath, runID)
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
	f.String("run", "", "run id to report: a declared run_id or a transcript-recognized transcript:<agent>:<session>:<turn> id")
	f.String("db", "", "summary database to read (default $LOOM_HOME/summaries.db)")
	_ = cmd.MarkFlagRequired("run")

	return cmd
}

func init() {
	rootCmd.AddCommand(runReportCmd)
}
