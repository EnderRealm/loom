package staging

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"loom/transport/internal/source"
)

func TestSnapshotAtomicReplacementAndRecovery(t *testing.T) {
	t.Setenv("LOOM_HOME", t.TempDir())
	records := []source.SnapshotRecord{{Format: source.CursorStoreFormat, Kind: "blobs", Key: "blob", ValueType: "blob", Value: []byte("complete")}}
	if _, err := CaptureSnapshot(source.CursorAgent, "project", "", "session", records); err != nil {
		t.Fatal(err)
	}
	path := Path(source.CursorAgent, "project", "", "session")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// A writer killed before rename leaves a temporary file, not a partial
	// committed journal. Reopening staging must ignore that incomplete work.
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), ".snapshot-interrupted"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if n, err := CaptureSnapshot(source.CursorAgent, "project", "", "session", records); err != nil || n != 0 {
		t.Fatalf("recovery added %d bytes: %v", n, err)
	}
	entries, err := List(source.CursorAgent)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary file appeared as a session: %v %v", entries, err)
	}
	// Failure before rename leaves the old journal byte-for-byte intact.
	if err := os.Chmod(filepath.Dir(path), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Dir(path), 0o700) })
	records[0].Value = []byte("changed")
	if _, err := CaptureSnapshot(source.CursorAgent, "project", "", "session", records); err == nil {
		t.Fatal("capture unexpectedly wrote to a read-only directory")
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("failed replacement changed the committed journal")
	}
	if n, err := CaptureSnapshot(source.CursorAgent, "project", "", "session", records); err != nil || n == 0 {
		t.Fatalf("retry did not recover: %d, %v", n, err)
	}
	if n, err := CaptureSnapshot(source.CursorAgent, "project", "", "session", records); err != nil || n != 0 {
		t.Fatalf("retry duplicated committed records: %d, %v", n, err)
	}
}
