package knowledge

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
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

func TestParseArtifactFirstNoticed(t *testing.T) {
	cases := []struct {
		name    string
		sources string
		want    string // "" means zero
	}{
		{
			name: "earliest of several wins",
			sources: `sources:
  - session: aaa
    project: loom
    date: 2026-04-08
    role: discovery
  - session: bbb
    date: 2026-01-17
  - date: 2026-06-30
    session: ccc
`,
			want: "2026-01-17",
		},
		{
			name:    "no sources block",
			sources: "",
		},
		{
			name: "undated source entry",
			sources: `sources:
  - session: aaa
    project: loom
    role: discovery
`,
		},
		{
			name: "unparseable date values",
			sources: `sources:
  - session: aaa
    date: <YYYY-MM-DD>
  - session: bbb
    date: 2026-13-45 to x
  - session: ccc
    date: 2026-
`,
		},
		{
			name: "range with a trailing end date",
			sources: `sources:
  - session: aaa
    date: 2026-03-19 to 2026-03-22
`,
			want: "2026-03-19",
		},
		{
			name: "slash-separated range",
			sources: `sources:
  - session: aaa
    date: 2026-03-09/2026-03-10
`,
			want: "2026-03-09",
		},
		{
			name: "earliest wins across ranges and single dates",
			sources: `sources:
  - session: aaa
    date: 2026-03-19 to 2026-03-22
  - session: bbb
    date: 2026-02-04
  - session: ccc
    date: 2026-01-30/2026-02-02
  - session: ddd
    date: <YYYY-MM-DD>
`,
			want: "2026-01-30",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := "---\nid: loom-example\ntitle: Example\n" + tc.sources + "verified_at: 2020-01-01\n---\n\n## Claim\n\nyes\n"
			a := parseArtifact(body, "/tmp/x.md", "loom", "truths", "validated")
			if tc.want == "" {
				if !a.FirstNoticed.IsZero() {
					t.Fatalf("FirstNoticed = %v, want zero", a.FirstNoticed)
				}
				mtime := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
				a.Modified = mtime
				if !a.AgeBasis().Equal(mtime) {
					t.Errorf("AgeBasis = %v, want mtime %v", a.AgeBasis(), mtime)
				}
				return
			}
			if got := a.FirstNoticed.Format("2006-01-02"); got != tc.want {
				t.Fatalf("FirstNoticed = %q, want %q", got, tc.want)
			}
			a.Modified = time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
			if !a.AgeBasis().Equal(a.FirstNoticed) {
				t.Errorf("AgeBasis = %v, want FirstNoticed %v", a.AgeBasis(), a.FirstNoticed)
			}
		})
	}
}

func TestParseArtifactNoFrontmatter(t *testing.T) {
	a := parseArtifact("just a body, no frontmatter\n", "/tmp/x.md", "loom", "truths", "validated")
	if a.ID != "" {
		t.Errorf("expected empty ID for frontmatter-less file, got %q", a.ID)
	}
}

// TestLoadOrdersByFirstNoticed confirms candidates still lead, and that within
// a status group the ordering follows AgeBasis (first-noticed date, mtime
// fallback) rather than mtime alone.
func TestLoadOrdersByFirstNoticed(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LOOM_KNOWLEDGE_ROOT", root)

	write := func(rel, sources string, mtime time.Time) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		id := strings.TrimSuffix(filepath.Base(rel), ".md")
		body := "---\nid: " + id + "\ntitle: " + id + "\n" + sources + "---\n\n## Claim\n\nyes\n"
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}

	src := func(date string) string {
		return "sources:\n  - session: aaa\n    date: " + date + "\n"
	}

	// Oldest-first by mtime would be new-date, old-date, no-date; oldest-first
	// by first-noticed is old-date, new-date, no-date (mtime fallback).
	write("truths/loom/no-date.md", "", time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
	write("truths/loom/old-date.md", src("2026-01-01"), time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))
	write("truths/loom/new-date.md", src("2026-06-01"), time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC))
	write("_candidates/truths/loom/cand.md", src("2020-01-01"), time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	arts, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var got []string
	for _, a := range arts {
		got = append(got, a.ID)
	}
	want := []string{"cand", "old-date", "new-date", "no-date"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v", got, want)
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

// TestLoadReadsOnlyMarkdownUnderScopeDirs pins the walker's shape: a file at
// the root, directly under a type directory, or a non-.md file inside a scope
// is never an artifact. The extractor's ledger (extract.state) lives outside
// the store, and this is what keeps a sidecar file from ever reading back as a
// truth or a candidate should the two trees coincide.
func TestLoadReadsOnlyMarkdownUnderScopeDirs(t *testing.T) {
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

	ledger := "{\"sessions\": {\"claude-code/x\": {\"outcome\": \"extracted\", \"scope\": \"loom\", \"remote\": \"github.com/a/loom\"}}}\n"
	mustWrite("truths/loom/good.md", "---\nid: loom-good\ntitle: Good\nstatus: validated\n---\n\n## Claim\n\nyes\n")
	mustWrite("extract.state", ledger)
	mustWrite("truths/extract.state", ledger)
	mustWrite("truths/loom/extract.state", ledger)
	mustWrite("_candidates/truths/loom/extract.state", ledger)

	arts, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(arts) != 1 || arts[0].ID != "loom-good" {
		t.Fatalf("got %+v, want only loom-good", arts)
	}
}

func TestParseArtifactContradicts(t *testing.T) {
	cases := []struct {
		name    string
		field   string
		wantIDs []string
		wantBad []string
	}{
		{name: "absent"},
		{name: "empty flow list", field: "contradicts: []\n"},
		{
			name:    "flow list",
			field:   "contradicts: [loom-a, \"loom-b\"]\n",
			wantIDs: []string{"loom-a", "loom-b"},
		},
		{
			name:    "block list",
			field:   "contradicts:\n  - loom-a\n  - 'loom-b'\n",
			wantIDs: []string{"loom-a", "loom-b"},
		},
		{
			name:    "doc-override mapping",
			field:   "contradicts:\n  - file: skills/x/SKILL.md\n    claim: \"stale wording\"\n    status: stale\n",
			wantBad: []string{"file: skills/x/SKILL.md"},
		},
		{name: "null", field: "contradicts: null\n"},
		{name: "tilde", field: "contradicts: ~\n"},
		{
			name:    "bare id scalar",
			field:   "contradicts: loom-a\n",
			wantIDs: []string{"loom-a"},
		},
		{
			name:    "bare non-id scalar",
			field:   "contradicts: see the launchd truth\n",
			wantBad: []string{"see the launchd truth"},
		},
		{
			name:    "indented scalar without a dash",
			field:   "contradicts:\n  loom-a\n",
			wantBad: []string{"loom-a"},
		},
		{
			name:    "block mapping without a dash",
			field:   "contradicts:\n  file: docs/x.md\n  claim: old\n",
			wantBad: []string{"file: docs/x.md", "claim: old"},
		},
		{
			name:    "id after a mapping entry",
			field:   "contradicts:\n  - file: docs/x.md\n    status: stale\n  - loom-a\n",
			wantIDs: []string{"loom-a"},
			wantBad: []string{"file: docs/x.md"},
		},
		{
			name:    "unterminated flow list",
			field:   "contradicts: [loom-a, loom-b\n",
			wantBad: []string{"[loom-a, loom-b"},
		},
		{
			name:    "mixed block list",
			field:   "contradicts:\n  - loom-a\n  - path: x.go\n",
			wantIDs: []string{"loom-a"},
			wantBad: []string{"path: x.go"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := "---\nid: loom-c\ntitle: C\nrelated: []\n" + tc.field + "verified_at: 2026-01-01\n---\n\n## Claim\n\nyes\n"
			a := parseArtifact(body, "/tmp/x.md", "loom", "truths", "candidate")
			if !reflect.DeepEqual(a.Contradicts, tc.wantIDs) {
				t.Errorf("Contradicts = %q, want %q", a.Contradicts, tc.wantIDs)
			}
			if !reflect.DeepEqual(a.badContradicts, tc.wantBad) {
				t.Errorf("badContradicts = %q, want %q", a.badContradicts, tc.wantBad)
			}
		})
	}
}

// TestLoadLinksContradictions pins both directions of a candidate →
// validated contradiction and the warnings for entries that resolve to no
// validated artifact.
func TestLoadLinksContradictions(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LOOM_KNOWLEDGE_ROOT", root)

	write := func(rel, id, status, contradicts string, date string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		body := "---\nid: " + id + "\ntitle: " + id + "\nstatus: " + status +
			"\nsources:\n  - session: aaa\n    date: " + date + "\n" + contradicts + "---\n\n## Claim\n\nyes\n"
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// The validated side carries a doc-override mapping of its own, which is
	// not linked and must not warn.
	write("truths/loom/target.md", "loom-target", "validated",
		"contradicts:\n  - file: docs/x.md\n    claim: old\n", "2026-01-01")
	write("truths/loom/other.md", "loom-other", "validated", "contradicts: []\n", "2026-01-02")
	write("_candidates/truths/loom/flow--1.md", "loom-flow", "candidate",
		"contradicts: [loom-target]\n", "2026-02-01")
	write("_candidates/truths/loom/block--1.md", "loom-block", "candidate",
		"contradicts:\n  - loom-target\n  - loom-missing\n", "2026-02-02")
	write("_candidates/truths/loom/bad--1.md", "loom-bad", "candidate",
		"contradicts:\n  - path: skills/x.md\n    note: stale\n", "2026-02-03")
	write("_candidates/truths/loom/cand-target--1.md", "loom-cand-target", "candidate",
		"contradicts: [loom-flow]\n", "2026-02-04")
	write("_candidates/truths/loom/undashed--1.md", "loom-undashed", "candidate",
		"contradicts:\n  loom-target\n", "2026-02-07")
	// The same candidate id in two files, one naming the target twice: linked
	// once on each side.
	write("_candidates/truths/loom/dup--1.md", "loom-dup", "candidate",
		"contradicts: [loom-other, loom-other]\n", "2026-02-05")
	write("_candidates/truths/loom/dup--2.md", "loom-dup", "candidate",
		"contradicts: [loom-other]\n", "2026-02-06")

	arts, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	byID := map[string]Artifact{}
	for _, a := range arts {
		byID[a.ID] = a
	}

	if got, want := byID["loom-target"].ContradictedBy, []string{"loom-flow", "loom-block"}; !reflect.DeepEqual(got, want) {
		t.Errorf("loom-target ContradictedBy = %q, want %q", got, want)
	}
	if got := byID["loom-target"].ContradictsWarnings; got != nil {
		t.Errorf("validated artifact got warnings %q", got)
	}
	if got, want := byID["loom-other"].ContradictedBy, []string{"loom-dup"}; !reflect.DeepEqual(got, want) {
		t.Errorf("loom-other ContradictedBy = %q, want %q", got, want)
	}
	for _, a := range arts {
		if a.ID == "loom-dup" && !reflect.DeepEqual(a.Conflicts, []string{"loom-other"}) {
			t.Errorf("%s Conflicts = %q, want [loom-other]", a.Path, a.Conflicts)
		}
	}
	if got, want := byID["loom-flow"].Conflicts, []string{"loom-target"}; !reflect.DeepEqual(got, want) {
		t.Errorf("loom-flow Conflicts = %q, want %q", got, want)
	}
	if got, want := byID["loom-block"].Conflicts, []string{"loom-target"}; !reflect.DeepEqual(got, want) {
		t.Errorf("loom-block Conflicts = %q, want %q", got, want)
	}

	warns := map[string]string{
		"loom-block":       "loom-missing",
		"loom-bad":         "path: skills/x.md",
		"loom-cand-target": "loom-flow",
		"loom-undashed":    "loom-target",
	}
	for id, needle := range warns {
		w := byID[id].ContradictsWarnings
		if len(w) != 1 || !strings.Contains(w[0], needle) {
			t.Errorf("%s ContradictsWarnings = %q, want one naming %q", id, w, needle)
		}
		if id != "loom-block" && byID[id].Conflicts != nil {
			t.Errorf("%s Conflicts = %q, want none", id, byID[id].Conflicts)
		}
	}
}
