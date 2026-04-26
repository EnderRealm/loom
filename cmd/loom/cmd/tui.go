package cmd

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"loom/internal/tui"
)

var tuiCmd = &cobra.Command{
	Use:   "tui",
	Short: "Open the loom dashboard",
	RunE: func(cmd *cobra.Command, args []string) error {
		p := tea.NewProgram(tui.New(), tea.WithAltScreen(), tea.WithMouseCellMotion())
		if _, err := p.Run(); err != nil {
			fmt.Fprintln(os.Stderr, "loom tui:", err)
			return err
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(tuiCmd)
}
