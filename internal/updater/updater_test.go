package updater

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"loom/internal/version"
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
// a fixed Current version, and a no-op kickstart (so tests never touch
// launchctl or the live daemon). It returns the install target path.
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
	origKick := kickstartAgents
	source = s
	version.Current = current
	loomBinary = func() (string, error) { return bin, nil }
	kickstartAgents = func() error { return nil }
	t.Cleanup(func() {
		source = origSrc
		version.Current = origVer
		loomBinary = origBin
		kickstartAgents = origKick
	})
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
