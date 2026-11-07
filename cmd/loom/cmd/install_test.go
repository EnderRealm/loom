package cmd

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"loom/internal/config"
	"loom/internal/launchd"
	"loom/internal/updater"
	"loom/transport/receiver"
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

	spec := receiverSpec("/Users/me/.local/bin/loom", "/tmp/receiver.log", "")
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
	// The project_state rebuild shells tk, which launchd's default PATH misses.
	if path := spec.Env["PATH"]; !strings.Contains(path, "/opt/homebrew/bin") {
		t.Fatalf("summarizer PATH = %q, want the canonical PATH", path)
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

func writeLoomConfig(t *testing.T, body string) {
	t.Helper()
	t.Setenv("LOOM_HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Dir(config.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.Path(), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func stubStop(t *testing.T) *[]string {
	t.Helper()
	stopped := new([]string)
	orig := stopAgent
	stopAgent = func(label string) error {
		*stopped = append(*stopped, label)
		return nil
	}
	t.Cleanup(func() { stopAgent = orig })
	return stopped
}

// The updater's re-bootstrap arrives here with a shipper still running the
// pre-update image, which has no transport check. A refused server_url has
// to stop that process, not merely decline to replace it.
func TestInstallShipperRefusalStopsTheRunningShipper(t *testing.T) {
	writeLoomConfig(t, `{"server_url":"http://loom.example:8765"}`)
	calls, _, _ := stubInstall(t, nil)
	stopped := stubStop(t)

	err := installShipper()
	if !errors.Is(err, shipper.ErrTransportRefused) {
		t.Fatalf("installShipper err = %v", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("calls = %v, want no install", *calls)
	}
	if !reflect.DeepEqual(*stopped, []string{shipper.AgentLabel}) {
		t.Fatalf("stopped = %v, want %s", *stopped, shipper.AgentLabel)
	}
}

// Any other config failure keeps its old meaning: nothing changed about
// where the running daemon sends, so it is left alone.
func TestInstallShipperOtherConfigErrorLeavesTheShipperRunning(t *testing.T) {
	t.Setenv("LOOM_HOME", t.TempDir())
	stubInstall(t, nil)
	stopped := stubStop(t)

	if err := installShipper(); err == nil {
		t.Fatal("installShipper with no config = nil, want error")
	}
	if len(*stopped) != 0 {
		t.Fatalf("stopped = %v, want none", *stopped)
	}
}

func TestInstallShipperAcceptsHTTPS(t *testing.T) {
	writeLoomConfig(t, `{"server_url":"https://loom.example"}`)
	calls, installed, _ := stubInstall(t, nil)
	stopped := stubStop(t)

	if err := installShipper(); err != nil {
		t.Fatalf("installShipper: %v", err)
	}
	if got := strings.Join(*calls, ","); got != "install,await" || installed.Label != shipper.AgentLabel {
		t.Fatalf("calls = %s, label = %s", got, installed.Label)
	}
	if len(*stopped) != 0 {
		t.Fatalf("stopped = %v, want none", *stopped)
	}
}

// installReceiver is what `loom install receiver` runs, and so what the
// updater's re-bootstrap runs: the bind comes from config.json, not from
// anything the installing process carries, so it survives an update.
func TestInstallReceiverUsesConfiguredAddr(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			hits.Add(1)
		}
	}))
	defer srv.Close()
	addr := srv.Listener.Addr().String()
	writeLoomConfig(t, `{"receiver_addr":"`+addr+`"}`)
	t.Setenv("LOOM_RECEIVER_TOKEN", "tok")
	_, installed, _ := stubInstall(t, nil)

	if err := installReceiver(); err != nil {
		t.Fatalf("installReceiver: %v", err)
	}
	if want := []string{"receiver", "--addr", addr}; !reflect.DeepEqual(installed.Args, want) {
		t.Fatalf("args = %v, want %v", installed.Args, want)
	}
	if !strings.Contains(installed.PlistXML(), "<string>"+addr+"</string>") {
		t.Fatalf("plist lacks the address:\n%s", installed.PlistXML())
	}
	if hits.Load() == 0 {
		t.Fatal("install never polled /healthz on the configured address")
	}
}

// No receiver_addr keeps today's plist exactly; a malformed one fails the
// install before launchd is touched, leaving any running receiver in place.
func TestInstallReceiverDefaultAndInvalidAddr(t *testing.T) {
	writeLoomConfig(t, `{}`)
	if got := receiverSpec("/bin/loom", "/tmp/r.log", "").Args; !reflect.DeepEqual(got, []string{"receiver"}) {
		t.Fatalf("default args = %v", got)
	}

	writeLoomConfig(t, `{"receiver_addr":"8765"}`)
	t.Setenv("LOOM_RECEIVER_TOKEN", "tok")
	calls, _, _ := stubInstall(t, nil)
	if err := installReceiver(); err == nil || !strings.Contains(err.Error(), "receiver_addr") {
		t.Fatalf("installReceiver err = %v", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("calls = %v, want no install", *calls)
	}
}

func TestReceiverHealthzURL(t *testing.T) {
	cases := map[string]string{
		"":                   "http://127.0.0.1:8765/healthz",
		receiver.DefaultAddr: "http://127.0.0.1:8765/healthz",
		":9000":              "http://127.0.0.1:9000/healthz",
		"0.0.0.0:9000":       "http://127.0.0.1:9000/healthz",
		"[::]:9000":          "http://127.0.0.1:9000/healthz",
		"127.0.0.1:9000":     "http://127.0.0.1:9000/healthz",
		"[::1]:9000":         "http://[::1]:9000/healthz",
		"100.64.0.1:8765":    "http://100.64.0.1:8765/healthz",
	}
	for addr, want := range cases {
		if got := receiverHealthzURL(addr); got != want {
			t.Errorf("receiverHealthzURL(%q) = %q, want %q", addr, got, want)
		}
	}
}
