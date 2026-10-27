package shipper

import (
	"bytes"
	"database/sql"
	"encoding/hex"
	"encoding/json"
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

const (
	idleParent = "33333333-3333-4333-8333-333333333333"
	idleChild  = "44444444-4444-4444-8444-444444444444"
)

// cursorStore lays out a Cursor CLI store the way Cursor leaves it on exit:
// WAL mode with the writer closed, so no -wal or -shm remains. A subagent
// has no meta.json and resolves its cwd through its parent's.
func cursorStore(t *testing.T, home, sid, parent string) string {
	t.Helper()
	dir := filepath.Join(home, ".cursor", "chats", "workspace-hash", sid)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	meta := map[string]any{"agentId": sid}
	if parent != "" {
		meta["subagentInfo"] = map[string]string{"parentAgentId": parent, "rootParentAgentId": parent, "typeName": "generalPurpose", "toolCallId": "dispatch-id"}
	} else {
		body, _ := json.Marshal(map[string]any{"schemaVersion": 1, "cwd": filepath.Join(home, "project")})
		if err := os.WriteFile(filepath.Join(dir, "meta.json"), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	body, _ := json.Marshal(meta)
	db := cursorWriter(t, filepath.Join(dir, "store.db"))
	for _, stmt := range []string{
		"PRAGMA journal_mode=WAL", "PRAGMA user_version=1",
		"CREATE TABLE blobs (id TEXT PRIMARY KEY, data BLOB)",
		"CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT)",
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec("INSERT INTO meta VALUES ('0', ?)", hex.EncodeToString(body)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}

func cursorWriter(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

func cursorSourceAdapter(t *testing.T) source.Adapter {
	t.Helper()
	for _, ad := range source.Adapters() {
		if ad.Agent() == source.CursorAgent {
			return ad
		}
	}
	t.Fatal("Cursor adapter not registered")
	return nil
}

func cursorTick(t *testing.T, ad source.Adapter, captured, failed int) {
	t.Helper()
	var counts tickCounts
	captureAdapters(&counts, []source.Adapter{ad})
	if counts.captured != captured || counts.captureFailed != failed {
		t.Fatalf("captured=%d capture-failed=%d, want %d and %d", counts.captured, counts.captureFailed, captured, failed)
	}
}

func cursorJournal(t *testing.T, ad source.Adapter, sid string) []source.SnapshotRecord {
	t.Helper()
	sessions, err := ad.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range sessions {
		if s.SessionID != sid {
			continue
		}
		data, err := os.ReadFile(staging.Path(source.CursorAgent, s.Project, s.ParentID(), s.SessionID))
		if err != nil {
			t.Fatal(err)
		}
		var records []source.SnapshotRecord
		for _, line := range bytes.Split(bytes.TrimSpace(data), []byte{'\n'}) {
			var r source.SnapshotRecord
			if err := json.Unmarshal(line, &r); err != nil {
				t.Fatal(err)
			}
			records = append(records, r)
		}
		return records
	}
	t.Fatalf("session %s not listed", sid)
	return nil
}

type fileState struct {
	size  int64
	mtime int64
}

func statTree(t *testing.T, root string) map[string]fileState {
	t.Helper()
	files := map[string]fileState{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		files[path] = fileState{size: info.Size(), mtime: info.ModTime().UnixNano()}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// A read-only SQLite open of a WAL store still creates, truncates and
// writes its -shm, so an idle tick must not open an unchanged store at all —
// including a parent opened only to resolve a subagent's cwd.
func TestCursorIdleTickWritesNothingUnderChats(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("LOOM_HOME", t.TempDir())
	cursorStore(t, home, idleParent, "")
	cursorStore(t, home, idleChild, idleParent)
	ad := cursorSourceAdapter(t)

	cursorTick(t, ad, 2, 0)
	chats := filepath.Join(home, ".cursor", "chats")
	before := statTree(t, chats)
	time.Sleep(50 * time.Millisecond)
	cursorTick(t, ad, 0, 0)
	after := statTree(t, chats)
	for path, was := range before {
		if now, ok := after[path]; !ok || now != was {
			t.Errorf("%s changed on idle tick: %+v -> %+v (present=%v)", path, was, now, ok)
		}
	}
	for path := range after {
		if _, ok := before[path]; !ok {
			t.Errorf("%s created on idle tick", path)
		}
	}
}

func TestCursorChangedStoreIsCapturedNextTick(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("LOOM_HOME", t.TempDir())
	dir := cursorStore(t, home, idleParent, "")
	history := filepath.Join(dir, "prompt_history.json")
	if err := os.WriteFile(history, []byte(`["one"]`), 0o600); err != nil {
		t.Fatal(err)
	}
	ad := cursorSourceAdapter(t)
	cursorTick(t, ad, 1, 0)
	cursorTick(t, ad, 0, 0)

	// Cursor running: the writer stays open, so the commit lives in -wal.
	db := cursorWriter(t, filepath.Join(dir, "store.db"))
	if _, err := db.Exec("INSERT INTO blobs VALUES ('turn', ?)", []byte("new turn")); err != nil {
		t.Fatal(err)
	}
	cursorTick(t, ad, 1, 0)
	records := cursorJournal(t, ad, idleParent)
	last := records[len(records)-1]
	if last.Kind != "blobs" || last.Key != "turn" || string(last.Value) != "new turn" {
		t.Fatalf("new blob not captured: %+v", last)
	}

	if err := os.WriteFile(history, []byte(`["one","two"]`), 0o600); err != nil {
		t.Fatal(err)
	}
	cursorTick(t, ad, 1, 0)
	records = cursorJournal(t, ad, idleParent)
	last = records[len(records)-1]
	if last.Kind != "file" || last.Key != "prompt_history.json" || string(last.Value) != `["one","two"]` {
		t.Fatalf("prompt history change not captured: %+v", last)
	}
}

func TestCursorFailedStageIsRetriedNextTick(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("LOOM_HOME", t.TempDir())
	cursorStore(t, home, idleParent, "")
	ad := cursorSourceAdapter(t)

	// A file where the agent's staging directory belongs fails the stage
	// without touching the source.
	blocker := filepath.Join(staging.Dir(), source.CursorAgent)
	if err := os.MkdirAll(staging.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cursorTick(t, ad, 0, 1)
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	cursorTick(t, ad, 1, 0)
	if records := cursorJournal(t, ad, idleParent); len(records) == 0 {
		t.Fatal("retried stage wrote no records")
	}
}
