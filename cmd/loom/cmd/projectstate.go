package cmd

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"loom/internal/config"
	"loom/internal/summaries"
)

// projectStateCmd is built by a constructor so a test can read a fixture DB of
// its own, rather than mutating the registered command's flags.
var projectStateCmd = newProjectStateCmd()

func newProjectStateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project-state [--project <name>] [--json]",
		Short: "Print each tracked project's liveness from the summary DB",
		Long: "Reads the project_state table of ~/.loom/summaries.db: one row per knowledge scope " +
			"(each truths/<scope>/ directory in the knowledge store), rebuilt by `loom summarize` " +
			"from the sessions and commits it folded and from tk. Each row carries the last commit, " +
			"session and ticket close, the commits, sessions and tickets closed in the window " +
			"ending at computed_at, the open tickets (any status but done or closed), and dormant: " +
			"nothing of the three in the window. The window is summarize's --state-window.\n\n" +
			"--json prints one object for --project, else an array. An empty table, a project " +
			"with no row and a database that predates the table are errors rather than empty " +
			"answers. See docs/project-state.md.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			project, _ := cmd.Flags().GetString("project")
			project = strings.TrimSpace(project)
			asJSON, _ := cmd.Flags().GetBool("json")
			dbPath, _ := cmd.Flags().GetString("db")
			if dbPath == "" {
				dbPath = filepath.Join(config.Home(), "summaries.db")
			}
			rows, err := summaries.LoadProjectState(dbPath)
			if err != nil {
				return err
			}
			if len(rows) == 0 {
				return fmt.Errorf("project_state in %s is empty — no rebuild has succeeded; `loom summarize -v` logs why", dbPath)
			}
			if project != "" {
				var names []string
				var match []summaries.ProjectState
				for _, r := range rows {
					names = append(names, r.Project)
					if r.Project == project {
						match = append(match, r)
					}
				}
				if len(match) == 0 {
					return fmt.Errorf("no project state for %q; tracked: %s", project, strings.Join(names, ", "))
				}
				rows = match
			}

			out := cmd.OutOrStdout()
			if asJSON {
				var v any = rows
				if project != "" {
					v = rows[0]
				}
				b, err := json.MarshalIndent(v, "", "  ")
				if err != nil {
					return err
				}
				fmt.Fprintln(out, string(b))
				return nil
			}
			fmt.Fprintf(out, "computed %s, window %s\n",
				rows[0].ComputedAt.Format(time.RFC3339), windowString(rows[0].WindowSeconds))
			for _, r := range rows {
				liveness := "active"
				if r.Dormant {
					liveness = "dormant"
				}
				fmt.Fprintf(out, "%-14s %-7s commits=%d sessions=%d open=%d closed=%d last_commit=%s last_session=%s last_closed=%s\n",
					r.Project, liveness, r.CommitsInWindow, r.SessionsInWindow, r.OpenTickets,
					r.TicketsClosedInWindow, dateOrNever(r.LastCommitAt), dateOrNever(r.LastSessionAt), dateOrNever(r.LastTicketClosedAt))
			}
			return nil
		},
	}

	f := cmd.Flags()
	f.String("project", "", "one project (knowledge scope) to print")
	f.Bool("json", false, "print JSON: one object for --project, else an array")
	f.String("db", "", "summary database to read (default $LOOM_HOME/summaries.db)")

	return cmd
}

// windowString renders a stored window the way --state-window takes it.
func windowString(seconds int64) string {
	d := time.Duration(seconds) * time.Second
	if d%(24*time.Hour) == 0 {
		return fmt.Sprintf("%dd", d/(24*time.Hour))
	}
	return d.String()
}

func dateOrNever(t *time.Time) string {
	if t == nil {
		return "never"
	}
	return t.UTC().Format(time.DateOnly)
}

func init() {
	rootCmd.AddCommand(projectStateCmd)
}
