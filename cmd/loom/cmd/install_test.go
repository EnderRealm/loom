package cmd

import (
	"errors"
	"strings"
	"testing"
	"time"

	"loom/internal/config"
	"loom/internal/launchd"
	"loom/internal/updater"
	"loom/transport/shipper"
)

func TestResolveReceiverTokenFromEnv(t *testing.T) {
	t.Setenv("LOOM_HOME", t.TempDir())
	t.Setenv("LOOM_RECEIVER_TOKEN", "env-token")

	got, err := resolveReceiverToken()
	if err != nil {
		t.Fatalf("resolveReceiverToken: %v", err)
	}
	if got != "env-token" {
		t.Fatalf("token = %q, want %q", got, "env-token")
	}
	// Env-provided installs seed the file so later runs resolve it.
	if persisted := config.ReadReceiverToken(); persisted != "env-token" {
		t.Fatalf("persisted token = %q, want %q", persisted, "env-token")
	}
}

func TestResolveReceiverTokenFromFile(t *testing.T) {
	t.Setenv("LOOM_HOME", t.TempDir())
	t.Setenv("LOOM_RECEIVER_TOKEN", "")
	if err := config.WriteReceiverToken("file-token"); err != nil {
		t.Fatal(err)
	}

	got, err := resolveReceiverToken()
	if err != nil {
		t.Fatalf("resolveReceiverToken: %v", err)
	}
	if got != "file-token" {
		t.Fatalf("token = %q, want %q", got, "file-token")
	}
}

func TestResolveReceiverTokenNonInteractiveErrors(t *testing.T) {
	t.Setenv("LOOM_HOME", t.TempDir())
	t.Setenv("LOOM_RECEIVER_TOKEN", "")

	// Force the non-interactive path; under `go test` this is already
	// false, but pin it so the prompt branch can never be reached (no hang).
	orig := stdinIsTTY
	stdinIsTTY = func() bool { return false }
	t.Cleanup(func() { stdinIsTTY = orig })

	if _, err := resolveReceiverToken(); err == nil {
		t.Fatal("resolveReceiverToken with no env/file = nil, want error")
	}
}

func TestReceiverSpecOmitsToken(t *testing.T) {
	t.Setenv("LOOM_HOME", t.TempDir())

	spec := receiverSpec("/Users/me/.local/bin/loom", "/tmp/receiver.log")
	if _, ok := spec.Env["LOOM_RECEIVER_TOKEN"]; ok {
		t.Fatal("receiver Spec env contains LOOM_RECEIVER_TOKEN")
	}
	if strings.Contains(spec.PlistXML(), "LOOM_RECEIVER_TOKEN") {
		t.Fatalf("receiver plist XML contains LOOM_RECEIVER_TOKEN:\n%s", spec.PlistXML())
	}
}

// The summarizer plist carries its sweep cadence as arguments, so the
// installed command reflects the configured seconds and the default alone.
func TestSummarizerSpecCarriesTheInterval(t *testing.T) {
	t.Setenv("LOOM_HOME", t.TempDir())

	spec := summarizerSpec("/Users/me/.local/bin/loom", "/tmp/summarizer.log", 5*time.Second)
	if got := strings.Join(spec.Args, " "); got != "summarize --watch --interval 5s" {
		t.Fatalf("args = %q", got)
	}
	spec = summarizerSpec("/Users/me/.local/bin/loom", "/tmp/summarizer.log", shipper.DefaultSummarizerInterval)
	if got := strings.Join(spec.Args, " "); got != "summarize --watch --interval 30s" {
		t.Fatalf("default args = %q", got)
	}
	if !strings.Contains(spec.PlistXML(), "<string>30s</string>") {
		t.Fatalf("plist XML lacks the interval:\n%s", spec.PlistXML())
	}
}

// stubInstall records the spec handed to launchd and the (label, bin) the
// post-install await was called with, in call order, so neither touches the
// host's launchd. awaitErr is what the await reports.
func stubInstall(t *testing.T, awaitErr error) (calls *[]string, installed *launchd.Spec, awaited *[2]string) {
	t.Helper()
	calls, installed, awaited = new([]string), new(launchd.Spec), new([2]string)
	origInstall, origAwait := installSpec, awaitRunning
	installSpec = func(spec launchd.Spec) error {
		*calls = append(*calls, "install")
		*installed = spec
		return nil
	}
	awaitRunning = func(label, bin string) error {
		*calls = append(*calls, "await")
		*awaited = [2]string{label, bin}
		return awaitErr
	}
	t.Cleanup(func() { installSpec, awaitRunning = origInstall, origAwait })
	return calls, installed, awaited
}

// A job launchd registered but never spawned must fail the install by
// label: the deploy that motivated this saw `loom install updater` exit 0
// with `launchctl list` showing no pid for com.loom.updater.
func TestInstallUpdaterFailsWhenNoProcess(t *testing.T) {
	t.Setenv("LOOM_HOME", t.TempDir())
	calls, installed, awaited := stubInstall(t, errors.New("no process"))

	err := installUpdater()
	if err == nil {
		t.Fatal("installUpdater with no process = nil, want error")
	}
	if !strings.Contains(err.Error(), updater.AgentLabel) {
		t.Fatalf("error %q does not name %s", err, updater.AgentLabel)
	}
	if got := strings.Join(*calls, ","); got != "install,await" {
		t.Fatalf("calls = %s, want install then await", got)
	}
	if installed.Label != updater.AgentLabel {
		t.Fatalf("installed label = %q, want %s", installed.Label, updater.AgentLabel)
	}
	if awaited[0] != updater.AgentLabel || awaited[1] != installed.Program {
		t.Fatalf("awaited = %v, want (%s, %s)", *awaited, updater.AgentLabel, installed.Program)
	}
}

func TestInstallUpdaterSucceedsWhenRunning(t *testing.T) {
	t.Setenv("LOOM_HOME", t.TempDir())
	calls, _, _ := stubInstall(t, nil)

	if err := installUpdater(); err != nil {
		t.Fatalf("installUpdater: %v", err)
	}
	if got := strings.Join(*calls, ","); got != "install,await" {
		t.Fatalf("calls = %s, want install then await", got)
	}
}
