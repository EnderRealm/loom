package cmd

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"loom/internal/config"
	"loom/internal/escapes"
)

var escapesCmd = newEscapesCmd()

func newEscapesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "escapes",
		Short: "Attribute done bugs to the agent sessions that introduced them",
		Long: "For every done bug in the tk store, finds its fix commits by their `[<ticket-id>]` " +
			"subject marker in the project's registered repo, blames the lines those fixes deleted " +
			"or modified (whitespace ignored, generated paths skipped) at the fix's parent, and " +
			"joins the blamed commits to the loom sessions that made them.\n\n" +
			"Prints one row per (agent, model, cli_version): distinct bug-introducing commits over " +
			"every loom commit in that slice, beside the fractional credit (a bug blamed on n " +
			"commits credits each 1/n); then the bugs no session could be credited with, by " +
			"reason, and precision against the embedded hand-labelled fixture. Every done bug is written once " +
			"to a JSONL file with its class, fix commits, blamed commits and matched sessions.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			dbPath, _ := cmd.Flags().GetString("db")
			if dbPath == "" {
				dbPath = filepath.Join(config.Home(), "summaries.db")
			}
			outPath, _ := cmd.Flags().GetString("out")
			if outPath == "" {
				outPath = filepath.Join(config.Home(), "escapes.jsonl")
			}

			rep, err := escapes.Run(dbPath, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			if err := escapes.WriteJSONL(outPath, rep.Records); err != nil {
				return err
			}
			escapes.Render(cmd.OutOrStdout(), rep)
			fmt.Fprintf(cmd.OutOrStdout(), "\nper-bug attribution: %s\n", outPath)
			return nil
		},
	}

	f := cmd.Flags()
	f.String("db", "", "summary database to read (default $LOOM_HOME/summaries.db)")
	f.String("out", "", "per-bug JSONL to write (default $LOOM_HOME/escapes.jsonl)")

	return cmd
}

func init() {
	rootCmd.AddCommand(escapesCmd)
}
