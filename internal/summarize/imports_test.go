package summarize

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"loom/internal/parse/codexparse"
	"loom/internal/summaries"
)

const (
	importSession  = "01a0dbf9-02d8-7861-af4a-736b144f469f"
	genuineSession = "01a0dbf9-0000-7861-af4a-736b144f469f"
)

// importCopy is the shape Codex Desktop writes when it imports another
// agent's session: the marker turn straight after session_meta and an
// "<EXTERNAL SESSION IMPORTED>" agent message.
const importCopy = `{"timestamp":"2026-09-26T04:28:44.632Z","type":"session_meta","payload":{"id":"` + importSession + `","timestamp":"2026-09-26T04:28:44.632Z","cwd":"/","originator":"Codex Desktop","source":"vscode"}}
{"timestamp":"2026-09-26T04:28:44.632Z","type":"event_msg","payload":{"type":"task_started","turn_id":"external-import-turn-1"}}
{"timestamp":"2026-09-26T04:28:44.632Z","type":"event_msg","payload":{"type":"user_message","message":"reply with only: OK"}}
{"timestamp":"2026-09-26T04:28:44.632Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"reply with only: OK"}]}}
{"timestamp":"2026-09-26T04:28:44.632Z","type":"event_msg","payload":{"type":"agent_message","message":"OK"}}
{"timestamp":"2026-09-26T04:28:44.632Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"OK"}]}}
{"timestamp":"2026-09-26T04:28:44.632Z","type":"event_msg","payload":{"type":"agent_message","message":"<EXTERNAL SESSION IMPORTED>"}}
{"timestamp":"2026-09-26T04:28:44.632Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"external-import-turn-1"}}
`

// genuineAtRoot is a real codex session that ran at cwd /.
const genuineAtRoot = `{"timestamp":"2026-09-26T05:00:00.000Z","type":"session_meta","payload":{"id":"` + genuineSession + `","timestamp":"2026-09-26T05:00:00.000Z","cwd":"/","originator":"codex_exec","source":"exec"}}
{"timestamp":"2026-09-26T05:00:00.100Z","type":"event_msg","payload":{"type":"task_started","turn_id":"01a0dbf9-1111-7861-af4a-736b144f469f"}}
{"timestamp":"2026-09-26T05:00:00.200Z","type":"turn_context","payload":{"turn_id":"01a0dbf9-1111-7861-af4a-736b144f469f","cwd":"/","model":"gpt-5"}}
{"timestamp":"2026-09-26T05:00:00.300Z","type":"event_msg","payload":{"type":"user_message","message":"hello"}}
{"timestamp":"2026-09-26T05:00:01.000Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"01a0dbf9-1111-7861-af4a-736b144f469f"}}
`

// An import copy the receiver already holds is never folded, and the rows an
// earlier sweep wrote for it are removed — even though the file is unchanged
// and those rows are current. The received file itself is left alone; a
// genuine session at cwd / still lands under _default.
func TestSweepPurgesCodexExternalImports(t *testing.T) {
	received := t.TempDir()
	dir := filepath.Join(received, "codex-cli", "_default")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	importPath := filepath.Join(dir, importSession+".jsonl")
	if err := os.WriteFile(importPath, []byte(importCopy), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, genuineSession+".jsonl"), []byte(genuineAtRoot), 0o644); err != nil {
		t.Fatal(err)
	}

	// Fold the copy the way a sweep from before this change did.
	dbPath := filepath.Join(t.TempDir(), "summaries.db")
	st, err := summaries.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(importPath)
	if err != nil {
		t.Fatal(err)
	}
	sum, err := codexparse.Parse(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(importPath)
	if err != nil {
		t.Fatal(err)
	}
	source := summaries.SourceInfo{Project: "_default", Path: importPath, Size: info.Size(), Mtime: info.ModTime(), CwdRaw: "/"}
	if err := st.WriteSummary(context.Background(), sum, source); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, st, "turns", importSession); n == 0 {
		t.Fatal("setup: the pre-existing fold wrote no turns")
	}
	st.Close()

	if err := Run(Options{ReceivedDir: received, DBPath: dbPath}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	st, err = summaries.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	for _, table := range []string{"sessions", "turns"} {
		if n := countRows(t, st, table, importSession); n != 0 {
			t.Errorf("%s rows for the import copy = %d, want 0", table, n)
		}
	}
	var project string
	if err := st.DB().QueryRow(`SELECT project FROM sessions WHERE agent = 'codex-cli' AND session_id = ?`,
		genuineSession).Scan(&project); err != nil {
		t.Fatalf("genuine cwd-/ session not folded: %v", err)
	}
	if project != "_default" {
		t.Errorf("genuine session project = %q, want _default", project)
	}
	if _, err := os.Stat(importPath); err != nil {
		t.Errorf("received import copy was touched: %v", err)
	}
}

func countRows(t *testing.T, st *summaries.Store, table, sessionID string) int {
	t.Helper()
	var n int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE agent = 'codex-cli' AND session_id = ?`,
		sessionID).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}
