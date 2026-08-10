// Package updater is loom's self-managing auto-updater. As a launchd
// KeepAlive daemon it polls EnderRealm/loom's latest GitHub Release on a
// tunable interval, and when a newer release than the running binary is
// available it downloads the platform tarball, verifies its sha256
// against the release's checksums.txt, atomically installs the extracted
// binary over the running one, and re-bootstraps every other loom agent
// (itself last). The pattern is documented in docs/auto-update-pattern.md.
//
// Unlike the prior source-build updater, this needs no git checkout and
// no Go toolchain on the host: the installed binary is always a released
// semver build. Development is a separate local `go build -o ./loom`.
package updater

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"loom/internal/config"
	"loom/internal/extract"
	"loom/internal/launchd"
	"loom/internal/version"
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

// githubAPIBase is the GitHub API root the default release source hits.
// A package var so tests can point it at an httptest server.
var githubAPIBase = "https://api.github.com"

// checksumsAsset is the release asset listing "<sha256>  <filename>"
// lines, one per tarball. GoReleaser names it checksums.txt.
const checksumsAsset = "checksums.txt"

// releaseSource fetches release metadata and assets. The default
// implementation hits GitHub over net/http; tests inject a fake.
type releaseSource interface {
	// Latest returns the latest release's tag and a map of asset name to
	// download URL.
	Latest(ctx context.Context) (tag string, assets map[string]string, err error)
	// Download fetches the asset at url and returns its bytes.
	Download(ctx context.Context, url string) ([]byte, error)
}

// source is the releaseSource Tick uses. Overridable in tests.
var source releaseSource = githubSource{}

// loomBinary resolves the install target (the running binary's path).
// A package var so tests can point the install at a temp file rather
// than the test binary.
var loomBinary = resolveLoomBinary

// Daemon runs Tick on a ticker driven by the configured interval. Like
// the shipper daemon, KeepAlive in the plist handles crash respawn so
// non-fatal errors here just log and continue.
//
// Cancellable via SIGINT / SIGTERM through the supplied context.
func Daemon(ctx context.Context) error {
	interval := intervalFromEnv()
	log.Printf("updater starting interval=%s current=%s", interval, version.String())

	// One immediate tick on startup so a freshly-installed updater
	// doesn't sit idle for the full interval before its first check.
	if err := Tick(ctx); err != nil {
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
			if err := Tick(ctx); err != nil {
				log.Printf("tick: %v", err)
			}
		}
	}
}

// Tick performs one check + (if a newer release is available) download +
// verify + install + re-bootstrap cycle. Exposed so a one-off `loom updater
// once` can drive the same logic without the daemon loop.
func Tick(ctx context.Context) error {
	tag, assets, err := source.Latest(ctx)
	if err != nil {
		return fmt.Errorf("latest release: %w", err)
	}

	current := version.Current
	latest := normalizeVersion(tag)
	// A dev build (or any version we can't match) always updates so a
	// machine never gets stuck on an unreleased binary.
	if current != "dev" && normalizeVersion(current) == latest {
		log.Printf("up to date (%s)", current)
		return nil
	}
	log.Printf("update available %s → %s", current, tag)

	bin, err := loomBinary()
	if err != nil {
		return fmt.Errorf("locate loom binary: %w", err)
	}
	if err := installRelease(ctx, tag, assets, bin); err != nil {
		return err
	}
	log.Printf("installed %s at %s", tag, bin)

	return reBootstrapAgents(bin)
}

// workerComponents maps each non-self agent's launchd label to the
// `loom install <component>` argument that re-bootstraps it. These installs
// are role-neutral (only `server`/`remote` write the machine role), so a
// re-bootstrap never changes the persisted role.
var workerComponents = []struct {
	label, component string
}{
	{shipper.AgentLabel, "shipper"},
	{receiver.AgentLabel, "receiver"},
	{SummarizerLabel, "summarizer"},
	{extract.AgentLabel, "extractor"},
}

// reBootstrapAgents reinstalls every loaded loom agent under the freshly
// installed binary, the updater itself last. A package var so tests can
// stub it out (driving the real launchctl path would kill the live daemon
// on the developer's machine).
//
// Why bootout+bootstrap and not kickstart: `launchctl kickstart` (even with
// -k) cannot activate a binary whose code signature changed under launchd —
// the downloaded artifact differs from the previously-bootstrapped binary,
// so launchd rejects the respawn (EX_CONFIG / "needs LWCR update") and the
// daemon never runs the new code. bootout+bootstrap both restarts the daemon
// and re-registers the launch constraint for the new binary; that is exactly
// what `loom install <component>` does, so we re-run it via the new binary.
var reBootstrapAgents = func(bin string) error {
	// Workers run in their OWN launchd jobs, so we can wait on each child:
	// the child boots out and bootstraps a different job and exits cleanly,
	// leaving this updater process untouched. A failure on one logs and
	// continues so a single bad agent doesn't strand the rest.
	for _, wc := range workerComponents {
		if !plistPresent(wc.label) {
			continue
		}
		if err := installComponent(bin, wc.component); err != nil {
			log.Printf("re-bootstrap %s: %v", wc.label, err)
			continue
		}
		log.Printf("re-bootstrapped %s", wc.label)
	}

	// Last: re-bootstrap self. We CANNOT wait on `<bin> install updater` —
	// it boots out com.loom.updater, which is THIS job, killing both this
	// process and the (same-job) child mid-bootout. Instead spawn a DETACHED
	// helper (Setsid: new session, survives our job's teardown) that sleeps
	// briefly so we exit first, then bootout+bootstraps the updater job
	// fresh under the new binary. We do not Wait; we just return and let
	// launchd's KeepAlive (or the helper's bootstrap) bring the new code up.
	if !plistPresent(AgentLabel) {
		return nil
	}
	log.Printf("re-bootstrapping self %s via detached helper — new binary takes over", AgentLabel)
	return spawnSelfReexec(bin)
}

// installComponent runs `<bin> install <component>` and waits for it. The
// child bootout+bootstraps its own launchd job, so completing is safe for
// the (different-job) updater process. A package var so tests can record
// invocations without spawning a real process.
var installComponent = func(bin, component string) error {
	out, err := exec.Command(bin, "install", component).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// spawnSelfReexec launches the detached `<bin> updater reexec` helper that
// re-bootstraps com.loom.updater after this process exits. Setsid detaches
// it into a new session so it survives the updater job's bootout. We do not
// Wait. A package var so tests can record the call without spawning.
var spawnSelfReexec = func(bin string) error {
	cmd := exec.Command(bin, "updater", "reexec")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("spawn self re-bootstrap: %w", err)
	}
	return nil
}

// installRelease downloads the platform tarball + checksums for tag,
// verifies the tarball sha256, extracts the loom binary, and atomically
// renames it over bin. Any error before the rename leaves bin untouched
// (download + verify + extract all land in a temp file in bin's dir;
// only a successful, verified extract is renamed into place).
func installRelease(ctx context.Context, tag string, assets map[string]string, bin string) error {
	name := assetName(tag)
	tarURL, ok := assets[name]
	if !ok {
		return fmt.Errorf("release %s has no asset %s", tag, name)
	}
	sumURL, ok := assets[checksumsAsset]
	if !ok {
		return fmt.Errorf("release %s has no %s", tag, checksumsAsset)
	}

	tarball, err := source.Download(ctx, tarURL)
	if err != nil {
		return fmt.Errorf("download %s: %w", name, err)
	}
	sums, err := source.Download(ctx, sumURL)
	if err != nil {
		return fmt.Errorf("download %s: %w", checksumsAsset, err)
	}
	if err := verifyChecksum(tarball, sums, name); err != nil {
		return fmt.Errorf("verify %s: %w", name, err)
	}

	binary, err := extractLoom(tarball)
	if err != nil {
		return fmt.Errorf("extract %s: %w", name, err)
	}

	// Stage in bin's directory so the final rename is atomic (same
	// filesystem). On any failure the existing binary is left untouched.
	dir := filepath.Dir(bin)
	tmp, err := os.CreateTemp(dir, ".loom-update-*")
	if err != nil {
		return fmt.Errorf("temp file: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(binary); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("chmod temp: %w", err)
	}
	if err := os.Rename(tmpPath, bin); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("install: %w", err)
	}
	return nil
}

// assetName returns the GoReleaser tarball name for tag on this host:
// loom_<version>_<os>_<arch>.tar.gz, version being the tag without a
// leading "v".
func assetName(tag string) string {
	return fmt.Sprintf("loom_%s_%s_%s.tar.gz", normalizeVersion(tag), runtime.GOOS, runtime.GOARCH)
}

// normalizeVersion strips a leading "v" so tags ("v1.1.1") and ldflag
// versions ("1.1.1") compare equal.
func normalizeVersion(v string) string {
	return strings.TrimPrefix(v, "v")
}

// verifyChecksum confirms tarball's sha256 matches the line for name in
// the checksums.txt body. GoReleaser's default format is "<sha256>  <filename>"
// (no binary-mode "*" marker); a future binary-mode config would prefix the
// filename and break the match.
func verifyChecksum(tarball, sums []byte, name string) error {
	want := ""
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == name {
			want = fields[0]
			break
		}
	}
	if want == "" {
		return fmt.Errorf("no checksum line for %s", name)
	}
	sum := sha256.Sum256(tarball)
	got := hex.EncodeToString(sum[:])
	if got != want {
		return fmt.Errorf("sha256 mismatch: got %s want %s", got, want)
	}
	return nil
}

// extractLoom returns the bytes of the "loom" entry at the root of the
// gzip+tar archive.
func extractLoom(tarball []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(tarball))
	if err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("tar: %w", err)
		}
		if filepath.Base(hdr.Name) == "loom" {
			return io.ReadAll(tr)
		}
	}
	return nil, fmt.Errorf("no loom binary in archive")
}

// githubSource is the default releaseSource: GitHub's REST API for
// metadata, plain HTTPS GETs for assets. EnderRealm/loom is public, so
// no auth is needed.
type githubSource struct{}

func (githubSource) Latest(ctx context.Context) (string, map[string]string, error) {
	url := githubAPIBase + "/repos/EnderRealm/loom/releases/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	var rel struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name        string `json:"name"`
			DownloadURL string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", nil, err
	}
	assets := make(map[string]string, len(rel.Assets))
	for _, a := range rel.Assets {
		assets[a.Name] = a.DownloadURL
	}
	return rel.TagName, assets, nil
}

func (githubSource) Download(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
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

// resolveLoomBinary returns the absolute path of the running loom binary
// so the install writes to the same path. Plists pin this path;
// installing in place + re-bootstrap is the entire deploy sequence.
func resolveLoomBinary() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	if rp, err := filepath.EvalSymlinks(self); err == nil {
		self = rp
	}
	return self, nil
}

// plistPresent reports whether label's launchd plist exists, gating which
// agents count as "loaded" and so get re-bootstrapped. A package var so
// tests can control the loaded set without touching the real filesystem.
var plistPresent = func(label string) bool {
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
