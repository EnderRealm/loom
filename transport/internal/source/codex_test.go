package source

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// writeRollout lays down one codex rollout file whose session_meta reports
// cwd and start, and returns the session id it was written under.
func writeRollout(t *testing.T, home, cwd, start string) string {
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
	name := fmt.Sprintf("rollout-2026-09-09T18-00-00-%s.jsonl", sid)
	if err := os.WriteFile(filepath.Join(dir, name), append(line, '\n'), 0o644); err != nil {
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
	writeRollout(t, home, workRoot, "2026-09-09T18:00:00Z")
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
			writeRollout(t, home, cwd, "2026-09-09T18:00:00Z")
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
	write := func(body string) string {
		p := filepath.Join(dir, "r.jsonl")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		return p
	}

	cwd, start, err := readCodexMeta(write(`{"timestamp":"2026-09-09T18:00:00Z","type":"session_meta","payload":{"cwd":"/x","timestamp":"2026-09-09T17:59:59Z"}}` + "\n"))
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
	_, start, err = readCodexMeta(write(`{"type":"session_meta","payload":{"cwd":"/x","timestamp":"2026-09-09T17:59:59Z"}}` + "\n"))
	if err != nil {
		t.Fatalf("readCodexMeta: %v", err)
	}
	if got, want := start.Format("2006-01-02T15:04:05Z"), "2026-09-09T17:59:59Z"; got != want {
		t.Errorf("start = %s, want the payload timestamp %s", got, want)
	}

	// Neither, or an unparseable one: zero time, cwd still answered.
	cwd, start, err = readCodexMeta(write(`{"timestamp":"whenever","type":"session_meta","payload":{"cwd":"/x"}}` + "\n"))
	if err != nil {
		t.Fatalf("readCodexMeta: %v", err)
	}
	if cwd != "/x" || !start.IsZero() {
		t.Errorf("got cwd=%q start=%v, want /x and the zero time", cwd, start)
	}
}
