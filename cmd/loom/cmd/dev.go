package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"loom/internal/config"
	"loom/internal/extract"
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

		role := config.ReadRole()
		labels := expectedDaemons(role, plistInstalled(updater.AgentLabel))
		running := 0
		for _, label := range labels {
			if daemonRunning(label) {
				running++
			}
		}
		pending := 0
		for _, p := range projects {
			pending += p.PendingCount
		}

		// State precedence: any expected daemon down is degraded (red)
		// regardless of pending; all up with a backlog is yellow; all up and
		// clear is green.
		state := "healthy"
		stateStyle := tui.StyleSuccess
		switch {
		case running < len(labels):
			state = "degraded"
			stateStyle = tui.StyleDanger
		case pending > 0:
			state = "backlog"
			stateStyle = tui.StyleWarning
		}
		dot := stateStyle.Render("●")
		var detail string
		if role != "" {
			detail = tui.StyleDim.Render(fmt.Sprintf("%s · %d/%d daemons · %d pending", role, running, len(labels), pending))
		} else {
			detail = tui.StyleDim.Render(fmt.Sprintf("%d/%d daemons · %d pending", running, len(labels), pending))
		}
		fmt.Printf("%s loom %s   %s\n", dot, stateStyle.Render(state), detail)
		if role == "" {
			fmt.Println(tui.StyleDim.Render("  no role set — run 'loom install server' or 'loom install remote'"))
		}
		fmt.Println()

		fmt.Println(tui.StyleSection.Render("Dirty repos"))
		type dirtyRow struct {
			name    string
			changed int
			path    string
		}
		var dirty []dirtyRow
		nameWidth := 0
		for _, p := range projects {
			if p.Path == "" {
				continue
			}
			changed, ok := gitDirtyCount(p.Path)
			if !ok || changed == 0 {
				continue
			}
			dirty = append(dirty, dirtyRow{p.Name, changed, devShortenPath(p.Path)})
			if len(p.Name) > nameWidth {
				nameWidth = len(p.Name)
			}
		}
		if len(dirty) == 0 {
			fmt.Println("  " + tui.StyleDim.Render("none"))
		} else {
			for _, r := range dirty {
				fmt.Printf("  %-*s   %d changed   %s\n", nameWidth, r.name, r.changed, r.path)
			}
		}
		fmt.Println()

		fmt.Println(tui.StyleSection.Render("Ready tickets"))
		type readyRow struct {
			name  string
			count int
		}
		var ready []readyRow
		nameWidth = 0
		for _, p := range projects {
			if p.Tickets == nil {
				continue
			}
			if n := p.Tickets.Status["ready"]; n > 0 {
				ready = append(ready, readyRow{p.Name, n})
				if len(p.Name) > nameWidth {
					nameWidth = len(p.Name)
				}
			}
		}
		if len(ready) == 0 {
			fmt.Println("  " + tui.StyleDim.Render("none"))
		} else {
			for _, r := range ready {
				fmt.Printf("  %-*s   %d ready\n", nameWidth, r.name, r.count)
			}
		}
		fmt.Println()

		fmt.Println(tui.StyleSection.Render("Unreleased changelog"))
		type changelogRow struct {
			name  string
			count int
		}
		var unreleased []changelogRow
		nameWidth = 0
		for _, p := range projects {
			if p.Path == "" {
				continue
			}
			count, ok := unreleasedChangelogEntries(p.Path)
			if !ok || count == 0 {
				continue
			}
			unreleased = append(unreleased, changelogRow{p.Name, count})
			if len(p.Name) > nameWidth {
				nameWidth = len(p.Name)
			}
		}
		if len(unreleased) == 0 {
			fmt.Println("  " + tui.StyleDim.Render("none"))
		} else {
			for _, r := range unreleased {
				fmt.Printf("  %-*s   %d entries\n", nameWidth, r.name, r.count)
			}
		}
		return nil
	},
}

// unreleasedChangelogEntries counts the bullet entries under the
// `## [Unreleased]` heading of a project's root CHANGELOG.md — the pending
// changes that a release would publish. ok is false when the project has no
// CHANGELOG.md, so callers treat changelog-less repos as not-applicable rather
// than failing the command. An empty scaffold (only `###` subsection headers,
// no bullets) yields a zero count.
func unreleasedChangelogEntries(repoPath string) (count int, ok bool) {
	data, err := os.ReadFile(filepath.Join(repoPath, "CHANGELOG.md"))
	if err != nil {
		return 0, false
	}
	inUnreleased := false
	for _, line := range strings.Split(string(data), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "## ") {
			// A top-level heading: enter the Unreleased block, or leave it on
			// reaching the next versioned section.
			inUnreleased = strings.Contains(strings.ToLower(t), "[unreleased]")
			continue
		}
		if inUnreleased && (strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "* ")) {
			count++
		}
	}
	return count, true
}

// expectedDaemons returns the set of daemon labels that should be running
// for a machine's role, used as the denominator of the dev health rollup.
// A server folds sessions (receiver + summarizer) and extracts knowledge
// from them (extractor); a remote only ships them. The updater is appended
// for either role only when its plist is actually installed — brew-installed
// machines lack a source checkout and never install it, so its absence must
// not count against health. An unknown or empty role falls back to the legacy
// all-four set.
func expectedDaemons(role string, updaterInstalled bool) []string {
	var labels []string
	switch role {
	case config.RoleServer:
		labels = []string{receiver.AgentLabel, summarizerLabel, extract.AgentLabel}
	case config.RoleRemote:
		labels = []string{shipper.AgentLabel}
	default:
		return []string{receiver.AgentLabel, summarizerLabel, shipper.AgentLabel, updater.AgentLabel}
	}
	if updaterInstalled {
		labels = append(labels, updater.AgentLabel)
	}
	return labels
}

// plistInstalled reports whether a daemon's launchd plist exists on disk,
// independent of whether launchd has it loaded or running. daemonRunning
// conflates not-installed with not-running, which is correct for the
// expected set but wrong for deciding whether to expect the updater at
// all — that decision needs the pure installed/not-installed signal.
func plistInstalled(label string) bool {
	plistPath, err := launchd.PlistPath(label)
	if err != nil || plistPath == "" {
		return false
	}
	_, err = os.Stat(plistPath)
	return err == nil
}

// daemonRunning reports whether a daemon's plist is installed and launchd
// shows it in the running state. Not-installed and not-loaded both count as
// not-running.
func daemonRunning(label string) bool {
	if !plistInstalled(label) {
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
