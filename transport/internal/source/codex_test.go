package source

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"loom/internal/parse/codexparse"
)

// genuineTurn is a turn id as codex itself mints them.
const genuineTurn = "01a0029b-b3a0-7c11-9d2e-3f4a5b6c7d8e"

// writeRollout lays down one codex rollout file whose session_meta reports
// cwd and start, followed by the task_started record of turnID, and returns
// the session id it was written under.
func writeRollout(t *testing.T, home, cwd, start, turnID string) string {
	t.Helper()
	sid := "01a0029b-b39f-7802-8b5f-56ffe644403b"
	dir := filepath.Join(home, ".codex", "sessions", "2026", "09", "09")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	meta := map[string]any{
		"timestamp": start,
		"type":      "session_meta",
		"payload": map[string]any{
			"session_id": sid,
			"cwd":        cwd,
			"originator": "codex_exec",
		},
	}
	line, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal session_meta: %v", err)
	}
	started, err := json.Marshal(map[string]any{
		"timestamp": start,
		"type":      "event_msg",
		"payload":   map[string]any{"type": "task_started", "turn_id": turnID},
	})
	if err != nil {
		t.Fatalf("marshal task_started: %v", err)
	}
	body := append(append(append(line, '\n'), started...), '\n')
	name := fmt.Sprintf("rollout-2026-09-09T18-00-00-%s.jsonl", sid)
	if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
		t.Fatalf("write rollout: %v", err)
	}
	return sid
}

// The seam the whole ticket turns on: a lens dispatched into a throwaway
// working root reports that root as its cwd, and the stamp its launcher
// wrote is what puts the session under the checkout it reviewed. The slug
// still names where codex ran — it is the storage directory, not identity.
func TestCodexListUsesStampedProjectForThrowawayCwd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workRoot := filepath.Join(home, "tmpwork")
	t.Setenv("TMPDIR", home)
	writeRollout(t, home, workRoot, "2026-09-09T18:00:00Z", genuineTurn)
	writeRegistry(t, record(workRoot, "/Users/steve/code/warp", "2026-09-09T17:59:59Z"))

	sessions, err := codexAdapter{}.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions, want 1", len(sessions))
	}
	if got, want := sessions[0].Cwd, "/Users/steve/code/warp"; got != want {
		t.Errorf("identity Cwd = %q, want %q", got, want)
	}
	if got, want := sessions[0].Project, encodeProjectPath(workRoot); got != want {
		t.Errorf("storage slug = %q, want %q (the directory codex ran in)", got, want)
	}
}

// Everything that isn't a stamped throwaway root behaves exactly as before.
func TestCodexListLeavesUnstampedSessionsAlone(t *testing.T) {
	cases := []struct {
		name     string
		cwd      func(home string) string
		registry []string
	}{
		{
			name:     "throwaway-root-with-no-stamp",
			cwd:      func(home string) string { return filepath.Join(home, "tmpwork") },
			registry: nil,
		},
		{
			name:     "throwaway-root-stamped-after-the-session-started",
			cwd:      func(home string) string { return filepath.Join(home, "tmpwork") },
			registry: []string{`STAMP`},
		},
		{
			name:     "real-checkout-with-a-stamp-for-it",
			cwd:      func(string) string { return "/Users/steve/code/loom" },
			registry: []string{`STAMP`},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("TMPDIR", home)
			cwd := c.cwd(home)
			writeRollout(t, home, cwd, "2026-09-09T18:00:00Z", genuineTurn)
			lines := make([]string, 0, len(c.registry))
			for range c.registry {
				// Stamped a day after the session began: no record here
				// describes this run.
				lines = append(lines, record(cwd, "/Users/steve/code/warp", "2026-09-10T18:00:00Z"))
			}
			writeRegistry(t, lines...)

			sessions, err := codexAdapter{}.List()
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(sessions) != 1 {
				t.Fatalf("got %d sessions, want 1", len(sessions))
			}
			if sessions[0].Cwd != cwd {
				t.Errorf("identity Cwd = %q, want the reported cwd %q", sessions[0].Cwd, cwd)
			}
		})
	}
}

// The timestamp is read for stamp matching only, so a rollout without one
// still enumerates and still carries its cwd.
func TestReadCodexMetaTimestamps(t *testing.T) {
	dir := t.TempDir()
	started := `{"type":"event_msg","payload":{"type":"task_started","turn_id":"` + genuineTurn + `"}}` + "\n"
	write := func(body string) string {
		p := filepath.Join(dir, "r.jsonl")
		if err := os.WriteFile(p, []byte(body+started), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		return p
	}

	cwd, start, _, err := readCodexMeta(write(`{"timestamp":"2026-09-09T18:00:00Z","type":"session_meta","payload":{"cwd":"/x","timestamp":"2026-09-09T17:59:59Z"}}` + "\n"))
	if err != nil {
		t.Fatalf("readCodexMeta: %v", err)
	}
	if cwd != "/x" {
		t.Errorf("cwd = %q, want /x", cwd)
	}
	if got, want := start.Format("2006-01-02T15:04:05Z"), "2026-09-09T18:00:00Z"; got != want {
		t.Errorf("start = %s, want the record timestamp %s", got, want)
	}

	// No wrapper timestamp: the payload's own dates the session.
	_, start, _, err = readCodexMeta(write(`{"type":"session_meta","payload":{"cwd":"/x","timestamp":"2026-09-09T17:59:59Z"}}` + "\n"))
	if err != nil {
		t.Fatalf("readCodexMeta: %v", err)
	}
	if got, want := start.Format("2006-01-02T15:04:05Z"), "2026-09-09T17:59:59Z"; got != want {
		t.Errorf("start = %s, want the payload timestamp %s", got, want)
	}

	// Neither, or an unparseable one: zero time, cwd still answered.
	cwd, start, _, err = readCodexMeta(write(`{"timestamp":"whenever","type":"session_meta","payload":{"cwd":"/x"}}` + "\n"))
	if err != nil {
		t.Fatalf("readCodexMeta: %v", err)
	}
	if cwd != "/x" || !start.IsZero() {
		t.Errorf("got cwd=%q start=%v, want /x and the zero time", cwd, start)
	}
}

// Codex Desktop writes a copy of a session it imports from another agent,
// marked by its task_started turn id. The copy is skipped wherever it ran;
// a genuine session at cwd / is captured as before, under _default, and is
// told apart by its turn id alone — not by its cwd, and not by a later
// record that merely mentions the marker.
func TestCodexListSkipsExternalImports(t *testing.T) {
	cases := []struct {
		name   string
		cwd    string
		turnID string
		want   int
	}{
		{"import-in-a-project", "/Users/steve/code/loom", codexparse.ExternalImportTurnPrefix + "1", 0},
		{"import-at-root", "/", codexparse.ExternalImportTurnPrefix + "1", 0},
		{"genuine-at-root", "/", genuineTurn, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			writeRollout(t, home, c.cwd, "2026-09-09T18:00:00Z", c.turnID)

			sessions, err := codexAdapter{}.List()
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(sessions) != c.want {
				t.Fatalf("got %d sessions, want %d", len(sessions), c.want)
			}
			if c.want == 0 {
				return
			}
			if got := sessions[0].Project; got != "_default" {
				t.Errorf("Project = %q, want _default", got)
			}
			if got := sessions[0].Cwd; got != "/" {
				t.Errorf("Cwd = %q, want /", got)
			}
		})
	}
}

// Until the record after session_meta is on disk a rollout could still turn
// out to be an import, so it waits like a rollout whose session_meta is
// unwritten. That one record decides: a genuine session whose second record
// is not a task_started is captured, and one that later quotes the marker is
// still genuine.
func TestReadCodexMetaDecidesOnTheSecondRecord(t *testing.T) {
	dir := t.TempDir()
	meta := `{"type":"session_meta","payload":{"cwd":"/"}}` + "\n"
	importStarted := `{"type":"event_msg","payload":{"type":"task_started","turn_id":"` + codexparse.ExternalImportTurnPrefix + `1"}}`
	cases := []struct {
		name         string
		body         string
		wantCwd      string
		wantImported bool
	}{
		{"meta-only", meta, "", false},
		{"marker-half-written", meta + importStarted, "", false},
		{"marker-written", meta + importStarted + "\n", "", true},
		{"genuine-opening-with-a-message", meta +
			`{"type":"response_item","payload":{"type":"message","role":"user","content":[]}}` + "\n", "/", false},
		{"genuine-quoting-the-marker", meta +
			`{"type":"event_msg","payload":{"type":"task_started","turn_id":"` + genuineTurn + `"}}` + "\n" +
			`{"type":"event_msg","payload":{"type":"user_message","message":"` + codexparse.ExternalImportTurnPrefix + `1"}}` + "\n" +
			importStarted + "\n", "/", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := filepath.Join(dir, c.name+".jsonl")
			if err := os.WriteFile(p, []byte(c.body), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
			cwd, _, imported, err := readCodexMeta(p)
			if err != nil {
				t.Fatalf("readCodexMeta: %v", err)
			}
			if cwd != c.wantCwd || imported != c.wantImported {
				t.Errorf("got cwd=%q imported=%t, want cwd=%q imported=%t", cwd, imported, c.wantCwd, c.wantImported)
			}
		})
	}
}
