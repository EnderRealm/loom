package shipper

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"loom/transport/internal/source"
	"loom/transport/internal/staging"
)

type stubSnapshotAdapter struct {
	records []source.SnapshotRecord
}

func (stubSnapshotAdapter) Agent() string { return source.CursorAgent }

func (stubSnapshotAdapter) List() ([]source.Session, error) { return nil, nil }

func (s stubSnapshotAdapter) ReadSnapshot(source.Session) ([]source.SnapshotRecord, error) {
	return s.records, nil
}

// stageSnapshot calls WriteIdentity every tick even when the snapshot delta
// is empty; unchanged identity must not touch staging.
func TestSnapshotIdleTickSkipsUnchangedIdentity(t *testing.T) {
	t.Setenv("LOOM_HOME", t.TempDir())

	records := []source.SnapshotRecord{{
		Format: source.CursorStoreFormat, Kind: "blobs", Key: "k", ValueType: "blob", Value: []byte("v"),
	}}
	ad := stubSnapshotAdapter{records: records}
	s := source.Session{
		Project: "proj", SessionID: "sess", Cwd: "/Users/steve/code/loom",
	}
	gitCache := map[string]string{"/Users/steve/code/loom": "git@github.com:EnderRealm/loom.git"}

	if n, err := stageSnapshot(ad, s, gitCache); err != nil || n == 0 {
		t.Fatalf("first capture: n=%d err=%v", n, err)
	}
	meta := filepath.Join(staging.Dir(), source.CursorAgent, "proj", "sess.meta.json")
	info, err := os.Stat(meta)
	if err != nil {
		t.Fatal(err)
	}
	firstMod := info.ModTime()

	time.Sleep(20 * time.Millisecond)

	if n, err := stageSnapshot(ad, s, gitCache); err != nil || n != 0 {
		t.Fatalf("idle tick: n=%d err=%v", n, err)
	}
	info, err = os.Stat(meta)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(firstMod) {
		t.Fatalf("meta mtime changed on idle tick: %v -> %v", firstMod, info.ModTime())
	}
	stagingRoot := staging.Dir()
	var tmpFound bool
	err = filepath.Walk(stagingRoot, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if fi.IsDir() {
			return nil
		}
		if filepath.Ext(path) == ".tmp" || filepath.Base(path) == ".snapshot-interrupted" {
			tmpFound = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if tmpFound {
		t.Fatal("idle tick left a temp file under staging")
	}
}
