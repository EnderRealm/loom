// loom-shipper walks agent session files, ships new bytes to the loom-receiver,
// and persists per-session cursors so subsequent runs are incremental.
//
// Commands:
//
//	loom-shipper once              single pass, ship and exit
//	loom-shipper install-agent     install a launchd user agent using config.interval_minutes
//	loom-shipper uninstall-agent   remove the launchd user agent
//	loom-shipper status            show launchctl state for the agent
package main

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"loom/internal/config"
	"loom/transport/internal/cursor"
	"loom/transport/internal/source"
	"loom/transport/internal/wire"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "once":
		runOnce()
	case "install-agent":
		installAgent()
	case "uninstall-agent":
		uninstallAgent()
	case "status":
		statusAgent()
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, "loom-shipper - ship agent session deltas to the loom receiver\n\n")
	fmt.Fprintf(os.Stderr, "usage:\n")
	fmt.Fprintf(os.Stderr, "  loom-shipper once              ship any new session bytes and exit\n")
	fmt.Fprintf(os.Stderr, "  loom-shipper install-agent     install launchd user agent using interval_minutes from config\n")
	fmt.Fprintf(os.Stderr, "  loom-shipper uninstall-agent   remove the launchd user agent\n")
	fmt.Fprintf(os.Stderr, "  loom-shipper status            show launchctl state for the agent\n\n")
	fmt.Fprintf(os.Stderr, "config: %s\n", config.Path())
	fmt.Fprintf(os.Stderr, "state:  %s\n", config.TransportDir())
}

// ---------- once ----------

func runOnce() {
	if err := acquireLock(); err != nil {
		fmt.Fprintln(os.Stderr, "another shipper is running, skipping")
		return
	}

	cfg, err := config.Load()
	if err != nil {
		die(err)
	}

	sessions, err := source.ListClaudeSessions()
	if err != nil {
		die(err)
	}

	shipped, skipped, failed := 0, 0, 0
	for _, s := range sessions {
		from, err := cursor.Read(source.Agent, s.SessionID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cursor read %s: %v\n", s.SessionID, err)
			failed++
			continue
		}
		lines, to, err := source.ReadDelta(s.Path, from)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read delta %s: %v\n", s.SessionID, err)
			failed++
			continue
		}
		if len(lines) == 0 {
			skipped++
			continue
		}
		req := wire.IngestRequest{
			Agent:      source.Agent,
			Project:    s.Project,
			SessionID:  s.SessionID,
			FromOffset: from,
			ToOffset:   to,
			Lines:      lines,
		}
		accepted, err := postIngest(cfg, req)
		if err != nil {
			fmt.Fprintf(os.Stderr, "POST %s: %v\n", s.SessionID, err)
			failed++
			continue
		}
		if err := cursor.Write(source.Agent, s.SessionID, accepted); err != nil {
			fmt.Fprintf(os.Stderr, "cursor write %s: %v\n", s.SessionID, err)
			failed++
			continue
		}
		shipped++
	}
	fmt.Printf("loom-shipper: shipped=%d skipped=%d failed=%d total=%d\n", shipped, skipped, failed, len(sessions))
}

func postIngest(cfg *config.Config, req wire.IngestRequest) (int64, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return 0, err
	}
	url := strings.TrimRight(cfg.ServerURL, "/") + "/v1/ingest"
	httpReq, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if cfg.AuthToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+cfg.AuthToken)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var out wire.IngestResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, fmt.Errorf("decode response: %w", err)
	}
	return out.AcceptedToOffset, nil
}

// ---------- lock ----------

func lockPath() string {
	return filepath.Join(config.TransportDir(), "shipper.lock")
}

func acquireLock() error {
	if err := os.MkdirAll(filepath.Dir(lockPath()), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(lockPath(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	// Non-blocking exclusive lock. f is intentionally leaked: the lock releases
	// automatically when this process exits.
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return err
	}
	return nil
}

// ---------- launchd agent management ----------

const agentLabel = "com.loom.shipper"

func agentPlistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", agentLabel+".plist"), nil
}

// launchctlDomain returns the gui/<uid> domain target for the current user.
// gui/ is the standard for user-facing LaunchAgents; user/ would cover SSH-only
// sessions but loom is meant for the user's own interactive Mac.
func launchctlDomain() string {
	return fmt.Sprintf("gui/%d", os.Getuid())
}

func launchctlTarget() string {
	return launchctlDomain() + "/" + agentLabel
}

func installAgent() {
	cfg, err := config.Load()
	if err != nil {
		die(err)
	}
	self, err := os.Executable()
	if err != nil {
		die(err)
	}
	if rp, err := filepath.EvalSymlinks(self); err == nil {
		self = rp
	}

	plistPath, err := agentPlistPath()
	if err != nil {
		die(err)
	}
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		die(err)
	}
	if err := os.MkdirAll(config.TransportDir(), 0o700); err != nil {
		die(err)
	}

	interval := cfg.IntervalMinutes
	if interval <= 0 {
		interval = config.DefaultIntervalMinutes
	}
	intervalSeconds := interval * 60
	logPath := filepath.Join(config.TransportDir(), "shipper.log")

	// Bake LOOM_HOME into the plist only if the user set it explicitly. Otherwise
	// let the agent fall back to ~/.loom naturally at runtime.
	loomHome := os.Getenv("LOOM_HOME")

	plist := buildPlist(self, intervalSeconds, logPath, loomHome)
	if err := os.WriteFile(plistPath, []byte(plist), 0o644); err != nil {
		die(err)
	}

	// Safety net: validate the plist before touching launchd. If this fails the
	// generator has a bug — delete the junk file so we don't poison ~/Library.
	if out, err := exec.Command("plutil", "-lint", plistPath).CombinedOutput(); err != nil {
		_ = os.Remove(plistPath)
		die(fmt.Errorf("plutil -lint: %v: %s", err, strings.TrimSpace(string(out))))
	}

	// Idempotency: bootout any previously-loaded instance before bootstrapping
	// the new one. bootout errors if nothing was loaded — intentionally ignored.
	_ = exec.Command("launchctl", "bootout", launchctlTarget()).Run()

	if out, err := exec.Command("launchctl", "bootstrap", launchctlDomain(), plistPath).CombinedOutput(); err != nil {
		die(fmt.Errorf("launchctl bootstrap: %v: %s", err, strings.TrimSpace(string(out))))
	}

	fmt.Printf("installed launchd agent:\n")
	fmt.Printf("  label:    %s\n", agentLabel)
	fmt.Printf("  plist:    %s\n", plistPath)
	fmt.Printf("  interval: %ds (%d min)\n", intervalSeconds, interval)
	fmt.Printf("  log:      %s\n", logPath)
}

func uninstallAgent() {
	plistPath, err := agentPlistPath()
	if err != nil {
		die(err)
	}

	// bootout first. Tolerate "not loaded" errors — launchctl's exit codes are
	// poorly documented, so filter on message fragments.
	out, err := exec.Command("launchctl", "bootout", launchctlTarget()).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" &&
			!strings.Contains(msg, "No such process") &&
			!strings.Contains(msg, "Could not find service") &&
			!strings.Contains(msg, "not find specified service") {
			fmt.Fprintf(os.Stderr, "launchctl bootout: %s\n", msg)
		}
	}

	if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
		die(err)
	}

	fmt.Println("removed loom-shipper launchd agent")
}

func statusAgent() {
	out, err := exec.Command("launchctl", "print", launchctlTarget()).CombinedOutput()
	if err != nil {
		fmt.Println("loom-shipper agent is not loaded")
		return
	}
	fmt.Print(string(out))
}

// buildPlist generates the LaunchAgent plist XML. Paths are XML-escaped so
// weird characters (though unlikely in a binary path) don't corrupt the file.
func buildPlist(binaryPath string, intervalSeconds int, logPath, loomHome string) string {
	binE := xmlEscape(binaryPath)
	logE := xmlEscape(logPath)

	envBlock := ""
	if loomHome != "" {
		envBlock = fmt.Sprintf(`
    <key>EnvironmentVariables</key>
    <dict>
        <key>LOOM_HOME</key>
        <string>%s</string>
    </dict>`, xmlEscape(loomHome))
	}

	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>%s</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
        <string>once</string>
    </array>
    <key>StartInterval</key>
    <integer>%d</integer>
    <key>RunAtLoad</key>
    <true/>
    <key>StandardOutPath</key>
    <string>%s</string>
    <key>StandardErrorPath</key>
    <string>%s</string>%s
</dict>
</plist>
`, agentLabel, binE, intervalSeconds, logE, logE, envBlock)
}

func xmlEscape(s string) string {
	var buf bytes.Buffer
	_ = xml.EscapeText(&buf, []byte(s))
	return buf.String()
}

func die(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
