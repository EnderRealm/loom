package cmd

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"time"

	"github.com/spf13/cobra"

	"loom/internal/config"
	"loom/internal/summarize"
)

// toolOutputCmd is built by a constructor so a test can run a query against a
// fixture home of its own, rather than mutating the registered command's flags.
var toolOutputCmd = newToolOutputCmd()

func newToolOutputCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tool-output",
		Short: "Find tool calls whose full output matches a pattern, from received transcripts",
		Long: "Reads every transcript under $LOOM_HOME/received/ — Claude Code with its subagent " +
			"transcripts, Codex CLI and Cursor CLI — and prints the tool calls started in " +
			"[--since, --until) whose whole output matches --match, as a JSON array of " +
			"{agent, session_id, ticket, started_at, key_arg, output} ordered by start. The output " +
			"is the call's result as the transcript recorded it, not summaries.db's truncated " +
			"result_summary. ticket comes from the runs in summaries.db and is null when no run " +
			"places the call under one ticket, or when there is no summaries.db.\n\n" +
			"A window with no matches prints [] and exits 0. An input that cannot be read — a " +
			"transcript, a directory, summaries.db — is one JSON line {path, error} on stderr, and " +
			"stderr carries nothing else; the rest are still read and printed, and the command exits " +
			"1 so a partial answer is never taken for a complete one.\n\n" +
			"Read-only: nothing under $LOOM_HOME is written. A transcript last modified before " +
			"--since is not read, since it cannot hold a call started after it.",
		Args: cobra.NoArgs,
		// The consumer is a collector, not a person: a refused query is one line
		// on stderr rather than cobra's error plus a usage dump.
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
			if !since.Before(until) {
				return fmt.Errorf("--since must be before --until")
			}
			pattern, _ := cmd.Flags().GetString("match")
			match, err := regexp.Compile(pattern)
			if err != nil {
				return fmt.Errorf("--match: %w", err)
			}
			command, _ := cmd.Flags().GetString("command")
			asJSON, _ := cmd.Flags().GetBool("json")

			matches, failures := summarize.ToolOutput(summarize.ToolOutputQuery{
				ReceivedDir: filepath.Join(config.Home(), "received"),
				DBPath:      filepath.Join(config.Home(), "summaries.db"),
				Since:       since,
				Until:       until,
				Match:       match,
				Command:     command,
			})

			if asJSON {
				out, err := json.MarshalIndent(matches, "", "  ")
				if err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), string(out))
			} else {
				for _, m := range matches {
					ticket := "-"
					if m.Ticket != nil {
						ticket = *m.Ticket
					}
					fmt.Fprintf(cmd.OutOrStdout(), "%s %s %s %s %s\n%s\n\n",
						m.StartedAt.Format(time.RFC3339), m.Agent, m.SessionID, ticket, m.KeyArg, m.Output)
				}
			}

			for _, f := range failures {
				line, err := json.Marshal(f)
				if err != nil {
					return err
				}
				fmt.Fprintln(cmd.ErrOrStderr(), string(line))
			}
			if len(failures) > 0 {
				return fmt.Errorf("tool-output: %d inputs unreadable: %w", len(failures), errReported)
			}
			return nil
		},
	}

	f := cmd.Flags()
	f.String("since", "", "calls started at or after this time (YYYY-MM-DD or RFC3339)")
	f.String("until", "", "calls started before this time (YYYY-MM-DD or RFC3339)")
	f.String("match", "", "Go regexp matched against each call's full output")
	f.String("command", "", "only calls whose key argument contains this substring")
	f.Bool("json", false, "print a JSON array")
	_ = cmd.MarkFlagRequired("since")
	_ = cmd.MarkFlagRequired("until")
	_ = cmd.MarkFlagRequired("match")

	return cmd
}

func init() {
	rootCmd.AddCommand(toolOutputCmd)
}
