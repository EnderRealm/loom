package cmd

import (
	"strings"
	"testing"
	"time"

	"loom/internal/config"
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
