package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"loom/internal/config"
	"loom/internal/launchd"
	"loom/transport/receiver"
	"loom/transport/shipper"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show loom component status",
	RunE: func(cmd *cobra.Command, args []string) error {
		printAgent("loom-receiver", receiver.AgentLabel, filepath.Join(config.Home(), "receiver.log"))
		printAgent("loom-summarizer", summarizerLabel, filepath.Join(config.Home(), "summarizer.log"))
		printAgent("loom-shipper", shipper.AgentLabel, filepath.Join(config.TransportDir(), "shipper.log"))
		fmt.Println("=== sync health ===")
		if err := shipper.PrintHealth(os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "  (no notify state: %v)\n", err)
		}
		fmt.Println()
		fmt.Println("=== config ===")
		fmt.Printf("  LOOM_HOME=%s\n", config.Home())
		if _, err := os.Stat(config.Path()); err == nil {
			fmt.Println("  config.json: present")
		} else {
			fmt.Println("  config.json: missing")
		}
		return nil
	},
}

func printAgent(human, label, logPath string) {
	plistPath, _ := launchd.PlistPath(label)
	if plistPath == "" {
		return
	}
	if _, err := os.Stat(plistPath); err != nil {
		return
	}
	fmt.Printf("=== %s ===\n", human)
	out, loaded, _ := launchd.Status(label)
	if !loaded {
		fmt.Println("  installed but not loaded")
	} else {
		// Pull a few key fields out of `launchctl print` output.
		for _, line := range strings.Split(out, "\n") {
			t := strings.TrimSpace(line)
			if strings.HasPrefix(t, "state =") ||
				strings.HasPrefix(t, "pid =") ||
				strings.HasPrefix(t, "program =") ||
				strings.HasPrefix(t, "last exit code =") ||
				strings.HasPrefix(t, "run interval =") {
				fmt.Println("  " + t)
			}
		}
	}
	fmt.Printf("  plist: %s\n", plistPath)
	fmt.Printf("  log:   %s\n", logPath)
	fmt.Println()
}

func init() {
	rootCmd.AddCommand(statusCmd)
}
