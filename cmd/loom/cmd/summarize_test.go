package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"loom/internal/summaries"
)

const summarizeSession = `{"type":"user","uuid":"v1","sessionId":"sess-home","promptId":"q1","timestamp":"2026-09-01T11:00:00.000Z","message":{"role":"user","content":"read the file"}}
{"type":"assistant","uuid":"v2","sessionId":"sess-home","timestamp":"2026-09-01T11:00:01.000Z","message":{"role":"assistant","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn"}}
`

// `loom summarize` with no --received/--db folds LOOM_HOME/received into
// LOOM_HOME/summaries.db and leaves $HOME/.loom untouched.
func TestSummarizeHonorsLoomHome(t *testing.T) {
	home := t.TempDir()
	userHome := t.TempDir()
	project := filepath.Join(home, "received", "claude-code", "loom")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "sess-home.jsonl"), []byte(summarizeSession), 0o644); err != nil {
		t.Fatal(err)
	}

	_, stderr, code := runLoomEnv(t, home, []string{"HOME=" + userHome}, "summarize", "--strict")
	if code != 0 {
		t.Fatalf("loom summarize exited %d\nstderr: %s", code, stderr)
	}

	if _, err := os.Stat(filepath.Join(userHome, ".loom")); !os.IsNotExist(err) {
		t.Errorf("$HOME/.loom exists after summarize under LOOM_HOME (stat err=%v)", err)
	}
	t.Setenv("LOOM_HOME", home)
	v, err := summaries.Load()
	if err != nil {
		t.Fatalf("load %s: %v", filepath.Join(home, "summaries.db"), err)
	}
	if !v.Available {
		t.Fatalf("no summaries.db under LOOM_HOME %s", home)
	}
	if len(v.BySession) != 1 {
		t.Errorf("sessions folded = %d, want 1 from LOOM_HOME/received", len(v.BySession))
	}
}
