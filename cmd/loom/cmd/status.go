package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"loom/internal/config"
	"loom/internal/extract"
	"loom/internal/launchd"
	"loom/internal/updater"
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

		role := config.ReadRole()
		// Mirror dev.go's math: the updater counts toward the expected set
		// only when its plist is installed, so brew machines without a
		// source checkout don't flag a missing updater.
		expected := map[string]bool{}
		for _, label := range expectedDaemons(role, plistInstalled(updater.AgentLabel)) {
			expected[label] = true
		}

		printAgent(agentReport{
			human:    "loom-receiver",
			label:    receiver.AgentLabel,
			logPath:  filepath.Join(config.Home(), "receiver.log"),
			interval: "n/a (HTTP server)",
			role:     role,
			expected: expected[receiver.AgentLabel],
		})
		printAgent(agentReport{
			human:    "loom-summarizer",
			label:    summarizerLabel,
			logPath:  filepath.Join(config.Home(), "summarizer.log"),
			interval: "30s (sweep ticker)",
			role:     role,
			expected: expected[summarizerLabel],
		})
		// The extractor shells out to extract.py from a loom checkout, which
		// the release tarball doesn't carry; surface whether it resolves so a
		// silently no-op agent is visible here and not only in its log. The
		// settings are the agent's own resolution (persisted tunables, then
		// defaults) rather than whatever this shell happens to export.
		es := extract.CurrentSettings()
		scriptNote := "script    = " + filepath.Join(es.ExtractorsDir, "extract.py")
		if script, err := extract.ScriptPath(); err == nil {
			scriptNote = "script    = " + script
		} else {
			scriptNote += " (MISSING — set LOOM_EXTRACTORS_DIR)"
		}
		printAgent(agentReport{
			human:    "loom-extractor",
			label:    extract.AgentLabel,
			logPath:  extract.LogPath(),
			interval: fmt.Sprintf("%s (sweep ticker)", extract.DefaultInterval),
			notes: []string{
				scriptNote,
				fmt.Sprintf("model     = %s:%s", es.Provider, es.Model),
				"knowledge = " + es.KnowledgeRoot,
			},
			role:     role,
			expected: expected[extract.AgentLabel],
		})
		printAgent(agentReport{
			human:    "loom-shipper",
			label:    shipper.AgentLabel,
			logPath:  filepath.Join(config.TransportDir(), "shipper.log"),
			interval: shipperInterval,
			role:     role,
			expected: expected[shipper.AgentLabel],
		})
		printAgent(agentReport{
			human:    "loom-updater",
			label:    updater.AgentLabel,
			logPath:  updater.LogPath(),
			interval: fmt.Sprintf("%dm (release poll)", updater.DefaultIntervalMinutes),
			role:     role,
			expected: expected[updater.AgentLabel],
		})
		// Sync health is shipper-specific notify state; only meaningful on
		// machines that actually run the shipper.
		if plistInstalled(shipper.AgentLabel) {
			fmt.Println("=== sync health ===")
			if err := shipper.PrintHealth(os.Stdout); err != nil {
				fmt.Fprintf(os.Stderr, "  (no notify state: %v)\n", err)
			}
			fmt.Println()
		}
		fmt.Println("=== config ===")
		fmt.Printf("  LOOM_HOME=%s\n", config.Home())
		if role != "" {
			fmt.Printf("  role: %s\n", role)
		} else {
			fmt.Println("  role: not set")
		}
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
	notes    []string // optional extra lines, e.g. resolved external dependencies
	role     string   // machine role, for the not-installed marker
	expected bool     // whether this role expects the component installed
}

func printAgent(r agentReport) {
	plistPath, _ := launchd.PlistPath(r.label)
	if plistPath == "" {
		return
	}
	if _, err := os.Stat(plistPath); err != nil {
		// A component a set role expects but that isn't installed gets an
		// explicit marker rather than a silent skip, so the gap is visible.
		// Components outside the role, and every component on a no-role
		// machine, stay silently skipped (the legacy behavior).
		if r.role != "" && r.expected {
			fmt.Printf("=== %s ===\n", r.human)
			fmt.Printf("  not installed (expected for %s role)\n", r.role)
			fmt.Println()
		}
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
	for _, n := range r.notes {
		fmt.Printf("  %s\n", n)
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
