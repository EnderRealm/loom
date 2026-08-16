// Package launchd owns macOS LaunchAgent management for every loom component.
// One Spec shape generates the plist XML, plutil-lints it, and drives
// bootout/bootstrap/kickstart. The shipper, receiver, and summarizer share
// this package so plist drift between components is impossible.
package launchd

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Spec describes one LaunchAgent. Zero values are sensible: an agent with
// only Label/Program/Args/LogPath set produces a one-shot daemon that runs
// at load and stays up.
type Spec struct {
	Label   string            // e.g. "com.loom.shipper"
	Program string            // absolute path to the binary
	Args    []string          // arguments after the program path
	LogPath string            // stdout and stderr both go here
	Env     map[string]string // EnvironmentVariables; nil/empty omits the block

	// KeepAlive maps to launchd's KeepAlive=true: respawn on exit. For
	// long-running daemons (receiver, summarizer, shipper-as-daemon) this
	// pairs with ThrottleInterval to bound crash-loop CPU.
	KeepAlive bool

	// RunAtLoad fires the job once at bootstrap time, regardless of other
	// triggers. Default-true for every loom component.
	RunAtLoad bool

	// ThrottleIntervalSeconds is the minimum seconds between successive
	// spawns. launchd's default is 10; we set it explicitly so respawn
	// behavior is documented in the plist.
	ThrottleIntervalSeconds int
}

// PlistXML renders the LaunchAgent plist. Order of keys is fixed so two runs
// against the same Spec produce byte-identical output.
func (s Spec) PlistXML() string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0">` + "\n")
	b.WriteString("<dict>\n")

	writeStringKey(&b, "Label", s.Label)

	b.WriteString("    <key>ProgramArguments</key>\n    <array>\n")
	b.WriteString("        <string>" + xmlEscape(s.Program) + "</string>\n")
	for _, a := range s.Args {
		b.WriteString("        <string>" + xmlEscape(a) + "</string>\n")
	}
	b.WriteString("    </array>\n")

	if len(s.Env) > 0 {
		// Stable key order for reproducibility.
		keys := make([]string, 0, len(s.Env))
		for k := range s.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteString("    <key>EnvironmentVariables</key>\n    <dict>\n")
		for _, k := range keys {
			b.WriteString("        <key>" + xmlEscape(k) + "</key>\n")
			b.WriteString("        <string>" + xmlEscape(s.Env[k]) + "</string>\n")
		}
		b.WriteString("    </dict>\n")
	}

	if s.RunAtLoad {
		b.WriteString("    <key>RunAtLoad</key>\n    <true/>\n")
	}
	if s.KeepAlive {
		b.WriteString("    <key>KeepAlive</key>\n    <true/>\n")
	}
	if s.ThrottleIntervalSeconds > 0 {
		fmt.Fprintf(&b, "    <key>ThrottleInterval</key>\n    <integer>%d</integer>\n", s.ThrottleIntervalSeconds)
	}

	if s.LogPath != "" {
		writeStringKey(&b, "StandardOutPath", s.LogPath)
		writeStringKey(&b, "StandardErrorPath", s.LogPath)
	}

	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}

func writeStringKey(b *strings.Builder, key, value string) {
	b.WriteString("    <key>" + xmlEscape(key) + "</key>\n")
	b.WriteString("    <string>" + xmlEscape(value) + "</string>\n")
}

func xmlEscape(s string) string {
	var buf bytes.Buffer
	_ = xml.EscapeText(&buf, []byte(s))
	return buf.String()
}

// PlistPath returns ~/Library/LaunchAgents/<label>.plist.
func PlistPath(label string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", label+".plist"), nil
}

// domain returns the gui/<uid> launchctl domain for the current user.
func domain() string {
	return fmt.Sprintf("gui/%d", os.Getuid())
}

// target returns gui/<uid>/<label>, the launchctl target string.
func target(label string) string {
	return domain() + "/" + label
}

// Install writes the plist, validates it with plutil, boots out any prior
// instance, and bootstraps the new one. Idempotent: re-running with the same
// Spec is a no-op against state but cheap.
func Install(s Spec) error {
	if err := s.validate(); err != nil {
		return err
	}
	plistPath, err := PlistPath(s.Label)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(plistPath, []byte(s.PlistXML()), 0o644); err != nil {
		return err
	}
	// plutil-lint catches generator bugs before they poison ~/Library.
	if out, err := exec.Command("plutil", "-lint", plistPath).CombinedOutput(); err != nil {
		_ = os.Remove(plistPath)
		return fmt.Errorf("plutil -lint: %v: %s", err, strings.TrimSpace(string(out)))
	}
	// bootout-then-bootstrap is the macOS-recommended replace pattern. bootout
	// returns nonzero if nothing was loaded; ignored.
	_ = exec.Command("launchctl", "bootout", target(s.Label)).Run()
	// bootout returns before launchd finishes tearing the job down, so an
	// immediate bootstrap can fail with EIO ("Bootstrap failed: 5: Input/
	// output error"). Wait for the service to actually disappear from the
	// domain, then retry bootstrap on transient EIO.
	waitForBootout(s.Label, 5*time.Second)
	if err := bootstrapWithRetry(domain(), plistPath, s.Label); err != nil {
		return err
	}
	return nil
}

func waitForBootout(label string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		out, err := exec.Command("launchctl", "print", target(label)).CombinedOutput()
		if err != nil && strings.Contains(string(out), "Could not find service") {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func bootstrapWithRetry(dom, plistPath, label string) error {
	var lastOut string
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		out, err := exec.Command("launchctl", "bootstrap", dom, plistPath).CombinedOutput()
		if err == nil {
			return nil
		}
		lastOut = strings.TrimSpace(string(out))
		lastErr = err
		// EIO during bootstrap usually means launchd is still tearing the
		// previous instance down. Brief backoff and retry.
		if !strings.Contains(lastOut, "Input/output error") {
			break
		}
		time.Sleep(time.Duration(200*(attempt+1)) * time.Millisecond)
	}
	return fmt.Errorf("launchctl bootstrap %s: %v: %s", label, lastErr, lastOut)
}

// Kickstart asks launchd to spawn the job now even if no trigger has fired.
// Used after Install to confirm the binary actually starts (catches problems
// like a non-executable program path before the first natural tick).
func Kickstart(label string) error {
	out, err := exec.Command("launchctl", "kickstart", target(label)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl kickstart %s: %v: %s", label, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Uninstall boots the job out of launchd and removes the plist. Tolerant of
// "not loaded" (the install may have been partial or already cleaned up).
func Uninstall(label string) error {
	out, err := exec.Command("launchctl", "bootout", target(label)).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		// launchctl exit codes are poorly documented; filter on message.
		if msg != "" &&
			!strings.Contains(msg, "No such process") &&
			!strings.Contains(msg, "Could not find service") &&
			!strings.Contains(msg, "not find specified service") {
			return fmt.Errorf("launchctl bootout %s: %s", label, msg)
		}
	}
	plistPath, err := PlistPath(label)
	if err != nil {
		return err
	}
	if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Status returns `launchctl print` output for the agent. The bool is false
// when the agent is not loaded (so callers can render a friendly message
// rather than dump the print error).
func Status(label string) (output string, loaded bool, err error) {
	out, runErr := exec.Command("launchctl", "print", target(label)).CombinedOutput()
	if runErr != nil {
		return "", false, nil
	}
	return string(out), true, nil
}

// StartTolerance is the slack allowed when comparing a process's start time
// against the mtime of the binary launchd runs for it. Start times are
// derived from second-granular ps elapsed time and the ps clock is not
// perfectly aligned with the filesystem's, so an exact comparison would
// flag a freshly restarted agent. Deliberately tight: a genuinely stale
// daemon is minutes to days older, never seconds.
const StartTolerance = 5 * time.Second

// Process is the live process launchd runs for a label: its pid, the
// absolute program path launchd executes, and when the process started.
type Process struct {
	PID     int
	Program string
	Started time.Time
}

// ProcessFor returns the process launchd currently runs for label. ok is
// false when the job is not loaded or is loaded with no process at all.
func ProcessFor(label string) (p Process, ok bool, err error) {
	out, loaded, err := Status(label)
	if err != nil || !loaded {
		return Process{}, false, err
	}
	return ProcessFromPrint(out)
}

// ProcessFromPrint parses `launchctl print` output into a Process, resolving
// the start time via ps (launchctl does not report it). ok is false when the
// job carries no `pid =` line, which is how launchd renders a loaded job that
// has no process; callers must read that as "unknown", never as stale.
func ProcessFromPrint(out string) (p Process, ok bool, err error) {
	pidStr, found := printField(out, "pid")
	if !found {
		return Process{}, false, nil
	}
	pid, err := strconv.Atoi(pidStr)
	if err != nil {
		return Process{}, false, fmt.Errorf("launchd: bad pid %q", pidStr)
	}
	started, err := processStart(pid)
	if err != nil {
		return Process{}, false, err
	}
	program, _ := printField(out, "program")
	return Process{PID: pid, Program: program, Started: started}, true, nil
}

// printField returns the value of the first occurrence of key in `launchctl
// print` output. launchd repeats several keys inside nested coalition blocks
// (state = active for resource/jetsam), and only the first occurrence is the
// job's own top-level field.
func printField(out, key string) (string, bool) {
	prefix := key + " = "
	for line := range strings.SplitSeq(out, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), prefix); ok {
			return strings.TrimSpace(v), true
		}
	}
	return "", false
}

// processStart derives a pid's start time from elapsed time rather than
// `ps -o lstart`, whose output is locale-formatted and so not parseable
// without pinning the locale. Second-granular; see StartTolerance.
func processStart(pid int) (time.Time, error) {
	out, err := psETime(pid)
	if err != nil {
		return time.Time{}, fmt.Errorf("ps -p %d: %w", pid, err)
	}
	elapsed, err := parseETime(out)
	if err != nil {
		return time.Time{}, err
	}
	return time.Now().Add(-elapsed), nil
}

// psETime reads a pid's elapsed running time. A package var so the parsing
// above is testable without a live process.
var psETime = func(pid int) (string, error) {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "etime=").Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// parseETime converts `ps -o etime=` output to a duration. macOS formats it
// as [[dd-]hh:]mm:ss. Anything else is an error rather than a zero duration,
// which would read as "just started" and mask a stale process.
func parseETime(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	rest := s
	days := 0
	if i := strings.Index(rest, "-"); i >= 0 {
		d, err := strconv.Atoi(rest[:i])
		if err != nil {
			return 0, fmt.Errorf("launchd: bad etime %q", s)
		}
		days = d
		rest = rest[i+1:]
	}
	parts := strings.Split(rest, ":")
	if len(parts) < 2 || len(parts) > 3 || (days > 0 && len(parts) != 3) {
		return 0, fmt.Errorf("launchd: bad etime %q", s)
	}
	nums := make([]int, len(parts))
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("launchd: bad etime %q", s)
		}
		nums[i] = n
	}
	hours := 0
	if len(nums) == 3 {
		hours, nums = nums[0], nums[1:]
	}
	return time.Duration(days)*24*time.Hour +
		time.Duration(hours)*time.Hour +
		time.Duration(nums[0])*time.Minute +
		time.Duration(nums[1])*time.Second, nil
}

func (s Spec) validate() error {
	if s.Label == "" {
		return fmt.Errorf("launchd: empty Label")
	}
	if strings.ContainsAny(s.Label, "/ ") {
		return fmt.Errorf("launchd: Label %q must not contain slashes or spaces", s.Label)
	}
	if s.Program == "" {
		return fmt.Errorf("launchd: empty Program for %s", s.Label)
	}
	if !filepath.IsAbs(s.Program) {
		return fmt.Errorf("launchd: Program for %s must be absolute, got %q", s.Label, s.Program)
	}
	return nil
}
