package updater

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"loom/internal/launchd"
	"loom/internal/version"
	"loom/transport/receiver"
	"loom/transport/shipper"
)

// fakeSource is an in-memory releaseSource. Latest returns a fixed tag +
// asset map; Download returns bytes keyed by URL. No network.
type fakeSource struct {
	tag    string
	assets map[string]string // name -> url
	blobs  map[string][]byte // url -> bytes
}

func (f fakeSource) Latest(ctx context.Context) (string, map[string]string, error) {
	return f.tag, f.assets, nil
}

func (f fakeSource) Download(ctx context.Context, url string) ([]byte, error) {
	b, ok := f.blobs[url]
	if !ok {
		return nil, fmt.Errorf("no blob for %s", url)
	}
	return b, nil
}

// makeTarball builds a gzip+tar archive holding a single "loom" file
// with the given content, mirroring GoReleaser's layout.
func makeTarball(t *testing.T, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "loom", Mode: 0o755, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// newFakeRelease assembles a fakeSource for tag whose tarball holds
// binContent, plus a checksums.txt. The checksum line is correct unless
// corruptSum is true, which feeds a wrong digest to exercise the verify
// failure path.
func newFakeRelease(t *testing.T, tag string, binContent []byte, corruptSum bool) fakeSource {
	t.Helper()
	tarball := makeTarball(t, binContent)
	name := assetName(tag)
	sum := sha256Hex(tarball)
	if corruptSum {
		sum = sha256Hex([]byte("not the tarball"))
	}
	sums := fmt.Sprintf("%s  %s\n", sum, name)

	tarURL := "https://example.test/" + name
	sumURL := "https://example.test/" + checksumsAsset
	return fakeSource{
		tag: tag,
		assets: map[string]string{
			name:           tarURL,
			checksumsAsset: sumURL,
		},
		blobs: map[string][]byte{
			tarURL: tarball,
			sumURL: []byte(sums),
		},
	}
}

// installEnv wires Tick to a temp install target, a fake release source,
// a fixed Current version, and a no-op re-bootstrap (so tests never touch
// launchctl, exec, or the live daemon). It returns the install target path.
func installEnv(t *testing.T, current string, s releaseSource, binContent []byte) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "loom")
	if err := os.WriteFile(bin, binContent, 0o755); err != nil {
		t.Fatal(err)
	}

	origSrc := source
	origVer := version.Current
	origBin := loomBinary
	origReboot := reBootstrapAgents
	source = s
	version.Current = current
	loomBinary = func() (string, error) { return bin, nil }
	reBootstrapAgents = func(string) error { return nil }
	t.Cleanup(func() {
		source = origSrc
		version.Current = origVer
		loomBinary = origBin
		reBootstrapAgents = origReboot
	})
	return bin
}

// stubRestartCheck points the post-install verification at an in-memory
// process table (a label absent from procs has no process) and shrinks the
// poll window so tests don't sleep. Kickstarts are recorded, never run
// against real launchd.
func stubRestartCheck(t *testing.T, procs map[string]launchd.Process, kicked *[]string) {
	t.Helper()
	origProc, origKick := agentProcess, kickstartAgent
	origTimeout, origInterval, origAfter := verifyTimeout, verifyInterval, kickstartAfter
	agentProcess = func(label string) (launchd.Process, bool, error) {
		p, ok := procs[label]
		return p, ok, nil
	}
	kickstartAgent = func(label string) error {
		if kicked != nil {
			*kicked = append(*kicked, label)
		}
		return nil
	}
	verifyTimeout, verifyInterval, kickstartAfter = 60*time.Millisecond, 5*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() {
		agentProcess, kickstartAgent = origProc, origKick
		verifyTimeout, verifyInterval, kickstartAfter = origTimeout, origInterval, origAfter
	})
}

// captureLog redirects the standard logger so tests can assert that a failed
// restart is actually surfaced.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	origOut, origFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(origOut)
		log.SetFlags(origFlags)
	})
	return &buf
}

// freshBin writes a stand-in installed binary so reBootstrapAgents has an
// mtime to hold agents against.
func freshBin(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "loom")
	if err := os.WriteFile(bin, []byte("new binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestAssetName(t *testing.T) {
	want := fmt.Sprintf("loom_1.2.3_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	if got := assetName("v1.2.3"); got != want {
		t.Fatalf("assetName(v1.2.3) = %q, want %q", got, want)
	}
	if got := assetName("1.2.3"); got != want {
		t.Fatalf("assetName(1.2.3) = %q, want %q", got, want)
	}
}

func TestTickUpToDateNoOp(t *testing.T) {
	rel := newFakeRelease(t, "v1.1.1", []byte("new binary"), false)
	bin := installEnv(t, "1.1.1", rel, []byte("current binary"))

	if err := Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if got := readFile(t, bin); got != "current binary" {
		t.Fatalf("binary changed on up-to-date tick: %q", got)
	}
}

func TestTickInstallsWhenNewer(t *testing.T) {
	rel := newFakeRelease(t, "v1.1.2", []byte("released binary"), false)
	bin := installEnv(t, "1.1.1", rel, []byte("old binary"))

	if err := Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if got := readFile(t, bin); got != "released binary" {
		t.Fatalf("binary = %q, want released bytes", got)
	}
	if fi, err := os.Stat(bin); err != nil {
		t.Fatal(err)
	} else if fi.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %v, want 0755", fi.Mode().Perm())
	}
	assertNoTempLeft(t, filepath.Dir(bin))
}

func TestTickInstallsWhenDev(t *testing.T) {
	rel := newFakeRelease(t, "v1.1.1", []byte("released binary"), false)
	bin := installEnv(t, "dev", rel, []byte("dev binary"))

	if err := Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if got := readFile(t, bin); got != "released binary" {
		t.Fatalf("dev build did not update: %q", got)
	}
}

// TestReBootstrapAgentsLoadedOnly drives the real reBootstrapAgents with
// stubbed plistPresent + per-component hooks (no exec, no launchctl). It
// asserts: only loaded agents are re-bootstrapped, each via the install
// (bootout+bootstrap) path with the right component arg, and self is the
// detached reexec done last.
func TestReBootstrapAgentsLoadedOnly(t *testing.T) {
	// shipper + updater loaded; receiver + summarizer not.
	loaded := map[string]bool{
		shipper.AgentLabel: true,
		AgentLabel:         true,
	}

	var installs []string
	selfSpawned := false

	origPlist := plistPresent
	origInstall := installComponent
	origSpawn := spawnSelfReexec
	plistPresent = func(label string) bool { return loaded[label] }
	installComponent = func(bin, component string) error {
		if selfSpawned {
			t.Fatalf("worker install %q ran after self reexec", component)
		}
		installs = append(installs, component)
		return nil
	}
	spawnSelfReexec = func(bin string) error {
		selfSpawned = true
		return nil
	}
	t.Cleanup(func() {
		plistPresent = origPlist
		installComponent = origInstall
		spawnSelfReexec = origSpawn
	})
	stubRestartCheck(t, map[string]launchd.Process{
		shipper.AgentLabel: {PID: 11, Started: time.Now()},
	}, nil)

	if err := reBootstrapAgents(freshBin(t)); err != nil {
		t.Fatalf("reBootstrapAgents: %v", err)
	}

	want := []string{"shipper"}
	if !reflect.DeepEqual(installs, want) {
		t.Fatalf("worker installs = %v, want %v (only loaded workers)", installs, want)
	}
	if !selfSpawned {
		t.Fatal("self reexec was not spawned for the loaded updater")
	}
	// receiver/summarizer are not loaded, so they must not be installed.
	for _, c := range installs {
		if c == "receiver" || c == "summarizer" {
			t.Fatalf("re-bootstrapped unloaded agent %q", c)
		}
	}
}

// TestReBootstrapAgentsSkipsSelfWhenAbsent confirms self is not re-bootstrapped
// when the updater plist is absent (matching plistPresent gating).
func TestReBootstrapAgentsSkipsSelfWhenAbsent(t *testing.T) {
	loaded := map[string]bool{receiver.AgentLabel: true}

	var installs []string
	selfSpawned := false

	origPlist := plistPresent
	origInstall := installComponent
	origSpawn := spawnSelfReexec
	plistPresent = func(label string) bool { return loaded[label] }
	installComponent = func(bin, component string) error {
		installs = append(installs, component)
		return nil
	}
	spawnSelfReexec = func(bin string) error {
		selfSpawned = true
		return nil
	}
	t.Cleanup(func() {
		plistPresent = origPlist
		installComponent = origInstall
		spawnSelfReexec = origSpawn
	})
	stubRestartCheck(t, map[string]launchd.Process{
		receiver.AgentLabel: {PID: 12, Started: time.Now()},
	}, nil)

	if err := reBootstrapAgents(freshBin(t)); err != nil {
		t.Fatalf("reBootstrapAgents: %v", err)
	}
	if !reflect.DeepEqual(installs, []string{"receiver"}) {
		t.Fatalf("worker installs = %v, want [receiver]", installs)
	}
	if selfSpawned {
		t.Fatal("self reexec spawned despite absent updater plist")
	}
}

// TestReBootstrapAgentsVerifiesNewImage drives the three post-install
// outcomes in one loop: an agent that comes up on the new binary, one that
// never gets a process, and one still running the pre-update image. The
// last two must be logged by name and must not stop the loop — a
// half-restarted fleet is exactly the state this check exists to catch.
func TestReBootstrapAgentsVerifiesNewImage(t *testing.T) {
	loaded := map[string]bool{
		shipper.AgentLabel:  true,
		receiver.AgentLabel: true,
		SummarizerLabel:     true,
	}

	var installs []string
	origPlist := plistPresent
	origInstall := installComponent
	origSpawn := spawnSelfReexec
	plistPresent = func(label string) bool { return loaded[label] }
	installComponent = func(bin, component string) error {
		installs = append(installs, component)
		return nil
	}
	// The updater plist is absent here, so self must not be re-bootstrapped;
	// stubbed regardless so a future edit to `loaded` can never launch a real
	// detached helper against this host.
	spawnSelfReexec = func(bin string) error {
		t.Errorf("self reexec spawned despite absent updater plist")
		return nil
	}
	t.Cleanup(func() {
		plistPresent = origPlist
		installComponent = origInstall
		spawnSelfReexec = origSpawn
	})

	var kicked []string
	stubRestartCheck(t, map[string]launchd.Process{
		shipper.AgentLabel: {PID: 21, Started: time.Now()},
		// receiver: absent from the table, i.e. bootstrapped but no process.
		SummarizerLabel: {PID: 23, Started: time.Now().Add(-10 * 24 * time.Hour)},
	}, &kicked)
	logs := captureLog(t)

	if err := reBootstrapAgents(freshBin(t)); err != nil {
		t.Fatalf("reBootstrapAgents: %v", err)
	}

	if !reflect.DeepEqual(installs, []string{"shipper", "receiver", "summarizer"}) {
		t.Fatalf("installs = %v, want every loaded worker (loop must continue past failures)", installs)
	}
	got := logs.String()
	for _, want := range []string{
		"re-bootstrapped " + shipper.AgentLabel,
		"re-bootstrap " + receiver.AgentLabel + ": no process",
		"re-bootstrap " + SummarizerLabel + ": pid 23 started",
		"still the old image",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("log missing %q\n--- log ---\n%s", want, got)
		}
	}
	if strings.Contains(got, "re-bootstrapped "+SummarizerLabel) {
		t.Errorf("stale summarizer reported as re-bootstrapped\n--- log ---\n%s", got)
	}
	// Only the processless agent gets the one kickstart.
	if !reflect.DeepEqual(kicked, []string{receiver.AgentLabel}) {
		t.Errorf("kickstarts = %v, want exactly one for %s", kicked, receiver.AgentLabel)
	}
}

// TestVerifyRestartedSelf covers the entry point the detached `loom updater
// reexec` helper calls after re-installing com.loom.updater. A stale updater
// is the worst outcome — the old image is the code that verifies nothing, so
// every later tick would revert silently — and must be named in the log.
func TestVerifyRestartedSelf(t *testing.T) {
	stubRestartCheck(t, map[string]launchd.Process{
		AgentLabel: {PID: 41, Started: time.Now().Add(-time.Hour)},
	}, nil)
	logs := captureLog(t)

	VerifyRestarted(AgentLabel, freshBin(t))

	got := logs.String()
	for _, want := range []string{
		"re-bootstrap " + AgentLabel + ": pid 41 started",
		"still the old image",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("log missing %q\n--- log ---\n%s", want, got)
		}
	}
	if strings.Contains(got, "re-bootstrapped "+AgentLabel) {
		t.Errorf("stale updater reported as re-bootstrapped\n--- log ---\n%s", got)
	}
}

// TestVerifyRestartedWithoutBinaryMtime pins the unverifiable case: with no
// mtime to hold the process against, the image comparison cannot run, so a
// running agent must not be logged as a verified restart.
func TestVerifyRestartedWithoutBinaryMtime(t *testing.T) {
	stubRestartCheck(t, map[string]launchd.Process{
		// Started long before any plausible install: still the old image, but
		// with no mtime there is nothing that proves it.
		AgentLabel: {PID: 42, Started: time.Now().Add(-10 * 24 * time.Hour)},
	}, nil)
	logs := captureLog(t)

	VerifyRestarted(AgentLabel, filepath.Join(t.TempDir(), "loom"))

	got := logs.String()
	for _, want := range []string{
		"cannot check whether " + AgentLabel + " is on the new image",
		"re-bootstrapped " + AgentLabel + " (running, image not verified)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("log missing %q\n--- log ---\n%s", want, got)
		}
	}
	if strings.Contains(got, "still the old image") {
		t.Errorf("image compared without a binary mtime\n--- log ---\n%s", got)
	}
}

func TestTickChecksumMismatchLeavesBinary(t *testing.T) {
	rel := newFakeRelease(t, "v1.1.2", []byte("released binary"), true)
	bin := installEnv(t, "1.1.1", rel, []byte("old binary"))

	err := Tick(context.Background())
	if err == nil {
		t.Fatal("Tick succeeded despite checksum mismatch")
	}
	if got := readFile(t, bin); got != "old binary" {
		t.Fatalf("binary overwritten on checksum failure: %q", got)
	}
	assertNoTempLeft(t, filepath.Dir(bin))
}

func TestVerifyChecksum(t *testing.T) {
	tarball := makeTarball(t, []byte("x"))
	name := "loom_1.0.0_test.tar.gz"
	good := fmt.Sprintf("%s  %s\n", sha256Hex(tarball), name)
	if err := verifyChecksum(tarball, []byte(good), name); err != nil {
		t.Fatalf("verifyChecksum(matching) = %v, want nil", err)
	}
	bad := fmt.Sprintf("%s  %s\n", sha256Hex([]byte("other")), name)
	if err := verifyChecksum(tarball, []byte(bad), name); err == nil {
		t.Fatal("verifyChecksum(mismatch) = nil, want error")
	}
	if err := verifyChecksum(tarball, []byte("deadbeef  other.tar.gz\n"), name); err == nil {
		t.Fatal("verifyChecksum(no matching line) = nil, want error")
	}
}

func assertNoTempLeft(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Base(e.Name()) != "loom" {
			t.Fatalf("stray file left in install dir: %s", e.Name())
		}
	}
}

func readFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
