package cmd

import (
	"bufio"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"loom/internal/config"
	"loom/internal/launchd"
	"loom/internal/updater"
	"loom/transport/receiver"
	"loom/transport/shipper"
)

const (
	summarizerLabel = "com.loom.summarizer"
)

var installCmd = &cobra.Command{
	Use:       "install <component>",
	Short:     "Install a loom launchd agent",
	Long:      "Components: server | remote | receiver | summarizer | shipper | updater. Each writes a launchd plist that runs the current loom binary. The server and remote profiles also record the machine's role for health reporting.",
	ValidArgs: []string{"server", "remote", "receiver", "summarizer", "shipper", "updater"},
	Args:      cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
	RunE: func(cmd *cobra.Command, args []string) error {
		switch args[0] {
		case "server":
			if err := installReceiver(); err != nil {
				return err
			}
			if err := installSummarizer(); err != nil {
				return err
			}
			return config.WriteRole(config.RoleServer)
		case "remote":
			if err := installShipper(); err != nil {
				return err
			}
			return config.WriteRole(config.RoleRemote)
		case "receiver":
			return installReceiver()
		case "summarizer":
			return installSummarizer()
		case "shipper":
			return installShipper()
		case "updater":
			return installUpdater()
		default:
			return fmt.Errorf("unknown component %q", args[0])
		}
	},
}

func init() {
	rootCmd.AddCommand(installCmd)
}

// loomBinary returns the absolute path to the running loom binary, with
// any symlinks resolved. Plists pin this path so launchd runs the same
// binary the user just installed; rebuilding to the same path picks up
// new code on the next respawn without touching the plist.
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

// loomHomeForPlist returns the value to bake into a plist's
// EnvironmentVariables only when the user explicitly set LOOM_HOME.
// Otherwise the agent falls back to ~/.loom at runtime.
func loomHomeForPlist() string {
	return os.Getenv("LOOM_HOME")
}

func installShipper() error {
	cfg, err := shipper.LoadConfig()
	if err != nil {
		return err
	}
	if cfg.IntervalMinutes <= 0 {
		cfg.IntervalMinutes = shipper.DefaultIntervalMinutes
	}
	bin, err := loomBinary()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(config.TransportDir(), 0o700); err != nil {
		return err
	}
	logPath := filepath.Join(config.TransportDir(), "shipper.log")

	spec := launchd.Spec{
		Label:                   shipper.AgentLabel,
		Program:                 bin,
		Args:                    []string{"shipper", "daemon"},
		LogPath:                 logPath,
		KeepAlive:               true,
		RunAtLoad:               true,
		ThrottleIntervalSeconds: 10,
	}
	if h := loomHomeForPlist(); h != "" {
		spec.Env = map[string]string{"LOOM_HOME": h}
	}
	if err := launchd.Install(spec); err != nil {
		return err
	}
	// bootstrap honors RunAtLoad on initial load, but a re-install (bootout +
	// bootstrap of an already-known label) can leave the job in pended/
	// speculative state. Kickstart forces an immediate spawn so a rebuild +
	// reinstall doesn't silently halt shipping until the next login.
	if err := launchd.Kickstart(spec.Label); err != nil {
		fmt.Fprintf(os.Stderr, "warn: kickstart: %v\n", err)
	}
	fmt.Printf("installed shipper:\n")
	fmt.Printf("  label:    %s\n", spec.Label)
	fmt.Printf("  binary:   %s\n", bin)
	fmt.Printf("  interval: %d min (in-process ticker; plist is KeepAlive)\n", cfg.IntervalMinutes)
	fmt.Printf("  log:      %s\n", logPath)
	return nil
}

// stdinIsTTY reports whether stdin is an interactive terminal. A package
// var so tests can force the non-interactive path deterministically.
var stdinIsTTY = func() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// resolveReceiverToken returns the receiver bearer token, persisting it to
// ~/.loom/receiver-token so later runs (and the updater's re-bootstrap)
// resolve it without the secret in their env. Resolution order: env
// LOOM_RECEIVER_TOKEN (also seeds/migrates the file), then the persisted
// file, then an interactive prompt. Non-interactive with neither errors
// rather than blocking on stdin.
func resolveReceiverToken() (string, error) {
	if tok := strings.TrimSpace(os.Getenv("LOOM_RECEIVER_TOKEN")); tok != "" {
		if err := config.WriteReceiverToken(tok); err != nil {
			return "", err
		}
		return tok, nil
	}
	if tok := config.ReadReceiverToken(); tok != "" {
		return tok, nil
	}
	if !stdinIsTTY() {
		return "", fmt.Errorf("LOOM_RECEIVER_TOKEN not set and no %s; export it or run install interactively", config.ReceiverTokenPath())
	}
	// Plain line read: echo is on (x/term is not in the module graph, so
	// no no-echo prompt is available without adding a dependency).
	fmt.Fprint(os.Stderr, "Receiver bearer token: ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("read token: %w", err)
	}
	tok := strings.TrimSpace(line)
	if tok == "" {
		return "", fmt.Errorf("receiver token is empty")
	}
	if err := config.WriteReceiverToken(tok); err != nil {
		return "", err
	}
	return tok, nil
}

// receiverSpec builds the receiver's launchd Spec. The token is read from
// ~/.loom/receiver-token at runtime, not the plist, so it isn't exposed via
// the plist file or `launchctl print`.
func receiverSpec(bin, logPath string) launchd.Spec {
	return launchd.Spec{
		Label:     receiver.AgentLabel,
		Program:   bin,
		Args:      []string{"receiver"},
		LogPath:   logPath,
		Env:       map[string]string{"LOOM_HOME": config.Home()},
		KeepAlive: true,
		RunAtLoad: true,
	}
}

func installReceiver() error {
	if _, err := resolveReceiverToken(); err != nil {
		return err
	}
	bin, err := loomBinary()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(config.Home(), "received"), 0o700); err != nil {
		return err
	}
	logPath := filepath.Join(config.Home(), "receiver.log")

	spec := receiverSpec(bin, logPath)
	if err := launchd.Install(spec); err != nil {
		return err
	}
	if err := launchd.Kickstart(spec.Label); err != nil {
		fmt.Fprintf(os.Stderr, "warn: kickstart: %v\n", err)
	}

	if waitForHealthz("http://127.0.0.1:8765/healthz", 10*time.Second) {
		fmt.Printf("installed receiver:\n")
		fmt.Printf("  label:    %s\n", spec.Label)
		fmt.Printf("  binary:   %s\n", bin)
		fmt.Printf("  log:      %s\n", logPath)
		fmt.Printf("  healthz:  ok\n")
	} else {
		fmt.Fprintf(os.Stderr, "warn: receiver did not respond on /healthz within 10s — check %s\n", logPath)
	}
	return nil
}

func installSummarizer() error {
	bin, err := loomBinary()
	if err != nil {
		return err
	}
	logPath := filepath.Join(config.Home(), "summarizer.log")

	spec := launchd.Spec{
		Label:     summarizerLabel,
		Program:   bin,
		Args:      []string{"summarize", "--watch"},
		LogPath:   logPath,
		Env:       map[string]string{"LOOM_HOME": config.Home()},
		KeepAlive: true,
		RunAtLoad: true,
	}
	if err := launchd.Install(spec); err != nil {
		return err
	}
	if err := launchd.Kickstart(spec.Label); err != nil {
		fmt.Fprintf(os.Stderr, "warn: kickstart: %v\n", err)
	}
	fmt.Printf("installed summarizer:\n")
	fmt.Printf("  label:    %s\n", spec.Label)
	fmt.Printf("  binary:   %s\n", bin)
	fmt.Printf("  log:      %s\n", logPath)
	return nil
}

// installUpdater installs the self-managing auto-updater. The updater
// polls EnderRealm/loom's latest GitHub Release on a tunable interval
// and, when a newer release than the running binary ships, downloads the
// platform tarball, verifies its checksum, installs it over the running
// binary, and re-bootstraps every other loom agent (itself last).
// LOOM_UPDATER_INTERVAL_MINUTES tunes the poll cadence.
func installUpdater() error {
	bin, err := loomBinary()
	if err != nil {
		return err
	}
	logPath := updater.LogPath()

	env := map[string]string{
		"LOOM_HOME":                     config.Home(),
		"LOOM_UPDATER_INTERVAL_MINUTES": strconv.Itoa(updater.DefaultIntervalMinutes),
		// PATH for launchctl discovery; launchd's stripped PATH already
		// covers /bin, but a comprehensive PATH keeps every loom agent
		// consistent.
		"PATH": canonicalPath(),
	}

	spec := launchd.Spec{
		Label:                   updater.AgentLabel,
		Program:                 bin,
		Args:                    []string{"updater", "daemon"},
		LogPath:                 logPath,
		Env:                     env,
		KeepAlive:               true,
		RunAtLoad:               true,
		ThrottleIntervalSeconds: 30,
	}
	if err := launchd.Install(spec); err != nil {
		return err
	}
	fmt.Printf("installed updater:\n")
	fmt.Printf("  label:    %s\n", spec.Label)
	fmt.Printf("  binary:   %s\n", bin)
	fmt.Printf("  interval: %d min\n", updater.DefaultIntervalMinutes)
	fmt.Printf("  log:      %s\n", logPath)
	return nil
}

// canonicalPath returns a PATH that includes the common Homebrew and
// Go install locations the updater needs. launchd's default PATH is
// /usr/bin:/bin:/usr/sbin:/sbin which doesn't include Homebrew's
// /opt/homebrew/bin or the user's ~/go/bin.
func canonicalPath() string {
	parts := []string{
		"/opt/homebrew/bin",
		"/opt/homebrew/sbin",
		"/usr/local/bin",
		"/usr/local/sbin",
		"/usr/bin",
		"/bin",
		"/usr/sbin",
		"/sbin",
	}
	if home, err := os.UserHomeDir(); err == nil {
		parts = append([]string{filepath.Join(home, ".local", "bin"), filepath.Join(home, "go", "bin")}, parts...)
	}
	return joinPath(parts)
}

func joinPath(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ":"
		}
		out += p
	}
	return out
}

// waitForHealthz polls the URL until 2xx or timeout. Used during install
// to surface "the binary started and listens" rather than relying on the
// user to tail the log.
func waitForHealthz(url string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	c := &http.Client{Timeout: time.Second}
	for time.Now().Before(deadline) {
		resp, err := c.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return true
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}
