package codexparse

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseRealCodexRollout(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	base := filepath.Join(home, ".codex", "sessions")
	var path string
	_ = filepath.Walk(base, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if filepath.Ext(p) != ".jsonl" {
			return nil
		}
		if info.Size() < 50_000 {
			return nil
		}
		path = p
		return filepath.SkipAll
	})
	if path == "" {
		t.Skip("no codex rollouts to test against")
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
		t.Errorf("no sessionId from %s", path)
	}
	if len(s.Turns) == 0 {
		t.Errorf("no turns from %s", path)
	}
	if len(s.Unknown) > 0 {
		t.Logf("UNKNOWN records seen (drift signal):")
		for _, u := range s.Unknown {
			t.Logf("  %s::%s × %d", u.Type, u.Subtype, u.Count)
		}
	}
	t.Logf("session=%s turns=%d toolCalls=%d errors=%d files=%d compactions=%d tokens=%d unknown=%d",
		s.SessionID, len(s.Turns), len(s.ToolCalls), len(s.Errors),
		len(s.FilesTouched), len(s.Compactions), len(s.TokenCounts),
		len(s.Unknown))
}
