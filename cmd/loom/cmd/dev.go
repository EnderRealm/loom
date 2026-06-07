package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"loom/internal/launchd"
	"loom/internal/tui"
	"loom/internal/updater"
	"loom/transport/receiver"
	"loom/transport/shipper"
)

var devCmd = &cobra.Command{
	Use:   "dev",
	Short: "At-a-glance view of development state on this machine",
	RunE: func(cmd *cobra.Command, args []string) error {
		projects, err := tui.LoadProjects()
		if err != nil {
			return err
		}

		fmt.Println("=== loom status ===")
		labels := []string{
			receiver.AgentLabel,
			summarizerLabel,
			shipper.AgentLabel,
			updater.AgentLabel,
		}
		running := 0
		for _, label := range labels {
			if daemonRunning(label) {
				running++
			}
		}
		fmt.Printf("  daemons: %d/%d running\n", running, len(labels))
		pending := 0
		for _, p := range projects {
			pending += p.PendingCount
		}
		fmt.Printf("  pending sync: %d sessions\n", pending)
		fmt.Println()

		fmt.Println("=== dirty repos ===")
		any := false
		for _, p := range projects {
			if p.Path == "" {
				continue
			}
			changed, ok := gitDirtyCount(p.Path)
			if !ok || changed == 0 {
				continue
			}
			any = true
			fmt.Printf("  %s: %d changed  (%s)\n", p.Name, changed, devShortenPath(p.Path))
		}
		if !any {
			fmt.Println("  none")
		}
		fmt.Println()

		fmt.Println("=== ready tickets ===")
		any = false
		for _, p := range projects {
			if p.Tickets == nil {
				continue
			}
			if n := p.Tickets.Status["ready"]; n > 0 {
				any = true
				fmt.Printf("  %s: %d ready\n", p.Name, n)
			}
		}
		if !any {
			fmt.Println("  none")
		}
		return nil
	},
}

// daemonRunning reports whether a daemon's plist is installed and launchd
// shows it in the running state. Not-installed and not-loaded both count as
// not-running.
func daemonRunning(label string) bool {
	plistPath, err := launchd.PlistPath(label)
	if err != nil || plistPath == "" {
		return false
	}
	if _, err := os.Stat(plistPath); err != nil {
		return false
	}
	out, loaded, err := launchd.Status(label)
	if err != nil || !loaded {
		return false
	}
	// Compare the first top-level state value exactly: launchctl emits
	// "state = not running" for a loaded-but-crash-looping daemon, and the
	// value repeats inside coalition blocks, so a substring match on the
	// first line is both wrong and ambiguous.
	for _, line := range strings.Split(out, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "state =") {
			return strings.TrimSpace(strings.TrimPrefix(t, "state =")) == "running"
		}
	}
	return false
}

// gitDirtyCount runs `git status --porcelain` and returns the number of
// changed files. ok is false when the path isn't a git repo or git errors —
// callers treat that as not-dirty rather than failing the command.
func gitDirtyCount(path string) (count int, ok bool) {
	out, err := exec.Command("git", "-C", path, "status", "--porcelain").Output()
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count, true
}

// devShortenPath contracts the home dir to ~ for readable output.
func devShortenPath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(p, home) {
		return "~" + p[len(home):]
	}
	return p
}

func init() {
	rootCmd.AddCommand(devCmd)
}
