package cmd

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"loom/internal/synthesis"
)

var synthesisInputCmd = newSynthesisInputCmd()

func newSynthesisInputCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "synthesis-input --project <name> --since <window>",
		Short: "Print one project's ticket state and sessions over a window, as JSON",
		Long: "Reads the project's tickets through the tk CLI and classifies each over the window " +
			"ending now, whose length --since gives (30d, 72h — a length, not a date): new (created in it), done (closed in it), edited (written in it, neither " +
			"created nor closed in it). A ticket created and closed in the window is both new and " +
			"done. Each ticket carries the sessions, from ~/.loom/summaries.db, whose commits carry " +
			"its [<id>] marker, with those commits. unticketed_sessions lists the project's " +
			"sessions active in the window that landed no commit marked for any listed ticket.\n\n" +
			"Prints one JSON object on stdout; tk's stderr and the counts behind the result go " +
			"to stderr. A missing tk, a missing summaries.db and one predating the commits table " +
			"are errors rather than empty answers.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			project, _ := cmd.Flags().GetString("project")
			project = strings.TrimSpace(project)
			if project == "" {
				return fmt.Errorf("--project is required")
			}
			sinceArg, _ := cmd.Flags().GetString("since")
			if strings.TrimSpace(sinceArg) == "" {
				return fmt.Errorf("--since is required")
			}
			window, err := parseWindow("--since", sinceArg)
			if err != nil {
				return err
			}
			until := time.Now().UTC()
			in, err := synthesis.Build(project, until.Add(-window), until, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			out, err := json.MarshalIndent(in, "", "  ")
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), string(out))
			return nil
		},
	}

	f := cmd.Flags()
	f.String("project", "", "tk namespace to read (e.g. loom)")
	f.String("since", "", "window length ending now: a Go duration (72h) or whole days (30d)")

	return cmd
}

// parseWindow reads flag's value as a positive length of time: whole days as
// "Nd", which Go durations have no unit for, or any Go duration.
func parseWindow(flag, value string) (time.Duration, error) {
	var d time.Duration
	var err error
	if n, ok := strings.CutSuffix(value, "d"); ok {
		var days int
		days, err = strconv.Atoi(n)
		d = time.Duration(days) * 24 * time.Hour
	} else {
		d, err = time.ParseDuration(value)
	}
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s: %q is not a positive window (e.g. 30d or 72h)", flag, value)
	}
	return d, nil
}

func init() {
	rootCmd.AddCommand(synthesisInputCmd)
}
