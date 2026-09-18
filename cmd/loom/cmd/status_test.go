package cmd

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"loom/internal/version"
)

func TestPrintBinaryStatus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "loom")
	if err := os.WriteFile(path, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	updated := time.Date(2026, time.September, 18, 10, 30, 0, 0, time.Local)
	if err := os.Chtimes(path, updated, updated); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	printBinaryStatus(&out, path, updated.Add(2*time.Hour+3*time.Minute))

	want := fmt.Sprintf("=== loom binary ===\n  version = %s\n  last updated = 2026-09-18 10:30:00 %s (2h 3m ago)\n\n",
		version.String(), updated.Format("MST"))
	if out.String() != want {
		t.Fatalf("output:\n%s\nwant:\n%s", out.String(), want)
	}
}

func TestPrintBinaryStatusWithUnknownUpdateTime(t *testing.T) {
	var out bytes.Buffer
	printBinaryStatus(&out, filepath.Join(t.TempDir(), "missing"), time.Now())

	want := fmt.Sprintf("=== loom binary ===\n  version = %s\n  last updated = unknown\n\n", version.String())
	if out.String() != want {
		t.Fatalf("output:\n%s\nwant:\n%s", out.String(), want)
	}
}
