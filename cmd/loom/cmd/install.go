package cmd

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"loom/internal/config"
	"loom/internal/launchd"
	"loom/transport/receiver"
	"loom/transport/shipper"
)

const (
	summarizerLabel = "com.loom.summarizer"
)

var installCmd = &cobra.Command{
	Use:       "install <component>",
	Short:     "Install a loom launchd agent",
	Long:      "Components: server | receiver | summarizer | shipper. Each writes a launchd plist that runs the current loom binary.",
	ValidArgs: []string{"server", "receiver", "summarizer", "shipper"},
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
			return nil
		case "receiver":
			return installReceiver()
		case "summarizer":
			return installSummarizer()
		case "shipper":
			return installShipper()
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
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.IntervalMinutes <= 0 {
		cfg.IntervalMinutes = config.DefaultIntervalMinutes
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

func installReceiver() error {
	token := os.Getenv("LOOM_RECEIVER_TOKEN")
	if token == "" {
		return fmt.Errorf("LOOM_RECEIVER_TOKEN is not set; export it before installing the receiver")
	}
	bin, err := loomBinary()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(config.Home(), "received"), 0o700); err != nil {
		return err
	}
	logPath := filepath.Join(config.Home(), "receiver.log")

	env := map[string]string{
		"LOOM_RECEIVER_TOKEN": token,
		"LOOM_HOME":           config.Home(),
	}

	spec := launchd.Spec{
		Label:     receiver.AgentLabel,
		Program:   bin,
		Args:      []string{"receiver"},
		LogPath:   logPath,
		Env:       env,
		KeepAlive: true,
		RunAtLoad: true,
	}
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
