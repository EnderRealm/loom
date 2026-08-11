package cmd

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runExtract parses argv through a command built exactly like the registered
// one, so the guards are exercised against real flag state — including which
// flags were passed — rather than a hand-made copy of it. The environment is
// isolated and stripped of extract.py: these cases assert that a guard rejects
// argv, and a regressed guard must land on a no-op rather than on the
// developer's own ~/.loom.
func runExtract(t *testing.T, args ...string) error {
	t.Helper()
	t.Setenv("LOOM_HOME", t.TempDir())
	t.Setenv("LOOM_KNOWLEDGE_ROOT", filepath.Join(t.TempDir(), "knowledge"))
	t.Setenv("LOOM_EXTRACTORS_DIR", filepath.Join(t.TempDir(), "absent"))
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	cmd := newExtractCmd()
	cmd.SetArgs(args)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	return cmd.Execute()
}

func TestExtractRejectsNegativeLimit(t *testing.T) {
	err := runExtract(t, "--backfill", "--limit", "-5")
	if err == nil {
		t.Fatal("--limit -5 accepted; a negative bound selects the entire backlog")
	}
	if !strings.Contains(err.Error(), "--limit") {
		t.Fatalf("error %v does not name the offending flag", err)
	}
}

// The guard is on the flag being passed, not on its value: --limit 0 and
// --dry-run=false read as "unset" if judged by value, so a sweep would silently
// accept them.
func TestExtractRejectsBackfillFlagsWithoutBackfill(t *testing.T) {
	for _, args := range [][]string{
		{"--limit", "0"},
		{"--dry-run=false"},
		{"--scope", "loom"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			err := runExtract(t, args...)
			if err == nil {
				t.Fatalf("%v accepted without --backfill", args)
			}
			if !strings.Contains(err.Error(), "require --backfill") {
				t.Fatalf("error %v does not explain the requirement", err)
			}
		})
	}
}

// The mirror of that guard: with --backfill, an explicit --limit 0 is the
// documented "unbounded" and must reach the run.
func TestExtractAcceptsExplicitZeroLimitWithBackfill(t *testing.T) {
	if err := runExtract(t, "--backfill", "--limit", "0"); err != nil {
		t.Fatalf("run: %v", err)
	}
}
