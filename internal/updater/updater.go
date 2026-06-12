// Package updater is loom's self-managing auto-updater. As a launchd
// KeepAlive daemon it polls the source repo's origin/main on a tunable
// interval, pulls clean, rebuilds the loom binary, and kickstarts every
// other loom agent (itself last). The pattern is documented in
// docs/auto-update-pattern.md.
//
// The updater is opt-in: source-checkout users run `loom install
// updater`; brew users typically don't bother and rely on
// `brew upgrade` instead.
package updater

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"loom/internal/config"
	"loom/internal/launchd"
	"loom/transport/receiver"
	"loom/transport/shipper"
)

// AgentLabel is the launchd label for the updater daemon.
const AgentLabel = "com.loom.updater"

// SummarizerLabel is duplicated here from cmd/loom/cmd/install.go so
// the updater package doesn't pull in cobra. Keep these in sync.
const SummarizerLabel = "com.loom.summarizer"

// DefaultIntervalMinutes is the poll cadence. 5 minutes matches the
// ghostwheel deployer; gives near-real-time deploys without hammering
// the remote.
const DefaultIntervalMinutes = 5

// Daemon runs Tick on a ticker driven by the configured interval. Like
// the shipper daemon, KeepAlive in the plist handles crash respawn so
// non-fatal errors here just log and continue.
//
// Cancellable via SIGINT / SIGTERM through the supplied context.
func Daemon(ctx context.Context) error {
	src, err := SourceDir()
	if err != nil {
		return fmt.Errorf("source dir: %w", err)
	}
	interval := intervalFromEnv()
	log.Printf("updater starting interval=%s source=%s", interval, src)

	// One immediate tick on startup so a freshly-installed updater
	// doesn't sit idle for the full interval before its first check.
	if err := Tick(ctx, src); err != nil {
		log.Printf("tick: %v", err)
	}

	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Printf("updater shutdown requested")
			return nil
		case <-t.C:
			if err := Tick(ctx, src); err != nil {
				log.Printf("tick: %v", err)
			}
		}
	}
}

// Tick performs one git fetch + (if origin/main moved cleanly) reset +
// rebuild + kickstart cycle. Exposed so a one-off `loom updater once`
// can drive the same logic without the daemon loop.
func Tick(ctx context.Context, src string) error {
	updated, err := updateSource(ctx, src)
	if err != nil {
		return err
	}
	if !updated {
		return nil
	}

	bin, err := loomBinary()
	if err != nil {
		return fmt.Errorf("locate loom binary: %w", err)
	}
	if err := rebuild(ctx, src, bin); err != nil {
		return fmt.Errorf("rebuild: %w", err)
	}
	log.Printf("rebuilt %s", bin)

	// Kickstart every loaded loom agent except ourselves. They respawn
	// under the new binary on KeepAlive. Order doesn't matter — they're
	// independent — but we save the updater (us) for last so we don't
	// kill ourselves mid-deploy.
	for _, label := range []string{shipper.AgentLabel, receiver.AgentLabel, SummarizerLabel} {
		if !plistPresent(label) {
			continue
		}
		if err := launchd.Kickstart(label); err != nil {
			log.Printf("kickstart %s: %v", label, err)
			continue
		}
		log.Printf("kickstarted %s", label)
	}

	// Last: kickstart self. launchd kills the current process and
	// respawns it under the new binary; from here on the new code runs.
	log.Printf("kickstarting self %s — new binary takes over", AgentLabel)
	if err := launchd.Kickstart(AgentLabel); err != nil {
		return fmt.Errorf("kickstart self: %w", err)
	}
	return nil
}

// updateSource fetches origin/main and, when the checkout can safely
// fast-forward to it, runs reset --hard and reports updated=true. Any
// other condition returns (false, nil) so the caller skips the deploy.
func updateSource(ctx context.Context, src string) (bool, error) {
	curSHA, err := gitOutput(ctx, src, "rev-parse", "HEAD")
	if err != nil {
		return false, fmt.Errorf("rev-parse HEAD: %w", err)
	}
	if _, err := gitOutput(ctx, src, "fetch", "--quiet", "origin", "main"); err != nil {
		return false, fmt.Errorf("fetch: %w", err)
	}
	remoteSHA, err := gitOutput(ctx, src, "rev-parse", "origin/main")
	if err != nil {
		return false, fmt.Errorf("rev-parse origin/main: %w", err)
	}
	if curSHA == remoteSHA {
		log.Printf("up to date (sha=%s)", curSHA[:short])
		return false, nil
	}

	// Guard against destroying local state on machines where the deploy
	// checkout is also a dev checkout. reset --hard is unrecoverable, so
	// it runs only for a pure fast-forward of a clean main. Checks run
	// after the fetch (the ancestry check needs the fresh origin/main)
	// and before the reset.
	if status, err := gitOutput(ctx, src, "status", "--porcelain"); err != nil {
		return false, fmt.Errorf("status: %w", err)
	} else if status != "" {
		log.Printf("skipping deploy: working tree dirty; resumes once clean")
		return false, nil
	}
	branch, err := gitOutput(ctx, src, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return false, fmt.Errorf("rev-parse branch: %w", err)
	}
	if branch != "main" {
		log.Printf("skipping deploy: HEAD on %q, not main", branch)
		return false, nil
	}
	// HEAD != origin/main was established above, so an affirmative
	// ancestor result means HEAD is strictly behind — a fast-forward.
	// A negative result means HEAD diverged or is ahead; refuse it.
	if ancestor, err := isAncestor(ctx, src, "HEAD", "origin/main"); err != nil {
		return false, fmt.Errorf("merge-base: %w", err)
	} else if !ancestor {
		log.Printf("skipping deploy: HEAD %s diverged from origin/main %s", curSHA[:short], remoteSHA[:short])
		return false, nil
	}
	log.Printf("update available %s → %s", curSHA[:short], remoteSHA[:short])

	if _, err := gitOutput(ctx, src, "reset", "--hard", "origin/main"); err != nil {
		return false, fmt.Errorf("reset: %w", err)
	}
	return true, nil
}

// isAncestor reports whether commit is an ancestor of ref. git
// merge-base --is-ancestor signals "no" with exit code 1 and no output,
// which gitOutput would surface as an error; this distinguishes that
// expected non-zero exit from a genuine failure.
func isAncestor(ctx context.Context, dir, commit, ref string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "merge-base", "--is-ancestor", commit, ref)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err == nil {
		return true, nil
	}
	if exit, ok := err.(*exec.ExitError); ok && exit.ProcessState.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
}

const short = 8

// SourceDir returns the directory the updater pulls and rebuilds in.
// Order: $LOOM_SOURCE env var (set on the plist when the user
// installed from a non-default location), then ~/code/loom which is
// the convention.
func SourceDir() (string, error) {
	if v := os.Getenv("LOOM_SOURCE"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "code", "loom"), nil
}

// intervalFromEnv reads LOOM_UPDATER_INTERVAL_MINUTES (set on the plist
// at install time) or returns DefaultIntervalMinutes.
func intervalFromEnv() time.Duration {
	if v := os.Getenv("LOOM_UPDATER_INTERVAL_MINUTES"); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 {
			return time.Duration(n) * time.Minute
		}
	}
	return DefaultIntervalMinutes * time.Minute
}

// loomBinary returns the absolute path of the running loom binary so
// rebuild can write to the same path. Plists pin this path; rebuilding
// in place + kickstart is the entire deploy sequence.
func loomBinary() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	if rp, err := filepath.EvalSymlinks(self); err == nil {
		self = rp
	}
	return self, nil
}

func rebuild(ctx context.Context, src, bin string) error {
	cmd := exec.CommandContext(ctx, "go", "build", "-o", bin, "./cmd/loom")
	cmd.Dir = src
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func plistPresent(label string) bool {
	p, err := launchd.PlistPath(label)
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return err == nil
}

// LogPath returns the canonical updater log file path.
func LogPath() string {
	return filepath.Join(config.Home(), "updater.log")
}
