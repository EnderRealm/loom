package cmd

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"loom/internal/summaries"
	"loom/internal/tui"
)

var uiCmd = &cobra.Command{
	Use:     "ui",
	Aliases: []string{"tui"},
	Short:   "Open the loom dashboard",
	// A refusal is one line on stderr, printed by Execute, rather than cobra's
	// error plus a usage dump.
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Before the alt-screen, so the refusal is readable rather than a
		// dashboard of zeros standing in for a store that isn't there.
		if err := summaries.RequireStore(); err != nil {
			return err
		}
		runID, _ := cmd.Flags().GetString("run")
		p := tea.NewProgram(tui.New(tui.Options{RunID: runID}), tea.WithAltScreen(), tea.WithMouseCellMotion())
		if _, err := p.Run(); err != nil {
			fmt.Fprintln(os.Stderr, "loom ui:", err)
			return err
		}
		return nil
	},
}

func init() {
	uiCmd.Flags().String("run", "", "open on one run's detail: a declared run_id or a transcript:<agent>:<session>:<turn> id")
	rootCmd.AddCommand(uiCmd)
}
