package cmd

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"loom/internal/config"
	"loom/internal/summaries"
)

// samplesCmd is built by a constructor so a test can run a query against a
// fixture home of its own, rather than mutating the registered command's flags.
var samplesCmd = newSamplesCmd()

func newSamplesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "samples --host <host> --since <time> --until <time>",
		Short: "Print a host's metric samples in a time range, as JSON",
		Long: "Reads the host_samples table of $LOOM_HOME/summaries.db, folded by `loom summarize` " +
			"from the host-metrics sample logs the shipper delivers, and prints the samples --host " +
			"took with ts in [--since, --until) as one JSON array ordered by ts. Each element is " +
			"the sample line exactly as the producer wrote it, whatever its v. A window with no " +
			"samples prints [] and exits 0; a missing summaries.db, or one that predates the " +
			"table, is an error. Read-only. See docs/host-metrics.md.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			host, _ := cmd.Flags().GetString("host")
			host = strings.TrimSpace(host)
			if host == "" {
				return fmt.Errorf("--host is required")
			}
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
			if !since.Before(until) {
				return fmt.Errorf("--since must be before --until")
			}
			samples, err := summaries.LoadHostSamples(filepath.Join(config.Home(), "summaries.db"), host, since, until)
			if err != nil {
				return err
			}
			// Joined by hand: json.Marshal would re-encode each line, escaping
			// characters the producer wrote literally.
			lines := make([]string, len(samples))
			for i, s := range samples {
				lines[i] = string(s)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "["+strings.Join(lines, ",")+"]")
			return nil
		},
	}

	f := cmd.Flags()
	f.String("host", "", "the host whose samples to print, as the sample's host field names it")
	f.String("since", "", "samples taken at or after this time (YYYY-MM-DD or RFC3339)")
	f.String("until", "", "samples taken before this time (YYYY-MM-DD or RFC3339)")
	_ = cmd.MarkFlagRequired("host")
	_ = cmd.MarkFlagRequired("since")
	_ = cmd.MarkFlagRequired("until")

	return cmd
}

func init() {
	rootCmd.AddCommand(samplesCmd)
}
