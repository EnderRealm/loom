package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

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
		// Shipper interval lives in config.json (in-process ticker);
		// summarizer ticks every 30s by default (plist-baked flag).
		shipperInterval := ""
		if cfg, err := shipper.LoadConfig(); err == nil {
			n := cfg.IntervalMinutes
			if n <= 0 {
				n = shipper.DefaultIntervalMinutes
			}
			shipperInterval = fmt.Sprintf("%dm (capture+ship ticker)", n)
		}

		printAgent(agentReport{
			human:    "loom-receiver",
			label:    receiver.AgentLabel,
			logPath:  filepath.Join(config.Home(), "receiver.log"),
			interval: "n/a (HTTP server)",
		})
		printAgent(agentReport{
			human:    "loom-summarizer",
			label:    summarizerLabel,
			logPath:  filepath.Join(config.Home(), "summarizer.log"),
			interval: "30s (sweep ticker)",
		})
		printAgent(agentReport{
			human:    "loom-shipper",
			label:    shipper.AgentLabel,
			logPath:  filepath.Join(config.TransportDir(), "shipper.log"),
			interval: shipperInterval,
		})
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

type agentReport struct {
	human    string
	label    string
	logPath  string
	interval string
}

func printAgent(r agentReport) {
	plistPath, _ := launchd.PlistPath(r.label)
	if plistPath == "" {
		return
	}
	if _, err := os.Stat(plistPath); err != nil {
		return
	}
	fmt.Printf("=== %s ===\n", r.human)
	out, loaded, _ := launchd.Status(r.label)
	if !loaded {
		fmt.Println("  installed but not loaded")
	} else {
		// Pull a few key fields out of `launchctl print` output. Each
		// field appears once at the top level and may repeat inside
		// coalition blocks (state = active for resource/jetsam); take
		// only the first occurrence so the output is one row per field.
		want := []string{"state =", "pid =", "program =", "last exit code ="}
		seen := map[string]bool{}
		for _, line := range strings.Split(out, "\n") {
			t := strings.TrimSpace(line)
			for _, prefix := range want {
				if seen[prefix] {
					continue
				}
				if strings.HasPrefix(t, prefix) {
					fmt.Println("  " + t)
					seen[prefix] = true
					break
				}
			}
		}
	}
	if r.interval != "" {
		fmt.Printf("  interval = %s\n", r.interval)
	}
	if mtime, ok := fileMtime(r.logPath); ok {
		ago := time.Since(mtime).Round(time.Second)
		fmt.Printf("  last activity = %s (%s ago)\n",
			mtime.Local().Format("2006-01-02 15:04:05 MST"), formatAgo(ago))
	} else {
		fmt.Println("  last activity = never (no log yet)")
	}
	fmt.Printf("  plist: %s\n", plistPath)
	fmt.Printf("  log:   %s\n", r.logPath)
	fmt.Println()
}

// fileMtime returns the file's last-modified time. Used as a coarse
// "process did something recently" signal; for KeepAlive daemons the log
// is the cheapest indicator that the binary is alive and writing.
func fileMtime(path string) (time.Time, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return time.Time{}, false
	}
	return fi.ModTime(), true
}

// formatAgo renders a duration as "Xd Yh", "Xh Ym", or "Xm Ys" — coarse
// enough to read at a glance, fine enough to tell "minutes ago" from
// "hours ago".
func formatAgo(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	days := int(d / (24 * time.Hour))
	hours := int((d % (24 * time.Hour)) / time.Hour)
	mins := int((d % time.Hour) / time.Minute)
	secs := int((d % time.Minute) / time.Second)
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, mins)
	case mins > 0:
		return fmt.Sprintf("%dm %ds", mins, secs)
	default:
		return fmt.Sprintf("%ds", secs)
	}
}

func init() {
	rootCmd.AddCommand(statusCmd)
}
