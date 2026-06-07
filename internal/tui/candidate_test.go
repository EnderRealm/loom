package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const candidateFixture = `---
id: loom-notify-state-default-path
title: Shipper notification state persists at $LOOM_HOME/transport/notify.state
scope: loom
type: truth
status: candidate
evidence:
  - path: transport/internal/notify/notify.go
    line: 42
    note: statePath() returns filepath.Join(config.TransportDir(), "notify.state")
sources:
  - session: n/a
    project: loom
related: []
verified_at: 2026-04-26
status: candidate
extracted_at: 2026-04-26T10:08:56
extracted_by: claude:sonnet
---

## Claim

The shipper persists notification state at $LOOM_HOME/transport/notify.state.
`

func seedCandidate(t *testing.T) (root string, art Artifact) {
	t.Helper()
	root = t.TempDir()
	t.Setenv("LOOM_KNOWLEDGE_ROOT", root)

	dir := filepath.Join(root, "_candidates", "truths", "loom")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "loom-notify-state-default-path--20260426-100856.md")
	if err := os.WriteFile(path, []byte(candidateFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	arts, err := LoadKnowledge()
	if err != nil {
		t.Fatalf("LoadKnowledge: %v", err)
	}
	if len(arts) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(arts))
	}
	return root, arts[0]
}

func TestPromoteCandidate(t *testing.T) {
	root, art := seedCandidate(t)
	if art.Status != "candidate" {
		t.Fatalf("seed status = %q, want candidate", art.Status)
	}

	dest, err := promoteCandidate(art)
	if err != nil {
		t.Fatalf("promoteCandidate: %v", err)
	}

	// Scope prefix stripped from the validated filename; lands in truths/loom/.
	want := filepath.Join(root, "truths", "loom", "notify-state-default-path.md")
	if dest != want {
		t.Errorf("dest = %q, want %q", dest, want)
	}
	if _, err := os.Stat(art.Path); !os.IsNotExist(err) {
		t.Errorf("source still present after promote: %v", err)
	}

	body, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	if strings.Contains(s, "status: candidate") {
		t.Errorf("promoted file still has candidate status:\n%s", s)
	}
	if strings.Count(s, "status:") != 1 {
		t.Errorf("expected exactly one status line, got %d:\n%s", strings.Count(s, "status:"), s)
	}
	if !strings.Contains(s, "status: validated") {
		t.Errorf("promoted file missing validated status:\n%s", s)
	}
	if strings.Contains(s, "extracted_at") || strings.Contains(s, "extracted_by") {
		t.Errorf("promoted file retains candidate-only provenance:\n%s", s)
	}
	if !strings.Contains(s, "verified_at:") {
		t.Errorf("promoted file missing verified_at:\n%s", s)
	}
	// Body and evidence sub-fields survive untouched.
	if !strings.Contains(s, "## Claim") || !strings.Contains(s, "statePath()") {
		t.Errorf("promoted file lost body/evidence content:\n%s", s)
	}

	// Reloading the store now classifies it as validated.
	arts, err := LoadKnowledge()
	if err != nil {
		t.Fatal(err)
	}
	if len(arts) != 1 || arts[0].Status != "validated" {
		t.Errorf("after promote, expected 1 validated artifact, got %+v", arts)
	}
}

func TestPromoteCandidateNoOverwrite(t *testing.T) {
	root, art := seedCandidate(t)
	// Pre-create the validated target.
	dir := filepath.Join(root, "truths", "loom")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(dir, "notify-state-default-path.md")
	if err := os.WriteFile(existing, []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := promoteCandidate(art); err == nil {
		t.Fatal("expected promote to refuse overwriting an existing validated file")
	}
	if b, _ := os.ReadFile(existing); string(b) != "keep me\n" {
		t.Errorf("existing validated file was clobbered: %q", string(b))
	}
	if _, err := os.Stat(art.Path); err != nil {
		t.Errorf("candidate should remain after a refused promote: %v", err)
	}
}

func TestRejectCandidate(t *testing.T) {
	root, art := seedCandidate(t)

	dest, err := rejectCandidate(art)
	if err != nil {
		t.Fatalf("rejectCandidate: %v", err)
	}
	want := filepath.Join(root, "_candidates", "_rejected", "truths", "loom",
		"loom-notify-state-default-path--20260426-100856.md")
	if dest != want {
		t.Errorf("dest = %q, want %q", dest, want)
	}
	if _, err := os.Stat(art.Path); !os.IsNotExist(err) {
		t.Errorf("source still present after reject: %v", err)
	}
	// Rejected content is preserved verbatim for extractor tuning.
	body, _ := os.ReadFile(dest)
	if string(body) != candidateFixture {
		t.Errorf("rejected file content altered")
	}
	// _rejected/ is skipped by the loader, so the store now lists nothing.
	arts, err := LoadKnowledge()
	if err != nil {
		t.Fatal(err)
	}
	if len(arts) != 0 {
		t.Errorf("rejected candidate still surfaced in list: %+v", arts)
	}
}
