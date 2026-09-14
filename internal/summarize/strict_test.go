package summarize

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// strictTree lays out one parseable session and, when broken is set, a
// dangling symlink beside it. The parsers fold malformed lines into Unknown
// rather than failing, so a session the sweep cannot open is the
// deterministic way to make it count an error.
func strictTree(t *testing.T, broken bool) string {
	t.Helper()
	received := t.TempDir()
	project := filepath.Join(received, "claude-code", "loom")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "sess-plain.jsonl"), []byte(plainSession), 0o644); err != nil {
		t.Fatal(err)
	}
	if broken {
		if err := os.Symlink(filepath.Join(project, "missing"), filepath.Join(project, "sess-gone.jsonl")); err != nil {
			t.Fatal(err)
		}
	}
	return received
}

// --strict turns an errored session into a non-zero exit; without it the
// sweep reports the count and exits clean as before.
func TestStrictFailsOnErroredSession(t *testing.T) {
	received := strictTree(t, true)
	dbPath := filepath.Join(t.TempDir(), "summaries.db")

	if err := Run(Options{ReceivedDir: received, DBPath: dbPath, Force: true}); err != nil {
		t.Fatalf("Run without Strict: %v", err)
	}
	err := Run(Options{ReceivedDir: received, DBPath: dbPath, Force: true, Strict: true})
	if err == nil {
		t.Fatal("Run with Strict: nil error, want one for the errored session")
	}
	if got, want := err.Error(), "strict: errored=1 of seen=2"; got != want {
		t.Errorf("Strict error = %q, want %q", got, want)
	}
}

func TestStrictPassesOnCleanSweep(t *testing.T) {
	received := strictTree(t, false)
	dbPath := filepath.Join(t.TempDir(), "summaries.db")
	if err := Run(Options{ReceivedDir: received, DBPath: dbPath, Force: true, Strict: true}); err != nil {
		t.Fatalf("Run with Strict over a clean tree: %v", err)
	}
}

// A sweep cut short by shutdown never saw the whole tree, so --strict cannot
// vouch for it even when nothing it reached errored. A context cancelled
// before the walk starts stands in for the signal.
func TestStrictFailsOnInterruptedSweep(t *testing.T) {
	received := strictTree(t, false)
	dbPath := filepath.Join(t.TempDir(), "summaries.db")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := run(ctx, Options{ReceivedDir: received, DBPath: dbPath, Force: true}); err != nil {
		t.Fatalf("run without Strict: %v", err)
	}
	err := run(ctx, Options{ReceivedDir: received, DBPath: dbPath, Force: true, Strict: true})
	if err == nil {
		t.Fatal("run with Strict: nil error, want one for the interrupted sweep")
	}
	if got, want := err.Error(), "strict: sweep interrupted after seen=0 errored=0"; got != want {
		t.Errorf("Strict error = %q, want %q", got, want)
	}
}

// A watch loop has no exit to report, so the pair is refused before any
// sweep runs: the DB must not have been created.
func TestStrictRejectsWatch(t *testing.T) {
	received := strictTree(t, false)
	dbPath := filepath.Join(t.TempDir(), "summaries.db")
	if err := Run(Options{ReceivedDir: received, DBPath: dbPath, Strict: true, Watch: true}); err == nil {
		t.Fatal("Run with Strict and Watch: nil error, want a refusal")
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Errorf("summaries.db exists after refused run (stat err = %v)", err)
	}
}
