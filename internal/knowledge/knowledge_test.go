package knowledge

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseArtifactEvidenceAndClaim(t *testing.T) {
	body := `---
id: loom-example-truth
title: Example truth title
scope: loom
type: truth
status: validated
evidence:
  - path: cmd/loom/cmd/relevant.go
    line: 42
    note: the ranker entry point
  - commit: abc123
    note: not a path, must be ignored
  - path: internal/knowledge/relevant.go
sources:
  - session: deadbeef
    project: loom
    path: should-not-leak-into-evidence.go
---

## Claim

The claim text spans this line and continues
onto a second line.

## Why it matters

This must not be captured as claim.
`
	a := parseArtifact(body, "/tmp/x.md", "loom", "truths", "validated")
	if a.ID != "loom-example-truth" {
		t.Errorf("ID = %q", a.ID)
	}
	if a.Title != "Example truth title" {
		t.Errorf("Title = %q", a.Title)
	}
	wantPaths := []string{"cmd/loom/cmd/relevant.go", "internal/knowledge/relevant.go"}
	if !reflect.DeepEqual(a.EvidencePaths, wantPaths) {
		t.Errorf("EvidencePaths = %v, want %v", a.EvidencePaths, wantPaths)
	}
	wantClaim := "The claim text spans this line and continues\nonto a second line."
	if a.Claim != wantClaim {
		t.Errorf("Claim = %q, want %q", a.Claim, wantClaim)
	}
}

func TestParseArtifactNoFrontmatter(t *testing.T) {
	a := parseArtifact("just a body, no frontmatter\n", "/tmp/x.md", "loom", "truths", "validated")
	if a.ID != "" {
		t.Errorf("expected empty ID for frontmatter-less file, got %q", a.ID)
	}
}

// TestLoadSkipsArtifactsWithoutFrontmatter confirms Load() drops files that
// parse to an empty ID (existing walkArtifacts behavior).
func TestLoadSkipsArtifactsWithoutFrontmatter(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LOOM_KNOWLEDGE_ROOT", root)

	mustWrite := func(rel, content string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	mustWrite("truths/loom/good.md", "---\nid: loom-good\ntitle: Good\nstatus: validated\n---\n\n## Claim\n\nyes\n")
	mustWrite("truths/loom/bad.md", "no frontmatter at all\n")

	arts, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(arts) != 1 {
		t.Fatalf("got %d artifacts, want 1 (frontmatter-less skipped)", len(arts))
	}
	if arts[0].ID != "loom-good" {
		t.Errorf("ID = %q, want loom-good", arts[0].ID)
	}
}
