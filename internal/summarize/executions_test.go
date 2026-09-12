package summarize

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"loom/internal/runs"
	"loom/internal/summaries"
)

// fixtureRun is the run internal/runs/testdata/executions.jsonl declares.
const fixtureRun = "0f4c3a6e-2d1b-4b7e-9c8a-5e2f1d0a9b31"

// The sweep folds a shipped registry under received/loom-executions/<host>/
// into the runs tables, and skips it on the next pass when unchanged.
func TestSweepImportsExecutionRegistry(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("..", "runs", "testdata", "executions.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	received := t.TempDir()
	dir := filepath.Join(received, summaries.ExecutionsAgent, "steves-mbp_local")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "executions.jsonl")
	if err := os.WriteFile(path, fixture, 0o644); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(t.TempDir(), "summaries.db")
	if err := Run(Options{ReceivedDir: received, DBPath: dbPath, Rebuild: true}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	st, err := summaries.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	run, err := runs.Load(st.DB(), fixtureRun)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if run.Root == nil || len(run.Root.Children) != 5 {
		t.Fatalf("root = %+v, want five children", run.Root)
	}
	if run.Source == nil || run.Source.Path != path {
		t.Errorf("run source = %+v, want %s", run.Source, path)
	}

	current, err := st.ExecutionImportCurrent(path, int64(len(fixture)), mtimeOf(t, path))
	if err != nil {
		t.Fatal(err)
	}
	if !current {
		t.Error("registry not marked current after import")
	}
	st.Close()

	// A second pass over the unchanged tree skips the file rather than
	// re-importing it; either way the row counts hold.
	if err := Run(Options{ReceivedDir: received, DBPath: dbPath}); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	st, err = summaries.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	var n int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM executions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 8 {
		t.Errorf("executions rows after second sweep = %d, want 8", n)
	}
}

func mtimeOf(t *testing.T, path string) time.Time {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.ModTime()
}
