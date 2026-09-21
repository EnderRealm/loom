package cmd

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"loom/internal/extract"
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

// A scope two remotes resolve to is named with both, alongside the pending
// lines: a merged namespace is invisible in the store itself. The remotes are
// rendered through the extractor's redaction, so a stored key that still
// carries userinfo never reaches the terminal with it.
func TestPrintScopeReportNamesCollidingScopes(t *testing.T) {
	var out bytes.Buffer
	printScopeReport(&out, extract.ScopeReport{
		TruthsDir:       "/x/truths",
		TruthsDirExists: true,
		Eligible:        5,
		MinTurns:        3,
		Scopes: []extract.ScopeStat{
			{Name: "loom", Onboarded: true, Sessions: 2},
			{Name: "origin", Sessions: 2},
			{Name: "tools", Onboarded: true, Sessions: 1},
		},
		Collisions: []extract.ScopeCollision{
			{Name: "origin", Remotes: []string{"/var/a/origin", "/var/b/origin"}},
			{Name: "tools", Remotes: []string{"github.com/b/tools", "u:secret@github.com/a/tools"}},
		},
		Unresolved: 1,
	})

	want := "  truths = /x/truths\n" +
		"  eligible sessions = 5 (claude-code, min-turns=3)\n" +
		"  onboarded: loom=2, tools=1\n" +
		"  not onboarded — 2 sessions skipped for want of a scope directory:\n" +
		"    origin=2\n" +
		"    onboard with: " + extract.ScopeAddCommand + "\n" +
		"  colliding — 2 scope(s) filed under more than one remote:\n" +
		"    origin: /var/a/origin, /var/b/origin\n" +
		"    tools: github.com/b/tools, github.com/a/tools\n" +
		"  unresolved: 1 sessions (no .loom-project marker naming a scope this store has, and no usable git remote)\n\n"
	if out.String() != want {
		t.Fatalf("output:\n%s\nwant:\n%s", out.String(), want)
	}
}
