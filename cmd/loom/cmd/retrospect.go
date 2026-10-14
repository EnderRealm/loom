package cmd

import (
	"github.com/spf13/cobra"

	"loom/internal/extract"
	"loom/internal/summaries"
)

var retrospectCmd = &cobra.Command{
	Use:   "retrospect <namespaced-ticket-id>",
	Short: "Extract knowledge candidates from the sessions that closed a ticket",
	Long: "Resolves every summarized session whose commits carry the ticket's marker and " +
		"runs the extraction pipeline over each, for truths and for decisions. Candidates " +
		"land in ~/.loom/knowledge/_candidates/ with their session and ticket sources filled " +
		"in.\n\n" +
		"Never writes to ~/.loom/knowledge/truths/ or decisions/: promotion stays human-gated. " +
		"Each run appends one entry to ~/.loom/knowledge/log.md.",
	Args: cobra.ExactArgs(1),
	// A refusal is one line on stderr, printed by Execute, rather than cobra's
	// error plus a usage dump.
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Without the DB every ticket resolves to zero sessions, which would read
		// as the ticket having none.
		if err := summaries.RequireStore(); err != nil {
			return err
		}
		return extract.Retrospect(extract.RetrospectOptions{TicketID: args[0]})
	},
}

func init() {
	rootCmd.AddCommand(retrospectCmd)
}
