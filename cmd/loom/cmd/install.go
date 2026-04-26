package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// TODO: port install logic natively from install.sh into internal/install/.
// For now we shell out so the CLI surface is consistent while the launchd
// plumbing stays in one place.

var installCmd = &cobra.Command{
	Use:       "install <component>",
	Short:     "Build and install a loom component",
	Long:      "Components: server | receiver | summarizer | shipper | tui",
	ValidArgs: []string{"server", "receiver", "summarizer", "shipper", "tui"},
	Args:      cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
	RunE: func(cmd *cobra.Command, args []string) error {
		flag := "--install-" + args[0]
		return runInstallScript(flag)
	},
}

func init() {
	rootCmd.AddCommand(installCmd)
}

func runInstallScript(args ...string) error {
	script, err := repoInstallScript()
	if err != nil {
		return err
	}
	return execPassthrough(script, args...)
}

func repoInstallScript() (string, error) {
	root, err := repoRoot()
	if err != nil {
		return "", fmt.Errorf("locate repo root: %w", err)
	}
	return root + "/install.sh", nil
}
