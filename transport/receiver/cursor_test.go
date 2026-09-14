package receiver

import (
	"bytes"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"loom/transport/cursor"
	"loom/transport/internal/source"
	"loom/transport/internal/staging"
	"loom/transport/shipper"
)

const cursorParent = "11111111-1111-4111-8111-111111111111"
const cursorChild = "22222222-2222-4222-8222-222222222222"

func cursorFixture(t *testing.T, home, sid, parent string) (*sql.DB, string) {
	t.Helper()
	dir := filepath.Join(home, ".cursor", "chats", "workspace-hash", sid)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, stmt := range []string{
		"PRAGMA journal_mode=WAL", "PRAGMA user_version=1",
		"CREATE TABLE blobs (id TEXT PRIMARY KEY, data BLOB)",
		"CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT)",
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	meta := map[string]any{"agentId": sid, "latestRootBlobId": "opaque-root", "unknownMetadata": []any{1, "retained"}}
	if parent != "" {
		meta["subagentInfo"] = map[string]string{"parentAgentId": parent, "rootParentAgentId": parent, "typeName": "generalPurpose", "toolCallId": "dispatch-id"}
	} else {
		body, _ := json.Marshal(map[string]any{"schemaVersion": 1, "cwd": filepath.Join(home, "project"), "hasConversation": true})
		if err := os.WriteFile(filepath.Join(dir, "meta.json"), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	body, _ := json.Marshal(meta)
	if _, err := db.Exec("INSERT INTO meta VALUES ('0', ?)", hex.EncodeToString(body)); err != nil {
		t.Fatal(err)
	}
	return db, dir
}

func cursorSessions(t *testing.T) (source.SnapshotAdapter, []source.Session) {
	t.Helper()
	for _, ad := range source.Adapters() {
		if ad.Agent() == source.CursorAgent {
			sessions, err := ad.List()
			if err != nil {
				t.Fatal(err)
			}
			return ad.(source.SnapshotAdapter), sessions
		}
	}
	t.Fatal("Cursor adapter not registered")
	return nil, nil
}

func TestCursorCaptureProcess(t *testing.T) {
	if os.Getenv("LOOM_CURSOR_CAPTURE_PROCESS") != "1" {
		return
	}
	shipper.Once()
}

func runCursorCapture(t *testing.T) string {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestCursorCaptureProcess$")
	cmd.Env = append(os.Environ(), "LOOM_CURSOR_CAPTURE_PROCESS=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("capture process: %v\n%s", err, out)
	}
	return string(out)
}

func readJournal(t *testing.T, path string) ([]byte, []source.SnapshotRecord) {
	t.Helper()
	data, err := os.ReadFile(path)
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
	return data, records
}

func TestCursorCaptureShipRestartAndSourceRemoval(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	loomHome := t.TempDir()
	t.Setenv("LOOM_HOME", loomHome)
	parentDB, parentDir := cursorFixture(t, home, cursorParent, "")
	childDB, _ := cursorFixture(t, home, cursorChild, cursorParent)
	// Tool inputs/results and unmapped binary content exceed scanner and
	// summary limits. The transport must preserve every byte without parsing.
	content := append([]byte{0, 255, 254}, []byte(strings.Repeat("message tool-input tool-result\n", 10000))...)
	for _, db := range []*sql.DB{parentDB, childDB} {
		if _, err := db.Exec("INSERT INTO blobs VALUES ('opaque', ?)", content); err != nil {
			t.Fatal(err)
		}
	}
	ad, sessions := cursorSessions(t)
	if len(sessions) != 2 {
		t.Fatalf("sessions=%d, want parent and child", len(sessions))
	}
	byID := map[string]source.Session{}
	for _, s := range sessions {
		byID[s.SessionID] = s
		if s.Cwd != filepath.Join(home, "project") {
			t.Fatalf("cwd=%q", s.Cwd)
		}
	}
	child := byID[cursorChild]
	if child.ParentID() != cursorParent || child.Subagent.ToolUseID != "dispatch-id" {
		t.Fatalf("child relationship=%+v", child.Subagent)
	}
	storage := t.TempDir()
	srv := &server{storage: storage, authToken: "test-token"}
	var available atomic.Bool
	var posts atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if !available.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	})
	mux.HandleFunc("/v1/ingest", func(w http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		srv.handleIngest(w, r)
	})
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()
	config, _ := json.Marshal(map[string]any{"server_url": httpServer.URL, "auth_token": "test-token", "notify_on_failure": false})
	if err := os.WriteFile(filepath.Join(loomHome, "config.json"), config, 0o600); err != nil {
		t.Fatal(err)
	}
	if log := runCursorCapture(t); !strings.Contains(log, "captured=2 capture-failed=0") {
		t.Fatalf("first capture:\n%s", log)
	}
	parent := byID[cursorParent]
	parentStage := staging.Path(source.CursorAgent, parent.Project, "", cursorParent)
	first, _ := readJournal(t, parentStage)
	if log := runCursorCapture(t); !strings.Contains(log, "captured=0 capture-failed=0") {
		t.Fatalf("unchanged capture after process restart:\n%s", log)
	}
	unchanged, _ := readJournal(t, parentStage)
	if !bytes.Equal(first, unchanged) {
		t.Fatal("restart duplicated captured records")
	}
	// A WAL writer remains open. Uncommitted source state is invisible.
	tx, err := parentDB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("INSERT INTO blobs VALUES ('new', ?)", []byte("new active turn")); err != nil {
		t.Fatal(err)
	}
	runCursorCapture(t)
	uncommitted, _ := readJournal(t, parentStage)
	if !bytes.Equal(first, uncommitted) {
		t.Fatal("captured uncommitted WAL state")
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := parentDB.Exec("INSERT INTO meta VALUES ('future', 'unmapped value')"); err != nil {
		t.Fatal(err)
	}
	if log := runCursorCapture(t); !strings.Contains(log, "captured=1 capture-failed=0") {
		t.Fatalf("incremental capture:\n%s", log)
	}
	second, records := readJournal(t, parentStage)
	if !bytes.HasPrefix(second, first) || len(records) != 5 {
		t.Fatalf("incremental journal: records=%d", len(records))
	}
	if !bytes.Equal(records[0].Value, content) {
		t.Fatal("binary/tool content changed")
	}
	// Reverting a mutable value must remain observable; deduplicating every
	// historical hash would incorrectly leave the previous value current.
	for _, value := range []string{"changed", "unmapped value"} {
		if _, err := parentDB.Exec("UPDATE meta SET value=? WHERE key='future'", value); err != nil {
			t.Fatal(err)
		}
		runCursorCapture(t)
	}
	if _, err := parentDB.Exec("DELETE FROM blobs WHERE id='new'"); err != nil {
		t.Fatal(err)
	}
	runCursorCapture(t)
	final, records := readJournal(t, parentStage)
	if len(records) != 8 || !records[7].Deleted || records[7].Key != "new" {
		t.Fatalf("updates/deletion not preserved: %+v", records[len(records)-1])
	}
	if _, err := ad.ReadSnapshot(parent); err != nil {
		t.Fatal(err)
	}
	parentDB.Close()
	childDB.Close()
	if err := os.RemoveAll(filepath.Dir(parentDir)); err != nil {
		t.Fatal(err)
	}
	available.Store(true)
	if log := runCursorCapture(t); !strings.Contains(log, "shipped=2") {
		t.Fatalf("ship after source removal:\n%s", log)
	}
	for _, s := range sessions {
		rel := filepath.Join(source.CursorAgent, s.Project)
		if s.ParentID() != "" {
			rel = filepath.Join(rel, s.ParentID(), "subagents")
		}
		received := filepath.Join(storage, rel, s.SessionID+".jsonl")
		got, _ := readJournal(t, received)
		want, _ := readJournal(t, staging.Path(source.CursorAgent, s.Project, s.ParentID(), s.SessionID))
		if !bytes.Equal(got, want) {
			t.Fatalf("received content differs for %s", s.SessionID)
		}
		if s.SessionID == cursorParent && !bytes.Equal(got, final) {
			t.Fatal("captured history lost after source removal")
		}
		identity, err := os.ReadFile(strings.TrimSuffix(received, ".jsonl") + ".meta.json")
		if err != nil || !bytes.Contains(identity, []byte(s.Cwd)) {
			t.Fatalf("received identity missing: %s, %v", identity, err)
		}
		// Simulate a crash after receiver acceptance and before ship cursor
		// persistence. The receiver must accept the replay without appending.
		if err := cursor.Write(cursor.KindShip, source.CursorAgent, s.Key(), 0); err != nil {
			t.Fatal(err)
		}
	}
	runCursorCapture(t)
	if posts.Load() != 4 {
		t.Fatalf("posts=%d, want initial send and replay for both sessions", posts.Load())
	}
	runCursorCapture(t)
	if posts.Load() != 4 {
		t.Fatal("unchanged sessions shipped again")
	}
	got, _ := readJournal(t, filepath.Join(storage, source.CursorAgent, parent.Project, cursorParent+".jsonl"))
	if !bytes.Equal(got, final) {
		t.Fatal("receiver replay duplicated records")
	}
}

func TestCursorUnsupportedSourcesAreDiagnosed(t *testing.T) {
	for _, tc := range []struct{ name, mutation, want string }{
		{"version", "PRAGMA user_version=99", "unsupported Cursor store user_version"},
		{"table", "CREATE TABLE future (data TEXT)", "unsupported Cursor store tables"},
		{"column", "ALTER TABLE blobs ADD COLUMN future TEXT", "unsupported Cursor blobs columns"},
		{"encoding", "UPDATE meta SET value='not-hex'", "unsupported meta encoding"},
		{"parent", "UPDATE meta SET value='7b7d'", "agentId does not match"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			db, _ := cursorFixture(t, home, cursorParent, "")
			if _, err := db.Exec(tc.mutation); err != nil {
				t.Fatal(err)
			}
			for _, ad := range source.Adapters() {
				if ad.Agent() != source.CursorAgent {
					continue
				}
				sessions, err := ad.List()
				if err == nil {
					_, err = ad.(source.SnapshotAdapter).ReadSnapshot(sessions[0])
				}
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("error=%v, want %s", err, tc.want)
				}
			}
		})
	}
}

func TestCursorBadSourceDoesNotHideOtherSessions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cursorFixture(t, home, cursorParent, "")
	db, dir := cursorFixture(t, home, cursorChild, "")
	db.Close()
	path := filepath.Join(dir, "store.db")
	if err := os.WriteFile(path, []byte("unsupported source format"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, ad := range source.Adapters() {
		if ad.Agent() != source.CursorAgent {
			continue
		}
		sessions, err := ad.List()
		if err == nil || !strings.Contains(err.Error(), path) || len(sessions) != 1 || sessions[0].SessionID != cursorParent {
			t.Fatalf("sessions=%+v error=%v", sessions, err)
		}
		if err := os.Chmod(path, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(path, 0o600) })
		_, err = ad.List()
		if err == nil || !strings.Contains(err.Error(), path) {
			t.Fatalf("unreadable source not diagnosed: %v", err)
		}
	}
}
