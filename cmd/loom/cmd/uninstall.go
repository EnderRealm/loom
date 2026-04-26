package cmd

import "github.com/spf13/cobra"

var uninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Remove all loom launchd agents (state preserved)",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runInstallScript("--uninstall")
	},
}

func init() {
	rootCmd.AddCommand(uninstallCmd)
}
