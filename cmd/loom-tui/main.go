// loom-tui is the interactive dashboard for managing Loom state on the local
// machine. It reads ~/.loom/{received,transport/staging} and per-project
// .tickets directories; it does not ship or receive anything itself.
package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"loom/internal/tui"
)

func main() {
	p := tea.NewProgram(tui.New(), tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "loom-tui:", err)
		os.Exit(1)
	}
}
