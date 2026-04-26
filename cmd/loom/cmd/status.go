package cmd

import "github.com/spf13/cobra"

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show loom component status",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runInstallScript("--status")
	},
}

func init() {
	rootCmd.AddCommand(statusCmd)
}
