package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupRelevant builds an isolated HOME with a tk central store containing one
// ticket and a knowledge root, then points the env at both. It returns the
// namespaced ticket ID under test.
func setupRelevant(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)

	central := filepath.Join(home, "tickets-store")
	mustWrite := func(p, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	mustWrite(filepath.Join(home, ".ticket", "config.yaml"), "central_root: "+central+"\n")

	mustWrite(filepath.Join(central, "tickets", "loom", "add-relevant-83f2.md"),
		`---
id: add-relevant-83f2
status: open
deps: []
links: []
created: 2026-04-26T16:09:58Z
type: feature
priority: 2
---
# Add loom relevant command

Read-only CLI that surfaces durable knowledge for a ticket. Touches cmd/loom/cmd/relevant.go.

## Acceptance Criteria

- ranked markdown list of related truths and decisions
`)

	knowledgeRoot := filepath.Join(home, "knowledge")
	t.Setenv("LOOM_KNOWLEDGE_ROOT", knowledgeRoot)
	mustWrite(filepath.Join(knowledgeRoot, "truths", "loom", "relevant-command.md"),
		`---
id: loom-relevant-command-truth
title: loom relevant command ranks durable knowledge for a ticket
scope: loom
type: truth
status: validated
evidence:
  - path: cmd/loom/cmd/relevant.go
    note: command entry point
sources:
  - session: x
    ticket: add-relevant-83f2
---

## Claim

The relevant command surfaces durable knowledge for a ticket.
`)

	return "loom/add-relevant-83f2"
}

func runRelevant(t *testing.T, id string) (stdout string, err error) {
	t.Helper()
	r, w, perr := os.Pipe()
	if perr != nil {
		t.Fatal(perr)
	}
	orig := os.Stdout
	os.Stdout = w
	relevantForTicket = id
	err = relevantCmd.RunE(relevantCmd, nil)
	w.Close()
	os.Stdout = orig

	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 1024)
	for {
		n, rerr := r.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if rerr != nil {
			break
		}
	}
	return string(buf), err
}

func TestRelevantHappyPath(t *testing.T) {
	id := setupRelevant(t)
	out, err := runRelevant(t, id)
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{
		"# Relevant knowledge for loom/add-relevant-83f2",
		"loom-relevant-command-truth",
		"loom relevant command ranks durable knowledge for a ticket",
		"truths/loom/relevant-command.md",
		"score ",
		"citation",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s", want, out)
		}
	}
}

func TestRelevantMissingTicket(t *testing.T) {
	setupRelevant(t)
	_, err := runRelevant(t, "loom/nope-0000")
	if err == nil {
		t.Fatal("expected error for missing ticket, got nil")
	}
}

func TestRelevantEmptyCorpus(t *testing.T) {
	id := setupRelevant(t)
	// Repoint the knowledge root at an empty dir.
	t.Setenv("LOOM_KNOWLEDGE_ROOT", t.TempDir())
	out, err := runRelevant(t, id)
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "No relevant knowledge found.") {
		t.Errorf("expected no-matches message, got:\n%s", out)
	}
}
