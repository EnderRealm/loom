package cmd

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/EnderRealm/ticket/v7/pkg/ticket"
	"github.com/spf13/cobra"

	"loom/internal/config"
	"loom/internal/knowledge"
)

var relevantForTicket string

var relevantCmd = &cobra.Command{
	Use:   "relevant --for-ticket <namespaced-id>",
	Short: "Rank stored truths/decisions relevant to a ticket",
	Long: "Scans the validated knowledge corpus at ~/.loom/knowledge/ and prints a " +
		"ranked markdown list of truths and decisions related to a ticket. Read-only: " +
		"no files are written and no ticket state is mutated.\n\n" +
		"Ranking precedence: direct citations → evidence-path overlap → keyword " +
		"overlap; ties broken by scope match.",
	RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(relevantForTicket) == "" {
			return fmt.Errorf("--for-ticket is required")
		}

		centralRoot, err := config.CentralStoreRoot()
		if err != nil {
			return err
		}
		store := ticket.NewMultiStore(filepath.Join(centralRoot, "tickets"))
		t, err := store.Get(relevantForTicket)
		if err != nil {
			return err
		}

		project, bareID := ticket.ParseNamespacedID(t.ID)
		ref := knowledge.TicketRef{
			ID:      t.ID,
			BareID:  bareID,
			Parent:  t.Parent,
			Project: project,
			Title:   t.Title,
			Body:    t.Body,
		}

		arts, err := knowledge.Load()
		if err != nil {
			return err
		}
		matches := knowledge.Rank(ref, arts)

		var b strings.Builder
		fmt.Fprintf(&b, "# Relevant knowledge for %s\n\n", t.ID)
		if len(matches) == 0 {
			b.WriteString("No relevant knowledge found.\n")
			fmt.Print(b.String())
			return nil
		}
		root := knowledge.Root()
		for _, m := range matches {
			fmt.Fprintf(&b, "- **%s** — %s (%s) — score %d, %s\n",
				m.Artifact.ID, m.Artifact.Title, relPath(root, m.Artifact.Path), m.Score, m.Signal)
		}
		fmt.Print(b.String())
		return nil
	},
}

// relPath renders an artifact path relative to the knowledge root, falling
// back to the absolute path when it can't be made relative.
func relPath(root, p string) string {
	if rel, err := filepath.Rel(root, p); err == nil {
		return rel
	}
	return p
}

func init() {
	relevantCmd.Flags().StringVar(&relevantForTicket, "for-ticket", "", "namespaced ticket id (project/id)")
	rootCmd.AddCommand(relevantCmd)
}
