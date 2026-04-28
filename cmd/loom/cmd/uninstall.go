package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"loom/internal/launchd"
	"loom/internal/updater"
	"loom/transport/receiver"
	"loom/transport/shipper"
)

var uninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Remove all loom launchd agents (state preserved)",
	RunE: func(cmd *cobra.Command, args []string) error {
		labels := []string{shipper.AgentLabel, receiver.AgentLabel, summarizerLabel, updater.AgentLabel}
		for _, label := range labels {
			plistPath, _ := launchd.PlistPath(label)
			if plistPath != "" {
				if _, err := os.Stat(plistPath); err != nil {
					continue
				}
			}
			if err := launchd.Uninstall(label); err != nil {
				fmt.Fprintf(os.Stderr, "warn: uninstall %s: %v\n", label, err)
				continue
			}
			fmt.Printf("removed %s\n", label)
			_ = filepath.Clean(plistPath)
		}
		fmt.Println("state preserved")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(uninstallCmd)
}
