package claudeparse

import (
	"os"
	"path/filepath"
	"testing"
)

// TestParseRealSession runs the parser against the first .jsonl found under
// ~/.claude/projects and asserts that the basic shape lands. It's a smoke
// test, not a fixture-based test — kept here because the catalog itself was
// validated against the same files.
func TestParseRealSession(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	base := filepath.Join(home, ".claude", "projects")
	var path string
	_ = filepath.Walk(base, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if filepath.Ext(p) != ".jsonl" {
			return nil
		}
		// Skip subagent rollups — they have isSidechain=true on every
		// record by design and don't represent a top-level session.
		if filepath.Base(filepath.Dir(p)) == "subagents" {
			return nil
		}
		if info.Size() < 200_000 {
			return nil
		}
		path = p
		return filepath.SkipAll
	})
	if path == "" {
		t.Skip("no real sessions to test against")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	s, err := Parse(f)
	if err != nil {
		t.Fatalf("parse failed for %s: %v", path, err)
	}
	if s.SessionID == "" {
		t.Errorf("no sessionId extracted from %s", path)
	}
	if len(s.Turns) == 0 {
		t.Errorf("no turns extracted from %s", path)
	}
	if len(s.Unknown) > 0 {
		t.Logf("UNKNOWN records seen (drift signal):")
		for _, u := range s.Unknown {
			t.Logf("  %s::%s × %d", u.Type, u.Subtype, u.Count)
		}
	}
	t.Logf("session=%s turns=%d toolCalls=%d errors=%d files=%d compactions=%d unknown=%d",
		s.SessionID, len(s.Turns), len(s.ToolCalls), len(s.Errors),
		len(s.FilesTouched), len(s.Compactions), len(s.Unknown))
}
